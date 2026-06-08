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
	"time"
)

// Provider identifiers (Config.Provider).
const (
	ProviderS3    = "s3"
	ProviderAzure = "azure"
	ProviderGCS   = "gcs"
)

// Object is a single stored object as returned by List.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
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
	// Get fetches an object's bytes and its content type (may be empty).
	Get(ctx context.Context, key string) (body []byte, contentType string, err error)
	// Put writes body under key with the given content type (empty = backend default).
	Put(ctx context.Context, key string, body []byte, contentType string) error
	// Delete removes an object.
	Delete(ctx context.Context, key string) error
	// Copy server-side-copies srcKey to dstKey within the same bucket
	// (used to implement after_action=move as copy+delete).
	Copy(ctx context.Context, srcKey, dstKey string) error
}

// Config is the resolved per-node configuration (after
// crypto.ResolveSecretsInJSON has replaced *_secret_id references with
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
		return nil, fmt.Errorf("objectstore: provider %q not implemented yet (#80 PR2)", cfg.Provider)
	case ProviderGCS:
		return nil, fmt.Errorf("objectstore: provider %q not implemented yet (#80 PR2)", cfg.Provider)
	default:
		return nil, fmt.Errorf("objectstore: unknown provider %q", cfg.Provider)
	}
}
