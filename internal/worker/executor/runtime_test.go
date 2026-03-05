package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeExecutorSyncSucceeded(t *testing.T) {
	runtimeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/v1/tasks" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "succeeded",
			"result": map[string]any{
				"answer":           "ok",
				"confidence":       0.9,
				"freshness_cutoff": "2026-03-05T00:00:00Z",
			},
		})
	}))
	t.Cleanup(runtimeSrv.Close)

	exec := NewRuntimeExecutor(runtimeSrv.URL)
	result, err := exec.ExecuteBrief(context.Background(), BriefInput{
		JobID:     "job_1",
		TenantID:  "tenant_a",
		ProjectID: "project_a",
		Date:      "2026-03-05",
	})
	if err != nil {
		t.Fatalf("ExecuteBrief failed: %v", err)
	}
	if result.Answer != "ok" {
		t.Fatalf("unexpected result answer: %s", result.Answer)
	}
}

func TestRuntimeExecutorAsyncAccepted(t *testing.T) {
	runtimeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/v1/tasks" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "accepted",
			"expert_task_id": "exp_t_1",
		})
	}))
	t.Cleanup(runtimeSrv.Close)

	exec := NewRuntimeExecutor(runtimeSrv.URL, WithRuntimeAsyncMode(true))
	_, err := exec.ExecuteBrief(context.Background(), BriefInput{
		JobID:     "job_1",
		TenantID:  "tenant_a",
		ProjectID: "project_a",
		Date:      "2026-03-05",
	})
	if err != ErrAsyncAccepted {
		t.Fatalf("expected ErrAsyncAccepted, got %v", err)
	}
}
