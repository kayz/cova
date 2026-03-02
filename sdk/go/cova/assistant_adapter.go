package cova

import (
	"context"
	"errors"
	"strings"

	"cova/pkg/apiv1"
)

// AdapterMeta carries caller scope and request metadata shared by all client adapters.
type AdapterMeta struct {
	TenantID       string
	ProjectID      string
	RequestID      string
	IdempotencyKey string
}

func (m AdapterMeta) validate() error {
	if strings.TrimSpace(m.TenantID) == "" {
		return errors.New("tenant_id is required")
	}
	if strings.TrimSpace(m.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	return nil
}

// AssistantAdapter is the minimal stable contract for assistant-side integrations.
type AssistantAdapter interface {
	SubmitBrief(ctx context.Context, req apiv1.SubmitBriefRequest, meta AdapterMeta) (*apiv1.SubmitBriefResponse, *apiv1.Problem, error)
	GetJobStatus(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error)
	GetJobResult(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobResultResponse, *apiv1.Problem, error)
	CancelJob(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error)
	ReplayJob(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error)
	Query(ctx context.Context, req apiv1.QueryRequest, meta AdapterMeta) (*apiv1.QueryResponse, *apiv1.Problem, error)
	ListExperts(ctx context.Context, meta AdapterMeta) (*apiv1.ExpertCapabilitiesResponse, *apiv1.Problem, error)
}
