package sdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strconv"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Large-payload claim-check. Payloads at or below the inline limit travel inside
// the envelope on NATS; larger ones are offloaded to a platform object store and
// the envelope carries only a reference (PayloadRef). This keeps every message
// under NATS's max_payload (1 MB by default) regardless of payload size, and is
// wired centrally into the SDK publish/consume path so EVERY connector benefits.
//
// NOTE (scope): the payload is still held whole in memory at the connector
// boundary (connectors produce/consume []byte), so this lifts the *message-bus*
// ceiling — payloads far beyond 1 MB now flow — but the practical limit is
// worker memory. Truly unbounded (multi-GB) transfer additionally requires the
// connector payload contract to become a stream (io.Reader). See #187.
const defaultInlineMaxBytes = 256 * 1024

// defaultRehydrateMaxBytes bounds how much an offloaded payload may be buffered
// back into memory for a connector that speaks []byte. Worker pods run with a
// 512 MiB limit (orchestrator.MemoryLimit), so 128 MiB leaves comfortable room
// for the envelope copy and parsing overhead. Past this the payload is rejected
// rather than OOM-killing the worker; a streaming-capable connector (ADR 0001)
// bypasses this path entirely and has no such limit.
const defaultRehydrateMaxBytes = 128 * 1024 * 1024

const (
	envInlineMax      = "PAYLOAD_INLINE_MAX_BYTES"
	envRehydrateMax   = "PAYLOAD_REHYDRATE_MAX_BYTES"
	envStoreProvider  = "PAYLOAD_STORE_PROVIDER"
	envStoreBucket    = "PAYLOAD_STORE_BUCKET"
	envStoreEndpoint  = "PAYLOAD_STORE_ENDPOINT"
	envStoreRegion    = "PAYLOAD_STORE_REGION"
	envStoreAccessKey = "PAYLOAD_STORE_ACCESS_KEY"
	envStoreSecretKey = "PAYLOAD_STORE_SECRET_KEY"
)

// openPayloadStore builds the platform object store used for large-payload
// offload from env. Returns (nil, nil) when unconfigured (no bucket set) — the
// common case for deployments that never exceed the inline limit; the publish
// path then keeps payloads inline and logs if one is too large to fit.
func openPayloadStore(ctx context.Context, logger *slog.Logger) (objectstore.ObjectStore, error) {
	bucket := os.Getenv(envStoreBucket)
	if bucket == "" {
		return nil, nil
	}
	access := os.Getenv(envStoreAccessKey)
	secret := os.Getenv(envStoreSecretKey)
	// Both or neither: a lone key makes the S3 backend silently fall back to the
	// AWS default credential chain, which then fails opaquely against MinIO. Fail
	// fast at startup instead.
	if (access == "") != (secret == "") {
		return nil, fmt.Errorf("payload store: set both %s and %s, or neither", envStoreAccessKey, envStoreSecretKey)
	}
	cfg := &objectstore.Config{
		Provider:        os.Getenv(envStoreProvider),
		Bucket:          bucket,
		Region:          os.Getenv(envStoreRegion),
		Endpoint:        os.Getenv(envStoreEndpoint),
		AccessKeyID:     access,
		SecretAccessKey: secret,
	}
	store, err := objectstore.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open payload store: %w", err)
	}
	provider := cfg.Provider
	if provider == "" {
		provider = objectstore.ProviderS3
	}
	logger.Info("payload offload store configured", "provider", provider, "bucket", bucket)
	return store, nil
}

// envBytes resolves a byte-size setting from the environment.
//
// Parsed as int64 rather than through envInt: these are memory limits an
// operator may legitimately set above 2 GiB, and int is not guaranteed to be
// 64-bit. A malformed value is logged and falls back to the default rather than
// being swallowed — somebody raising a cap needs to find out that their value
// did not take effect, and finding out from a warning beats finding out from a
// payload that is still being rejected.
func envBytes(key string, def int64, logger *slog.Logger) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		logger.Warn("invalid byte-size setting; falling back to the default",
			"env", key, "value", raw, "default", def, "error", err)
		return def
	}
	return n
}

// inlineMaxFromEnv resolves the inline payload limit (PAYLOAD_INLINE_MAX_BYTES),
// defaulting to 256 KiB. A non-positive value disables offload (everything stays
// inline), which is the pre-claim-check behavior.
func inlineMaxFromEnv(logger *slog.Logger) int {
	// Compared against len(payload), so it lives in an int. Clamped rather than
	// truncated: a wrapped negative here would silently disable offload.
	n := envBytes(envInlineMax, defaultInlineMaxBytes, logger)
	if n > math.MaxInt {
		return math.MaxInt
	}
	return int(n)
}

// rehydrateMaxFromEnv resolves the buffering cap (PAYLOAD_REHYDRATE_MAX_BYTES),
// defaulting to 128 MiB. A non-positive value disables the cap (unbounded
// buffering — only sensible if worker memory has been raised to match).
func rehydrateMaxFromEnv(logger *slog.Logger) int64 {
	return envBytes(envRehydrateMax, defaultRehydrateMaxBytes, logger)
}

