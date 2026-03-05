package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"strings"
	"time"

	expertruntime "cova/internal/expert/runtime"
	"cova/internal/registry"
	workerexecutor "cova/internal/worker/executor"
)

func main() {
	var addr string
	var registryPath string
	var authEnabled bool
	var authToken string
	var asyncDefault bool
	var stageDelay time.Duration
	var taskTimeout time.Duration
	var eventCallbackURL string
	var eventSigningSecret string
	var eventTimeout time.Duration

	flag.StringVar(&addr, "addr", ":8082", "HTTP listen address")
	flag.StringVar(&registryPath, "registry", "configs/experts/registry.yaml", "path to expert registry yaml")
	flag.BoolVar(&authEnabled, "auth-enabled", false, "enable bearer token authentication for runtime endpoints")
	flag.StringVar(&authToken, "auth-token", "", "bearer token accepted by runtime when auth-enabled=true")
	flag.BoolVar(&asyncDefault, "async-default", false, "default runtime task mode to async")
	flag.DurationVar(&stageDelay, "mock-stage-delay", 200*time.Millisecond, "mock runtime execution delay per stage")
	flag.DurationVar(&taskTimeout, "task-timeout", 180*time.Second, "runtime task execution timeout")
	flag.StringVar(&eventCallbackURL, "event-callback-url", "", "orchestrator expert events endpoint URL")
	flag.StringVar(&eventSigningSecret, "event-signing-secret", "", "HMAC secret for event callback signatures")
	flag.DurationVar(&eventTimeout, "event-timeout", 5*time.Second, "event callback HTTP timeout")
	flag.Parse()

	snapshot, err := registry.LoadSnapshot(registryPath)
	if err != nil {
		log.Fatalf("load registry snapshot: %v", err)
	}

	opts := []expertruntime.Option{
		expertruntime.WithExecutor(workerexecutor.NewMockExecutor(workerexecutor.WithStageDelay(stageDelay))),
		expertruntime.WithDefaultAsync(asyncDefault),
		expertruntime.WithDefaultTimeout(taskTimeout),
	}

	if authEnabled {
		authToken = strings.TrimSpace(authToken)
		if authToken == "" {
			log.Fatal("auth-token is required when auth-enabled=true")
		}
		opts = append(opts, expertruntime.WithAuthToken(authToken))
	} else {
		opts = append(opts, expertruntime.WithAuthDisabled())
	}

	eventCallbackURL = strings.TrimSpace(eventCallbackURL)
	if eventCallbackURL != "" {
		opts = append(opts, expertruntime.WithEventSink(expertruntime.NewHTTPEventSink(eventCallbackURL, eventSigningSecret, eventTimeout)))
		log.Printf("runtime events enabled: callback=%s", eventCallbackURL)
	}

	handler := expertruntime.NewServer(snapshot, opts...)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("expert runtime listening on %s (registry=%s)", addr, registryPath)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("expert runtime server failed: %v", err)
	}
}
