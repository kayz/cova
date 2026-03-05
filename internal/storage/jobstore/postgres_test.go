package jobstore

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPostgresStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("COVA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("COVA_TEST_POSTGRES_DSN not set")
	}

	store, err := NewPostgresStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore failed: %v", err)
	}
	t.Cleanup(store.Close)

	now := time.Date(2026, 3, 5, 1, 2, 3, 0, time.UTC)
	in := State{
		NextID: 5,
		Jobs: map[string]Job{
			"job_5": {
				ID:          "job_5",
				TenantID:    "tenant_a",
				ProjectID:   "project_a",
				ExpertID:    "fi_cn_primary",
				ExpertName:  "FICC",
				ExpertType:  "fixed_income",
				Date:        "2026-03-05",
				Status:      "queued",
				Progress:    0,
				SubmittedAt: now,
				UpdatedAt:   now,
				Attempt:     1,
			},
		},
		Idempotency: map[string]IdempotencyEntry{
			"tenant_a::project_a::idem_1": {
				PayloadHash: "hash_1",
				JobID:       "job_5",
			},
		},
	}

	if err := store.Save(context.Background(), in); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	out, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if out.NextID != in.NextID {
		t.Fatalf("next_id mismatch: got=%d want=%d", out.NextID, in.NextID)
	}
	if out.Jobs["job_5"].TenantID != "tenant_a" {
		t.Fatalf("unexpected state payload: %+v", out.Jobs["job_5"])
	}
}
