package sdk

import (
	"context"
	"log/slog"

	"github.com/ValueRetail/vrsky/pkg/claimcheck"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Large-payload claim-check. The implementation lives in pkg/claimcheck so the
// standalone transform services (cmd/data-filter, cmd/data-converter — not on
// the SDK runner, see ADR 0002) share the exact offload/rehydrate semantics;
// these wrappers keep the SDK's internal call sites and tests unchanged.
//
// NOTE (scope): on this buffered path the payload is still held whole in memory
// at the connector boundary — the rehydrate cap bounds that. Truly unbounded
// (multi-GB) transfer uses the streaming contract (streaming.go, ADR 0001).
const defaultRehydrateMaxBytes = claimcheck.DefaultRehydrateMaxBytes

const envStoreBucket = claimcheck.EnvStoreBucket

func openPayloadStore(ctx context.Context, logger *slog.Logger) (objectstore.ObjectStore, error) {
	return claimcheck.OpenStoreFromEnv(ctx, logger)
}

func inlineMaxFromEnv(logger *slog.Logger) int { return claimcheck.InlineMaxFromEnv(logger) }

func rehydrateMaxFromEnv(logger *slog.Logger) int64 { return claimcheck.RehydrateMaxFromEnv(logger) }

func payloadChecksum(b []byte) string { return claimcheck.Checksum(b) }

func payloadKey(env *envelope.Envelope) string { return claimcheck.Key(env) }

func offloadIfLarge(ctx context.Context, store objectstore.ObjectStore, env *envelope.Envelope, inlineMax int, logger *slog.Logger) (bool, error) {
	return claimcheck.OffloadIfLarge(ctx, store, env, inlineMax, logger)
}

func rehydrate(ctx context.Context, store objectstore.ObjectStore, env *envelope.Envelope, maxBytes int64) error {
	return claimcheck.Rehydrate(ctx, store, env, maxBytes)
}
