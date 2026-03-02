package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxBodyBytes = 1 << 20 // 1 MiB
	defaultRateRPS      = 20.0
	defaultRateBurst    = 40
)

// Config controls gateway middleware behaviors.
type Config struct {
	AuthEnabled bool
	AuthTokens  []string
	AuthBindings []AuthBinding

	RateLimitEnabled bool
	RatePerSecond    float64
	RateBurst        int

	MaxBodyBytes int64
	Now          func() time.Time

	AccessLogEnabled bool
	AccessLogger     func(AccessLogRecord)
}

// AuthBinding binds one bearer token to one tenant/project scope.
type AuthBinding struct {
	Token     string
	TenantID  string
	ProjectID string
}

type authPrincipal struct {
	TenantID  string
	ProjectID string
}

type Middleware struct {
	next http.Handler
	cfg  Config

	authTokens map[string]authPrincipal
	limiter    *ipLimiter
	seq        uint64

	metrics      *gatewayMetrics
	accessLogger func(AccessLogRecord)
}

func NewHandler(next http.Handler, cfg Config) (*Middleware, error) {
	if next == nil {
		return nil, errors.New("next handler is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if cfg.RatePerSecond <= 0 {
		cfg.RatePerSecond = defaultRateRPS
	}
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = defaultRateBurst
	}

	authTokens := make(map[string]authPrincipal, len(cfg.AuthTokens)+len(cfg.AuthBindings))
	for _, raw := range cfg.AuthTokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		authTokens[token] = authPrincipal{}
	}
	for _, item := range cfg.AuthBindings {
		token := strings.TrimSpace(item.Token)
		if token == "" {
			continue
		}
		tenantID := strings.TrimSpace(item.TenantID)
		projectID := strings.TrimSpace(item.ProjectID)
		if tenantID == "" || projectID == "" {
			return nil, errors.New("auth binding requires token + tenant_id + project_id")
		}
		authTokens[token] = authPrincipal{
			TenantID:  tenantID,
			ProjectID: projectID,
		}
	}
	if cfg.AuthEnabled && len(authTokens) == 0 {
		return nil, errors.New("auth is enabled but no auth tokens configured")
	}

	var limiter *ipLimiter
	if cfg.RateLimitEnabled {
		limiter = newIPLimiter(cfg.RatePerSecond, cfg.RateBurst, cfg.Now)
	}

	accessLogger := cfg.AccessLogger
	if accessLogger == nil {
		accessLogger = defaultAccessLogger
	}

	return &Middleware{
		next:         next,
		cfg:          cfg,
		authTokens:   authTokens,
		limiter:      limiter,
		metrics:      newGatewayMetrics(cfg.Now),
		accessLogger: accessLogger,
	}, nil
}

func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.injectRequestID(r)

	startedAt := m.cfg.Now().UTC()
	clientIP := clientKey(r)
	outcome := "forwarded"

	rec := &responseRecorder{ResponseWriter: w}
	defer func() {
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		finishedAt := m.cfg.Now().UTC()
		duration := finishedAt.Sub(startedAt)

		m.metrics.Record(status, outcome, finishedAt)

		if m.cfg.AccessLogEnabled {
			m.accessLogger(AccessLogRecord{
				Timestamp:    finishedAt.Format(time.RFC3339Nano),
				RequestID:    strings.TrimSpace(r.Header.Get("X-Request-Id")),
				Method:       r.Method,
				Path:         r.URL.Path,
				Status:       status,
				DurationMs:   duration.Milliseconds(),
				ClientIP:     clientIP,
				Outcome:      outcome,
				BytesWritten: rec.bytesWritten,
				UserAgent:    r.UserAgent(),
			})
		}
	}()

	if m.cfg.AuthEnabled {
		principal, err := m.checkAuth(r)
		if err != nil {
			outcome = "rejected_auth"
			writeProblem(rec, r, http.StatusUnauthorized, "unauthorized", "Unauthorized", err.Error())
			return
		}
		if err := m.injectTenantScope(r, principal); err != nil {
			outcome = "rejected_auth"
			writeProblem(rec, r, http.StatusUnauthorized, "unauthorized", "Unauthorized", err.Error())
			return
		}
	}

	if m.cfg.RateLimitEnabled {
		if !m.limiter.Allow(clientIP) {
			outcome = "rejected_rate_limit"
			rec.Header().Set("Retry-After", "1")
			writeProblem(rec, r, http.StatusTooManyRequests, "rate-limit-exceeded", "Too Many Requests", "rate limit exceeded")
			return
		}
	}

	if err := m.validateRequest(rec, r); err != nil {
		outcome = "rejected_validation"
		switch {
		case errors.Is(err, errUnsupportedMediaType):
			writeProblem(rec, r, http.StatusUnsupportedMediaType, "invalid-content-type", "Invalid content type", err.Error())
		case errors.Is(err, errPayloadTooLarge):
			writeProblem(rec, r, http.StatusRequestEntityTooLarge, "payload-too-large", "Payload too large", err.Error())
		default:
			writeProblem(rec, r, http.StatusBadRequest, "invalid-request", "Invalid request", err.Error())
		}
		return
	}

	m.next.ServeHTTP(rec, r)
}

