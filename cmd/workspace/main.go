// Command workspace runs the cova workspace server.
//
// The workspace server is the central hub that connects messaging channels
// (Feishu, WeChat Work, etc.) to coco agent instances. It manages:
//
//   - Channel integrations: receive and send messages to external platforms
//   - Agent connections: accept WebSocket connections from coco agents
//   - Message routing: route incoming messages to the appropriate agent(s)
//   - Agent registry: track which agents are registered and online
//
// Usage:
//
//	go run ./cmd/workspace \
//	  -registry configs/agents/registry.yaml \
//	  -feishu-app-id cli_xxx \
//	  -feishu-app-secret xxx
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cova/internal/agent"
	feishuch "cova/internal/channel/feishu"
	"cova/internal/routing"
)

func main() {
	var (
		addr             string
		registryPath     string
		authToken        string
		feishuAppID      string
		feishuAppSecret  string
		feishuBotMapping string // "botname=agentid,botname2=agentid2"
	)

	flag.StringVar(&addr, "addr", ":18900", "HTTP listen address")
	flag.StringVar(&registryPath, "registry", "configs/agents/registry.yaml", "agent registry path")
	flag.StringVar(&authToken, "auth-token", "", "shared auth token for agent connections")
	flag.StringVar(&feishuAppID, "feishu-app-id", "", "Feishu app ID")
	flag.StringVar(&feishuAppSecret, "feishu-app-secret", "", "Feishu app secret")
	flag.StringVar(&feishuBotMapping, "feishu-bot-mapping", "", "Feishu bot name to agent ID mapping (name=id,...)")
	flag.Parse()

	// Load agent registry
	reg, agents, err := agent.LoadRegistryWithAgents(registryPath)
	if err != nil {
		log.Fatalf("load agent registry: %v", err)
	}
	log.Printf("[workspace] loaded registry: workspace=%q, %d agents", reg.Workspace, len(agents))

	for _, a := range agents {
		log.Printf("[workspace]   agent: id=%s name=%s role=%s", a.ID, a.Name, a.Role)
	}

	// Create agent connection pool
	pool := agent.NewPool(agent.PoolConfig{
		AuthToken: authToken,
		Workspace: reg.Workspace,
	})

	// Register known agents
	for _, a := range agents {
		pool.RegisterAgent(a)
	}

	// Create message router
	router := routing.New(pool)

	// Wire up agent responses to go back through the router
	pool = agent.NewPool(agent.PoolConfig{
		AuthToken:  authToken,
		Workspace:  reg.Workspace,
		OnResponse: router.HandleAgentResponse,
	})
	for _, a := range agents {
		pool.RegisterAgent(a)
	}

	// Set up Feishu channel if configured
	if feishuAppID != "" && feishuAppSecret != "" {
		botMapping := parseBotMapping(feishuBotMapping)
		ch, err := feishuch.New(feishuch.Config{
			AppID:            feishuAppID,
			AppSecret:        feishuAppSecret,
			BotNameToAgentID: botMapping,
		})
		if err != nil {
			log.Fatalf("init feishu channel: %v", err)
		}
		router.RegisterChannel(ch)

		ctx := context.Background()
		if err := ch.Start(ctx); err != nil {
			log.Fatalf("start feishu channel: %v", err)
		}
		defer ch.Stop()
	} else {
		log.Printf("[workspace] feishu channel not configured (no app credentials)")
	}

	// Set up HTTP server
	mux := http.NewServeMux()

	// WebSocket endpoint for coco agents
	mux.HandleFunc("/ws", pool.HandleWebSocket)

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"workspace": reg.Workspace,
		})
	})

	// List agents API
	mux.HandleFunc("/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workspace": reg.Workspace,
			"agents":    pool.ListAgents(),
		})
	})

	// Webhook endpoint for agent responses (HTTP fallback)
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var resp agent.AgentResponse
		if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		agentID := r.Header.Get("X-Agent-Id")
		if agentID == "" {
			http.Error(w, "missing X-Agent-Id header", http.StatusBadRequest)
			return
		}
		router.HandleAgentResponse(agentID, resp)
		w.WriteHeader(http.StatusAccepted)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("[workspace] listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("[workspace] shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[workspace] shutdown error: %v", err)
	}
	log.Printf("[workspace] stopped")
}

func parseBotMapping(raw string) map[string]string {
	m := make(map[string]string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return m
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			name := strings.TrimSpace(parts[0])
			id := strings.TrimSpace(parts[1])
			if name != "" && id != "" {
				m[name] = id
			}
		}
	}
	return m
}
