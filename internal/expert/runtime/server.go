package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cova/internal/observability/logjson"
	"cova/internal/registry"
	workerexecutor "cova/internal/worker/executor"
	"cova/pkg/apiv1"
)

type Option func(*Server)

type EventSink interface {
	SendTaskCompleted(ctx context.Context, event TaskCompletedEvent) error
}

type capability struct {
	ExpertID       string   `json:"expert_id"`
	Version        string   `json:"version"`
	Supports       []string `json:"supports"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxConcurrency int      `json:"max_concurrency"`
}

type TaskRequest struct {
	TaskID      string          `json:"task_id"`
	JobID       string          `json:"job_id"`
	TenantID    string          `json:"tenant_id"`
	ProjectID   string          `json:"project_id"`
	TaskType    string          `json:"task_type"`
	Input       json.RawMessage `json:"input"`
	Constraints TaskConstraints `json:"constraints"`
	TraceID     string          `json:"trace_id"`
}

type TaskConstraints struct {
	TimeoutSeconds int     `json:"timeout_seconds"`
	MaxCostUSD     float64 `json:"max_cost_usd"`
}

type taskInput struct {
	Date string `json:"date"`
}

type taskResponse struct {
	Status       string              `json:"status"`
	Result       *apiv1.ExpertResult `json:"result,omitempty"`
	ExpertTaskID string              `json:"expert_task_id,omitempty"`
}

type cancelResponse struct {
	Status string `json:"status"`
}

type pendingTask struct {
	cancel context.CancelFunc
}

type taskMetrics struct {
	total     uint64
	succeeded uint64
	accepted  uint64
	canceled  uint64
	failed    uint64
}

type MetricsSnapshot struct {
	TimestampUTC string `json:"timestamp_utc"`
	Total        uint64 `json:"total"`
	Succeeded    uint64 `json:"succeeded"`
	Accepted     uint64 `json:"accepted"`
	Canceled     uint64 `json:"canceled"`
	Failed       uint64 `json:"failed"`
}

type Server struct {
	executor  workerexecutor.Executor
	eventSink EventSink
	now       func() time.Time

	authEnabled bool
	authToken   string

	defaultAsync   bool
	defaultTimeout time.Duration
	capability     capability

	mu      sync.Mutex
	pending map[string]pendingTask
	seq     uint64
	metrics taskMetrics
}

type nopEventSink struct{}

func (nopEventSink) SendTaskCompleted(ctx context.Context, event TaskCompletedEvent) error {
	_ = ctx
	_ = event
	return nil
}

func WithExecutor(exec workerexecutor.Executor) Option {
	return func(s *Server) {
		if exec != nil {
			s.executor = exec
		}
	}
}

func WithEventSink(sink EventSink) Option {
	return func(s *Server) {
		if sink != nil {
			s.eventSink = sink
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(s *Server) {
		if now != nil {
			s.now = now
		}
	}
}

func WithAuthToken(token string) Option {
	return func(s *Server) {
		token = strings.TrimSpace(token)
		if token == "" {
			return
		}
		s.authEnabled = true
		s.authToken = token
	}
}

func WithAuthDisabled() Option {
	return func(s *Server) {
		s.authEnabled = false
		s.authToken = ""
	}
}

func WithDefaultAsync(v bool) Option {
	return func(s *Server) {
		s.defaultAsync = v
	}
}

func WithDefaultTimeout(v time.Duration) Option {
	return func(s *Server) {
		if v > 0 {
			s.defaultTimeout = v
		}
	}
}

func NewServer(snapshot *registry.Snapshot, opts ...Option) *Server {
	cap := capability{
		ExpertID:       "unknown_expert",
		Version:        "1.0.0",
		Supports:       []string{"daily_brief", "query"},
		TimeoutSeconds: 180,
		MaxConcurrency: 4,
	}
	if snapshot != nil && len(snapshot.Experts) > 0 {
		first := snapshot.Experts[0]
		cap.ExpertID = first.Ref.ExpertID
		if strings.TrimSpace(first.Definition.Metadata.Version) != "" {
			cap.Version = first.Definition.Metadata.Version
		}
		if len(first.Definition.Spec.Capabilities) > 0 {
			cap.Supports = append([]string(nil), first.Definition.Spec.Capabilities...)
		}
	}

	s := &Server{
		executor:       workerexecutor.NewMockExecutor(),
		eventSink:      nopEventSink{},
		now:            time.Now,
		authEnabled:    false,
		defaultAsync:   false,
		defaultTimeout: 180 * time.Second,
		capability:     cap,
		pending:        make(map[string]pendingTask, 128),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.authEnabled {
		if err := s.checkAuth(r); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"type":   "https://api.cova.example.com/errors/unauthorized",
				"title":  "Unauthorized",
				"status": http.StatusUnauthorized,
				"detail": err.Error(),
			})
			return
		}
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/runtime/v1/healthz":
		s.handleHealthz(w)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/runtime/v1/capabilities":
		s.handleCapabilities(w)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/runtime/v1/tasks":
		s.handleCreateTask(w, r)
		return
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/runtime/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/cancel"):
		taskID := strings.TrimPrefix(r.URL.Path, "/runtime/v1/tasks/")
		taskID = strings.TrimSuffix(taskID, "/cancel")
		taskID = strings.Trim(taskID, "/")
		s.handleCancelTask(w, taskID)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/metrics":
		writeJSON(w, http.StatusOK, s.MetricsSnapshot())
		return
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"time":   s.now().UTC().Format(time.RFC3339Nano),
		})
		return
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) MetricsSnapshot() MetricsSnapshot {
	return MetricsSnapshot{
		TimestampUTC: s.now().UTC().Format(time.RFC3339Nano),
		Total:        atomic.LoadUint64(&s.metrics.total),
		Succeeded:    atomic.LoadUint64(&s.metrics.succeeded),
		Accepted:     atomic.LoadUint64(&s.metrics.accepted),
		Canceled:     atomic.LoadUint64(&s.metrics.canceled),
		Failed:       atomic.LoadUint64(&s.metrics.failed),
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.capability.Version,
	})
}

func (s *Server) handleCapabilities(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"expert_id": s.capability.ExpertID,
		"version":   s.capability.Version,
		"supports":  s.capability.Supports,
		"limits": map[string]any{
			"timeout_seconds": s.capability.TimeoutSeconds,
			"max_concurrency": s.capability.MaxConcurrency,
		},
	})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"type":   "https://api.cova.example.com/errors/invalid-input",
			"title":  "Invalid input",
			"status": http.StatusBadRequest,
			"detail": "request body must be valid JSON",
		})
		return
	}
	if err := validateTaskRequest(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"type":   "https://api.cova.example.com/errors/invalid-input",
			"title":  "Invalid input",
			"status": http.StatusBadRequest,
			"detail": err.Error(),
		})
		return
	}
	atomic.AddUint64(&s.metrics.total, 1)

	var input taskInput
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &input)
	}
	if strings.TrimSpace(input.Date) == "" {
		input.Date = s.now().UTC().Format("2006-01-02")
	}

	async := s.defaultAsync || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-COVA-Execution-Mode")), "async")
	if async {
		expertTaskID := s.newExpertTaskID()
		ctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		s.pending[req.TaskID] = pendingTask{cancel: cancel}
		s.mu.Unlock()
		atomic.AddUint64(&s.metrics.accepted, 1)

		go s.runAsyncTask(ctx, req, input)
		writeJSON(w, http.StatusAccepted, taskResponse{
			Status:       "accepted",
			ExpertTaskID: expertTaskID,
		})
		return
	}

	timeout := s.defaultTimeout
	if req.Constraints.TimeoutSeconds > 0 {
		timeout = time.Duration(req.Constraints.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	result, err := s.execute(ctx, req, input)
	if err != nil {
		atomic.AddUint64(&s.metrics.failed, 1)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"type":   "https://api.cova.example.com/errors/dependency-unavailable",
			"title":  "Dependency unavailable",
			"status": http.StatusBadGateway,
			"detail": err.Error(),
		})
		return
	}

	atomic.AddUint64(&s.metrics.succeeded, 1)
	writeJSON(w, http.StatusOK, taskResponse{
		Status: "succeeded",
		Result: &result,
	})
}

func (s *Server) handleCancelTask(w http.ResponseWriter, taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"type":   "https://api.cova.example.com/errors/invalid-input",
			"title":  "Invalid input",
			"status": http.StatusBadRequest,
			"detail": "task_id is required",
		})
		return
	}

	s.mu.Lock()
	task, ok := s.pending[taskID]
	if ok {
		delete(s.pending, taskID)
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"type":   "https://api.cova.example.com/errors/not-found",
			"title":  "Not found",
			"status": http.StatusNotFound,
			"detail": "task not found",
		})
		return
	}
	task.cancel()
	atomic.AddUint64(&s.metrics.canceled, 1)
	writeJSON(w, http.StatusAccepted, cancelResponse{Status: "canceling"})
}

func (s *Server) runAsyncTask(ctx context.Context, req TaskRequest, input taskInput) {
	defer func() {
		s.mu.Lock()
		delete(s.pending, req.TaskID)
		s.mu.Unlock()
	}()

	timeout := s.defaultTimeout
	if req.Constraints.TimeoutSeconds > 0 {
		timeout = time.Duration(req.Constraints.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := s.execute(runCtx, req, input)
	if err != nil {
		atomic.AddUint64(&s.metrics.failed, 1)
		logjson.Emit("error", "runtime task failed", map[string]any{
			"task_id": req.TaskID,
			"job_id":  req.JobID,
			"error":   err.Error(),
		})
		return
	}
	atomic.AddUint64(&s.metrics.succeeded, 1)

	event := TaskCompletedEvent{
		SpecVersion:     "1.0",
		ID:              fmt.Sprintf("evt_%d", s.now().UTC().UnixNano()),
		Type:            "com.cova.expert.task.completed",
		Source:          "expert://" + s.capability.ExpertID,
		Time:            s.now().UTC().Format(time.RFC3339),
		DataContentType: "application/json",
		Data: TaskCompletedData{
			TaskID:  req.TaskID,
			JobID:   req.JobID,
			TraceID: req.TraceID,
			Status:  "succeeded",
			Result:  result,
		},
	}
	if err := s.eventSink.SendTaskCompleted(context.Background(), event); err != nil {
		logjson.Emit("error", "runtime send event failed", map[string]any{
			"task_id": req.TaskID,
			"job_id":  req.JobID,
			"error":   err.Error(),
		})
	}
}

func (s *Server) execute(ctx context.Context, req TaskRequest, input taskInput) (apiv1.ExpertResult, error) {
	result, err := s.executor.ExecuteBrief(ctx, workerexecutor.BriefInput{
		JobID:      req.JobID,
		ExpertID:   s.capability.ExpertID,
		ExpertName: s.capability.ExpertID,
		ExpertType: "fixed_income",
		Date:       input.Date,
	})
	if err != nil {
		return apiv1.ExpertResult{}, err
	}
	return result, nil
}

func (s *Server) checkAuth(r *http.Request) error {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return errors.New("missing Authorization header")
	}
	parts := strings.Fields(raw)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return errors.New("Authorization must use Bearer token")
	}
	if parts[1] != s.authToken {
		return errors.New("invalid bearer token")
	}
	return nil
}

func (s *Server) newExpertTaskID() string {
	seq := atomic.AddUint64(&s.seq, 1)
	return "exp_t_" + strconv.FormatInt(s.now().UTC().UnixNano(), 10) + "_" + strconv.FormatUint(seq, 10)
}

func validateTaskRequest(req TaskRequest) error {
	if strings.TrimSpace(req.TaskID) == "" {
		return errors.New("field `task_id` is required")
	}
	if strings.TrimSpace(req.JobID) == "" {
		return errors.New("field `job_id` is required")
	}
	if strings.TrimSpace(req.TenantID) == "" {
		return errors.New("field `tenant_id` is required")
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		return errors.New("field `project_id` is required")
	}
	if strings.TrimSpace(req.TaskType) == "" {
		return errors.New("field `task_type` is required")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
