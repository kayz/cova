package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	apigateway "cova/internal/api/gateway"
)

func main() {
	var addr string
	var orchestratorURL string
	var authEnabled bool
	var authTokensCSV string
	var authTokenBindingsCSV string
	var rateLimitEnabled bool
	var ratePerSecond float64
	var rateBurst int
	var maxBodyBytes int64
	var accessLogEnabled bool
	var metricsPath string

	flag.StringVar(&addr, "addr", ":8080", "HTTP listen address")
	flag.StringVar(&orchestratorURL, "orchestrator-url", "http://127.0.0.1:8081", "upstream orchestrator base URL")
	flag.BoolVar(&authEnabled, "auth-enabled", false, "enable bearer token authentication")
	flag.StringVar(&authTokensCSV, "auth-tokens", "", "comma-separated bearer tokens accepted by gateway (required when auth-enabled=true)")
	flag.StringVar(&authTokenBindingsCSV, "auth-token-bindings", "", "comma-separated token bindings: token=tenant_id:project_id")
	flag.BoolVar(&rateLimitEnabled, "rate-limit-enabled", true, "enable per-client rate limiting in gateway")
	flag.Float64Var(&ratePerSecond, "rate-limit-rps", 20, "per-client request rate per second")
	flag.IntVar(&rateBurst, "rate-limit-burst", 40, "per-client burst size")
	flag.Int64Var(&maxBodyBytes, "max-body-bytes", 1<<20, "max body size for JSON POST routes")
	flag.BoolVar(&accessLogEnabled, "access-log-enabled", true, "enable structured access logs")
	flag.StringVar(&metricsPath, "metrics-path", "/metrics", "gateway metrics endpoint path")
	flag.Parse()

	orchestratorURL = strings.TrimSpace(orchestratorURL)
	target, err := url.Parse(orchestratorURL)
	if err != nil {
		log.Fatalf("invalid orchestrator-url: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		if req.Header.Get("X-Request-Id") == "" {
			req.Header.Set("X-Request-Id", "gw_"+time.Now().UTC().Format("20060102T150405.000000000"))
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		log.Printf("gateway proxy error: method=%s path=%s err=%v", req.Method, req.URL.Path, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}

	middleware, err := apigateway.NewHandler(proxy, apigateway.Config{
		AuthEnabled:      authEnabled,
		AuthTokens:       splitCSV(authTokensCSV),
		AuthBindings:     parseAuthBindings(authTokenBindingsCSV),
		RateLimitEnabled: rateLimitEnabled,
		RatePerSecond:    ratePerSecond,
		RateBurst:        rateBurst,
		MaxBodyBytes:     maxBodyBytes,
		AccessLogEnabled: accessLogEnabled,
	})
	if err != nil {
		log.Fatalf("init gateway middleware: %v", err)
	}

	metricsPath = normalizePath(metricsPath, "/metrics")
	mux := http.NewServeMux()
	mux.Handle(metricsPath, middleware.MetricsHandler())
	mux.Handle("/", middleware)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("gateway listening on %s -> orchestrator %s (metrics: %s)", addr, orchestratorURL, metricsPath)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("gateway server failed: %v", err)
	}
}

func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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

func parseAuthBindings(raw string) []apigateway.AuthBinding {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	out := make([]apigateway.AuthBinding, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 {
			continue
		}
		token := strings.TrimSpace(kv[0])
		scope := strings.TrimSpace(kv[1])
		scopeParts := strings.SplitN(scope, ":", 2)
		if len(scopeParts) != 2 {
			continue
		}
		tenantID := strings.TrimSpace(scopeParts[0])
		projectID := strings.TrimSpace(scopeParts[1])
		if token == "" || tenantID == "" || projectID == "" {
			continue
		}
		out = append(out, apigateway.AuthBinding{
			Token:     token,
			TenantID:  tenantID,
			ProjectID: projectID,
		})
	}
	return out
}
