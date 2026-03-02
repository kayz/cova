package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthRequired(t *testing.T) {
	upstreamCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	mw, err := NewHandler(next, Config{
		AuthEnabled:      true,
		AuthTokens:       []string{"token-123"},
		RateLimitEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if upstreamCalled {
		t.Fatal("upstream should not be called")
	}
}

func TestAuthSuccess(t *testing.T) {
	upstreamCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	mw, err := NewHandler(next, Config{
		AuthEnabled:      true,
		AuthTokens:       []string{"token-123"},
		RateLimitEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req.Header.Set("Authorization", "Bearer token-123")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if !upstreamCalled {
		t.Fatal("upstream should be called")
	}
}

func TestAuthBindingInjectsTenantScope(t *testing.T) {
	var seenTenant string
	var seenProject string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTenant = r.Header.Get("X-Tenant-Id")
		seenProject = r.Header.Get("X-Project-Id")
		w.WriteHeader(http.StatusNoContent)
	})
	mw, err := NewHandler(next, Config{
		AuthEnabled: true,
		AuthBindings: []AuthBinding{
			{Token: "token-123", TenantID: "tenant_a", ProjectID: "proj_alpha"},
		},
		RateLimitEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req.Header.Set("Authorization", "Bearer token-123")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if seenTenant != "tenant_a" || seenProject != "proj_alpha" {
		t.Fatalf("unexpected tenant scope headers: tenant=%q project=%q", seenTenant, seenProject)
	}
}

func TestAuthBindingRejectsMismatchedTenantScope(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mw, err := NewHandler(next, Config{
		AuthEnabled: true,
		AuthBindings: []AuthBinding{
			{Token: "token-123", TenantID: "tenant_a", ProjectID: "proj_alpha"},
		},
		RateLimitEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("X-Tenant-Id", "tenant_b")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRateLimit(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	now := time.Unix(1700000000, 0).UTC()
	mw, err := NewHandler(next, Config{
		AuthEnabled:      false,
		RateLimitEnabled: true,
		RatePerSecond:    0,
		RateBurst:        1,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req1 := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req1.RemoteAddr = "127.0.0.1:9000"
	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("expected first request 204, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req2.RemoteAddr = "127.0.0.1:9000"
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request 429, got %d", rec2.Code)
	}
}

func TestValidationContentType(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mw, err := NewHandler(next, Config{
		AuthEnabled:      false,
		RateLimitEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/assistant/query", bytes.NewBufferString(`{"expert_id":"fi_cn_primary","question":"hi"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
}

func TestValidationBodyTooLarge(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mw, err := NewHandler(next, Config{
		AuthEnabled:      false,
		RateLimitEnabled: false,
		MaxBodyBytes:     8,
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/assistant/query", bytes.NewBufferString(`{"a":"very-long"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestMetricsSnapshot(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	current := time.Unix(1700000000, 0).UTC()
	nowFn := func() time.Time { return current }

	mw, err := NewHandler(next, Config{
		RateLimitEnabled: true,
		RatePerSecond:    100,
		RateBurst:        1,
		Now:              nowFn,
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req1 := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req1.RemoteAddr = "127.0.0.1:9000"
	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("expected first request 204, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req2.RemoteAddr = "127.0.0.1:9000"
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request 429, got %d", rec2.Code)
	}

	current = current.Add(1 * time.Second)
	snapshot := mw.MetricsSnapshot()
	if snapshot.RequestsTotal != 2 {
		t.Fatalf("expected requests_total=2, got %d", snapshot.RequestsTotal)
	}
	if snapshot.RateLimitRejected != 1 {
		t.Fatalf("expected rate_limit_rejected=1, got %d", snapshot.RateLimitRejected)
	}
	if snapshot.StatusCodeCount["204"] != 1 || snapshot.StatusCodeCount["429"] != 1 {
		t.Fatalf("unexpected status counts: %+v", snapshot.StatusCodeCount)
	}
	if snapshot.RequestsLast1m != 2 {
		t.Fatalf("expected requests_last_1m=2, got %d", snapshot.RequestsLast1m)
	}
}

func TestAccessLoggerCalled(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	records := make([]AccessLogRecord, 0, 2)

	mw, err := NewHandler(next, Config{
		AccessLogEnabled: true,
		AccessLogger: func(record AccessLogRecord) {
			records = append(records, record)
		},
	})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	req.Header.Set("X-Request-Id", "req_test")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 access log record, got %d", len(records))
	}
	if records[0].RequestID != "req_test" || records[0].Status != http.StatusNoContent || records[0].Outcome != "forwarded" {
		t.Fatalf("unexpected access log record: %+v", records[0])
	}
}

func TestMetricsHandler(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mw, err := NewHandler(next, Config{})
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}

	// Generate one request first.
	req := httptest.NewRequest(http.MethodGet, "/v1/assistant/jobs/job_1", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	mw.MetricsHandler().ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected metrics status 200, got %d", metricsRec.Code)
	}
	var payload MetricsSnapshot
	if err := json.NewDecoder(metricsRec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode metrics response failed: %v", err)
	}
	if payload.RequestsTotal < 1 {
		t.Fatalf("expected requests_total >= 1, got %d", payload.RequestsTotal)
	}
}

