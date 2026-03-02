package mock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cova/internal/api/openapi"
	deliverywebhook "cova/internal/delivery/webhook"
	"cova/internal/registry"
	"cova/internal/storage/jobstore"
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

type Option func(*Server)

type webhookDispatcher interface {
	DeliverJobCompleted(ctx context.Context, callback apiv1.CallbackConfig, payload deliverywebhook.JobCompletedData) error
}

type webhookDeliveryReader interface {
	ListByJobID(ctx context.Context, jobID string) ([]deliverywebhook.DeliveryRecord, error)
}

// WithProcessDelay sets delay per mock processing stage.
func WithProcessDelay(delay time.Duration) Option {
	return func(s *Server) {
		if delay > 0 {
			s.processDelay = delay
		}
	}
}

// WithClock overrides current time source for testing.
func WithClock(now func() time.Time) Option {
	return func(s *Server) {
		if now != nil {
			s.now = now
		}
	}
}

// Server is an in-memory mock worker implementing OpenAPI-generated server interface.
type Server struct {
	mu sync.RWMutex

	experts map[string]expertMetadata
	jobs    map[string]*job
	idem    map[string]idempotencyRecord
	nextID  uint64

	processDelay time.Duration
	maxAttempts  int
	jobTTL       time.Duration
	now          func() time.Time
	webhook      webhookDispatcher
	stateStore   jobstore.Store
	deliveryRead webhookDeliveryReader

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
	JobsTotal    int                      `json:"jobs_total"`
	ByStatus     map[string]int           `json:"by_status"`
	ByTenant     map[string]TenantMetrics `json:"by_tenant"`
}

