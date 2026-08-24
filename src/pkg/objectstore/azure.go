package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
)

// azureStore is the Azure Blob Storage ObjectStore backend. The bucket field
// holds the container name.
type azureStore struct {
	client    *azblob.Client
	container string
	sse       SSEConfig
}

// newAzureStore builds an Azure Blob client from either a connection string
// (also the way to target the Azurite emulator) or an account name + shared key.
func newAzureStore(_ context.Context, cfg *Config) (ObjectStore, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("azure: container (bucket) is required")
	}

	var (
		client *azblob.Client
		err    error
	)
	switch {
	case cfg.ConnectionString != "":
		client, err = azblob.NewClientFromConnectionString(cfg.ConnectionString, nil)
	case cfg.AccountName != "" && cfg.AccountKey != "":
		cred, cerr := azblob.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
		if cerr != nil {
			return nil, fmt.Errorf("azure: shared key credential: %w", cerr)
		}
		serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", cfg.AccountName)
		client, err = azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
	default:
		return nil, errors.New("azure: set connection_string, or account_name + account_key")
	}
	if err != nil {
		return nil, fmt.Errorf("azure: new client: %w", err)
	}
	return &azureStore{client: client, container: cfg.Bucket, sse: cfg.SSE}, nil
}

func (a *azureStore) List(ctx context.Context, prefix string) ([]Object, error) {
	var prefixPtr *string
	if prefix != "" {
		prefixPtr = &prefix
	}
	var out []Object
	pager := a.client.NewListBlobsFlatPager(a.container, &azblob.ListBlobsFlatOptions{Prefix: prefixPtr})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("azure list %q: %w", prefix, err)
		}
		if page.Segment == nil {
			continue
		}
		for _, b := range page.Segment.BlobItems {
			if b.Name == nil {
				continue
			}
			obj := Object{Key: *b.Name}
			if b.Properties != nil {
				if b.Properties.ContentLength != nil {
					obj.Size = *b.Properties.ContentLength
				}
				if b.Properties.LastModified != nil {
					obj.LastModified = *b.Properties.LastModified
				}
			}
			out = append(out, obj)
		}
	}
	return out, nil
}

func (a *azureStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := a.client.DownloadStream(ctx, a.container, key, nil)
	if err != nil {
		return nil, "", fmt.Errorf("azure get %q: %w", key, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("azure read %q: %w", key, err)
	}
	ct := ""
	if resp.ContentType != nil {
		ct = *resp.ContentType
	}
	return body, ct, nil
}

// GetStream returns the blob body as a stream. The caller must Close it. The
// download body is streamed, so a multi-GB blob is not buffered in memory.
func (a *azureStore) GetStream(ctx context.Context, key string) (io.ReadCloser, string, error) {
	resp, err := a.client.DownloadStream(ctx, a.container, key, nil)
	if err != nil {
		return nil, "", fmt.Errorf("azure get %q: %w", key, err)
	}
	ct := ""
	if resp.ContentType != nil {
		ct = *resp.ContentType
	}
	return resp.Body, ct, nil
}

// PutStream uploads from body using UploadStream, which stages the reader into
// blocks and commits them, so nothing is buffered whole in memory.
func (a *azureStore) PutStream(ctx context.Context, key string, body io.Reader, contentType string) error {
	opts := &azblob.UploadStreamOptions{}
	if contentType != "" {
		ct := contentType
		opts.HTTPHeaders = &blob.HTTPHeaders{BlobContentType: &ct}
	}
	if a.sse.KMSKeyID != "" {
		scope := a.sse.KMSKeyID
		opts.CPKScopeInfo = &blob.CPKScopeInfo{EncryptionScope: &scope}
	}
	if _, err := a.client.UploadStream(ctx, a.container, key, body, opts); err != nil {
		return fmt.Errorf("azure put-stream %q: %w", key, err)
	}
	return nil
}

func (a *azureStore) Put(ctx context.Context, key string, body []byte, contentType string) error {
	opts := &azblob.UploadBufferOptions{}
	if contentType != "" {
		ct := contentType
		opts.HTTPHeaders = &blob.HTTPHeaders{BlobContentType: &ct}
	}
	// Azure blobs are always encrypted at rest; an encryption scope (named in
	// KMSKeyID) is the per-blob configurable knob.
	if a.sse.KMSKeyID != "" {
		scope := a.sse.KMSKeyID
		opts.CPKScopeInfo = &blob.CPKScopeInfo{EncryptionScope: &scope}
	}
	if _, err := a.client.UploadBuffer(ctx, a.container, key, body, opts); err != nil {
		return fmt.Errorf("azure put %q: %w", key, err)
	}
	return nil
}

func (a *azureStore) Delete(ctx context.Context, key string) error {
	if _, err := a.client.DeleteBlob(ctx, a.container, key, nil); err != nil {
		return fmt.Errorf("azure delete %q: %w", key, err)
	}
	return nil
}

// Copy implements after_action=move via a streamed download+upload. Azure's
// server-side copy (StartCopyFromURL) needs a SAS-signed source URL and, for
// large blobs, an async poll-until-complete loop, so a client-side round-trip is
// simpler and the only caller is the move after-action. Unlike the previous
// buffered Get+Put, this streams source→dest so a multi-GB blob is bounded to a
// small buffer instead of being held whole in memory (which would OOM the worker).
func (a *azureStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	rc, ct, err := a.GetStream(ctx, srcKey)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := a.PutStream(ctx, dstKey, rc, ct); err != nil {
		return fmt.Errorf("azure copy %q->%q: %w", srcKey, dstKey, err)
	}
	return nil
}

// Close is a no-op: the azblob client holds no resources requiring release.
func (s *azureStore) Close() error { return nil }
