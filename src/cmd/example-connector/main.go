// Command example-connector is the canonical, minimal VRSky connector — the
// reference every new connector can be cloned from. It is a Consumer: it
// ingests from the "outside world" (here, a timer) and publishes envelopes into
// the pipeline.
//
// Everything a real connector needs and nothing it doesn't:
//   - embed sdk.BaseConsumer            → Name/Type/Version/Start/Stop/Health for free
//   - Configure(ctx, *sdk.Resources)    → read your own env / wire dependencies
//   - Run(ctx, publish)                 → your ingestion loop; block until ctx is done
//   - func main() { sdk.RunConsumer(…) }→ the runner owns NATS, health, signals, shutdown
//
// Run it against a live stack with:
//
//	NATS_URL=nats://localhost:4222 EXAMPLE_INTERVAL=5s \
//	EXAMPLE_TENANT=tenant-1 EXAMPLE_CONNECTION=conn-1 go run ./cmd/example-connector
//
// See docs/sdk/ for the full guide. Total: well under 150 lines.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// exampleConnector emits one small JSON envelope per tick. A real connector
// would instead read a webhook, poll an API, watch a directory, etc.
type exampleConnector struct {
	sdk.BaseConsumer // gives us the component.Component plumbing

	logger   *slog.Logger
	interval time.Duration
	tenant   string
	conn     string
}

func main() {
	if err := sdk.RunConsumer(context.Background(), "example-connector", &exampleConnector{}); err != nil {
		slog.Error("example-connector exited", "error", err)
		os.Exit(1)
	}
}

// Configure is called once before ingestion starts. Read whatever environment
// or per-connection config you need here; res gives you a labelled logger, the
// management DB (if DATABASE_URL is set), the NATS connection (for control-plane
// work), and a readiness toggle.
func (c *exampleConnector) Configure(ctx context.Context, res *sdk.Resources) error {
	c.logger = res.Logger

	c.interval = 5 * time.Second
	if v := os.Getenv("EXAMPLE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("EXAMPLE_INTERVAL %q is not a duration: %w", v, err)
		}
		c.interval = d
	}

	// Every envelope must carry a tenant + connection (integration) id so the
	// pipeline can route it. A real connector derives these from the inbound
	// request / connection config; here they come from the environment.
	c.tenant = getenv("EXAMPLE_TENANT", "example-tenant")
	c.conn = getenv("EXAMPLE_CONNECTION", "example-connection")

	res.Health.SetReady(true)
	c.logger.Info("example-connector configured", "interval", c.interval, "tenant", c.tenant, "connection", c.conn)
	return nil
}

// Run drives the ingestion loop. It MUST block until ctx is cancelled (the SDK
// cancels it on SIGTERM/SIGINT). Call publish for each envelope you produce;
// the runner marshals it and emits it onto the data stream — you never touch
// NATS for the happy path.
func (c *exampleConnector) Run(ctx context.Context, publish sdk.PublishFunc) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	var seq int
	for {
		select {
		case <-ctx.Done():
			c.logger.Info("example-connector stopping", "emitted", seq)
			return nil
		case t := <-ticker.C:
			seq++
			env := c.build(seq, t)
			if err := publish(ctx, env); err != nil {
				// Transient publish errors are worth logging; the loop keeps
				// going. (A real connector might back off or surface health.)
				c.logger.Error("publish failed", "seq", seq, "error", err)
				continue
			}
			c.logger.Debug("published envelope", "id", env.ID, "seq", seq)
		}
	}
}

// build assembles one envelope. envelope.New() seeds the ID, timestamps and TTL;
// you fill in routing (tenant/integration), the payload, and any metadata.
func (c *exampleConnector) build(seq int, t time.Time) *envelope.Envelope {
	payload, _ := json.Marshal(map[string]any{
		"sequence":   seq,
		"emitted_at": t.UTC().Format(time.RFC3339),
		"message":    "hello from the reference connector",
	})

	env := envelope.New()
	env.TenantID = c.tenant
	env.IntegrationID = c.conn
	env.ContentType = "application/json"
	env.Source = "example-connector"
	env.Payload = payload
	env.PayloadSize = int64(len(payload))
	env.StepHistory = []string{"example-connector"}
	env.Metadata = map[string]any{"sequence": seq}
	return env
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
