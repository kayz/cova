package jobstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, errors.New("postgres dsn is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	store := &PostgresStore{pool: pool}
	if err := store.init(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) init(ctx context.Context) error {
	ddl := `
CREATE TABLE IF NOT EXISTS cova_job_state (
	state_id SMALLINT PRIMARY KEY,
	state JSONB NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`
	if _, err := s.pool.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("init cova_job_state: %w", err)
	}
	return nil
}

func (s *PostgresStore) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT state FROM cova_job_state WHERE state_id=1`).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return emptyState(), nil
		}
		return State{}, fmt.Errorf("load state: %w", err)
	}
	if len(raw) == 0 {
		return emptyState(), nil
	}

	var out State
	if err := json.Unmarshal(raw, &out); err != nil {
		return State{}, fmt.Errorf("decode state: %w", err)
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

func (s *PostgresStore) Save(ctx context.Context, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		return fmt.Errorf("encode state: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
INSERT INTO cova_job_state(state_id, state, updated_at)
VALUES (1, $1::jsonb, NOW())
ON CONFLICT (state_id) DO UPDATE
SET state = EXCLUDED.state, updated_at = NOW()`, raw)
	if err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

func (s *PostgresStore) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}
