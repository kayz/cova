package webhook

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONLStoreWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "webhook_deliveries.jsonl")
	store, err := NewJSONLStore(path)
	if err != nil {
		t.Fatalf("NewJSONLStore failed: %v", err)
	}

	record := DeliveryRecord{
		DeliveryID:    "evt_1.1",
		EventID:       "evt_1",
		EventType:     "com.cova.job.completed",
		JobID:         "job_1",
		CallbackURL:   "https://example.com/hook",
		Attempt:       1,
		MaxAttempts:   3,
		Success:       true,
		RequestedAt:   "2026-02-27T00:00:00Z",
		DurationMs:    12,
		PayloadSHA256: "abc",
	}

	if err := store.Write(record); err != nil {
		t.Fatalf("store.Write failed: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open persisted file failed: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("expected one jsonl line")
	}

	var got DeliveryRecord
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal persisted line failed: %v", err)
	}
	if got.DeliveryID != "evt_1.1" || got.JobID != "job_1" || got.CallbackURL != "https://example.com/hook" {
		t.Fatalf("unexpected persisted record: %+v", got)
	}
	if scanner.Scan() {
		t.Fatal("expected exactly one jsonl line")
	}

	list, err := store.ListByJobID(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("ListByJobID failed: %v", err)
	}
	if len(list) != 1 || list[0].DeliveryID != "evt_1.1" {
		t.Fatalf("unexpected list result: %+v", list)
	}
}

func TestMemoryStoreListByJobID(t *testing.T) {
	store := NewMemoryStore()
	_ = store.Write(DeliveryRecord{DeliveryID: "a.1", JobID: "job_a"})
	_ = store.Write(DeliveryRecord{DeliveryID: "a.2", JobID: "job_a"})
	_ = store.Write(DeliveryRecord{DeliveryID: "b.1", JobID: "job_b"})

	list, err := store.ListByJobID(context.Background(), "job_a")
	if err != nil {
		t.Fatalf("ListByJobID failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 records for job_a, got %d", len(list))
	}
}