// payloadChecksum is the canonical "sha256:<hex>" digest stamped on offloaded
// payloads and verified when they are read back.
func payloadChecksum(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// payloadKey is the object key an offloaded payload is stored under. Tenant- and
// connection-scoped, unique per envelope so concurrent workers never collide.
func payloadKey(env *envelope.Envelope) string {
	return fmt.Sprintf("spill/%s/%s/%s", env.TenantID, env.IntegrationID, env.ID)
}

// offloadIfLarge moves an over-threshold payload to the object store and rewrites
// the envelope to carry a reference (claim-check) instead of the bytes, so the
// published message stays small. Returns true if it offloaded.
//
// If the payload is large but no store is configured, it logs and leaves the
// payload inline: the publish then fails at NATS if it exceeds max_payload,
// which surfaces the misconfiguration rather than silently losing data.
func offloadIfLarge(ctx context.Context, store objectstore.ObjectStore, env *envelope.Envelope, inlineMax int, logger *slog.Logger) (bool, error) {
	if inlineMax <= 0 || len(env.Payload) <= inlineMax {
		return false, nil
	}
	if store == nil {
		logger.Warn("large payload but no offload store configured; keeping inline",
			"size", len(env.Payload), "inline_max", inlineMax, "envelope_id", env.ID)
		return false, nil
	}
	key := payloadKey(env)
	if err := store.PutStream(ctx, key, bytes.NewReader(env.Payload), env.ContentType); err != nil {
		return false, fmt.Errorf("offload payload %q: %w", key, err)
	}
	env.PayloadSize = int64(len(env.Payload))
	env.Checksum = payloadChecksum(env.Payload)
	env.PayloadRef = key
	env.Payload = nil
	return true, nil
}

// rehydrate loads an offloaded payload back into the envelope before the
// connector sees it, and clears the reference. No-op when the payload is inline.
// A missing/unreadable object is returned as an error so the message is retried
// (the object may be transiently unavailable) rather than delivered empty.
func rehydrate(ctx context.Context, store objectstore.ObjectStore, env *envelope.Envelope, maxBytes int64) error {
	if env.PayloadRef == "" {
		return nil
	}
	if store == nil {
		return fmt.Errorf("envelope %s references offloaded payload %q but no offload store is configured", env.ID, env.PayloadRef)
	}
	// Rejection happens in two stages. The declared size is checked first, so an
	// honestly-labelled oversized payload costs nothing to refuse; but
	// PayloadSize is envelope metadata and may be absent or understated, so the
	// read below is bounded as well. Either way the worker never buffers a
	// payload this large, which is the OOM no retry could fix.
	//
	// Returning a plain error rides the SDK's default Retriable classification ON
	// PURPOSE — Permanent would ack and silently drop a customer payload, whereas
	// exhausting the retries routes the envelope to the DLQ, where an operator can
	// inspect it and replay once a streaming-capable connector is in place. The
	// spilled object outlives the message (1-day lifecycle TTL), so replay within
	// that window recovers the data.
	if maxBytes > 0 && env.PayloadSize > maxBytes {
		return fmt.Errorf("payload %q is %d bytes, over the %d-byte rehydrate cap (%s): this connector buffers payloads in memory; use a streaming-capable connector or raise the cap",
			env.PayloadRef, env.PayloadSize, maxBytes, envRehydrateMax)
	}
	rc, ct, err := store.GetStream(ctx, env.PayloadRef)
	if err != nil {
		return fmt.Errorf("rehydrate payload %q: %w", env.PayloadRef, err)
	}
	defer rc.Close()
	// PayloadSize is envelope metadata and may understate the object, so bound
	// the read itself too (+1 byte to detect an over-cap object).
	var src io.Reader = rc
	if maxBytes > 0 {
		src = io.LimitReader(rc, maxBytes+1)
	}
	body, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("rehydrate read %q: %w", env.PayloadRef, err)
	}
	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return fmt.Errorf("object %q exceeds the %d-byte rehydrate cap (%s) despite a declared size of %d",
			env.PayloadRef, maxBytes, envRehydrateMax, env.PayloadSize)
	}
	// Verify integrity across the offload hop. Skipped when the envelope carries
	// no checksum (inline-era envelopes still in flight during a rollout).
	if env.Checksum != "" {
		if got := payloadChecksum(body); got != env.Checksum {
			return fmt.Errorf("checksum mismatch for %q: envelope declares %s, object is %s", env.PayloadRef, env.Checksum, got)
		}
	}
	env.Payload = body
	// Fall back to the store's content type if the envelope didn't carry one, so
	// connectors always see a content type when the object has one.
	if env.ContentType == "" {
		env.ContentType = ct
	}
	env.PayloadRef = ""
	return nil
}