func (m *Middleware) MetricsSnapshot() MetricsSnapshot {
	return m.metrics.Snapshot()
}

func (m *Middleware) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.metrics.Snapshot())
	})
}

func (m *Middleware) injectRequestID(r *http.Request) {
	if strings.TrimSpace(r.Header.Get("X-Request-Id")) != "" {
		return
	}
	seq := atomic.AddUint64(&m.seq, 1)
	id := "gw_" + m.cfg.Now().UTC().Format("20060102T150405.000000000") + "_" + strconv.FormatUint(seq, 10)
	r.Header.Set("X-Request-Id", id)
}

func (m *Middleware) checkAuth(r *http.Request) (authPrincipal, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return authPrincipal{}, errors.New("missing Authorization header")
	}
	parts := strings.Fields(raw)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return authPrincipal{}, errors.New("Authorization must use Bearer token")
	}
	token := parts[1]
	principal, ok := m.authTokens[token]
	if !ok {
		return authPrincipal{}, errors.New("invalid bearer token")
	}
	return principal, nil
}

func (m *Middleware) injectTenantScope(r *http.Request, principal authPrincipal) error {
	if principal.TenantID == "" && principal.ProjectID == "" {
		return nil
	}

	reqTenant := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	reqProject := strings.TrimSpace(r.Header.Get("X-Project-Id"))
	if reqTenant != "" && reqTenant != principal.TenantID {
		return errors.New("X-Tenant-Id does not match authenticated token scope")
	}
	if reqProject != "" && reqProject != principal.ProjectID {
		return errors.New("X-Project-Id does not match authenticated token scope")
	}
	r.Header.Set("X-Tenant-Id", principal.TenantID)
	r.Header.Set("X-Project-Id", principal.ProjectID)
	return nil
}

var (
	errUnsupportedMediaType = errors.New("content-type must be application/json")
	errPayloadTooLarge      = errors.New("request body exceeds gateway max size")
	errInvalidHeader        = errors.New("invalid header value")
)

func (m *Middleware) validateRequest(w http.ResponseWriter, r *http.Request) error {
	if len(r.Header.Get("X-Request-Id")) > 128 {
		return fmt.Errorf("%w: X-Request-Id too long", errInvalidHeader)
	}
	if len(r.Header.Get("Idempotency-Key")) > 256 {
		return fmt.Errorf("%w: Idempotency-Key too long", errInvalidHeader)
	}

	isJSONPost := (r.Method == http.MethodPost) && (r.URL.Path == "/v1/assistant/jobs" || r.URL.Path == "/v1/assistant/query")
	if isJSONPost {
		contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
		if !strings.HasPrefix(contentType, "application/json") {
			return errUnsupportedMediaType
		}
		if r.ContentLength > m.cfg.MaxBodyBytes {
			return errPayloadTooLarge
		}
		r.Body = http.MaxBytesReader(w, r.Body, m.cfg.MaxBodyBytes)
	}
	return nil
}

type problem struct {
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Status    int     `json:"status"`
	Detail    *string `json:"detail,omitempty"`
	Instance  *string `json:"instance,omitempty"`
	RequestID *string `json:"request_id,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	reqID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	resp := problem{
		Type:      "https://api.cova.example.com/errors/" + code,
		Title:     title,
		Status:    status,
		Detail:    stringPtr(detail),
		Instance:  stringPtr(r.URL.Path),
		RequestID: stringPtr(reqID),
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func stringPtr(v string) *string {
	return &v
}

func clientKey(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

type ipLimiter struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	now      func() time.Time
	entries  map[string]*bucket
	requests uint64
}

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

func newIPLimiter(rate float64, burst int, now func() time.Time) *ipLimiter {
	return &ipLimiter{
		rate:    rate,
		burst:   float64(burst),
		now:     now,
		entries: make(map[string]*bucket, 1024),
	}
}

func (l *ipLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	current := l.now()
	b, ok := l.entries[key]
	if !ok {
		l.entries[key] = &bucket{
			tokens:   l.burst - 1,
			last:     current,
			lastSeen: current,
		}
		l.cleanupIfNeeded(current)
		return true
	}

	elapsed := current.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.last = current
	b.lastSeen = current

	allowed := b.tokens >= 1
	if allowed {
		b.tokens -= 1
	}
	l.cleanupIfNeeded(current)
	return allowed
}

func (l *ipLimiter) cleanupIfNeeded(now time.Time) {
	l.requests++
	if l.requests%100 != 0 {
		return
	}
	const ttl = 10 * time.Minute
	for key, b := range l.entries {
		if now.Sub(b.lastSeen) > ttl {
			delete(l.entries, key)
		}
	}
}

// AccessLogRecord is one structured gateway access log line.
type AccessLogRecord struct {
	Timestamp    string `json:"timestamp"`
	RequestID    string `json:"request_id"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Status       int    `json:"status"`
	DurationMs   int64  `json:"duration_ms"`
	ClientIP     string `json:"client_ip"`
	Outcome      string `json:"outcome"`
	BytesWritten int64  `json:"bytes_written"`
	UserAgent    string `json:"user_agent,omitempty"`
}

