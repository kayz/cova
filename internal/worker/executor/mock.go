package executor

import (
	"context"
	"fmt"
	"time"

	"cova/pkg/apiv1"
)

// Executor runs expert execution logic. It does not own job state transitions.
type Executor interface {
	ExecuteBrief(ctx context.Context, input BriefInput) (apiv1.ExpertResult, error)
}

type BriefInput struct {
	JobID      string
	TenantID   string
	ProjectID  string
	ExpertID   string
	ExpertName string
	ExpertType string
	Date       string
	TraceID    string
}

type mockConfig struct {
	stageDelay time.Duration
	now        func() time.Time
}

// Option customizes mock executor behavior.
type Option func(*mockConfig)

func WithStageDelay(delay time.Duration) Option {
	return func(c *mockConfig) {
		if delay > 0 {
			c.stageDelay = delay
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(c *mockConfig) {
		if now != nil {
			c.now = now
		}
	}
}

type MockExecutor struct {
	cfg mockConfig
}

func NewMockExecutor(opts ...Option) *MockExecutor {
	cfg := mockConfig{
		stageDelay: 200 * time.Millisecond,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &MockExecutor{cfg: cfg}
}

func (m *MockExecutor) ExecuteBrief(ctx context.Context, input BriefInput) (apiv1.ExpertResult, error) {
	if err := sleepWithContext(ctx, m.cfg.stageDelay); err != nil {
		return apiv1.ExpertResult{}, err
	}
	if err := sleepWithContext(ctx, m.cfg.stageDelay); err != nil {
		return apiv1.ExpertResult{}, err
	}

	now := m.cfg.now().UTC()
	title := fmt.Sprintf("%s market brief", input.Date)
	return apiv1.ExpertResult{
		Title:           &title,
		Answer:          fmt.Sprintf("Mock brief generated for expert %s on %s.", input.ExpertID, input.Date),
		Confidence:      0.82,
		FreshnessCutoff: now.Format(time.RFC3339),
		References: []apiv1.ReferenceRecord{
			{
				"kind":         "article",
				"ref_id":       "src_mock_001",
				"title":        "Mock Market Source",
				"uri":          "https://example.com/mock-source",
				"published_at": now.Add(-15 * time.Minute).Format(time.RFC3339),
			},
		},
		Limitations: []string{"mock runtime response"},
	}, nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
