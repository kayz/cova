package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cova/internal/api/openapi"
	deliverywebhook "cova/internal/delivery/webhook"
	"cova/internal/observability/logjson"
	"cova/internal/registry"
	"cova/internal/storage/jobstore"
	workerexecutor "cova/internal/worker/executor"
	"cova/pkg/apiv1"
)

const (
	statusQueued    = "queued"
	statusRunning   = "running"
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCanceled  = "canceled"
	statusExpired   = "expired"
)

type webhookDispatcher interface {
	DeliverJobCompleted(ctx context.Context, callback apiv1.CallbackConfig, payload deliverywebhook.JobCompletedData) error
}

type expertEventVerifier interface {
	Verify(headers http.Header, body []byte) (deliverywebhook.Event, error)
}

type webhookDeliveryReader interface {
	ListByJobID(ctx context.Context, jobID string) ([]deliverywebhook.DeliveryRecord, error)
}

type Option func(*Server)

func WithClock(now func() time.Time) Option {
	return func(s *Server) {
		if now != nil {
			s.now = now
		}
	}
}

func WithWebhookDispatcher(dispatcher webhookDispatcher) Option {
	return func(s *Server) {
		if dispatcher != nil {
			s.webhook = dispatcher
		}
	}
}

func WithWebhookDeliveryReader(reader webhookDeliveryReader) Option {
	return func(s *Server) {
		s.deliveryRead = reader
	}
}

func WithExpertEventVerifier(verifier expertEventVerifier) Option {
	return func(s *Server) {
		s.eventVerifier = verifier
	}
}

func WithJobStore(store jobstore.Store) Option {
	return func(s *Server) {
		if store != nil {
			s.stateStore = store
		}
	}
}

func WithExecutor(exec workerexecutor.Executor) Option {
	return func(s *Server) {
		if exec != nil {
			s.executor = exec
		}
	}
}

func WithQueueSize(size int) Option {
	return func(s *Server) {
		if size > 0 {
			s.queueSize = size
		}
	}
}

func WithQueue(queue jobQueue) Option {
	return func(s *Server) {
		if queue != nil {
			s.queue = queue
		}
	}
}

func WithMaxAttempts(max int) Option {
	return func(s *Server) {
		if max > 0 {
			s.maxAttempts = max
		}
	}
}

func WithJobTTL(ttl time.Duration) Option {
	return func(s *Server) {
		if ttl > 0 {
			s.jobTTL = ttl
		}
	}
}

type Server struct {
	mu sync.RWMutex

	experts map[string]expertMetadata
	jobs    map[string]*job
	idem    map[string]idempotencyRecord
	dlq     map[string]jobstore.DeadLetterEntry
	nextID  uint64

	now           func() time.Time
	webhook       webhookDispatcher
	eventVerifier expertEventVerifier
	deliveryRead  webhookDeliveryReader
	stateStore    jobstore.Store
	executor      workerexecutor.Executor

	queueSize   int
	maxAttempts int
	jobTTL      time.Duration
	queue       jobQueue
	workerCtx   context.Context
	cancel      context.CancelFunc

	httpHandler http.Handler
}

type expertMetadata struct {
	ExpertID   string
	ExpertName string
	ExpertType string
	Version    string
	Supports   []string
}

type job struct {
	ID          string
	TenantID    string
	ProjectID   string
	ExpertID    string
	ExpertName  string
	ExpertType  string
	Date        string
	Status      string
	Progress    int
	SubmittedAt time.Time
	UpdatedAt   time.Time
	Attempt     int
	MaxAttempts int
	ExpiresAt   *time.Time
	LastError   *string
	Result      *apiv1.ExpertResult
	Callback    *apiv1.CallbackConfig
}

type idempotencyRecord struct {
	PayloadHash string
	JobID       string
}

