package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// gcsStore is the Google Cloud Storage ObjectStore backend.
type gcsStore struct {
	client *storage.Client
	bucket string
	sse    SSEConfig
}

// newGCSStore builds a GCS client. Credentials come from a service-account JSON
// when set; an Endpoint override targets the fake-gcs-server emulator (no auth).
func newGCSStore(ctx context.Context, cfg *Config) (ObjectStore, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("gcs: bucket is required")
	}

	var opts []option.ClientOption
	if cfg.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(cfg.Endpoint))
	}
	switch {
	case cfg.CredentialsJSON != "":
		opts = append(opts, option.WithCredentialsJSON([]byte(cfg.CredentialsJSON)))
	case cfg.Endpoint != "":
		// Emulator (fake-gcs-server) needs no credentials.
		opts = append(opts, option.WithoutAuthentication())
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs: new client: %w", err)
	}
	return &gcsStore{client: client, bucket: cfg.Bucket, sse: cfg.SSE}, nil
}

func (g *gcsStore) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	it := g.client.Bucket(g.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gcs list %q: %w", prefix, err)
		}
		if attrs.Name == "" { // synthetic prefix entry
			continue
		}
		out = append(out, Object{Key: attrs.Name, Size: attrs.Size, LastModified: attrs.Updated})
	}
	return out, nil
}

func (g *gcsStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	rc, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("gcs get %q: %w", key, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("gcs read %q: %w", key, err)
	}
	return body, rc.Attrs.ContentType, nil
}

func (g *gcsStore) Put(ctx context.Context, key string, body []byte, contentType string) error {
	w := g.client.Bucket(g.bucket).Object(key).NewWriter(ctx)
	if contentType != "" {
		w.ContentType = contentType
	}
	// GCS objects are always encrypted at rest; a customer-managed key (CMEK,
	// named in KMSKeyID) is the configurable knob.
	if g.sse.KMSKeyID != "" {
		w.KMSKeyName = g.sse.KMSKeyID
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("gcs put %q: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("gcs put %q close: %w", key, err)
	}
	return nil
}

func (g *gcsStore) Delete(ctx context.Context, key string) error {
	if err := g.client.Bucket(g.bucket).Object(key).Delete(ctx); err != nil {
		return fmt.Errorf("gcs delete %q: %w", key, err)
	}
	return nil
}

func (g *gcsStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	src := g.client.Bucket(g.bucket).Object(srcKey)
	dst := g.client.Bucket(g.bucket).Object(dstKey)
	if _, err := dst.CopierFrom(src).Run(ctx); err != nil {
		return fmt.Errorf("gcs copy %q->%q: %w", srcKey, dstKey, err)
	}
	return nil
}
