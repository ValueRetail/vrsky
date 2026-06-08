//go:build integration

// Integration tests for the S3 backend against a live S3-compatible store
// (MinIO). Run with:
//
//	docker compose up -d minio-test
//	S3_TEST_ENDPOINT=http://localhost:9000 go test -tags=integration ./pkg/objectstore/...
//
// The test creates its own bucket and cleans up after itself. It is skipped
// unless S3_TEST_ENDPOINT is set, so a plain `go test` never touches the network.
package objectstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3_RoundTrip_Integration(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_TEST_ENDPOINT not set; skipping S3 integration test")
	}
	accessKey := envOr("S3_TEST_ACCESS_KEY", "minioadmin")
	secretKey := envOr("S3_TEST_SECRET_KEY", "minioadmin")
	const bucket = "objectstore-it"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Ensure the bucket exists (CreateBucket is idempotent enough for MinIO).
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	raw := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	_, _ = raw.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})

	store, err := New(ctx, &Config{
		Provider:        ProviderS3,
		Bucket:          bucket,
		Region:          "us-east-1",
		Endpoint:        endpoint,
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	key := "in/order-1.json"
	payload := []byte(`{"id":1,"name":"Acme"}`)
	if err := store.Put(ctx, key, payload, "application/json"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	objs, err := store.List(ctx, "in/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 1 || objs[0].Key != key {
		t.Fatalf("List = %+v, want one object %q", objs, key)
	}

	body, ct, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != string(payload) {
		t.Errorf("Get body = %q, want %q", body, payload)
	}
	if ct != "application/json" {
		t.Errorf("Get content-type = %q, want application/json", ct)
	}

	// Copy then delete the original (after_action=move semantics).
	dst := "in/processed/order-1.json"
	if err := store.Copy(ctx, key, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Get(ctx, dst); err != nil {
		t.Fatalf("Get after copy: %v", err)
	}
	if _, _, err := store.Get(ctx, key); err == nil {
		t.Error("Get of deleted key should fail")
	}

	// Cleanup.
	_ = store.Delete(ctx, dst)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
