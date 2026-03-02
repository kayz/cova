package main

import (
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

	jobStore := jobstore.NewNopStore()
	jobStatePath = strings.TrimSpace(jobStatePath)
	if jobStatePath != "" {
		store, err := jobstore.NewJSONFileStore(jobStatePath)
		if err != nil {
			log.Fatalf("init orchestrator job state store: %v", err)
		}
		jobStore = store
		log.Printf("orchestrator job state will be persisted to %s", jobStatePath)
	}

	exec := workerexecutor.NewMockExecutor(
		workerexecutor.WithStageDelay(workerStageDelay),
	)

	handler := orchestrator.NewServer(
		snapshot,
		orchestrator.WithExecutor(exec),
		orchestrator.WithWebhookDispatcher(dispatcher),
		orchestrator.WithWebhookDeliveryReader(deliveryReader),
		orchestrator.WithJobStore(jobStore),
	)
	defer handler.Close()

	metricsPath = normalizePath(metricsPath, "/metrics")
	healthPath = normalizePath(healthPath, "/healthz")
	mux := http.NewServeMux()
	mux.Handle(metricsPath, handler.MetricsHandler())
	mux.Handle(healthPath, handler.HealthHandler())
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
