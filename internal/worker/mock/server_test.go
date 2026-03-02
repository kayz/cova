package mock

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	deliverywebhook "cova/internal/delivery/webhook"
	"cova/internal/registry"
	"cova/internal/storage/jobstore"
	"cova/pkg/apiv1"
)

func TestSubmitStatusResultLifecycle(t *testing.T) {
	server := NewServer(buildSnapshot(), WithProcessDelay(5*time.Millisecond))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	submitBody := map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}
	resp := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", submitBody, map[string]string{
		"Idempotency-Key": "idem-lifecycle",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	var submit apiv1.SubmitBriefResponse
	mustDecode(t, resp, &submit)
	if submit.JobId == "" {
		t.Fatal("job_id should not be empty")
	}
	if submit.Status != apiv1.JobStatus(statusQueued) {
		t.Fatalf("submit status must be queued, got %s", submit.Status)
	}

	statusResp := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submit.JobId, nil, nil)
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on status endpoint, got %d", statusResp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultResp := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submit.JobId+"/result", nil, nil)
		if resultResp.StatusCode == http.StatusConflict {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if resultResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200/409 on result endpoint, got %d", resultResp.StatusCode)
		}

		var result apiv1.JobResultResponse
		mustDecode(t, resultResp, &result)
		if result.Status != statusSucceeded {
			t.Fatalf("expected succeeded status, got %s", result.Status)
		}
		if !strings.Contains(result.Result.Answer, "Mock brief generated") {
			t.Fatalf("unexpected answer: %s", result.Result.Answer)
		}
		return
	}

	t.Fatal("result not ready before timeout")
}

func TestSubmitRejectsUnknownExpert(t *testing.T) {
	server := NewServer(buildSnapshot(), WithProcessDelay(1*time.Millisecond))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "does_not_exist",
		"date":      "2026-02-27",
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var problem apiv1.Problem
	mustDecode(t, resp, &problem)
	if problem.Status != http.StatusBadRequest {
		t.Fatalf("expected problem status 400, got %d", problem.Status)
	}
}

func TestIdempotencyConflict(t *testing.T) {
	server := NewServer(buildSnapshot(), WithProcessDelay(1*time.Millisecond))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	headers := map[string]string{"Idempotency-Key": "same-key"}
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
		t.Fatalf("expected idempotency conflict 409, got %d", second.StatusCode)
	}
}

func TestIdempotencyReplayReturnsSameJobID(t *testing.T) {
	server := NewServer(buildSnapshot(), WithProcessDelay(1*time.Millisecond))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	headers := map[string]string{"Idempotency-Key": "same-key-same-payload"}
	body := map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}

	firstResp := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", body, headers)
	secondResp := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", body, headers)
	if firstResp.StatusCode != http.StatusAccepted || secondResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected both submits to be 202, got %d and %d", firstResp.StatusCode, secondResp.StatusCode)
	}

	var first apiv1.SubmitBriefResponse
	var second apiv1.SubmitBriefResponse
	mustDecode(t, firstResp, &first)
	mustDecode(t, secondResp, &second)
	if first.JobId != second.JobId {
		t.Fatalf("expected same job id, got %s and %s", first.JobId, second.JobId)
	}
}

func TestWebhookDeliveredWithSignature(t *testing.T) {
	secret := "test-secret"
	type callbackPayload struct {
		Timestamp string
		Signature string
		Body      []byte
	}
	callbackCh := make(chan callbackPayload, 1)

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		callbackCh <- callbackPayload{
			Timestamp: r.Header.Get("X-COVA-Timestamp"),
			Signature: r.Header.Get("X-COVA-Signature"),
			Body:      raw,
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(callbackSrv.Close)

	dispatcher := deliverywebhook.NewDispatcher(nil,
		deliverywebhook.WithMaxAttempts(1),
		deliverywebhook.WithJitterFraction(0),
	)
	server := NewServer(
		buildSnapshot(),
		WithProcessDelay(1*time.Millisecond),
		WithWebhookDispatcher(dispatcher),
	)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	submit := doJSON(t, http.MethodPost, ts.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
		"callback": map[string]any{
			"url": callbackSrv.URL,
			"signing": map[string]any{
				"algorithm":  "hmac-sha256",
				"secret_ref": secret,
			},
		},
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	select {
	case received := <-callbackCh:
		if received.Timestamp == "" || received.Signature == "" {
			t.Fatalf("expected signature headers, got timestamp=%q signature=%q", received.Timestamp, received.Signature)
		}
		expect := "sha256=" + signWebhook(secret, received.Timestamp, received.Body)
		if received.Signature != expect {
			t.Fatalf("signature mismatch: expected %q got %q", expect, received.Signature)
		}

		var event deliverywebhook.Event
		if err := json.Unmarshal(received.Body, &event); err != nil {
			t.Fatalf("decode webhook event: %v", err)
		}
		if event.Type != "com.cova.job.completed" {
			t.Fatalf("unexpected event type: %s", event.Type)
		}
		if !strings.HasPrefix(event.ID, "evt_") {
			t.Fatalf("unexpected event id: %s", event.ID)
		}
		dataMap, ok := event.Data.(map[string]any)
		if !ok {
			t.Fatalf("event.data should be object, got %T", event.Data)
		}
		if dataMap["job_id"] != submitResp.JobId {
			t.Fatalf("unexpected job_id in event: %v", dataMap["job_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive webhook callback")
	}
}

func TestWebhookRetryUntilSuccess(t *testing.T) {
	var attempts atomic.Int32
	done := make(chan struct{}, 1)

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		current := attempts.Add(1)
		if current < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		done <- struct{}{}
	}))
	t.Cleanup(callbackSrv.Close)

	dispatcher := deliverywebhook.NewDispatcher(nil,
		deliverywebhook.WithMaxAttempts(5),
		deliverywebhook.WithInitialBackoff(1*time.Millisecond),
		deliverywebhook.WithMaxBackoff(2*time.Millisecond),
		deliverywebhook.WithRequestTimeout(200*time.Millisecond),
		deliverywebhook.WithJitterFraction(0),
	)
	server := NewServer(
		buildSnapshot(),
		WithProcessDelay(1*time.Millisecond),
		WithWebhookDispatcher(dispatcher),
	)
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
		t.Fatal("webhook was not delivered successfully within timeout")
	}

	if attempts.Load() != 3 {
		t.Fatalf("expected 3 webhook attempts, got %d", attempts.Load())
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
		WithProcessDelay(1*time.Millisecond),
		WithWebhookDispatcher(dispatcher),
		WithWebhookDeliveryReader(deliveries),
	)
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
		deliveriesResp := doJSON(t, http.MethodGet, ts.URL+"/v1/assistant/jobs/"+submitResp.JobId+"/deliveries", nil, nil)
		if deliveriesResp.StatusCode != http.StatusOK {
			t.Fatalf("expected deliveries=200, got %d", deliveriesResp.StatusCode)
		}
		var payload apiv1.JobDeliveriesResponse
		mustDecode(t, deliveriesResp, &payload)
		if payload.JobId != submitResp.JobId {
			t.Fatalf("job_id mismatch: got %s want %s", payload.JobId, submitResp.JobId)
		}
		if len(payload.Deliveries) < 1 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		first := payload.Deliveries[0]
		if first.Attempt != 1 || !first.Success {
			t.Fatalf("unexpected first delivery record: %+v", first)
		}
		return
	}
	t.Fatal("expected at least 1 delivery record before timeout")
}

func TestJobStatePersistsAcrossRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "jobs_state.json")
	store, err := jobstore.NewJSONFileStore(statePath)
	if err != nil {
		t.Fatalf("NewJSONFileStore failed: %v", err)
	}

	server1 := NewServer(
		buildSnapshot(),
		WithProcessDelay(1*time.Millisecond),
		WithJobStore(store),
	)
	ts1 := httptest.NewServer(server1)

	submit := doJSON(t, http.MethodPost, ts1.URL+"/v1/assistant/jobs", map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}, nil)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("expected submit=202, got %d", submit.StatusCode)
	}
	var submitResp apiv1.SubmitBriefResponse
	mustDecode(t, submit, &submitResp)

	deadline := time.Now().Add(2 * time.Second)
	done := false
	for time.Now().Before(deadline) {
		resultResp := doJSON(t, http.MethodGet, ts1.URL+"/v1/assistant/jobs/"+submitResp.JobId+"/result", nil, nil)
		if resultResp.StatusCode == http.StatusConflict {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if resultResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200/409 on result endpoint, got %d", resultResp.StatusCode)
		}
		resultResp.Body.Close()
		done = true
		break
	}
	if !done {
		t.Fatal("job did not reach succeeded before restart")
	}
	ts1.Close()

	server2 := NewServer(
		buildSnapshot(),
		WithProcessDelay(1*time.Millisecond),
		WithJobStore(store),
	)
	ts2 := httptest.NewServer(server2)
	defer ts2.Close()

	status := doJSON(t, http.MethodGet, ts2.URL+"/v1/assistant/jobs/"+submitResp.JobId, nil, nil)
	if status.StatusCode != http.StatusOK {
		t.Fatalf("expected status=200 after restart, got %d", status.StatusCode)
	}
	var statusResp apiv1.JobStatusResponse
	mustDecode(t, status, &statusResp)
	if statusResp.Status != apiv1.JobStatus(statusSucceeded) {
		t.Fatalf("expected persisted status=succeeded, got %s", statusResp.Status)
	}
}

func TestIdempotencyPersistsAcrossRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "jobs_state.json")
	store, err := jobstore.NewJSONFileStore(statePath)
	if err != nil {
		t.Fatalf("NewJSONFileStore failed: %v", err)
	}

	server1 := NewServer(
		buildSnapshot(),
		WithProcessDelay(1*time.Millisecond),
		WithJobStore(store),
	)
	ts1 := httptest.NewServer(server1)

	body := map[string]any{
		"expert_id": "fi_cn_primary",
		"date":      "2026-02-27",
	}
	headers := map[string]string{"Idempotency-Key": "persisted-idem-key"}
	first := doJSON(t, http.MethodPost, ts1.URL+"/v1/assistant/jobs", body, headers)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("expected first submit=202, got %d", first.StatusCode)
	}
	var firstResp apiv1.SubmitBriefResponse
	mustDecode(t, first, &firstResp)
	ts1.Close()

	server2 := NewServer(
		buildSnapshot(),
		WithProcessDelay(1*time.Millisecond),
		WithJobStore(store),
	)
	ts2 := httptest.NewServer(server2)
	defer ts2.Close()

	second := doJSON(t, http.MethodPost, ts2.URL+"/v1/assistant/jobs", body, headers)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("expected second submit=202, got %d", second.StatusCode)
	}
	var secondResp apiv1.SubmitBriefResponse
	mustDecode(t, second, &secondResp)

	if firstResp.JobId != secondResp.JobId {
		t.Fatalf("expected same job id across restart, got %s and %s", firstResp.JobId, secondResp.JobId)
	}
}

func TestMetricsAndHealthHandlers(t *testing.T) {
	server := NewServer(buildSnapshot(), WithProcessDelay(1*time.Millisecond))

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

func signWebhook(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

