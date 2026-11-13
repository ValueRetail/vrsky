// Package objectstore is a thin, provider-agnostic interface over cloud object
// storage (Amazon S3, Azure Blob, Google Cloud Storage). The cloud-storage
// consumer and producer connectors program against ObjectStore so their logic
// is identical across providers; the concrete backend is chosen at runtime from
// Config.Provider via New.
//
// PR 1 of #80 ships the interface + the S3 backend (which also drives any
// S3-compatible store such as MinIO via Config.Endpoint). Azure Blob and GCS
// backends land in PR 2; per-bucket server-side encryption (SSEConfig) is
// applied in PR 3.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Provider identifiers (Config.Provider).
const (
	ProviderS3    = "s3"
	ProviderAzure = "azure"
	ProviderGCS   = "gcs"
)

// ErrNotFound is wrapped by Get, GetStream and Stat (on every backend) when
// the object does not exist. A missing bucket or container, a denied request and a
// server error are not ErrNotFound. Test for it with errors.Is.
var ErrNotFound = errors.New("objectstore: object not found")

// ErrVersionChanged is wrapped by DeleteIf and CopyIf when the object under
// the key is not at the version given: it was written again since that
// version was read. Test for it with errors.Is.
var ErrVersionChanged = errors.New("objectstore: object version changed")

// Conditional is implemented by backends that can delete or copy an object
// only while it is still at a given Version, in the same request (#718). It
// closes the moment between a Stat and the delete in which a new upload under
// the same key would be removed unread. All three backends implement it; test
// fakes and wrappers need not, and callers fall back to Delete and Copy.
//
// A store that ignores the condition (the pinned MinIO does for DeleteObject)
// deletes anyway, so callers keep their own Stat check.
type Conditional interface {
	// DeleteIf removes key only while its Version is version. Otherwise the
	// error wraps ErrVersionChanged; a missing key wraps ErrNotFound.
	DeleteIf(ctx context.Context, key, version string) error
	// CopyIf copies srcKey to dstKey only while srcKey's Version is version,
	// with the same errors as DeleteIf. The destination is written
	// unconditionally.
	CopyIf(ctx context.Context, srcKey, dstKey, version string) error
}

// versionChanged wraps a provider's precondition failure as ErrVersionChanged.
func versionChanged(err error) error {
	return fmt.Errorf("%w: %w", ErrVersionChanged, err)
}

// quotedETag is an Object.Version from S3 or Azure as an If-Match value.
func quotedETag(version string) string {
	return `"` + version + `"`
}

// notFound wraps a provider SDK error that says the object is missing so that
// errors.Is(err, ErrNotFound) holds and the original error stays in the chain.
func notFound(err error) error {
	return fmt.Errorf("%w: %w", ErrNotFound, err)
}

// Object is a single stored object as returned by List and Stat.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
	// Version changes when the object is written again under the same key:
	// the ETag on S3 and Azure, the generation on GCS. Empty when the backend
	// did not return one. Compare it only for equality.
	Version string
}

// etagVersion is an ETag as Object.Version. Providers quote it in some answers
// and not in others (Azure: quoted in a header, bare in a listing).
func etagVersion(etag string) string {
	return strings.Trim(etag, `"`)
}

// SSEConfig describes per-bucket server-side encryption. Backends apply it on
// Put. Implemented in PR 3 — the zero value (Mode == "" / "none") means no
// explicit SSE (the bucket's default applies).
type SSEConfig struct {
	Mode     string `json:"mode"`       // "" | "none" | "sse-s3" | "sse-kms" (provider-specific)
	KMSKeyID string `json:"kms_key_id"` // key id/arn for sse-kms (and Azure/GCS CMEK equivalents)
}

// ObjectStore is the minimal surface the connectors need. Implementations wrap a
// provider SDK; tests inject an in-memory fake so connector logic runs without a
// live store (no Docker).
type ObjectStore interface {
	// List returns every (non-directory) object under prefix.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Get fetches an object's bytes and its content type (may be empty). It
	// buffers the whole object in memory, so it's for small payloads only; use
	// GetStream for anything that may be large. A missing object returns an
	// error wrapping ErrNotFound.
	Get(ctx context.Context, key string) (body []byte, contentType string, err error)
	// Put writes body under key with the given content type (empty = backend
	// default). It holds the whole object in memory; use PutStream for large
	// payloads.
	Put(ctx context.Context, key string, body []byte, contentType string) error
	// GetStream opens an object for streaming reads without buffering it in
	// memory — the path for large (multi-GB) payloads. The caller MUST Close the
	// returned reader. contentType may be empty. A missing object returns an
	// error wrapping ErrNotFound.
	GetStream(ctx context.Context, key string) (body io.ReadCloser, contentType string, err error)
	// PutStream writes an object by streaming from body, without holding the
	// whole object in memory. Backends use the provider's native multipart /
	// chunked upload, so a multi-GB object transfers with a bounded buffer. The
	// caller retains ownership of body (PutStream does not Close it).
	PutStream(ctx context.Context, key string, body io.Reader, contentType string) error
	// Stat returns an object's size, modification time and version without
	// reading it. A missing object returns an error wrapping ErrNotFound.
	Stat(ctx context.Context, key string) (Object, error)
	// Delete removes an object.
	Delete(ctx context.Context, key string) error
	// Copy copies srcKey to dstKey within the same bucket (used to implement
	// after_action=move as copy+delete). Backends use a native server-side copy
	// where available (S3, GCS); the Azure backend falls back to download+upload.
	Copy(ctx context.Context, srcKey, dstKey string) error
	// Close releases any resources held by the backend's underlying client
	// (e.g. the GCS storage.Client's connection pool). Callers that open a
	// store per operation must Close it to avoid leaking connections/goroutines.
	// S3 and Azure clients hold no closable state, so their Close is a no-op.
	Close() error
}

// Config is the resolved per-node configuration (after
// crypto.ResolveNodeSecrets has replaced *_secret_id references with
// plaintext). It carries the union of fields across providers; only the subset
// relevant to Provider is used.
type Config struct {
	Provider string `json:"provider"` // "s3" | "azure" | "gcs" (empty defaults to s3)
	Bucket   string `json:"bucket"`   // bucket (S3/GCS) or container (Azure)
	Prefix   string `json:"prefix"`   // key prefix to scope reads/writes

	// --- S3 ---
	Region          string `json:"region"`
	Endpoint        string `json:"endpoint"` // custom endpoint for MinIO / S3-compatible
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"` // secret (secret_access_key_secret_id)

	// --- Azure (PR 2) ---
	AccountName      string `json:"account_name"`
	AccountKey       string `json:"account_key"`       // secret
	ConnectionString string `json:"connection_string"` // secret (alternative to name+key)

	// --- GCS (PR 2) ---
	CredentialsJSON string `json:"credentials_json"` // secret (service-account JSON)

	// --- Server-side encryption (PR 3) ---
	SSE SSEConfig `json:"sse"`
}

// New constructs the backend for cfg.Provider. The returned ObjectStore is safe
// for concurrent use.
func New(ctx context.Context, cfg *Config) (ObjectStore, error) {
	if cfg == nil {
		return nil, errors.New("objectstore: nil config")
	}
	switch cfg.Provider {
	case ProviderS3, "":
		return newS3Store(ctx, cfg)
	case ProviderAzure:
		return newAzureStore(ctx, cfg)
	case ProviderGCS:
		return newGCSStore(ctx, cfg)
	default:
		return nil, fmt.Errorf("objectstore: unknown provider %q", cfg.Provider)
	}
}