func NewServer(snapshot *registry.Snapshot, opts ...Option) *Server {
	s := &Server{
		experts:      make(map[string]expertMetadata),
		jobs:         make(map[string]*job),
		idem:         make(map[string]idempotencyRecord),
		processDelay: 200 * time.Millisecond,
		maxAttempts:  3,
		jobTTL:       24 * time.Hour,
		now:          time.Now,
		webhook:      deliverywebhook.NewDispatcher(nil),
		stateStore:   jobstore.NewNopStore(),
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

	if err := s.loadState(); err != nil {
		log.Printf("load job state failed: %v", err)
	}

	s.httpHandler = openapi.NewHandler(s)
	return s
}

func WithWebhookDispatcher(dispatcher webhookDispatcher) Option {
	return func(s *Server) {
		if dispatcher != nil {
			s.webhook = dispatcher
		}
	}
}

func WithJobStore(store jobstore.Store) Option {
	return func(s *Server) {
		if store != nil {
			s.stateStore = store
		}
	}
}

func WithWebhookDeliveryReader(reader webhookDeliveryReader) Option {
	return func(s *Server) {
		s.deliveryRead = reader
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
		JobsTotal:    len(s.jobs),
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
			"status":    "ok",
			"jobs_total": len(s.jobs),
			"timestamp": s.now().UTC().Format(time.RFC3339Nano),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
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

	s.mu.Lock()
	defer s.mu.Unlock()

	if idempotencyKey != "" {
		if record, exists := s.idem[idempotencyKey]; exists {
			if record.PayloadHash != payloadHash {
				return http.StatusConflict, problem("/v1/assistant/jobs", requestID, "idempotency-mismatch", "Idempotency mismatch", "same Idempotency-Key with different payload"), nil
			}
			existing, found := s.jobs[record.JobID]
			if !found {
				return http.StatusInternalServerError, problem("/v1/assistant/jobs", requestID, "internal-error", "Internal error", "idempotency record exists without job"), nil
			}
			return http.StatusAccepted, apiv1.SubmitBriefResponse{
				JobId:       existing.ID,
				TenantId:    existing.TenantID,
				ProjectId:   existing.ProjectID,
				ExpertId:    existing.ExpertID,
				ExpertName:  stringPtr(existing.ExpertName),
				ExpertType:  expertTypePtr(existing.ExpertType),
				Status:      apiv1.JobStatus(existing.Status),
				SubmittedAt: existing.SubmittedAt.UTC().Format(time.RFC3339),
				RequestId:   stringPtr(requestID),
			}, nil
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
	if idempotencyKey != "" {
		s.idem[idempotencyKey] = idempotencyRecord{
			PayloadHash: payloadHash,
			JobID:       jobID,
		}
	}
	s.persistLocked()

	go s.runMockJob(jobID)

	return http.StatusAccepted, apiv1.SubmitBriefResponse{
		JobId:       newJob.ID,
		TenantId:    newJob.TenantID,
		ProjectId:   newJob.ProjectID,
		ExpertId:    newJob.ExpertID,
		ExpertName:  stringPtr(newJob.ExpertName),
		ExpertType:  expertTypePtr(newJob.ExpertType),
		Status:      apiv1.JobStatus(newJob.Status),
		SubmittedAt: newJob.SubmittedAt.UTC().Format(time.RFC3339),
		RequestId:   stringPtr(requestID),
	}, nil
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
	current.LastError = nil
	current.UpdatedAt = s.now().UTC()
	s.persistLocked()
	s.mu.Unlock()

	go s.runMockJob(params.JobID)

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

func (s *Server) runMockJob(jobID string) {
	if s.shouldSkipJob(jobID) {
		return
	}
	s.transitionJob(jobID, statusRunning, 30, nil)
	time.Sleep(s.processDelay)

	if s.shouldSkipJob(jobID) {
		return
	}
	s.transitionJob(jobID, statusRunning, 70, nil)
	time.Sleep(s.processDelay)

	s.mu.RLock()
	current, ok := s.jobs[jobID]
	s.mu.RUnlock()
	if !ok {
		return
	}

	now := s.now().UTC()
	title := fmt.Sprintf("%s market brief", current.Date)
	result := &apiv1.ExpertResult{
		Title:           &title,
		Answer:          fmt.Sprintf("Mock brief generated for expert %s on %s.", current.ExpertID, current.Date),
		Confidence:      0.82,
		FreshnessCutoff: now.Format(time.RFC3339),
		References: []apiv1.ReferenceRecord{
			{
				"kind":         "article",
				"ref_id":       "src_mock_001",
				"title":        "Mock Market Source",
				"uri":          "https://example.com/mock-source",
				"published_at": now.Add(-15 * time.Minute).Format(time.RFC3339),
			},
		},
		Limitations: []string{"mock runtime response"},
	}
	s.transitionJob(jobID, statusSucceeded, 100, result)

	s.mu.RLock()
	current, ok = s.jobs[jobID]
	s.mu.RUnlock()
	if !ok || current.Callback == nil || strings.TrimSpace(current.Callback.Url) == "" {
		return
	}

	callback := cloneCallbackConfig(current.Callback)
	if callback == nil {
		return
	}
	go s.dispatchJobCompletedWebhook(jobID, *callback)
}

func (s *Server) transitionJob(jobID, status string, progress int, result *apiv1.ExpertResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.jobs[jobID]
	if !ok {
		return
	}
	if current.Status == statusCanceled || current.Status == statusExpired {
		return
	}
	if isExpired(current, s.now().UTC()) {
		markExpired(current, s.now().UTC())
		s.persistLocked()
		return
	}
	current.Status = status
	current.Progress = progress
	current.UpdatedAt = s.now().UTC()
	if result != nil {
		current.Result = result
	}
	s.persistLocked()
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

func (s *Server) shouldSkipJob(jobID string) bool {
	s.expireJobIfNeeded(jobID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	current, ok := s.jobs[jobID]
	if !ok {
		return true
	}
	return current.Status == statusCanceled || current.Status == statusExpired
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

func (s *Server) dispatchJobCompletedWebhook(jobID string, callback apiv1.CallbackConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := s.webhook.DeliverJobCompleted(ctx, callback, deliverywebhook.JobCompletedData{
		JobID:     jobID,
		Status:    statusSucceeded,
		ResultURL: "/v1/assistant/jobs/" + jobID + "/result",
	})
	if err != nil {
		log.Printf("webhook delivery failed: job_id=%s callback_url=%s err=%v", jobID, callback.Url, err)
	}
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

func scopedIdempotencyKey(tenantID, projectID, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return tenantID + "::" + projectID + "::" + raw
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
	return (len(trimmed) + 3) / 4
}

func (s *Server) loadState() error {
	state, err := s.stateStore.Load(context.Background())
	if err != nil {
		return err
	}

	s.nextID = state.NextID
	s.jobs = make(map[string]*job, len(state.Jobs))
	for id, stored := range state.Jobs {
		s.jobs[id] = &job{
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
		if s.jobs[id].MaxAttempts <= 0 {
			s.jobs[id].MaxAttempts = s.maxAttempts
		}
	}

	s.idem = make(map[string]idempotencyRecord, len(state.Idempotency))
	for key, stored := range state.Idempotency {
		s.idem[key] = idempotencyRecord{
			PayloadHash: stored.PayloadHash,
			JobID:       stored.JobID,
		}
	}
	return nil
}

func (s *Server) persistLocked() {
	state := jobstore.State{
		NextID:      s.nextID,
		Jobs:        make(map[string]jobstore.Job, len(s.jobs)),
		Idempotency: make(map[string]jobstore.IdempotencyEntry, len(s.idem)),
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

	if err := s.stateStore.Save(context.Background(), state); err != nil {
		log.Printf("persist job state failed: %v", err)
	}
}