func defaultAccessLogger(record AccessLogRecord) {
	raw, err := json.Marshal(record)
	if err != nil {
		log.Printf("gateway access log marshal failed: err=%v record=%+v", err, record)
		return
	}
	log.Print(string(raw))
}

type responseRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.status == 0 {
		r.status = statusCode
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytesWritten += int64(n)
	return n, err
}

type MetricsSnapshot struct {
	Timestamp          string            `json:"timestamp"`
	UptimeSeconds      float64           `json:"uptime_seconds"`
	RequestsTotal      uint64            `json:"requests_total"`
	RequestsLast1m     uint64            `json:"requests_last_1m"`
	QPSLast1m          float64           `json:"qps_last_1m"`
	AvgQPS             float64           `json:"avg_qps"`
	StatusCodeCount    map[string]uint64 `json:"status_code_count"`
	RateLimitRejected  uint64            `json:"rate_limit_rejected"`
	AuthRejected       uint64            `json:"auth_rejected"`
	ValidationRejected uint64            `json:"validation_rejected"`
}

type gatewayMetrics struct {
	mu sync.Mutex

	now       func() time.Time
	startedAt time.Time

	total      uint64
	statusCode map[int]uint64

	rateLimitRejected  uint64
	authRejected       uint64
	validationRejected uint64

	perSecond [60]metricBucket
}

type metricBucket struct {
	second int64
	count  uint64
}

func newGatewayMetrics(now func() time.Time) *gatewayMetrics {
	start := now().UTC()
	return &gatewayMetrics{
		now:        now,
		startedAt:  start,
		statusCode: make(map[int]uint64, 16),
	}
}

func (m *gatewayMetrics) Record(status int, outcome string, now time.Time) {
	if status == 0 {
		status = http.StatusOK
	}
	now = now.UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.total++
	m.statusCode[status]++
	switch outcome {
	case "rejected_rate_limit":
		m.rateLimitRejected++
	case "rejected_auth":
		m.authRejected++
	case "rejected_validation":
		m.validationRejected++
	}

	sec := now.Unix()
	idx := int(sec % int64(len(m.perSecond)))
	if m.perSecond[idx].second != sec {
		m.perSecond[idx].second = sec
		m.perSecond[idx].count = 0
	}
	m.perSecond[idx].count++
}

func (m *gatewayMetrics) Snapshot() MetricsSnapshot {
	now := m.now().UTC()
	nowSec := now.Unix()

	m.mu.Lock()
	defer m.mu.Unlock()

	recent := uint64(0)
	for _, bucket := range m.perSecond {
		if bucket.second == 0 {
			continue
		}
		if nowSec-bucket.second < 60 {
			recent += bucket.count
		}
	}

	uptime := now.Sub(m.startedAt).Seconds()
	if uptime < 1 {
		uptime = 1
	}
	window := math.Min(60, uptime)
	if window < 1 {
		window = 1
	}

	statusCopy := make(map[string]uint64, len(m.statusCode))
	for code, count := range m.statusCode {
		statusCopy[strconv.Itoa(code)] = count
	}

	return MetricsSnapshot{
		Timestamp:          now.Format(time.RFC3339Nano),
		UptimeSeconds:      now.Sub(m.startedAt).Seconds(),
		RequestsTotal:      m.total,
		RequestsLast1m:     recent,
		QPSLast1m:          float64(recent) / window,
		AvgQPS:             float64(m.total) / uptime,
		StatusCodeCount:    statusCopy,
		RateLimitRejected:  m.rateLimitRejected,
		AuthRejected:       m.authRejected,
		ValidationRejected: m.validationRejected,
	}
}
