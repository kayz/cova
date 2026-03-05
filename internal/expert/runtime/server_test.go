package runtime

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cova/internal/registry"
)

func TestHealthzAndCapabilities(t *testing.T) {
	server := NewServer(buildSnapshot(), WithAuthDisabled())
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	healthz := doRuntimeJSON(t, http.MethodGet, ts.URL+"/runtime/v1/healthz", nil, nil)
	if healthz.StatusCode != http.StatusOK {
		t.Fatalf("expected healthz=200, got %d", healthz.StatusCode)
	}

	caps := doRuntimeJSON(t, http.MethodGet, ts.URL+"/runtime/v1/capabilities", nil, nil)
	if caps.StatusCode != http.StatusOK {
		t.Fatalf("expected capabilities=200, got %d", caps.StatusCode)
	}
	var payload map[string]any
	mustDecodeRuntime(t, caps, &payload)
	if payload["expert_id"] != "fi_cn_primary" {
		t.Fatalf("unexpected expert_id: %v", payload["expert_id"])
	}
}

func TestCreateTaskSyncSucceeded(t *testing.T) {
	server := NewServer(buildSnapshot(), WithAuthDisabled())
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp := doRuntimeJSON(t, http.MethodPost, ts.URL+"/runtime/v1/tasks", map[string]any{
		"task_id":    "tsk_1",
		"job_id":     "job_1",
		"tenant_id":  "tenant_a",
		"project_id": "proj_a",
		"task_type":  "daily_brief",
		"input": map[string]any{
			"date": "2026-03-05",
		},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected create task=200, got %d", resp.StatusCode)
	}
	var payload taskResponse
	mustDecodeRuntime(t, resp, &payload)
	if payload.Status != "succeeded" || payload.Result == nil {
		t.Fatalf("unexpected task response: %+v", payload)
	}
}

func TestCreateTaskAsyncSendsSignedEvent(t *testing.T) {
	secret := "runtime-secret"
	callbackCh := make(chan struct {
		timestamp string
		signature string
		body      []byte
	}, 1)
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		callbackCh <- struct {
			timestamp string
			signature string
			body      []byte
		}{
			timestamp: r.Header.Get("X-COVA-Timestamp"),
			signature: r.Header.Get("X-COVA-Signature"),
			body:      raw,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(callbackSrv.Close)

	server := NewServer(
		buildSnapshot(),
		WithAuthDisabled(),
		WithDefaultAsync(true),
		WithEventSink(NewHTTPEventSink(callbackSrv.URL, secret, 3*time.Second)),
	)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp := doRuntimeJSON(t, http.MethodPost, ts.URL+"/runtime/v1/tasks", map[string]any{
		"task_id":    "tsk_async_1",
		"job_id":     "job_async_1",
		"tenant_id":  "tenant_a",
		"project_id": "proj_a",
		"task_type":  "daily_brief",
		"input": map[string]any{
			"date": "2026-03-05",
		},
	}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected create async task=202, got %d", resp.StatusCode)
	}

	select {
	case got := <-callbackCh:
		if got.timestamp == "" || got.signature == "" {
			t.Fatalf("missing callback signature headers: timestamp=%q signature=%q", got.timestamp, got.signature)
		}
		expect := "sha256=" + signPayload(secret, got.timestamp, got.body)
		if got.signature != expect {
			t.Fatalf("signature mismatch: expected %s got %s", expect, got.signature)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime callback not received")
	}
}

func buildSnapshot() *registry.Snapshot {
	return &registry.Snapshot{
		Experts: []registry.LoadedExpert{
			{
				Ref: registry.RegistryRef{
					ExpertID: "fi_cn_primary",
					Enabled:  true,
				},
				Definition: registry.ExpertDefinition{
					Metadata: registry.ExpertMetadata{
						ExpertID:   "fi_cn_primary",
						ExpertName: "FICC Observor CN Primary",
						ExpertType: "fixed_income",
						Version:    "1.0.0",
					},
					Spec: registry.ExpertSpec{
						Capabilities: []string{"daily_brief", "query"},
					},
				},
			},
		},
	}
}

func doRuntimeJSON(t *testing.T, method, url string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body failed: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build request failed: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func mustDecodeRuntime(t *testing.T, resp *http.Response, target any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
}
