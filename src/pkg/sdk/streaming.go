package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// Streaming payload contract (ADR 0001). The []byte contract in Consumer /
// Producer bounds a payload by worker memory (512 MiB pods → ~128 MiB cap, see
// payloadstore.go). A connector that already holds an io.Reader — a file on
// disk, an SFTP session, an object-store body, an HTTP response — can instead
// implement the optional interfaces below and move unbounded (multi-GB) payloads
// with memory bounded by the copy buffer.
//
// These are OPT-IN companions, not replacements: the SDK type-asserts for them
// and falls back to the existing []byte path, so connectors that deal in small
// records (the retail/ERP APIs) need no changes, and nothing new appears as a
// node type or in the UI.

// PublishStreamFunc emits a large payload without ever holding it in memory. The
// SDK streams body straight into the payload store (provider-native multipart),
// stamping PayloadRef, PayloadSize and Checksum on env as it goes, then
// publishes the small envelope. env carries the metadata (ID, TenantID,
// IntegrationID, ContentType); its Payload field is ignored and cleared.
//
// Requires a configured payload store — streaming has nowhere to go without one,
// so calling it on an unconfigured worker is a permanent error rather than a
// silent buffer.
type PublishStreamFunc func(ctx context.Context, env *envelope.Envelope, body io.Reader) error

// StreamingConsumer is a Consumer that can also ingest large payloads as
// streams. When a connector implements it, the SDK calls RunStream instead of
// Run, supplying both publish (small payloads, unchanged semantics) and
// publishStream (large ones). Implement Run as well — it remains the contract
// for a worker whose payload store is not configured.
type StreamingConsumer interface {
	Consumer
	RunStream(ctx context.Context, publish PublishFunc, publishStream PublishStreamFunc) error
}

// ErrStreamUnsupported is returned by DeliverStream when THIS message cannot be
// streamed even though the connector generally can — the canonical case being
// fan-out to several destinations, which needs more than one pass over a payload
// that can only be read once. The SDK then falls back to buffered delivery via
// Deliver, so behaviour is identical to a non-streaming connector (and the
// rehydrate cap applies again). Returning it is cheap: nothing has been read.
var ErrStreamUnsupported = errors.New("sdk: this message cannot be streamed")

// StreamingProducer is a Producer that can deliver an offloaded payload straight
// from the object store. When the inbound envelope carries a PayloadRef and the
// connector implements this, the SDK skips rehydration entirely and hands over
// the object stream — so the rehydrate cap does not apply. body is checksum-
// verified as it is read: a corrupted object surfaces as a read error rather
// than silently-wrong data downstream.
//
// Deliver is still required, and still receives inline (small) payloads.
type StreamingProducer interface {
	Producer
	DeliverStream(ctx context.Context, env *envelope.Envelope, body io.Reader) error
}

// streamDeliverFunc is the SDK-internal shape of StreamingProducer.DeliverStream;
// subscribeDispatch takes it as an optional streaming path (nil for connectors
// and node kinds that don't stream).
type streamDeliverFunc func(ctx context.Context, env *envelope.Envelope, body io.Reader) error

// newPublishStream builds the PublishStreamFunc handed to a StreamingConsumer.
func newPublishStream(publish PublishFunc, res *Resources) PublishStreamFunc {
	return func(ctx context.Context, env *envelope.Envelope, body io.Reader) error {
		if res.payloadStore == nil {
			return Permanent(fmt.Errorf("publishStream requires a payload offload store: set %s (and credentials) on this worker", envStoreBucket))
		}
		if env.ID == "" {
			// The object key is derived from the envelope ID; an empty one would
			// collide across concurrent messages and silently overwrite payloads.
			return Permanent(errors.New("publishStream: envelope ID must be set before publishing"))
		}

		// Hash and count while the store consumes the reader, so neither the
		// checksum nor the size costs an extra pass or a buffer.
		h := sha256.New()
		counter := &countingWriter{}
		tee := io.TeeReader(body, io.MultiWriter(h, counter))

		key := payloadKey(env)
		if err := res.payloadStore.PutStream(ctx, key, tee, env.ContentType); err != nil {
			return fmt.Errorf("stream payload %q: %w", key, err)
		}

		env.Payload = nil
		env.PayloadRef = key
		env.PayloadSize = counter.n
		env.Checksum = "sha256:" + hex.EncodeToString(h.Sum(nil))

		// Reuse the normal publish path: the envelope is now small (a reference),
		// so it takes the same route as any other message — offloadIfLarge is a
		// no-op on it, and tracing/metrics stay identical.
		return publish(ctx, env)
	}
}

// deliverStreamed hands an offloaded payload to a StreamingProducer as a stream,
// bypassing rehydration (and therefore the buffering cap).
func deliverStreamed(ctx context.Context, store objectstore.ObjectStore, env *envelope.Envelope, fn streamDeliverFunc) error {
	rc, ct, err := store.GetStream(ctx, env.PayloadRef)
	if err != nil {
		return fmt.Errorf("stream payload %q: %w", env.PayloadRef, err)
	}
	defer rc.Close()
	if env.ContentType == "" {
		env.ContentType = ct
	}
	// PayloadRef is deliberately left set: the connector is being handed the
	// object's contents, and the reference is useful context (logging, keys).
	return fn(ctx, env, newVerifyingReader(rc, env.Checksum))
}

// countingWriter counts bytes written; used to learn a streamed payload's size
// without buffering it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// verifyingReader checks a streamed payload against its expected checksum as it
// is read, failing the read at EOF on mismatch so the connector's own read
// surfaces the corruption. Verification is skipped when want is empty (envelopes
// published before checksums existed, still in flight across a rollout).
//
// A connector that stops reading early is not verified — it never saw the whole
// payload, so there is nothing to attest.
type verifyingReader struct {
	rc       io.ReadCloser
	h        hash.Hash
	want     string
	verified bool
}

func newVerifyingReader(rc io.ReadCloser, want string) io.Reader {
	return &verifyingReader{rc: rc, h: sha256.New(), want: want}
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	n, err := v.rc.Read(p)
	if n > 0 {
		v.h.Write(p[:n])
	}
	if errors.Is(err, io.EOF) && v.want != "" && !v.verified {
		v.verified = true
		if got := "sha256:" + hex.EncodeToString(v.h.Sum(nil)); got != v.want {
			return n, fmt.Errorf("checksum mismatch on streamed payload: expected %s, read %s", v.want, got)
		}
	}
	return n, err
}
