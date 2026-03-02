package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cova/pkg/apiv1"
)

const (
	defaultMaxAttempts    = 10
	defaultInitialBackoff = 200 * time.Millisecond
	defaultMaxBackoff     = 3 * time.Second
	defaultRequestTimeout = 3 * time.Second
	defaultJitterFraction = 0.2
	defaultSource         = "cova://orchestrator"
)

type sleepFunc func(ctx context.Context, d time.Duration) error

type config struct {
	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	requestTimeout time.Duration
	jitterFraction float64
	source         string
	now            func() time.Time
	sleep          sleepFunc
	store          Store
}

// Option customizes Dispatcher behavior.
type Option func(*config)

func WithMaxAttempts(v int) Option {
	return func(c *config) {
		if v > 0 {
			c.maxAttempts = v
		}
	}
}

func WithInitialBackoff(v time.Duration) Option {
	return func(c *config) {
		if v >= 0 {
			c.initialBackoff = v
		}
	}
}

func WithMaxBackoff(v time.Duration) Option {
	return func(c *config) {
		if v >= 0 {
			c.maxBackoff = v
		}
	}
}

func WithRequestTimeout(v time.Duration) Option {
	return func(c *config) {
		if v > 0 {
			c.requestTimeout = v
		}
	}
}

func WithJitterFraction(v float64) Option {
	return func(c *config) {
		if v >= 0 {
			c.jitterFraction = v
		}
	}
}

