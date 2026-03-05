package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"strings"
	"time"

	deliverywebhook "cova/internal/delivery/webhook"
	"cova/internal/orchestrator"
	"cova/internal/registry"
	"cova/internal/storage/jobstore"
	workerexecutor "cova/internal/worker/executor"
)

func main() {
	var addr string
	var registryPath string
	var jobStatePath string
	var workerStageDelay time.Duration
	var webhookMaxAttempts int
	var webhookInitialBackoff time.Duration
	var webhookMaxBackoff time.Duration
	var webhookRequestTimeout time.Duration
	var webhookJitter float64
	var webhookDeliveryLogPath string
	var metricsPath string
	var healthPath string
	var runtimeURL string
	var runtimeAuthToken string
	var runtimeAsync bool
	var runtimeTaskTimeout time.Duration
	var expertEventSigningSecret string
	var postgresDSN string
	var redisURL string
	var redisStream string
	var redisGroup string
	var redisConsumer string

	flag.StringVar(&addr, "addr", ":8081", "HTTP listen address")
	flag.StringVar(&registryPath, "registry", "configs/experts/registry.yaml", "path to expert registry yaml")
	flag.StringVar(&jobStatePath, "job-state-path", "docs/log/orchestrator_jobs_state.json", "path to persisted orchestrator job state, empty to keep in-memory only")
	flag.DurationVar(&workerStageDelay, "worker-stage-delay", 200*time.Millisecond, "mock worker execution delay per stage")
	flag.IntVar(&webhookMaxAttempts, "webhook-max-attempts", 10, "max webhook delivery attempts")
	flag.DurationVar(&webhookInitialBackoff, "webhook-initial-backoff", 200*time.Millisecond, "webhook retry initial backoff")
	flag.DurationVar(&webhookMaxBackoff, "webhook-max-backoff", 3*time.Second, "webhook retry max backoff")
	flag.DurationVar(&webhookRequestTimeout, "webhook-timeout", 3*time.Second, "per-attempt webhook request timeout")
	flag.Float64Var(&webhookJitter, "webhook-jitter", 0.2, "webhook retry jitter fraction, e.g. 0.2 means +/-20%")
	flag.StringVar(&webhookDeliveryLogPath, "webhook-delivery-log", "docs/log/webhook_deliveries.jsonl", "path to webhook delivery JSONL log, empty to disable persistence")
	flag.StringVar(&metricsPath, "metrics-path", "/metrics", "orchestrator metrics endpoint path")
	flag.StringVar(&healthPath, "health-path", "/healthz", "orchestrator health endpoint path")
	flag.StringVar(&runtimeURL, "runtime-url", "", "expert runtime base URL, e.g. http://127.0.0.1:8082")
	flag.StringVar(&runtimeAuthToken, "runtime-auth-token", "", "bearer token sent to expert runtime")
	flag.BoolVar(&runtimeAsync, "runtime-async", true, "request async mode when calling expert runtime")
	flag.DurationVar(&runtimeTaskTimeout, "runtime-task-timeout", 60*time.Second, "expert runtime task timeout")
	flag.StringVar(&expertEventSigningSecret, "expert-event-signing-secret", "", "HMAC secret for verifying /v1/expert/events callbacks")
	flag.StringVar(&postgresDSN, "postgres-dsn", "", "postgres DSN for orchestrator state store")
	flag.StringVar(&redisURL, "redis-url", "", "redis URL for durable queue, e.g. redis://127.0.0.1:6379/0")
	flag.StringVar(&redisStream, "redis-stream", "cova:jobs", "redis stream name for queue")
	flag.StringVar(&redisGroup, "redis-group", "cova-orchestrator", "redis stream consumer group")
	flag.StringVar(&redisConsumer, "redis-consumer", "orchestrator-1", "redis stream consumer name")
	flag.Parse()

	snapshot, err := registry.LoadSnapshot(registryPath)
	if err != nil {
		log.Fatalf("load registry snapshot: %v", err)
	}

	dispatcherOpts := []deliverywebhook.Option{
		deliverywebhook.WithMaxAttempts(webhookMaxAttempts),
		deliverywebhook.WithInitialBackoff(webhookInitialBackoff),
		deliverywebhook.WithMaxBackoff(webhookMaxBackoff),
		deliverywebhook.WithRequestTimeout(webhookRequestTimeout),
		deliverywebhook.WithJitterFraction(webhookJitter),
	}
	var deliveryReader deliverywebhook.Reader
	webhookDeliveryLogPath = strings.TrimSpace(webhookDeliveryLogPath)
	if webhookDeliveryLogPath != "" {
		store, err := deliverywebhook.NewJSONLStore(webhookDeliveryLogPath)
		if err != nil {
			log.Fatalf("init webhook delivery log store: %v", err)
		}
		dispatcherOpts = append(dispatcherOpts, deliverywebhook.WithStore(store))
		deliveryReader = store
		log.Printf("webhook deliveries will be persisted to %s", webhookDeliveryLogPath)
	}
	dispatcher := deliverywebhook.NewDispatcher(nil, dispatcherOpts...)

	var closers []func()
	jobStore := jobstore.NewNopStore()
	postgresDSN = strings.TrimSpace(postgresDSN)
	if postgresDSN != "" {
		store, err := jobstore.NewPostgresStore(context.Background(), postgresDSN)
		if err != nil {
			log.Fatalf("init postgres job state store: %v", err)
		}
		jobStore = store
		closers = append(closers, store.Close)
		log.Printf("orchestrator job state uses postgres store")
	} else {
		jobStatePath = strings.TrimSpace(jobStatePath)
		if jobStatePath != "" {
			store, err := jobstore.NewJSONFileStore(jobStatePath)
			if err != nil {
				log.Fatalf("init orchestrator job state store: %v", err)
			}
			jobStore = store
			log.Printf("orchestrator job state will be persisted to %s", jobStatePath)
		}
	}

	var exec workerexecutor.Executor = workerexecutor.NewMockExecutor(
		workerexecutor.WithStageDelay(workerStageDelay),
	)
	runtimeURL = strings.TrimSpace(runtimeURL)
	if runtimeURL != "" {
		exec = workerexecutor.NewRuntimeExecutor(
			runtimeURL,
			workerexecutor.WithRuntimeAuthToken(runtimeAuthToken),
			workerexecutor.WithRuntimeAsyncMode(runtimeAsync),
			workerexecutor.WithRuntimeTaskTimeout(runtimeTaskTimeout),
		)
		log.Printf("orchestrator uses expert runtime executor: %s (async=%v)", runtimeURL, runtimeAsync)
	}

	var eventVerifier *deliverywebhook.Verifier
	expertEventSigningSecret = strings.TrimSpace(expertEventSigningSecret)
	if expertEventSigningSecret != "" {
		eventVerifier = deliverywebhook.NewVerifier(expertEventSigningSecret)
		log.Printf("orchestrator expert event signature verification enabled")
	}

	serverOpts := []orchestrator.Option{
		orchestrator.WithExecutor(exec),
		orchestrator.WithWebhookDispatcher(dispatcher),
		orchestrator.WithWebhookDeliveryReader(deliveryReader),
		orchestrator.WithJobStore(jobStore),
	}
	redisURL = strings.TrimSpace(redisURL)
	if redisURL != "" {
		queue, err := orchestrator.NewRedisQueue(context.Background(), orchestrator.RedisQueueConfig{
			URL:      redisURL,
			Stream:   redisStream,
			Group:    redisGroup,
			Consumer: redisConsumer,
		})
		if err != nil {
			log.Fatalf("init redis queue: %v", err)
		}
		serverOpts = append(serverOpts, orchestrator.WithQueue(queue))
		log.Printf("orchestrator queue uses redis stream: stream=%s group=%s consumer=%s", redisStream, redisGroup, redisConsumer)
	}
	if eventVerifier != nil {
		serverOpts = append(serverOpts, orchestrator.WithExpertEventVerifier(eventVerifier))
	}

	handler := orchestrator.NewServer(snapshot, serverOpts...)
	defer handler.Close()
	defer func() {
		for _, closeFn := range closers {
			closeFn()
		}
	}()

	metricsPath = normalizePath(metricsPath, "/metrics")
	healthPath = normalizePath(healthPath, "/healthz")
	mux := http.NewServeMux()
	mux.Handle(metricsPath, handler.MetricsHandler())
	mux.Handle(healthPath, handler.HealthHandler())
	mux.Handle("/v1/expert/events", handler.ExpertEventsHandler())
	mux.Handle("/", handler)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("orchestrator listening on %s, enabled experts=%d, registry=%s (metrics=%s health=%s)", addr, len(snapshot.Experts), registryPath, metricsPath, healthPath)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("orchestrator server failed: %v", err)
	}
}

func normalizePath(path string, fallback string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = fallback
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}
