package orchestrator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	deliverywebhook "cova/internal/delivery/webhook"
	"cova/internal/registry"
	workerexecutor "cova/internal/worker/executor"
	"cova/pkg/apiv1"
)

type flakyExecutor struct {
	mu        sync.Mutex
	failUntil int
	calls     int
}

type acceptedExecutor struct{}

func (acceptedExecutor) ExecuteBrief(ctx context.Context, input workerexecutor.BriefInput) (apiv1.ExpertResult, error) {
	_ = ctx
	_ = input
	return apiv1.ExpertResult{}, workerexecutor.ErrAsyncAccepted
}

func (e *flakyExecutor) ExecuteBrief(ctx context.Context, input workerexecutor.BriefInput) (apiv1.ExpertResult, error) {
	_ = ctx
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.calls <= e.failUntil {
		return apiv1.ExpertResult{}, fmt.Errorf("forced failure %d", e.calls)
	}
	now := time.Now().UTC()
	title := "replayed brief"
	return apiv1.ExpertResult{
		Title:           &title,
		Answer:          "ok",
		Confidence:      0.8,
		FreshnessCutoff: now.Format(time.RFC3339),
	}, nil
}

func TestSubmitStatusResultLifecycle(t *testing.T) {
	server := NewServer(
		buildSnapshot(),
		WithExecutor(workerexecutor.NewMockExecutor(workerexecutor.WithStageDelay(1*time.Millisecond))),
	)
	t.Cleanup(server.Close)

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}, map[string]string{
		"Idempotency-Key": "orchestrator-lifecycle",
	})
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)
	if submitResp.JobId == "" {
		t.Fatal("job_id should not be empty")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusResp := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId, nil, nil)
		if statusResp.StatusCode != http.StatusOK {
			t.Fatalf("expected status=200, got %d", statusResp.StatusCode)
		}
		var status apiv1.JobStatusResponse
		mustDecode(t, statusResp, &status)

		resultResp := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId+"/result", nil, nil)
		if resultResp.StatusCode == http.StatusConflict {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if resultResp.StatusCode != http.StatusOK {
			t.Fatalf("expected result=200/409, got %d", resultResp.StatusCode)
		}
		var result apiv1.JobResultResponse
		mustDecode(t, resultResp, &result)
		if result.Status != statusSucceeded {
			t.Fatalf("expected succeeded status, got %s", result.Status)
		}
		return
	}

	t.Fatal("job result not ready before timeout")
}

func TestIdempotencyConflict(t *testing.T) {
	server := NewServer(
		buildSnapshot(),
		WithExecutor(workerexecutor.NewMockExecutor(workerexecutor.WithStageDelay(1*time.Millisecond))),
	)
	t.Cleanup(server.Close)

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	headers := map[string]string{"Idempotency-Key": "orchestrator-idem"}
	first := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}, headers)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("expected first submit=202, got %d", first.StatusCode)
	}

	second := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-28",
	}, headers)
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("expected second submit=409, got %d", second.StatusCode)
	}
}

func TestGetJobDeliveries(t *testing.T) {
	deliveries := deliverywebhook.NewMemoryStore()
	done := make(chan struct{}, 1)
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		w.WriteHeader(http.StatusNoContent)
		done <- struct{}{}
	}))
	t.Cleanup(callbackSrv.Close)

	dispatcher := deliverywebhook.NewDispatcher(nil,
		deliverywebhook.WithStore(deliveries),
		deliverywebhook.WithMaxAttempts(1),
		deliverywebhook.WithJitterFraction(0),
	)

	server := NewServer(
		buildSnapshot(),
		WithExecutor(workerexecutor.NewMockExecutor(workerexecutor.WithStageDelay(1*time.Millisecond))),
		WithWebhookDispatcher(dispatcher),
		WithWebhookDeliveryReader(deliveries),
	)
	t.Cleanup(server.Close)

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
		"callback": map[string]any{
			"url": callbackSrv.URL,
		},
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("callback not received")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		deliveryResp := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId+"/deliveries", nil, nil)
		if deliveryResp.StatusCode != http.StatusOK {
			t.Fatalf("expected deliveries=200, got %d", deliveryResp.StatusCode)
		}
		var payload apiv1.JobDeliveriesResponse
		mustDecode(t, deliveryResp, &payload)
		if payload.JobId != submitResp.JobId {
			t.Fatalf("job_id mismatch: got %s want %s", payload.JobId, submitResp.JobId)
		}
		if len(payload.Deliveries) >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected at least 1 delivery record before timeout")
}

