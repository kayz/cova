package webhook

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrMissingTimestamp      = errors.New("missing X-COVA-Timestamp")
	ErrMissingSignature      = errors.New("missing X-COVA-Signature")
	ErrInvalidSignature      = errors.New("invalid webhook signature")
	ErrTimestampOutOfWindow  = errors.New("timestamp outside allowed window")
	ErrInvalidEventPayload   = errors.New("invalid webhook payload")
	ErrMissingEventID        = errors.New("missing webhook event id")
	ErrReplayDetected        = errors.New("webhook replay detected")
	ErrInvalidSignatureShape = errors.New("invalid signature header format")
)

const (
	defaultAllowedSkew = 5 * time.Minute
	defaultReplayTTL   = 15 * time.Minute
)

// ReplayGuard tracks processed event IDs and blocks duplicates.
type ReplayGuard interface {
	CheckAndMark(eventID string, now time.Time, ttl time.Duration) (alreadySeen bool, err error)
}

type verifyConfig struct {
	allowedSkew time.Duration
	replayTTL   time.Duration
	now         func() time.Time
	replayGuard ReplayGuard
}

// VerifyOption customizes verifier behavior.
type VerifyOption func(*verifyConfig)

func WithAllowedSkew(v time.Duration) VerifyOption {
	return func(c *verifyConfig) {
		if v > 0 {
			c.allowedSkew = v
		}
	}
}

func WithReplayTTL(v time.Duration) VerifyOption {
	return func(c *verifyConfig) {
		if v > 0 {
			c.replayTTL = v
		}
	}
}

func WithVerifyClock(now func() time.Time) VerifyOption {
	return func(c *verifyConfig) {
		if now != nil {
			c.now = now
		}
	}
}

func WithReplayGuard(guard ReplayGuard) VerifyOption {
	return func(c *verifyConfig) {
		if guard != nil {
			c.replayGuard = guard
		}
	}
}

// Verifier verifies webhook signature and replay safety.
type Verifier struct {
	secret string
	cfg    verifyConfig
}

func NewVerifier(secret string, opts ...VerifyOption) *Verifier {
	cfg := verifyConfig{
		allowedSkew: defaultAllowedSkew,
		replayTTL:   defaultReplayTTL,
		now:         time.Now,
		replayGuard: NewInMemoryReplayGuard(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Verifier{
		secret: strings.TrimSpace(secret),
		cfg:    cfg,
	}
}

// Verify validates signature + timestamp + event_id replay protection.
func (v *Verifier) Verify(headers http.Header, body []byte) (Event, error) {
	timestamp := strings.TrimSpace(headers.Get("X-COVA-Timestamp"))
	if timestamp == "" {
		return Event{}, ErrMissingTimestamp
	}
	signature := strings.TrimSpace(headers.Get("X-COVA-Signature"))
	if signature == "" {
		return Event{}, ErrMissingSignature
	}

	eventTime, err := parseUnixTimestamp(timestamp)
	if err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrTimestampOutOfWindow, err)
	}
	now := v.cfg.now().UTC()
	if absDuration(now.Sub(eventTime)) > v.cfg.allowedSkew {
		return Event{}, ErrTimestampOutOfWindow
	}

	rawSig, err := parseSignatureHeader(signature)
	if err != nil {
		return Event{}, err
	}

	expected := computeHMAC(v.secret, timestamp, body)
	if !hmac.Equal([]byte(rawSig), []byte(expected)) {
		return Event{}, ErrInvalidSignature
	}

	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalidEventPayload, err)
	}
	eventID := strings.TrimSpace(event.ID)
	if eventID == "" {
		return Event{}, ErrMissingEventID
	}

	alreadySeen, err := v.cfg.replayGuard.CheckAndMark(eventID, now, v.cfg.replayTTL)
	if err != nil {
		return Event{}, err
	}
	if alreadySeen {
		return Event{}, ErrReplayDetected
	}
	return event, nil
}

func parseUnixTimestamp(value string) (time.Time, error) {
	sec, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0).UTC(), nil
}

func parseSignatureHeader(raw string) (string, error) {
	const prefix = "sha256="
	if !strings.HasPrefix(strings.ToLower(raw), prefix) {
		return "", ErrInvalidSignatureShape
	}
	out := strings.TrimSpace(raw[len(prefix):])
	if out == "" {
		return "", ErrInvalidSignatureShape
	}
	return out, nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// InMemoryReplayGuard is a process-local replay guard with TTL.
type InMemoryReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewInMemoryReplayGuard() *InMemoryReplayGuard {
	return &InMemoryReplayGuard{
		seen: make(map[string]time.Time, 256),
	}
}

func (g *InMemoryReplayGuard) CheckAndMark(eventID string, now time.Time, ttl time.Duration) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if ttl <= 0 {
		ttl = defaultReplayTTL
	}
	now = now.UTC()

	// Opportunistic cleanup.
	for id, expireAt := range g.seen {
		if !expireAt.After(now) {
			delete(g.seen, id)
		}
	}

	if expireAt, ok := g.seen[eventID]; ok && expireAt.After(now) {
		return true, nil
	}
	g.seen[eventID] = now.Add(ttl)
	return false, nil
}
