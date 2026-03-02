package cova

import (
	"context"

	"cova/pkg/apiv1"
)

// OpenClawAdapter binds openclaw flows to the same Assistant API contract used by coco.
type OpenClawAdapter struct {
	client *Client
}

func NewOpenClawAdapter(client *Client) *OpenClawAdapter {
	return &OpenClawAdapter{client: client}
}

func (a *OpenClawAdapter) SubmitBrief(ctx context.Context, req apiv1.SubmitBriefRequest, meta AdapterMeta) (*apiv1.SubmitBriefResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.SubmitBriefJob(ctx, req, meta.RequestID, meta.IdempotencyKey, meta.TenantID, meta.ProjectID)
}

func (a *OpenClawAdapter) GetJobStatus(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.GetJobStatus(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *OpenClawAdapter) GetJobResult(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobResultResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.GetJobResult(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *OpenClawAdapter) CancelJob(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.CancelJob(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *OpenClawAdapter) ReplayJob(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.ReplayJob(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *OpenClawAdapter) Query(ctx context.Context, req apiv1.QueryRequest, meta AdapterMeta) (*apiv1.QueryResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.QueryExpert(ctx, req, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *OpenClawAdapter) ListExperts(ctx context.Context, meta AdapterMeta) (*apiv1.ExpertCapabilitiesResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.GetExpertCapabilities(ctx, meta.RequestID, meta.TenantID, meta.ProjectID)
}
