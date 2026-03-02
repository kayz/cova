package webhook

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestVerifierSuccess(t *testing.T) {
	secret := "verify-secret"
	now := time.Unix(1700000000, 0).UTC()
	payload := []byte(`{"specversion":"1.0","id":"evt_ok","type":"com.cova.job.completed","source":"cova://orchestrator","time":"2026-02-27T00:00:00Z","datacontenttype":"application/json","data":{"job_id":"job_1","status":"succeeded","result_url":"/v1/assistant/jobs/job_1/result"}}`)
	ts := "1700000000"

	headers := http.Header{}
	headers.Set("X-COVA-Timestamp", ts)
	headers.Set("X-COVA-Signature", "sha256="+computeHMAC(secret, ts, payload))

	verifier := NewVerifier(secret,
		WithVerifyClock(func() time.Time { return now }),
		WithAllowedSkew(5*time.Minute),
		WithReplayTTL(10*time.Minute),
	)

	event, err := verifier.Verify(headers, payload)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if event.ID != "evt_ok" {
		t.Fatalf("unexpected event id: %s", event.ID)
	}
}

func TestVerifierInvalidSignature(t *testing.T) {
	secret := "verify-secret"
	now := time.Unix(1700000000, 0).UTC()
	payload := []byte(`{"id":"evt_invalid_sig"}`)
	ts := "1700000000"

	headers := http.Header{}
	headers.Set("X-COVA-Timestamp", ts)
	headers.Set("X-COVA-Signature", "sha256=deadbeef")

	verifier := NewVerifier(secret, WithVerifyClock(func() time.Time { return now }))
	_, err := verifier.Verify(headers, payload)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestVerifierTimestampOutOfWindow(t *testing.T) {
	secret := "verify-secret"
	now := time.Unix(1700000600, 0).UTC()
	payload := []byte(`{"id":"evt_old"}`)
	ts := "1700000000"

	headers := http.Header{}
	headers.Set("X-COVA-Timestamp", ts)
	headers.Set("X-COVA-Signature", "sha256="+computeHMAC(secret, ts, payload))

	verifier := NewVerifier(secret,
		WithVerifyClock(func() time.Time { return now }),
		WithAllowedSkew(2*time.Minute),
	)
	_, err := verifier.Verify(headers, payload)
	if !errors.Is(err, ErrTimestampOutOfWindow) {
		t.Fatalf("expected ErrTimestampOutOfWindow, got %v", err)
	}
}

func TestVerifierReplayDetected(t *testing.T) {
	secret := "verify-secret"
	now := time.Unix(1700000000, 0).UTC()
	payload := []byte(`{"id":"evt_replay"}`)
	ts := "1700000000"

	headers := http.Header{}
	headers.Set("X-COVA-Timestamp", ts)
	headers.Set("X-COVA-Signature", "sha256="+computeHMAC(secret, ts, payload))

	verifier := NewVerifier(secret,
		WithVerifyClock(func() time.Time { return now }),
		WithAllowedSkew(5*time.Minute),
		WithReplayTTL(10*time.Minute),
	)

	if _, err := verifier.Verify(headers, payload); err != nil {
		t.Fatalf("first verify failed: %v", err)
	}
	if _, err := verifier.Verify(headers, payload); !errors.Is(err, ErrReplayDetected) {
		t.Fatalf("expected ErrReplayDetected, got %v", err)
	}
}

