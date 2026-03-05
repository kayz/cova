package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cova/pkg/apiv1"
)

var ErrAsyncAccepted = errors.New("runtime task accepted for async processing")

type RuntimeExecutor struct {
	baseURL     string
	authToken   string
	httpClient  *http.Client
	asyncMode   bool
	taskTimeout time.Duration
}

type RuntimeOption func(*RuntimeExecutor)

func WithRuntimeAuthToken(token string) RuntimeOption {
	return func(e *RuntimeExecutor) {
		e.authToken = strings.TrimSpace(token)
	}
}

func WithRuntimeHTTPClient(client *http.Client) RuntimeOption {
	return func(e *RuntimeExecutor) {
		if client != nil {
			e.httpClient = client
		}
	}
}

func WithRuntimeAsyncMode(v bool) RuntimeOption {
	return func(e *RuntimeExecutor) {
		e.asyncMode = v
	}
}

func WithRuntimeTaskTimeout(v time.Duration) RuntimeOption {
	return func(e *RuntimeExecutor) {
		if v > 0 {
			e.taskTimeout = v
		}
	}
}

func NewRuntimeExecutor(baseURL string, opts ...RuntimeOption) *RuntimeExecutor {
	out := &RuntimeExecutor{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient:  http.DefaultClient,
		asyncMode:   false,
		taskTimeout: 60 * time.Second,
	}
	for _, opt := range opts {
		opt(out)
	}
	return out
}

type runtimeTaskRequest struct {
	TaskID      string             `json:"task_id"`
	JobID       string             `json:"job_id"`
	TenantID    string             `json:"tenant_id"`
	ProjectID   string             `json:"project_id"`
	TaskType    string             `json:"task_type"`
	Input       runtimeTaskInput   `json:"input"`
	Constraints runtimeConstraints `json:"constraints"`
	TraceID     string             `json:"trace_id,omitempty"`
}

type runtimeTaskInput struct {
	Date string `json:"date"`
}

type runtimeConstraints struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

type runtimeTaskResponse struct {
	Status string              `json:"status"`
	Result *apiv1.ExpertResult `json:"result,omitempty"`
}

func (e *RuntimeExecutor) ExecuteBrief(ctx context.Context, input BriefInput) (apiv1.ExpertResult, error) {
	if strings.TrimSpace(e.baseURL) == "" {
		return apiv1.ExpertResult{}, errors.New("runtime base url is required")
	}

	timeoutSeconds := int(e.taskTimeout / time.Second)
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	reqBody := runtimeTaskRequest{
		TaskID:    input.JobID,
		JobID:     input.JobID,
		TenantID:  input.TenantID,
		ProjectID: input.ProjectID,
		TaskType:  "daily_brief",
		Input: runtimeTaskInput{
			Date: input.Date,
		},
		Constraints: runtimeConstraints{
			TimeoutSeconds: timeoutSeconds,
		},
		TraceID: input.TraceID,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return apiv1.ExpertResult{}, fmt.Errorf("marshal runtime task request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/runtime/v1/tasks", bytes.NewReader(raw))
	if err != nil {
		return apiv1.ExpertResult{}, fmt.Errorf("build runtime request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+e.authToken)
	}
	if e.asyncMode {
		req.Header.Set("X-COVA-Execution-Mode", "async")
	}
	if strings.TrimSpace(input.TraceID) != "" {
		req.Header.Set("X-Request-Id", input.TraceID)
		req.Header.Set("traceparent", traceparentFromID(input.TraceID))
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return apiv1.ExpertResult{}, fmt.Errorf("call runtime api: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusAccepted {
		return apiv1.ExpertResult{}, ErrAsyncAccepted
	}
	if resp.StatusCode != http.StatusOK {
		return apiv1.ExpertResult{}, fmt.Errorf("runtime api status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload runtimeTaskResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return apiv1.ExpertResult{}, fmt.Errorf("decode runtime response: %w", err)
	}
	if payload.Result == nil {
		return apiv1.ExpertResult{}, errors.New("runtime response missing result")
	}
	return *payload.Result, nil
}

func traceparentFromID(id string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
	traceID := hex.EncodeToString(sum[:16])
	spanID := hex.EncodeToString(sum[16:24])
	return "00-" + traceID + "-" + spanID + "-01"
}
