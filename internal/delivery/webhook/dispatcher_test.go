package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cova/pkg/apiv1"
)

func TestDeliverJobCompletedSigned(t *testing.T) {
	secret := "test-secret"
	var seenTimestamp string
	var seenSignature string
	var seenPayload []byte
	called := make(chan struct{}, 1)

	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		seenTimestamp = r.Header.Get("X-COVA-Timestamp")
		seenSignature = r.Header.Get("X-COVA-Signature")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		seenPayload = raw
		w.WriteHeader(http.StatusNoContent)
		called <- struct{}{}
	}))
	defer cb.Close()

	now := time.Unix(1700000000, 0).UTC()
	dispatcher := NewDispatcher(nil,
		WithClock(func() time.Time { return now }),
		WithJitterFraction(0),
		WithMaxAttempts(1),
	)

	err := dispatcher.DeliverJobCompleted(context.Background(), apiv1.CallbackConfig{
		Url: cb.URL,
		Signing: &apiv1.CallbackSigning{
			Algorithm: "hmac-sha256",
			SecretRef: secret,
		},
	}, JobCompletedData{
		JobID:     "job_123",
		Status:    "succeeded",
		ResultURL: "/v1/assistant/jobs/job_123/result",
	})
	if err != nil {
		t.Fatalf("DeliverJobCompleted failed: %v", err)
	}

	select {
	case <-called:
	case <-time.After(1 * time.Second):
		t.Fatal("webhook endpoint was not called")
	}

	if seenTimestamp == "" || seenSignature == "" {
		t.Fatalf("expected signature headers, got timestamp=%q signature=%q", seenTimestamp, seenSignature)
	}

	expected := "sha256=" + computeHMAC(secret, seenTimestamp, seenPayload)
	if seenSignature != expected {
		t.Fatalf("signature mismatch, expected %q got %q", expected, seenSignature)
	}
}

func TestDeliverJobCompletedRetryThenSuccess(t *testing.T) {
	var attempts atomic.Int32

	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer cb.Close()

	dispatcher := NewDispatcher(nil,
		WithMaxAttempts(5),
		WithInitialBackoff(1*time.Millisecond),
		WithMaxBackoff(2*time.Millisecond),
		WithJitterFraction(0),
	)

	err := dispatcher.DeliverJobCompleted(context.Background(), apiv1.CallbackConfig{
		Url: cb.URL,
	}, JobCompletedData{
		JobID:     "job_123",
		Status:    "succeeded",
		ResultURL: "/v1/assistant/jobs/job_123/result",
	})
	if err != nil {
		t.Fatalf("expected retry eventually success, got: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestDeliverJobCompletedRetryExhausted(t *testing.T) {
	var attempts atomic.Int32

	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer cb.Close()

	dispatcher := NewDispatcher(nil,
		WithMaxAttempts(3),
		WithInitialBackoff(1*time.Millisecond),
		WithMaxBackoff(2*time.Millisecond),
		WithJitterFraction(0),
	)

	err := dispatcher.DeliverJobCompleted(context.Background(), apiv1.CallbackConfig{
		Url: cb.URL,
	}, JobCompletedData{
		JobID:     "job_123",
		Status:    "succeeded",
		ResultURL: "/v1/assistant/jobs/job_123/result",
	})
	if err == nil {
		t.Fatal("expected exhausted retry error")
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestDeliverJobCompletedPersistsRecords(t *testing.T) {
	store := NewMemoryStore()
	var attempts atomic.Int32

	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer cb.Close()

	dispatcher := NewDispatcher(nil,
		WithStore(store),
		WithMaxAttempts(3),
		WithInitialBackoff(1*time.Millisecond),
		WithMaxBackoff(1*time.Millisecond),
		WithJitterFraction(0),
	)

	err := dispatcher.DeliverJobCompleted(context.Background(), apiv1.CallbackConfig{
		Url: cb.URL,
	}, JobCompletedData{
		JobID:     "job_123",
		Status:    "succeeded",
		ResultURL: "/v1/assistant/jobs/job_123/result",
	})
	if err != nil {
		t.Fatalf("DeliverJobCompleted failed: %v", err)
	}

	records := store.Records()
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].Success {
		t.Fatal("first attempt should be failed")
	}
	if records[0].HTTPStatus == nil || *records[0].HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("first attempt status mismatch: %+v", records[0].HTTPStatus)
	}
	if records[0].NextRetryAt == nil {
		t.Fatal("first attempt should include next_retry_at")
	}
	if !records[1].Success {
		t.Fatal("second attempt should be success")
	}
	if records[1].HTTPStatus == nil || *records[1].HTTPStatus != http.StatusNoContent {
		t.Fatalf("second attempt status mismatch: %+v", records[1].HTTPStatus)
	}
	if records[1].NextRetryAt != nil {
		t.Fatal("successful attempt should not include next_retry_at")
	}
	if records[0].JobID != "job_123" || records[1].JobID != "job_123" {
		t.Fatalf("job_id not persisted: %+v", records)
	}
}

func TestDeliverJobCompletedNonRetryableErrorPersistsSingleRecord(t *testing.T) {
	store := NewMemoryStore()
	var calls atomic.Int32

	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer cb.Close()

	dispatcher := NewDispatcher(nil,
		WithStore(store),
		WithMaxAttempts(5),
		WithJitterFraction(0),
	)

	err := dispatcher.DeliverJobCompleted(context.Background(), apiv1.CallbackConfig{
		Url: cb.URL,
		Signing: &apiv1.CallbackSigning{
			Algorithm: "rsa-sha256",
			SecretRef: "secret",
		},
	}, JobCompletedData{
		JobID:     "job_123",
		Status:    "succeeded",
		ResultURL: "/v1/assistant/jobs/job_123/result",
	})
	if err == nil {
		t.Fatal("expected non-retryable error")
	}
	if !strings.Contains(err.Error(), "unsupported signing algorithm") {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("callback should not be called on invalid signing config, got %d", calls.Load())
	}

	records := store.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Attempt != 1 || records[0].MaxAttempts != 5 {
		t.Fatalf("unexpected attempt metadata: %+v", records[0])
	}
	if records[0].NextRetryAt != nil {
		t.Fatal("non-retryable error should not include next_retry_at")
	}
}

