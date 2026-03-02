package webhook

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DeliveryRecord is one persisted webhook delivery attempt record.
type DeliveryRecord struct {
	DeliveryID    string  `json:"delivery_id"`
	EventID       string  `json:"event_id"`
	EventType     string  `json:"event_type"`
	JobID         string  `json:"job_id,omitempty"`
	CallbackURL   string  `json:"callback_url"`
	Attempt       int     `json:"attempt"`
	MaxAttempts   int     `json:"max_attempts"`
	Success       bool    `json:"success"`
	HTTPStatus    *int    `json:"http_status,omitempty"`
	Error         *string `json:"error,omitempty"`
	RequestedAt   string  `json:"requested_at"`
	DurationMs    int64   `json:"duration_ms"`
	NextRetryAt   *string `json:"next_retry_at,omitempty"`
	PayloadSHA256 string  `json:"payload_sha256"`
}

// Store persists webhook delivery records.
type Store interface {
	Write(record DeliveryRecord) error
}

// Reader loads persisted webhook delivery records.
type Reader interface {
	ListByJobID(ctx context.Context, jobID string) ([]DeliveryRecord, error)
}

type nopStore struct{}

func (nopStore) Write(record DeliveryRecord) error {
	_ = record
	return nil
}

// MemoryStore stores records in memory; useful for testing.
type MemoryStore struct {
	mu      sync.Mutex
	records []DeliveryRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records: make([]DeliveryRecord, 0, 16),
	}
}

func (s *MemoryStore) Write(record DeliveryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *MemoryStore) Records() []DeliveryRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeliveryRecord, len(s.records))
	copy(out, s.records)
	return out
}

func (s *MemoryStore) ListByJobID(ctx context.Context, jobID string) ([]DeliveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jobID = strings.TrimSpace(jobID)

	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeliveryRecord, 0, len(s.records))
	for _, rec := range s.records {
		if rec.JobID == jobID {
			out = append(out, rec)
		}
	}
	return out, nil
}

// JSONLStore persists records to newline-delimited JSON file.
type JSONLStore struct {
	mu   sync.Mutex
	path string
}

func NewJSONLStore(path string) (*JSONLStore, error) {
	path = filepath.Clean(path)
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &JSONLStore{path: path}, nil
}

func (s *JSONLStore) Write(record DeliveryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := f.Write(encoded); err != nil {
		return err
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		return err
	}
	return nil
}

func (s *JSONLStore) ListByJobID(ctx context.Context, jobID string) ([]DeliveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jobID = strings.TrimSpace(jobID)

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []DeliveryRecord{}, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	out := make([]DeliveryRecord, 0, 16)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec DeliveryRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.JobID == jobID {
			out = append(out, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