type TenantMetrics struct {
	TenantID        string `json:"tenant_id"`
	ProjectID       string `json:"project_id"`
	JobsTotal       int    `json:"jobs_total"`
	Queued          int    `json:"queued"`
	Running         int    `json:"running"`
	Succeeded       int    `json:"succeeded"`
	Failed          int    `json:"failed"`
	Canceled        int    `json:"canceled"`
	Expired         int    `json:"expired"`
	RetryCount      int    `json:"retry_count"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

type MetricsSnapshot struct {
	TimestampUTC string                   `json:"timestamp_utc"`
	QueueDepth   int                      `json:"queue_depth"`
	JobsTotal    int                      `json:"jobs_total"`
	DLQTotal     int                      `json:"dlq_total"`
	ByStatus     map[string]int           `json:"by_status"`
	ByTenant     map[string]TenantMetrics `json:"by_tenant"`
}

func NewServer(snapshot *registry.Snapshot, opts ...Option) *Server {
	s := &Server{
		experts:      make(map[string]expertMetadata),
		jobs:         make(map[string]*job),
		idem:         make(map[string]idempotencyRecord),
		dlq:          make(map[string]jobstore.DeadLetterEntry),
		now:          time.Now,
		webhook:      deliverywebhook.NewDispatcher(nil),
		stateStore:   jobstore.NewNopStore(),
		executor:     workerexecutor.NewMockExecutor(),
		queueSize:    256,
		maxAttempts:  3,
		jobTTL:       24 * time.Hour,
		deliveryRead: nil,
	}

	if snapshot != nil {
		for _, loaded := range snapshot.Experts {
			s.experts[loaded.Ref.ExpertID] = expertMetadata{
				ExpertID:   loaded.Ref.ExpertID,
				ExpertName: loaded.Definition.Metadata.ExpertName,
				ExpertType: loaded.Definition.Metadata.ExpertType,
				Version:    loaded.Definition.Metadata.Version,
				Supports:   append([]string(nil), loaded.Definition.Spec.Capabilities...),
			}
		}
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.queue == nil {
		s.queue = newMemoryQueue(s.queueSize)
	}
	s.workerCtx, s.cancel = context.WithCancel(context.Background())
	if err := s.loadState(); err != nil {
		logjson.Emit("error", "load orchestrator state failed", map[string]any{"error": err.Error()})
	}

	go s.workerLoop()

	s.httpHandler = openapi.NewHandler(s)
	return s
}

func (s *Server) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.queue != nil {
		_ = s.queue.Close()
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpHandler.ServeHTTP(w, r)
}

func (s *Server) MetricsSnapshot() MetricsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := MetricsSnapshot{
		TimestampUTC: s.now().UTC().Format(time.RFC3339Nano),
		QueueDepth:   s.queue.Depth(context.Background()),
		JobsTotal:    len(s.jobs),
		DLQTotal:     len(s.dlq),
		ByStatus: map[string]int{
			statusQueued:    0,
			statusRunning:   0,
			statusSucceeded: 0,
			statusFailed:    0,
			statusCanceled:  0,
			statusExpired:   0,
		},
		ByTenant: make(map[string]TenantMetrics),
	}

	for _, item := range s.jobs {
		out.ByStatus[item.Status]++
		key := item.TenantID + "::" + item.ProjectID
		tenant := out.ByTenant[key]
		tenant.TenantID = item.TenantID
		tenant.ProjectID = item.ProjectID
		tenant.JobsTotal++
		tenant.RetryCount += maxInt(item.Attempt-1, 0)
		switch item.Status {
		case statusQueued:
			tenant.Queued++
		case statusRunning:
			tenant.Running++
		case statusSucceeded:
			tenant.Succeeded++
		case statusFailed:
			tenant.Failed++
		case statusCanceled:
			tenant.Canceled++
		case statusExpired:
			tenant.Expired++
		}
		if item.Result != nil {
			tenant.EstimatedTokens += estimateTokens(item.Result.Answer)
		}
		out.ByTenant[key] = tenant
	}

	return out
}

func (s *Server) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.MetricsSnapshot())
	})
}

func (s *Server) HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		payload := map[string]any{
			"status":      "ok",
			"queue_depth": s.queue.Depth(context.Background()),
			"jobs_total":  len(s.jobs),
			"timestamp":   s.now().UTC().Format(time.RFC3339Nano),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
}

type expertTaskEventData struct {
	TaskID  string              `json:"task_id"`
	JobID   string              `json:"job_id"`
	TraceID string              `json:"trace_id,omitempty"`
	Status  string              `json:"status"`
	Result  *apiv1.ExpertResult `json:"result,omitempty"`
	Error   *string             `json:"error,omitempty"`
}

func (s *Server) ExpertEventsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"type":   "https://api.cova.example.com/errors/invalid-request",
				"title":  "Invalid request",
				"status": http.StatusBadRequest,
				"detail": "invalid event payload",
			})
			return
		}

		event, err := s.parseExpertEvent(r, raw)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"type":   "https://api.cova.example.com/errors/unauthorized",
				"title":  "Unauthorized",
				"status": http.StatusUnauthorized,
				"detail": err.Error(),
			})
			return
		}
		data, err := parseExpertEventData(event.Data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"type":   "https://api.cova.example.com/errors/invalid-input",
				"title":  "Invalid input",
				"status": http.StatusBadRequest,
				"detail": err.Error(),
			})
			return
		}

		var callback *apiv1.CallbackConfig
		s.mu.Lock()
		current, ok := s.jobs[data.JobID]
		if !ok {
			s.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]any{
				"type":   "https://api.cova.example.com/errors/not-found",
				"title":  "Not found",
				"status": http.StatusNotFound,
				"detail": "job not found",
			})
			return
		}
		if current.Status != statusSucceeded && current.Status != statusFailed && current.Status != statusCanceled && current.Status != statusExpired {
			now := s.now().UTC()
			switch strings.TrimSpace(data.Status) {
			case statusSucceeded:
				current.Status = statusSucceeded
				current.Progress = 100
				current.Result = data.Result
				current.LastError = nil
				callback = cloneCallbackConfig(current.Callback)
				delete(s.dlq, current.ID)
			case statusFailed:
				current.Status = statusFailed
				current.Progress = 100
				current.LastError = data.Error
				s.dlq[current.ID] = jobstore.DeadLetterEntry{
					JobID:       current.ID,
					TenantID:    current.TenantID,
					ProjectID:   current.ProjectID,
					Reason:      "runtime_failed",
					Attempts:    current.Attempt,
					FailedAt:    now,
					LastError:   ptrValue(data.Error),
					FinalStatus: current.Status,
				}
			case statusCanceled:
				current.Status = statusCanceled
				current.Progress = 100
			default:
				s.mu.Unlock()
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"type":   "https://api.cova.example.com/errors/invalid-input",
					"title":  "Invalid input",
					"status": http.StatusBadRequest,
					"detail": "unsupported event status",
				})
				return
			}
			current.UpdatedAt = now
			s.persistLocked()
		}
		s.mu.Unlock()

		if callback != nil && strings.TrimSpace(callback.Url) != "" {
			go s.dispatchJobCompletedWebhook(data.JobID, *callback)
		}

		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "accepted",
			"job_id": data.JobID,
		})
	})
}

func (s *Server) SubmitBriefJob(
	_ context.Context,
	params openapi.SubmitBriefJobParams,
	req apiv1.SubmitBriefRequest,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	tenantID := strings.TrimSpace(params.XTenantId)
	projectID := strings.TrimSpace(params.XProjectId)
	if tenantID == "" || projectID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}
	if req.TenantId != nil && strings.TrimSpace(*req.TenantId) != "" && strings.TrimSpace(*req.TenantId) != tenantID {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "body `tenant_id` does not match header `X-Tenant-Id`"), nil
	}
	if req.ProjectId != nil && strings.TrimSpace(*req.ProjectId) != "" && strings.TrimSpace(*req.ProjectId) != projectID {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "body `project_id` does not match header `X-Project-Id`"), nil
	}

	expertID := strings.TrimSpace(req.ExpertId)
	if expertID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "field `expert_id` is required"), nil
	}
	if strings.TrimSpace(req.Date) == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "field `date` is required"), nil
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "field `date` must use YYYY-MM-DD"), nil
	}

	expert, ok := s.lookupExpert(expertID)
	if !ok {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "unknown `expert_id`"), nil
	}
	if !expertSupports(expert, "daily_brief") {
		return http.StatusBadRequest, problem("/v1/assistant/jobs", requestID, "invalid-request", "Invalid request", "expert does not support `daily_brief`"), nil
	}

	expertName := ptrValue(req.ExpertName)
	if strings.TrimSpace(expertName) == "" {
		expertName = expert.ExpertName
	}
	expertType := ""
	if req.ExpertType != nil {
		expertType = string(*req.ExpertType)
	}
	if strings.TrimSpace(expertType) == "" {
		expertType = expert.ExpertType
	}

	payloadHash, err := hashRequestPayload(req)
	if err != nil {
		return http.StatusInternalServerError, problem("/v1/assistant/jobs", requestID, "internal-error", "Internal error", "failed to hash request"), nil
	}
	idempotencyKey := scopedIdempotencyKey(tenantID, projectID, params.IdempotencyKey)

	var enqueuedJobID string

	s.mu.Lock()
	if idempotencyKey != "" {
		if record, exists := s.idem[idempotencyKey]; exists {
			if record.PayloadHash != payloadHash {
				s.mu.Unlock()
				return http.StatusConflict, problem("/v1/assistant/jobs", requestID, "idempotency-mismatch", "Idempotency mismatch", "same Idempotency-Key with different payload"), nil
			}
			existing, found := s.jobs[record.JobID]
			if !found {
				s.mu.Unlock()
				return http.StatusInternalServerError, problem("/v1/assistant/jobs", requestID, "internal-error", "Internal error", "idempotency record exists without job"), nil
			}
			resp := apiv1.SubmitBriefResponse{
				JobId:       existing.ID,
				TenantId:    existing.TenantID,
				ProjectId:   existing.ProjectID,
				ExpertId:    existing.ExpertID,
				ExpertName:  stringPtr(existing.ExpertName),
				ExpertType:  expertTypePtr(existing.ExpertType),
				Status:      apiv1.JobStatus(existing.Status),
				SubmittedAt: existing.SubmittedAt.UTC().Format(time.RFC3339),
				RequestId:   stringPtr(requestID),
			}
			s.mu.Unlock()
			return http.StatusAccepted, resp, nil
		}
	}

	s.nextID++
	jobID := "job_" + strconv.FormatUint(s.nextID, 10)
	now := s.now().UTC()
	expiresAt := now.Add(s.jobTTL)
	newJob := &job{
		ID:          jobID,
		TenantID:    tenantID,
		ProjectID:   projectID,
		ExpertID:    expertID,
		ExpertName:  expertName,
		ExpertType:  expertType,
		Date:        req.Date,
		Status:      statusQueued,
		Progress:    0,
		SubmittedAt: now,
		UpdatedAt:   now,
		Attempt:     1,
		MaxAttempts: s.maxAttempts,
		ExpiresAt:   &expiresAt,
		Callback:    cloneCallbackConfig(req.Callback),
	}
	s.jobs[jobID] = newJob
	delete(s.dlq, jobID)
	if idempotencyKey != "" {
		s.idem[idempotencyKey] = idempotencyRecord{
			PayloadHash: payloadHash,
			JobID:       jobID,
		}
	}
	s.persistLocked()
	enqueuedJobID = jobID
	resp := apiv1.SubmitBriefResponse{
		JobId:       newJob.ID,
		TenantId:    newJob.TenantID,
		ProjectId:   newJob.ProjectID,
		ExpertId:    newJob.ExpertID,
		ExpertName:  stringPtr(newJob.ExpertName),
		ExpertType:  expertTypePtr(newJob.ExpertType),
		Status:      apiv1.JobStatus(newJob.Status),
		SubmittedAt: newJob.SubmittedAt.UTC().Format(time.RFC3339),
		RequestId:   stringPtr(requestID),
	}
	s.mu.Unlock()

	s.enqueue(enqueuedJobID)
	return http.StatusAccepted, resp, nil
}

func (s *Server) GetJobStatus(
	_ context.Context,
	params openapi.GetJobStatusParams,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	tenantID := strings.TrimSpace(params.XTenantId)
	projectID := strings.TrimSpace(params.XProjectId)
	if tenantID == "" || projectID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs/"+params.JobID, requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}
	s.expireJobIfNeeded(params.JobID)

	s.mu.RLock()
	current, ok := s.jobs[params.JobID]
	s.mu.RUnlock()
	if !ok {
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID, requestID, "not-found", "Not Found", "job not found"), nil
	}
	if !jobInScope(current, tenantID, projectID) {
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID, requestID, "not-found", "Not Found", "job not found"), nil
	}

	progress := current.Progress
	return http.StatusOK, apiv1.JobStatusResponse{
		JobId:       current.ID,
		TenantId:    current.TenantID,
		ProjectId:   current.ProjectID,
		ExpertId:    current.ExpertID,
		ExpertName:  stringPtr(current.ExpertName),
		ExpertType:  expertTypePtr(current.ExpertType),
		Status:      apiv1.JobStatus(current.Status),
		Progress:    &progress,
		SubmittedAt: current.SubmittedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   current.UpdatedAt.UTC().Format(time.RFC3339),
		Attempt:     current.Attempt,
	}, nil
}

func (s *Server) GetJobResult(
	_ context.Context,
	params openapi.GetJobResultParams,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	tenantID := strings.TrimSpace(params.XTenantId)
	projectID := strings.TrimSpace(params.XProjectId)
	if tenantID == "" || projectID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs/"+params.JobID+"/result", requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}
	s.expireJobIfNeeded(params.JobID)

	s.mu.RLock()
	current, ok := s.jobs[params.JobID]
	s.mu.RUnlock()
	if !ok {
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID+"/result", requestID, "not-found", "Not Found", "job not found"), nil
	}
	if !jobInScope(current, tenantID, projectID) {
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID+"/result", requestID, "not-found", "Not Found", "job not found"), nil
	}
	if current.Status != statusSucceeded || current.Result == nil {
		return http.StatusConflict, problem("/v1/assistant/jobs/"+params.JobID+"/result", requestID, "result-not-ready", "Result not ready", "job is still running"), nil
	}

	return http.StatusOK, apiv1.JobResultResponse{
		JobId:      current.ID,
		TenantId:   current.TenantID,
		ProjectId:  current.ProjectID,
		ExpertId:   current.ExpertID,
		ExpertName: stringPtr(current.ExpertName),
		ExpertType: expertTypePtr(current.ExpertType),
		Status:     current.Status,
		Result:     *current.Result,
	}, nil
}

func (s *Server) GetJobDeliveries(
	ctx context.Context,
	params openapi.GetJobDeliveriesParams,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	tenantID := strings.TrimSpace(params.XTenantId)
	projectID := strings.TrimSpace(params.XProjectId)
	if tenantID == "" || projectID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs/"+params.JobID+"/deliveries", requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}
	s.expireJobIfNeeded(params.JobID)

	s.mu.RLock()
	current, ok := s.jobs[params.JobID]
	s.mu.RUnlock()
	if !ok {
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID+"/deliveries", requestID, "not-found", "Not Found", "job not found"), nil
	}
	if !jobInScope(current, tenantID, projectID) {
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID+"/deliveries", requestID, "not-found", "Not Found", "job not found"), nil
	}

	records := make([]deliverywebhook.DeliveryRecord, 0, 4)
	if s.deliveryRead != nil {
		found, err := s.deliveryRead.ListByJobID(ctx, params.JobID)
		if err != nil {
			return http.StatusInternalServerError, problem("/v1/assistant/jobs/"+params.JobID+"/deliveries", requestID, "internal-error", "Internal error", "failed to list webhook deliveries"), nil
		}
		records = found
	}

	resp := apiv1.JobDeliveriesResponse{
		JobId:      params.JobID,
		TenantId:   current.TenantID,
		ProjectId:  current.ProjectID,
		Deliveries: make([]apiv1.WebhookDeliveryRecord, 0, len(records)),
	}
	for _, rec := range records {
		item := apiv1.WebhookDeliveryRecord{
			DeliveryId:    rec.DeliveryID,
			EventId:       rec.EventID,
			EventType:     rec.EventType,
			CallbackUrl:   rec.CallbackURL,
			Attempt:       rec.Attempt,
			MaxAttempts:   rec.MaxAttempts,
			Success:       rec.Success,
			HttpStatus:    rec.HTTPStatus,
			Error:         rec.Error,
			RequestedAt:   rec.RequestedAt,
			DurationMs:    int(rec.DurationMs),
			NextRetryAt:   rec.NextRetryAt,
			PayloadSha256: rec.PayloadSHA256,
		}
		if strings.TrimSpace(rec.JobID) != "" {
			item.JobId = stringPtr(rec.JobID)
		}
		resp.Deliveries = append(resp.Deliveries, item)
	}
	return http.StatusOK, resp, nil
}

func (s *Server) CancelJob(
	_ context.Context,
	params openapi.CancelJobParams,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	tenantID := strings.TrimSpace(params.XTenantId)
	projectID := strings.TrimSpace(params.XProjectId)
	if tenantID == "" || projectID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs/"+params.JobID+"/cancel", requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}
	s.expireJobIfNeeded(params.JobID)

	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[params.JobID]
	if !ok || !jobInScope(current, tenantID, projectID) {
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID+"/cancel", requestID, "not-found", "Not Found", "job not found"), nil
	}
	if current.Status == statusSucceeded || current.Status == statusFailed || current.Status == statusCanceled || current.Status == statusExpired {
		return http.StatusConflict, problem("/v1/assistant/jobs/"+params.JobID+"/cancel", requestID, "invalid-transition", "Invalid state transition", "job is already terminal"), nil
	}
	current.Status = statusCanceled
	current.UpdatedAt = s.now().UTC()
	s.persistLocked()

	progress := current.Progress
	return http.StatusAccepted, apiv1.JobStatusResponse{
		JobId:       current.ID,
		TenantId:    current.TenantID,
		ProjectId:   current.ProjectID,
		ExpertId:    current.ExpertID,
		ExpertName:  stringPtr(current.ExpertName),
		ExpertType:  expertTypePtr(current.ExpertType),
		Status:      apiv1.JobStatus(current.Status),
		Progress:    &progress,
		SubmittedAt: current.SubmittedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   current.UpdatedAt.UTC().Format(time.RFC3339),
		Attempt:     current.Attempt,
	}, nil
}

func (s *Server) ReplayJob(
	_ context.Context,
	params openapi.ReplayJobParams,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	tenantID := strings.TrimSpace(params.XTenantId)
	projectID := strings.TrimSpace(params.XProjectId)
	if tenantID == "" || projectID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/jobs/"+params.JobID+"/replay", requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}
	s.expireJobIfNeeded(params.JobID)

	s.mu.Lock()
	current, ok := s.jobs[params.JobID]
	if !ok || !jobInScope(current, tenantID, projectID) {
		s.mu.Unlock()
		return http.StatusNotFound, problem("/v1/assistant/jobs/"+params.JobID+"/replay", requestID, "not-found", "Not Found", "job not found"), nil
	}
	if current.Status != statusFailed && current.Status != statusCanceled && current.Status != statusExpired {
		s.mu.Unlock()
		return http.StatusConflict, problem("/v1/assistant/jobs/"+params.JobID+"/replay", requestID, "invalid-transition", "Invalid state transition", "only failed/canceled/expired jobs can be replayed"), nil
	}

	current.Status = statusQueued
	current.Progress = 0
	current.Attempt = 1
	current.UpdatedAt = s.now().UTC()
	current.LastError = nil
	delete(s.dlq, current.ID)
	s.persistLocked()
	s.mu.Unlock()

	s.enqueue(current.ID)

	progress := current.Progress
	return http.StatusAccepted, apiv1.JobStatusResponse{
		JobId:       current.ID,
		TenantId:    current.TenantID,
		ProjectId:   current.ProjectID,
		ExpertId:    current.ExpertID,
		ExpertName:  stringPtr(current.ExpertName),
		ExpertType:  expertTypePtr(current.ExpertType),
		Status:      apiv1.JobStatus(current.Status),
		Progress:    &progress,
		SubmittedAt: current.SubmittedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   current.UpdatedAt.UTC().Format(time.RFC3339),
		Attempt:     current.Attempt,
	}, nil
}

func (s *Server) QueryExpert(
	_ context.Context,
	params openapi.QueryExpertParams,
	req apiv1.QueryRequest,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	tenantID := strings.TrimSpace(params.XTenantId)
	projectID := strings.TrimSpace(params.XProjectId)
	if tenantID == "" || projectID == "" {
		return http.StatusBadRequest, problem("/v1/assistant/query", requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}
	if req.TenantId != nil && strings.TrimSpace(*req.TenantId) != "" && strings.TrimSpace(*req.TenantId) != tenantID {
		return http.StatusBadRequest, problem("/v1/assistant/query", requestID, "invalid-request", "Invalid request", "body `tenant_id` does not match header `X-Tenant-Id`"), nil
	}
	if req.ProjectId != nil && strings.TrimSpace(*req.ProjectId) != "" && strings.TrimSpace(*req.ProjectId) != projectID {
		return http.StatusBadRequest, problem("/v1/assistant/query", requestID, "invalid-request", "Invalid request", "body `project_id` does not match header `X-Project-Id`"), nil
	}
	if strings.TrimSpace(req.ExpertId) == "" {
		return http.StatusBadRequest, problem("/v1/assistant/query", requestID, "invalid-request", "Invalid request", "field `expert_id` is required"), nil
	}
	if strings.TrimSpace(req.Question) == "" {
		return http.StatusBadRequest, problem("/v1/assistant/query", requestID, "invalid-request", "Invalid request", "field `question` is required"), nil
	}
	expert, ok := s.lookupExpert(req.ExpertId)
	if !ok {
		return http.StatusBadRequest, problem("/v1/assistant/query", requestID, "invalid-request", "Invalid request", "unknown `expert_id`"), nil
	}
	if !expertSupports(expert, "query") {
		return http.StatusBadRequest, problem("/v1/assistant/query", requestID, "invalid-request", "Invalid request", "expert does not support `query`"), nil
	}

	title := fmt.Sprintf("Quick answer from %s", expert.ExpertName)
	now := s.now().UTC()
	resp := apiv1.QueryResponse{
		Title:           &title,
		Answer:          fmt.Sprintf("Mock answer for question: %s", req.Question),
		Confidence:      0.76,
		FreshnessCutoff: now.Format(time.RFC3339),
		References: []apiv1.ReferenceRecord{
			{
				"kind":         "article",
				"ref_id":       "src_query_001",
				"title":        "Mock Query Source",
				"uri":          "https://example.com/mock-query-source",
				"published_at": now.Add(-10 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	return http.StatusOK, resp, nil
}

func (s *Server) GetExpertCapabilities(
	_ context.Context,
	params openapi.GetExpertCapabilitiesParams,
) (int, any, error) {
	requestID := normalizeRequestID(params.XRequestId)
	if strings.TrimSpace(params.XTenantId) == "" || strings.TrimSpace(params.XProjectId) == "" {
		return http.StatusBadRequest, problem("/v1/assistant/experts", requestID, "invalid-request", "Invalid request", "headers `X-Tenant-Id` and `X-Project-Id` are required"), nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	experts := make([]apiv1.ExpertCapability, 0, len(s.experts))
	for _, e := range s.experts {
		experts = append(experts, apiv1.ExpertCapability{
			ExpertId:   e.ExpertID,
			ExpertName: e.ExpertName,
			ExpertType: apiv1.ExpertType(e.ExpertType),
			Supports:   append([]string(nil), e.Supports...),
			Status:     "active",
			Version:    e.Version,
		})
	}
	return http.StatusOK, apiv1.ExpertCapabilitiesResponse{Experts: experts}, nil
}

func (s *Server) workerLoop() {
	for {
		select {
		case <-s.workerCtx.Done():
			return
		default:
		}
		item, err := s.queue.Dequeue(s.workerCtx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			logjson.Emit("error", "queue dequeue failed", map[string]any{"error": err.Error()})
			time.Sleep(100 * time.Millisecond)
			continue
		}
		s.processBriefJob(item.ID)
		if item.Ack != nil {
			if err := item.Ack(s.workerCtx); err != nil && !errors.Is(err, context.Canceled) {
				logjson.Emit("error", "queue ack failed", map[string]any{"job_id": item.ID, "error": err.Error()})
			}
		}
	}
}

func (s *Server) processBriefJob(jobID string) {
	s.mu.Lock()
	current, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return
	}
	if current.Status == statusCanceled {
		s.mu.Unlock()
		return
	}
	if isExpired(current, s.now().UTC()) {
		markExpired(current, s.now().UTC())
		s.persistLocked()
		s.mu.Unlock()
		return
	}
	current.Status = statusRunning
	current.Progress = 30
	current.UpdatedAt = s.now().UTC()
	s.persistLocked()
	input := workerexecutor.BriefInput{
		JobID:      current.ID,
		TenantID:   current.TenantID,
		ProjectID:  current.ProjectID,
		ExpertID:   current.ExpertID,
		ExpertName: current.ExpertName,
		ExpertType: current.ExpertType,
		Date:       current.Date,
		TraceID:    current.ID,
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(s.workerCtx, 60*time.Second)
	result, err := s.executor.ExecuteBrief(ctx, input)
	cancel()
	if err != nil {
		if errors.Is(err, workerexecutor.ErrAsyncAccepted) {
			s.mu.Lock()
			if current, ok := s.jobs[jobID]; ok {
				if current.Status != statusCanceled && !isExpired(current, s.now().UTC()) {
					current.Status = statusRunning
					current.Progress = 60
					current.UpdatedAt = s.now().UTC()
					s.persistLocked()
				}
			}
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		if current, ok := s.jobs[jobID]; ok {
			now := s.now().UTC()
			lastError := err.Error()
			current.LastError = &lastError
			current.UpdatedAt = now
			if current.Status == statusCanceled {
				s.persistLocked()
				s.mu.Unlock()
				return
			}
			if isExpired(current, now) {
				markExpired(current, now)
				s.persistLocked()
				s.mu.Unlock()
				return
			}
			if current.Attempt < current.MaxAttempts {
				current.Attempt++
				current.Status = statusQueued
				current.Progress = 0
				s.persistLocked()
				s.mu.Unlock()
				s.enqueue(jobID)
				logjson.Emit("warn", "brief execution retry scheduled", map[string]any{
					"job_id":       jobID,
					"attempt":      current.Attempt,
					"max_attempts": current.MaxAttempts,
					"error":        err.Error(),
				})
				return
			}
			current.Status = statusFailed
			current.Progress = 100
			s.dlq[jobID] = jobstore.DeadLetterEntry{
				JobID:       current.ID,
				TenantID:    current.TenantID,
				ProjectID:   current.ProjectID,
				Reason:      "max_attempts_exhausted",
				Attempts:    current.Attempt,
				FailedAt:    now,
				LastError:   lastError,
				FinalStatus: current.Status,
			}
			s.persistLocked()
		}
		s.mu.Unlock()
		logjson.Emit("error", "brief execution failed", map[string]any{"job_id": jobID, "error": err.Error()})
		return
	}

	s.mu.Lock()
	var callback *apiv1.CallbackConfig
	if current, ok := s.jobs[jobID]; ok {
		now := s.now().UTC()
		if current.Status == statusCanceled {
			s.persistLocked()
			s.mu.Unlock()
			return
		}
		if isExpired(current, now) {
			markExpired(current, now)
			s.persistLocked()
			s.mu.Unlock()
			return
		}
		current.Status = statusSucceeded
		current.Progress = 100
		current.Result = &result
		current.LastError = nil
		current.UpdatedAt = now
		callback = cloneCallbackConfig(current.Callback)
		delete(s.dlq, jobID)
		s.persistLocked()
	}
	s.mu.Unlock()

	if callback != nil && strings.TrimSpace(callback.Url) != "" {
		go s.dispatchJobCompletedWebhook(jobID, *callback)
	}
}

func (s *Server) dispatchJobCompletedWebhook(jobID string, callback apiv1.CallbackConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := s.webhook.DeliverJobCompleted(ctx, callback, deliverywebhook.JobCompletedData{
		JobID:     jobID,
		Status:    statusSucceeded,
		ResultURL: "/v1/assistant/jobs/" + jobID + "/result",
	})
	if err != nil {
		logjson.Emit("error", "webhook delivery failed", map[string]any{
			"job_id":       jobID,
			"callback_url": callback.Url,
			"error":        err.Error(),
		})
	}
}

func (s *Server) enqueue(jobID string) {
	ctx, cancel := context.WithTimeout(s.workerCtx, 3*time.Second)
	defer cancel()
	if err := s.queue.Enqueue(ctx, jobID); err != nil && !errors.Is(err, context.Canceled) {
		logjson.Emit("error", "queue enqueue failed", map[string]any{"job_id": jobID, "error": err.Error()})
	}
}

func (s *Server) lookupExpert(expertID string) (expertMetadata, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	expert, ok := s.experts[expertID]
	return expert, ok
}

func hashRequestPayload(req apiv1.SubmitBriefRequest) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) expireJobIfNeeded(jobID string) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[jobID]
	if !ok {
		return
	}
	if current.Status == statusSucceeded || current.Status == statusFailed || current.Status == statusCanceled || current.Status == statusExpired {
		return
	}
	if isExpired(current, now) {
		markExpired(current, now)
		s.persistLocked()
	}
}

func (s *Server) loadState() error {
	state, err := s.stateStore.Load(context.Background())
	if err != nil {
		return err
	}

	s.nextID = state.NextID
	s.jobs = make(map[string]*job, len(state.Jobs))
	pending := make([]string, 0, len(state.Jobs))
	for id, stored := range state.Jobs {
		restored := &job{
			ID:          stored.ID,
			TenantID:    stored.TenantID,
			ProjectID:   stored.ProjectID,
			ExpertID:    stored.ExpertID,
			ExpertName:  stored.ExpertName,
			ExpertType:  stored.ExpertType,
			Date:        stored.Date,
			Status:      stored.Status,
			Progress:    stored.Progress,
			SubmittedAt: stored.SubmittedAt,
			UpdatedAt:   stored.UpdatedAt,
			Attempt:     stored.Attempt,
			MaxAttempts: stored.MaxAttempts,
			ExpiresAt:   stored.ExpiresAt,
			LastError:   stored.LastError,
			Result:      stored.Result,
			Callback:    cloneCallbackConfig(stored.Callback),
		}
		if restored.MaxAttempts <= 0 {
			restored.MaxAttempts = s.maxAttempts
		}
		if restored.Status == statusRunning {
			restored.Status = statusQueued
			restored.Progress = 0
		}
		if restored.Status == statusQueued {
			pending = append(pending, id)
		}
		s.jobs[id] = restored
	}

	s.idem = make(map[string]idempotencyRecord, len(state.Idempotency))
	for key, stored := range state.Idempotency {
		s.idem[key] = idempotencyRecord{
			PayloadHash: stored.PayloadHash,
			JobID:       stored.JobID,
		}
	}
	s.dlq = make(map[string]jobstore.DeadLetterEntry, len(state.DeadLetters))
	for key, item := range state.DeadLetters {
		s.dlq[key] = item
	}

	if len(pending) > 0 {
		s.persistLocked()
		for _, jobID := range pending {
			s.enqueue(jobID)
		}
	}
	return nil
}

func (s *Server) persistLocked() {
	state := jobstore.State{
		NextID:      s.nextID,
		Jobs:        make(map[string]jobstore.Job, len(s.jobs)),
		Idempotency: make(map[string]jobstore.IdempotencyEntry, len(s.idem)),
		DeadLetters: make(map[string]jobstore.DeadLetterEntry, len(s.dlq)),
	}

	for id, j := range s.jobs {
		state.Jobs[id] = jobstore.Job{
			ID:          j.ID,
			TenantID:    j.TenantID,
			ProjectID:   j.ProjectID,
			ExpertID:    j.ExpertID,
			ExpertName:  j.ExpertName,
			ExpertType:  j.ExpertType,
			Date:        j.Date,
			Status:      j.Status,
			Progress:    j.Progress,
			SubmittedAt: j.SubmittedAt,
			UpdatedAt:   j.UpdatedAt,
			Attempt:     j.Attempt,
			MaxAttempts: j.MaxAttempts,
			ExpiresAt:   j.ExpiresAt,
			LastError:   j.LastError,
			Result:      j.Result,
			Callback:    cloneCallbackConfig(j.Callback),
		}
	}
	for key, rec := range s.idem {
		state.Idempotency[key] = jobstore.IdempotencyEntry{
			PayloadHash: rec.PayloadHash,
			JobID:       rec.JobID,
		}
	}
	for key, item := range s.dlq {
		state.DeadLetters[key] = item
	}

	if err := s.stateStore.Save(context.Background(), state); err != nil {
		logjson.Emit("error", "persist orchestrator state failed", map[string]any{"error": err.Error()})
	}
}

func (s *Server) parseExpertEvent(r *http.Request, raw []byte) (deliverywebhook.Event, error) {
	if s.eventVerifier != nil {
		return s.eventVerifier.Verify(r.Header, raw)
	}
	var event deliverywebhook.Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return deliverywebhook.Event{}, fmt.Errorf("invalid event payload: %w", err)
	}
	return event, nil
}

func parseExpertEventData(raw any) (expertTaskEventData, error) {
	switch v := raw.(type) {
	case expertTaskEventData:
		if strings.TrimSpace(v.JobID) == "" {
			return expertTaskEventData{}, errors.New("event data missing job_id")
		}
		return v, nil
	case *expertTaskEventData:
		if v == nil || strings.TrimSpace(v.JobID) == "" {
			return expertTaskEventData{}, errors.New("event data missing job_id")
		}
		return *v, nil
	default:
		encoded, err := json.Marshal(raw)
		if err != nil {
			return expertTaskEventData{}, errors.New("event data is invalid")
		}
		var out expertTaskEventData
		if err := json.Unmarshal(encoded, &out); err != nil {
			return expertTaskEventData{}, errors.New("event data is invalid")
		}
		if strings.TrimSpace(out.JobID) == "" {
			return expertTaskEventData{}, errors.New("event data missing job_id")
		}
		return out, nil
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func problem(instance, requestID, code, title, detail string) apiv1.Problem {
	return apiv1.Problem{
		Type:      "https://api.cova.example.com/errors/" + code,
		Title:     title,
		Status:    inferStatus(code),
		Detail:    stringPtr(detail),
		Instance:  stringPtr(instance),
		RequestId: stringPtr(requestID),
	}
}

func inferStatus(code string) int {
	switch code {
	case "invalid-request":
		return http.StatusBadRequest
	case "idempotency-mismatch":
		return http.StatusConflict
	case "not-found":
		return http.StatusNotFound
	case "result-not-ready":
		return http.StatusConflict
	case "invalid-transition":
		return http.StatusConflict
	case "internal-error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func normalizeRequestID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID != "" {
		return requestID
	}
	return "req_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func stringPtr(v string) *string {
	return &v
}

func ptrValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func expertTypePtr(v string) *apiv1.ExpertType {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	t := apiv1.ExpertType(v)
	return &t
}

func cloneCallbackConfig(in *apiv1.CallbackConfig) *apiv1.CallbackConfig {
	if in == nil || strings.TrimSpace(in.Url) == "" {
		return nil
	}
	out := &apiv1.CallbackConfig{
		Url: strings.TrimSpace(in.Url),
	}
	if in.Signing != nil {
		out.Signing = &apiv1.CallbackSigning{
			Algorithm: strings.TrimSpace(in.Signing.Algorithm),
			SecretRef: in.Signing.SecretRef,
		}
	}
	return out
}

func scopedIdempotencyKey(tenantID, projectID, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return tenantID + "::" + projectID + "::" + raw
}

func isExpired(j *job, now time.Time) bool {
	if j == nil || j.ExpiresAt == nil {
		return false
	}
	return now.After(*j.ExpiresAt)
}

func markExpired(j *job, now time.Time) {
	if j == nil {
		return
	}
	j.Status = statusExpired
	j.UpdatedAt = now
}

func jobInScope(j *job, tenantID, projectID string) bool {
	if j == nil {
		return false
	}
	return j.TenantID == tenantID && j.ProjectID == projectID
}

func expertSupports(expert expertMetadata, capability string) bool {
	for _, item := range expert.Supports {
		if strings.TrimSpace(item) == capability {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func estimateTokens(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	// Coarse estimate: ~4 chars/token for mixed language short-form content.
	return (len(trimmed) + 3) / 4
}
