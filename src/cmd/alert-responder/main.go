// Command alert-responder diagnoses alerts and, in act mode, takes a small
// allowlisted set of actions to resolve them (plans/alert-responder.md).
//
// The management API hands every alert it receives from Alertmanager on over
// NATS (vrsky.alerts.<tenant>). For each, the responder runs Claude with tools
// that read the affected workspace through the management API — with that
// workspace's own API key, so isolation and the audit log are the API's — and
// posts its findings back through the same alerts webhook as a
// ResponderReport, which reaches the same Teams channel as the alert.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/ValueRetail/vrsky/pkg/health"
	"github.com/ValueRetail/vrsky/pkg/logging"
)

var runsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vrsky_responder_runs_total",
	Help: "Alert responder runs by outcome (reported, error, report_failed).",
}, []string{"outcome"})

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// parseTenantKeys reads RESPONDER_TENANT_KEYS: one "<tenant id>=<api key>"
// per line (or comma-separated). Lines starting with # are comments.
func parseTenantKeys(raw string) (map[string]string, error) {
	keys := map[string]string{}
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' }) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tenant, key, ok := strings.Cut(line, "=")
		tenant, key = strings.TrimSpace(tenant), strings.TrimSpace(key)
		if !ok || tenant == "" || key == "" {
			return nil, fmt.Errorf("RESPONDER_TENANT_KEYS: each entry must be <tenant id>=<api key>")
		}
		keys[tenant] = key
	}
	return keys, nil
}

func main() {
	logger := logging.New("alert-responder")
	slog.SetDefault(logger)
	if err := runService(logger); err != nil {
		logger.Error("alert-responder exited", "error", err)
		os.Exit(1)
	}
}

func runService(logger *slog.Logger) error {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("RESPONDER_MODE")))
	if mode == "" {
		mode = modeObserve
	}
	if mode != modeObserve && mode != modeAct {
		return fmt.Errorf("RESPONDER_MODE must be %q or %q", modeObserve, modeAct)
	}
	keys, err := parseTenantKeys(os.Getenv("RESPONDER_TENANT_KEYS"))
	if err != nil {
		return err
	}
	apiURL := strings.TrimRight(os.Getenv("MGMT_API_URL"), "/")
	token := os.Getenv("ALERTS_WEBHOOK_TOKEN")
	if apiURL == "" || token == "" {
		return fmt.Errorf("MGMT_API_URL and ALERTS_WEBHOOK_TOKEN are required")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is required")
	}
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hs := health.NewServer(health.DefaultConfig())
	if err := hs.Start(ctx); err != nil {
		return fmt.Errorf("health server: %w", err)
	}

	nc, err := nats.Connect(natsURL, nats.Name("alert-responder"), nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true))
	if err != nil {
		return fmt.Errorf("connect NATS: %w", err)
	}
	defer nc.Close()

	httpClient := &http.Client{Timeout: 35 * time.Second}
	maxCalls := envInt("RESPONDER_MAX_TOOL_CALLS", 25)
	s := &responder{
		mode:     mode,
		model:    newClaudeModel(os.Getenv("RESPONDER_MODEL"), maxCalls+2),
		api:      &apiClient{baseURL: apiURL, http: httpClient, keys: keys},
		guard:    newGuard(envInt("RESPONDER_MAX_RUNS_PER_HOUR", 20)),
		reporter: &webhookReporter{url: apiURL + "/api/v1/alerts/webhook", token: token, http: httpClient},
		maxCalls: maxCalls,
		logger:   logger,
		sem:      make(chan struct{}, 1),
	}
	sub, err := s.subscribe(nc)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	hs.SetReady(true)
	logger.Info("alert responder running", "mode", mode, "workspaces", len(keys), "max_tool_calls", maxCalls)

	<-ctx.Done()
	logger.Info("shutdown signal received")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Stop(shutdown)
}
