package cova

import (
	"context"

	"cova/pkg/apiv1"
)

// CocoAdapter binds coco flows to the generic COVA Assistant API.
type CocoAdapter struct {
	client *Client
}

func NewCocoAdapter(client *Client) *CocoAdapter {
	return &CocoAdapter{client: client}
}

func (a *CocoAdapter) SubmitBrief(ctx context.Context, req apiv1.SubmitBriefRequest, meta AdapterMeta) (*apiv1.SubmitBriefResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.SubmitBriefJob(ctx, req, meta.RequestID, meta.IdempotencyKey, meta.TenantID, meta.ProjectID)
}

func (a *CocoAdapter) GetJobStatus(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.GetJobStatus(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *CocoAdapter) GetJobResult(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobResultResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.GetJobResult(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *CocoAdapter) CancelJob(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.CancelJob(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *CocoAdapter) ReplayJob(ctx context.Context, jobID string, meta AdapterMeta) (*apiv1.JobStatusResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.ReplayJob(ctx, jobID, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *CocoAdapter) Query(ctx context.Context, req apiv1.QueryRequest, meta AdapterMeta) (*apiv1.QueryResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.QueryExpert(ctx, req, meta.RequestID, meta.TenantID, meta.ProjectID)
}

func (a *CocoAdapter) ListExperts(ctx context.Context, meta AdapterMeta) (*apiv1.ExpertCapabilitiesResponse, *apiv1.Problem, error) {
	if err := meta.validate(); err != nil {
		return nil, nil, err
	}
	return a.client.GetExpertCapabilities(ctx, meta.RequestID, meta.TenantID, meta.ProjectID)
}