func TestCancelJob(t *testing.T) {
	server := NewServer(
		buildSnapshot(),
		WithExecutor(workerexecutor.NewMockExecutor(workerexecutor.WithStageDelay(50*time.Millisecond))),
	)
	t.Cleanup(server.Close)

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	cancel := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId+"/cancel", nil, nil)
	if cancel.StatusCode != http.StatusAccepted {
		t.Fatalf("expected cancel=202, got %d", cancel.StatusCode)
	}

	status := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId, nil, nil)
	if status.StatusCode != http.StatusOK {
		t.Fatalf("expected status=200, got %d", status.StatusCode)
	}
	var statusResp apiv1.JobStatusResponse
	mustDecode(t, status, &statusResp)
	if statusResp.Status != apiv1.JobStatus(statusCanceled) {
		t.Fatalf("expected status canceled, got %s", statusResp.Status)
	}
}

func TestRetryAndReplayJob(t *testing.T) {
	exec := &flakyExecutor{failUntil: 10}
	server := NewServer(
		buildSnapshot(),
		WithExecutor(exec),
		WithMaxAttempts(2),
	)
	t.Cleanup(server.Close)

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId, nil, nil)
		if status.StatusCode != http.StatusOK {
			t.Fatalf("expected status=200, got %d", status.StatusCode)
		}
		var statusResp apiv1.JobStatusResponse
		mustDecode(t, status, &statusResp)
		if statusResp.Status == apiv1.JobStatus(statusFailed) {
			if statusResp.Attempt != 2 {
				t.Fatalf("expected attempt=2, got %d", statusResp.Attempt)
			}
			goto replay
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not reach failed before timeout")

replay:
	replayResp := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId+"/replay", nil, nil)
	if replayResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected replay=202, got %d", replayResp.StatusCode)
	}
	var replayStatus apiv1.JobStatusResponse
	mustDecode(t, replayResp, &replayStatus)
	if replayStatus.Attempt != 1 {
		t.Fatalf("expected replayed attempt reset to 1, got %d", replayStatus.Attempt)
	}
}

func TestJobExpires(t *testing.T) {
	server := NewServer(
		buildSnapshot(),
		WithExecutor(workerexecutor.NewMockExecutor(workerexecutor.WithStageDelay(10*time.Millisecond))),
		WithJobTTL(1*time.Millisecond),
	)
	t.Cleanup(server.Close)

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId, nil, nil)
		if status.StatusCode != http.StatusOK {
			t.Fatalf("expected status=200, got %d", status.StatusCode)
		}
		var statusResp apiv1.JobStatusResponse
		mustDecode(t, status, &statusResp)
		if statusResp.Status == apiv1.JobStatus(statusExpired) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not reach expired before timeout")
}

func TestMetricsAndHealthHandlers(t *testing.T) {
	server := NewServer(
		buildSnapshot(),
		WithExecutor(workerexecutor.NewMockExecutor(workerexecutor.WithStageDelay(1*time.Millisecond))),
	)
	t.Cleanup(server.Close)

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	server.MetricsHandler().ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected metrics=200, got %d", metricsRec.Code)
	}
	var snapshot MetricsSnapshot
	if err := json.NewDecoder(metricsRec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode metrics failed: %v", err)
	}
	if snapshot.ByStatus == nil || snapshot.ByTenant == nil {
		t.Fatalf("unexpected metrics payload: %+v", snapshot)
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	server.HealthHandler().ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected health=200, got %d", healthRec.Code)
	}
}

