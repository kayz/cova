package jobstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cova/pkg/apiv1"
)

// State is the persisted worker state.
type State struct {
	NextID      uint64                      `json:"next_id"`
	Jobs        map[string]Job              `json:"jobs"`
	Idempotency map[string]IdempotencyEntry `json:"idempotency"`
	DeadLetters map[string]DeadLetterEntry  `json:"dead_letters,omitempty"`
}

// Job is the persisted form of one async job.
type Job struct {
	ID          string                `json:"id"`
	TenantID    string                `json:"tenant_id,omitempty"`
	ProjectID   string                `json:"project_id,omitempty"`
	ExpertID    string                `json:"expert_id"`
	ExpertName  string                `json:"expert_name"`
	ExpertType  string                `json:"expert_type"`
	Date        string                `json:"date"`
	Status      string                `json:"status"`
	Progress    int                   `json:"progress"`
	SubmittedAt time.Time             `json:"submitted_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
	Attempt     int                   `json:"attempt"`
	MaxAttempts int                   `json:"max_attempts,omitempty"`
	ExpiresAt   *time.Time            `json:"expires_at,omitempty"`
	LastError   *string               `json:"last_error,omitempty"`
	Result      *apiv1.ExpertResult   `json:"result,omitempty"`
	Callback    *apiv1.CallbackConfig `json:"callback,omitempty"`
}

// IdempotencyEntry is the persisted form of idempotency lookup.
type IdempotencyEntry struct {
	PayloadHash string `json:"payload_hash"`
	JobID       string `json:"job_id"`
}

// DeadLetterEntry records jobs that exhausted retries.
type DeadLetterEntry struct {
	JobID       string    `json:"job_id"`
	TenantID    string    `json:"tenant_id"`
	ProjectID   string    `json:"project_id"`
	Reason      string    `json:"reason"`
	Attempts    int       `json:"attempts"`
	FailedAt    time.Time `json:"failed_at"`
	LastError   string    `json:"last_error,omitempty"`
	FinalStatus string    `json:"final_status"`
}

// Store loads and saves worker state.
type Store interface {
	Load(ctx context.Context) (State, error)
	Save(ctx context.Context, state State) error
}

type nopStore struct{}

func (nopStore) Load(ctx context.Context) (State, error) {
	_ = ctx
	return emptyState(), nil
}

func (nopStore) Save(ctx context.Context, state State) error {
	_ = ctx
	_ = state
	return nil
}

// NewNopStore returns a no-op in-memory store.
func NewNopStore() Store {
	return nopStore{}
}

// JSONFileStore persists state to one JSON file.
type JSONFileStore struct {
	mu   sync.Mutex
	path string
}

func NewJSONFileStore(path string) (*JSONFileStore, error) {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &JSONFileStore{path: path}, nil
}

func (s *JSONFileStore) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyState(), nil
		}
		return State{}, err
	}
	if len(raw) == 0 {
		return emptyState(), nil
	}

	var out State
	if err := json.Unmarshal(raw, &out); err != nil {
		return State{}, err
	}
	if out.Jobs == nil {
		out.Jobs = map[string]Job{}
	}
	if out.Idempotency == nil {
		out.Idempotency = map[string]IdempotencyEntry{}
	}
	if out.DeadLetters == nil {
		out.DeadLetters = map[string]DeadLetterEntry{}
	}
	return out, nil
}

func (s *JSONFileStore) Save(ctx context.Context, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if state.Jobs == nil {
		state.Jobs = map[string]Job{}
	}
	if state.Idempotency == nil {
		state.Idempotency = map[string]IdempotencyEntry{}
	}
	if state.DeadLetters == nil {
		state.DeadLetters = map[string]DeadLetterEntry{}
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}

	_ = os.Remove(s.path)
	return os.Rename(tmp, s.path)
}

func emptyState() State {
	return State{
		NextID:      0,
		Jobs:        map[string]Job{},
		Idempotency: map[string]IdempotencyEntry{},
		DeadLetters: map[string]DeadLetterEntry{},
	}
}
