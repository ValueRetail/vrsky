# Migrating a worker onto the SDK — file-producer case study

file-producer was the PR #1 proof-of-concept. It went from **~870 to ~640
lines**; ~226 lines of boilerplate moved into the SDK.

## What was removed (now the SDK's job)

| Old code | Replaced by |
|----------|-------------|
| `LOG_LEVEL` parse + logger build | runner builds a labelled `*slog.Logger` |
| `Config` struct + env loading | connector reads its own env in `Configure` |
| `sql.Open` + ping + pool tuning | runner opens `DATABASE_URL` → `Resources.DB` |
| `initNATS` + `nc.JetStream()` | runner connects + provides JetStream |
| `messaging.Subscribe(...)` block | `RunProducer` + `Deliver` |
| `FileProducerService.Start/Stop`, `stopCh/stoppedCh` | runner lifecycle |
| `os/signal` handling in `main` | runner handles SIGTERM/SIGINT + 30s graceful stop |
| envelope unmarshal in `handleMessage` | runner unmarshals → `Deliver(ctx, env)` |
| hand-rolled `/files` `http.Server` | `RegisterHTTPHandler` + the SDK aux server |

## What stayed (domain logic)

`getConnectionConfigs` + cache, `writeFile` + path-safety + the folder feature,
`generateFilename`/`deriveExtension`, and the `/files` handler (now registered
via the SDK hook). `auth_test.go` is unchanged and still passes.

## The shape

```go
type fileProducer struct {
    sdk.BaseProducer
    db *sql.DB; defaultOutputDir string; allowedRoots []string
    // + config cache
}

func (p *fileProducer) Configure(ctx context.Context, res *sdk.Resources) error {
    if res.DB == nil { return errors.New("file-producer requires DATABASE_URL") }
    p.db = res.DB
    // ... output dir, allowedRoots ...
    p.RegisterHTTPHandler("/files", filesHandler(p.allowedRoots, authToken, allowedOrigin, p.logger))
    return nil
}

func (p *fileProducer) Deliver(ctx context.Context, env *sdk.Envelope) error {
    // former handleMessage body; transient write errors → sdk.Retriable;
    // path-not-allowed → sdk.Permanent (drop)
}

func main() { sdk.RunProducer(context.Background(), "file-producer", &fileProducer{}) }
```

## Behavior deltas to know

- `Deliver` receives the parsed envelope, not the raw NATS subject. file-producer
  previously fell back to parsing the connection ID from the subject when
  `IntegrationID` was empty; it now requires `IntegrationID` (returns Permanent
  otherwise). In practice the envelope always carries it.
- A transient write failure now **retries** (was silently dropped). A
  path-not-allowed config error is dropped (logged), as before.
- `/health` now lives on `HEALTH_PORT` (default 8080); `/files` stays on
  `FILE_PRODUCER_HTTP_PORT` (9900). The container healthcheck is file-based, so
  this move is transparent.

## Testing it without Docker

```go
h := harness.NewProducerHarness(t, &fileProducer{}, harness.Options{Name: "file-producer", DB: sqlmockDB})
h.Publish(t, env)
harness.Eventually(t, 5*time.Second, "file written", func() bool { ... os.Stat ... })
```

See `cmd/file-producer/producer_test.go`.