func TestExpertEventCompletesAsyncAcceptedJob(t *testing.T) {
	server := NewServer(
		buildSnapshot(),
		WithExecutor(acceptedExecutor{}),
	)
	t.Cleanup(server.Close)

	mux := http.NewServeMux()
	mux.Handle("/v1/expert/events", server.ExpertEventsHandler())
	mux.Handle("/", server)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-03-05",
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	time.Sleep(20 * time.Millisecond)

	eventResp := doJSON(t, http.MethodPost, ts.URL+"/v1/expert/events", map[string]any{
		"specversion":     "1.0",
		"id":              "evt_1",
		"type":            "com.cova.expert.task.completed",
		"source":          "expert://fi_cn_primary",
		"time":            "2026-03-05T00:00:00Z",
		"datacontenttype": "application/json",
		"data": map[string]any{
			"task_id": submitResp.JobId,
			"job_id":  submitResp.JobId,
			"status":  "succeeded",
			"result": map[string]any{
				"answer":           "done",
				"confidence":       0.91,
				"freshness_cutoff": "2026-03-05T00:00:00Z",
			},
		},
	}, nil)
	if eventResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected event=202, got %d", eventResp.StatusCode)
	}
	eventResp.Body.Close()

	resultResp := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId+"/result", nil, nil)
	if resultResp.StatusCode != http.StatusOK {
		t.Fatalf("expected result=200, got %d", resultResp.StatusCode)
	}
	var result apiv1.JobResultResponse
	mustDecode(t, resultResp, &result)
	if result.Result.Answer != "done" {
		t.Fatalf("unexpected result answer: %s", result.Result.Answer)
	}
}

func TestExpertEventRequiresValidSignatureWhenVerifierEnabled(t *testing.T) {
	secret := "event-secret"
	server := NewServer(
		buildSnapshot(),
		WithExecutor(acceptedExecutor{}),
		WithExpertEventVerifier(deliverywebhook.NewVerifier(secret)),
	)
	t.Cleanup(server.Close)

	mux := http.NewServeMux()
	mux.Handle("/v1/expert/events", server.ExpertEventsHandler())
	mux.Handle("/", server)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-03-05",
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	body, err := json.Marshal(map[string]any{
		"specversion":     "1.0",
		"id":              "evt_signed_1",
		"type":            "com.cova.expert.task.completed",
		"source":          "expert://fi_cn_primary",
		"time":            "2026-03-05T00:00:00Z",
		"datacontenttype": "application/json",
		"data": map[string]any{
			"task_id": submitResp.JobId,
			"job_id":  submitResp.JobId,
			"status":  "succeeded",
			"result": map[string]any{
				"answer":           "done",
				"confidence":       0.91,
				"freshness_cutoff": "2026-03-05T00:00:00Z",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal event body: %v", err)
	}

	unsignedReq, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/expert/events", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build unsigned request: %v", err)
	}
	unsignedReq.Header.Set("Content-Type", "application/json")
	unsignedResp, err := http.DefaultClient.Do(unsignedReq)
	if err != nil {
		t.Fatalf("send unsigned request: %v", err)
	}
	if unsignedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unsigned event=401, got %d", unsignedResp.StatusCode)
	}
	unsignedResp.Body.Close()

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := signEvent(secret, timestamp, body)
	signedReq, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/expert/events", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build signed request: %v", err)
	}
	signedReq.Header.Set("Content-Type", "application/json")
	signedReq.Header.Set("X-COVA-Timestamp", timestamp)
	signedReq.Header.Set("X-COVA-Signature", "sha256="+signature)
	signedResp, err := http.DefaultClient.Do(signedReq)
	if err != nil {
		t.Fatalf("send signed request: %v", err)
	}
	if signedResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected signed event=202, got %d", signedResp.StatusCode)
	}
	signedResp.Body.Close()
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

func doJSON(t *testing.T, method, url string, body any, headers map[string]string) *http.Response {
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
	req.Header.Set("X-Tenant-Id", "tenant_test")
	req.Header.Set("X-Project-Id", "project_test")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func mustDecode(t *testing.T, resp *http.Response, target any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
}

func signEvent(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
