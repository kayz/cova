package jobstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONFileStoreLoadMissingReturnsEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs_state.json")
	store, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("NewJSONFileStore failed: %v", err)
	}

	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if state.NextID != 0 || len(state.Jobs) != 0 || len(state.Idempotency) != 0 {
		t.Fatalf("unexpected empty state: %+v", state)
	}
}

func TestJSONFileStoreSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs_state.json")
	store, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("NewJSONFileStore failed: %v", err)
	}

	now := time.Date(2026, 2, 27, 20, 0, 0, 0, time.UTC)
	in := State{
		NextID: 7,
		Jobs: map[string]Job{
			"job_7": {
				ID:          "job_7",
				ExpertID:    "fi_cn_primary",
				ExpertName:  "FICC",
				ExpertType:  "fixed_income",
				Date:        "2026-02-27",
				Status:      "succeeded",
				Progress:    100,
				SubmittedAt: now,
				UpdatedAt:   now.Add(2 * time.Minute),
				Attempt:     1,
			},
		},
		Idempotency: map[string]IdempotencyEntry{
			"idem-key": {
				PayloadHash: "hash",
				JobID:       "job_7",
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
		t.Fatalf("next_id mismatch: got %d want %d", out.NextID, in.NextID)
	}
	if len(out.Jobs) != 1 || len(out.Idempotency) != 1 {
		t.Fatalf("unexpected maps: jobs=%d idempotency=%d", len(out.Jobs), len(out.Idempotency))
	}
	job := out.Jobs["job_7"]
	if job.ExpertID != "fi_cn_primary" || job.Status != "succeeded" || job.Progress != 100 {
		t.Fatalf("unexpected job content: %+v", job)
	}
	if out.Idempotency["idem-key"].JobID != "job_7" {
		t.Fatalf("unexpected idempotency content: %+v", out.Idempotency["idem-key"])
	}
}