func WithSource(v string) Option {
	return func(c *config) {
		v = strings.TrimSpace(v)
		if v != "" {
			c.source = v
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(c *config) {
		if now != nil {
			c.now = now
		}
	}
}

func WithSleeper(sleep sleepFunc) Option {
	return func(c *config) {
		if sleep != nil {
			c.sleep = sleep
		}
	}
}

func WithStore(store Store) Option {
	return func(c *config) {
		if store != nil {
			c.store = store
		}
	}
}

// Dispatcher delivers webhook events with retry and optional HMAC signing.
type Dispatcher struct {
	client *http.Client
	cfg    config

	seq uint64
	mu  sync.Mutex
	rng *rand.Rand
}

type Event struct {
	SpecVersion     string `json:"specversion"`
	ID              string `json:"id"`
	Type            string `json:"type"`
	Source          string `json:"source"`
	Time            string `json:"time"`
	DataContentType string `json:"datacontenttype"`
	Data            any    `json:"data"`
}

type JobCompletedData struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	ResultURL string `json:"result_url"`
}

func NewDispatcher(client *http.Client, opts ...Option) *Dispatcher {
	cfg := config{
		maxAttempts:    defaultMaxAttempts,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
		requestTimeout: defaultRequestTimeout,
		jitterFraction: defaultJitterFraction,
		source:         defaultSource,
		now:            time.Now,
		sleep:          sleepWithContext,
		store:          nopStore{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Dispatcher{
		client: client,
		cfg:    cfg,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (d *Dispatcher) DeliverJobCompleted(ctx context.Context, callback apiv1.CallbackConfig, payload JobCompletedData) error {
	event := d.newEvent("com.cova.job.completed", payload)
	return d.deliver(ctx, callback, event)
}

func (d *Dispatcher) newEvent(eventType string, payload any) Event {
	now := d.cfg.now().UTC()
	seq := atomic.AddUint64(&d.seq, 1)
	return Event{
		SpecVersion:     "1.0",
		ID:              fmt.Sprintf("evt_%d_%d", now.UnixNano(), seq),
		Type:            eventType,
		Source:          d.cfg.source,
		Time:            now.Format(time.RFC3339),
		DataContentType: "application/json",
		Data:            payload,
	}
}

func (d *Dispatcher) deliver(ctx context.Context, callback apiv1.CallbackConfig, event Event) error {
	url := strings.TrimSpace(callback.Url)
	if url == "" {
		return nil
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal webhook event: %w", err)
	}
	payloadHash := payloadSHA256(payload)
	jobID := extractJobID(event.Data)

	var lastErr error
	for attempt := 1; attempt <= d.cfg.maxAttempts; attempt++ {
		attemptTime := d.cfg.now().UTC()
		result := d.tryOnce(ctx, callback, url, payload)
		lastErr = result.err
		retryableFailure := lastErr != nil && attempt < d.cfg.maxAttempts && !isNonRetryable(lastErr)
		retryDelay := time.Duration(0)
		if retryableFailure {
			retryDelay = d.backoff(attempt)
		}

		var status *int
		if result.statusCode > 0 {
			status = &result.statusCode
		}
		var errStr *string
		if lastErr != nil {
			s := lastErr.Error()
			errStr = &s
		}
		var nextRetryAt *string
		if retryableFailure {
			retryAt := attemptTime.Add(retryDelay).Format(time.RFC3339Nano)
			nextRetryAt = &retryAt
		}
		d.persist(DeliveryRecord{
			DeliveryID:    fmt.Sprintf("%s.%d", event.ID, attempt),
			EventID:       event.ID,
			EventType:     event.Type,
			JobID:         jobID,
			CallbackURL:   url,
			Attempt:       attempt,
			MaxAttempts:   d.cfg.maxAttempts,
			Success:       lastErr == nil,
			HTTPStatus:    status,
			Error:         errStr,
			RequestedAt:   attemptTime.Format(time.RFC3339Nano),
			DurationMs:    result.duration.Milliseconds(),
			NextRetryAt:   nextRetryAt,
			PayloadSHA256: payloadHash,
		})

		if lastErr == nil {
			return nil
		}
		if attempt == d.cfg.maxAttempts || isNonRetryable(lastErr) {
			break
		}
		if waitErr := d.cfg.sleep(ctx, retryDelay); waitErr != nil {
			return fmt.Errorf("webhook retry canceled: %w", waitErr)
		}
	}
	return fmt.Errorf("webhook delivery failed after %d attempts: %w", d.cfg.maxAttempts, lastErr)
}

type attemptResult struct {
	statusCode int
	duration   time.Duration
	err        error
}

func (d *Dispatcher) tryOnce(ctx context.Context, callback apiv1.CallbackConfig, url string, payload []byte) attemptResult {
	started := time.Now()
	reqCtx, cancel := context.WithTimeout(ctx, d.cfg.requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return attemptResult{duration: time.Since(started), err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	if callback.Signing != nil {
		algorithm := strings.ToLower(strings.TrimSpace(callback.Signing.Algorithm))
		secret := strings.TrimSpace(callback.Signing.SecretRef)
		if algorithm != "hmac-sha256" {
			return attemptResult{duration: time.Since(started), err: nonRetryableError{err: fmt.Errorf("unsupported signing algorithm: %s", callback.Signing.Algorithm)}}
		}
		if secret == "" {
			return attemptResult{duration: time.Since(started), err: nonRetryableError{err: fmt.Errorf("empty signing secret")}}
		}
		ts := strconv.FormatInt(d.cfg.now().Unix(), 10)
		req.Header.Set("X-COVA-Timestamp", ts)
		req.Header.Set("X-COVA-Signature", "sha256="+computeHMAC(secret, ts, payload))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return attemptResult{duration: time.Since(started), err: fmt.Errorf("send request: %w", err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return attemptResult{statusCode: resp.StatusCode, duration: time.Since(started), err: fmt.Errorf("unexpected status code: %d", resp.StatusCode)}
	}
	return attemptResult{statusCode: resp.StatusCode, duration: time.Since(started), err: nil}
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	base := float64(d.cfg.initialBackoff)
	if base < 0 {
		base = 0
	}
	power := math.Pow(2, float64(attempt-1))
	backoff := time.Duration(base * power)
	if d.cfg.maxBackoff > 0 && backoff > d.cfg.maxBackoff {
		backoff = d.cfg.maxBackoff
	}
	if backoff <= 0 || d.cfg.jitterFraction <= 0 {
		return backoff
	}

	d.mu.Lock()
	r := d.rng.Float64()
	d.mu.Unlock()

	jitter := 1 + ((r*2)-1)*d.cfg.jitterFraction
	if jitter < 0 {
		jitter = 0
	}
	return time.Duration(float64(backoff) * jitter)
}

func computeHMAC(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *Dispatcher) persist(record DeliveryRecord) {
	_ = d.cfg.store.Write(record)
}

type nonRetryableError struct {
	err error
}

func (e nonRetryableError) Error() string {
	if e.err == nil {
		return "non-retryable error"
	}
	return e.err.Error()
}

func isNonRetryable(err error) bool {
	var nre nonRetryableError
	return errors.As(err, &nre)
}

func payloadSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func extractJobID(data any) string {
	switch v := data.(type) {
	case JobCompletedData:
		return v.JobID
	case *JobCompletedData:
		if v == nil {
			return ""
		}
		return v.JobID
	case map[string]any:
		if raw, ok := v["job_id"].(string); ok {
			return raw
		}
	}
	return ""
}
