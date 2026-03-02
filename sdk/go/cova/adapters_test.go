package cova

import (
	"context"
	"testing"

	"cova/pkg/apiv1"
)

func TestAdapterMetaValidate(t *testing.T) {
	meta := AdapterMeta{
		TenantID:  "tenant_a",
		ProjectID: "proj_alpha",
	}
	if err := meta.validate(); err != nil {
		t.Fatalf("expected valid meta, got %v", err)
	}
	if err := (AdapterMeta{}).validate(); err == nil {
		t.Fatal("expected validation error for empty meta")
	}
}

func TestCocoAdapterValidatesMetaBeforeCall(t *testing.T) {
	adapter := NewCocoAdapter(nil)
	_, _, err := adapter.SubmitBrief(context.Background(), apiv1.SubmitBriefRequest{
		ExpertId: "fi_cn_primary",
		Date:     "2026-03-02",
	}, AdapterMeta{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestOpenClawAdapterValidatesMetaBeforeCall(t *testing.T) {
	adapter := NewOpenClawAdapter(nil)
	_, _, err := adapter.Query(context.Background(), apiv1.QueryRequest{
		ExpertId: "fi_cn_primary",
		Question: "hi",
	}, AdapterMeta{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
