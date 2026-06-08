package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3Store is the S3 (and S3-compatible, e.g. MinIO) ObjectStore backend.
type s3Store struct {
	client *s3.Client
	bucket string
}

// newS3Store builds an S3 client. Static credentials are used when configured
// (the common case for a tenant's bucket); otherwise the AWS default credential
// chain applies (env/role). When Endpoint is set the client uses path-style
// addressing so it works against MinIO and other S3-compatible stores.
func newS3Store(ctx context.Context, cfg *Config) (ObjectStore, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket is required")
	}

	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // MinIO / S3-compatible stores need path-style.
		}
	})
	return &s3Store{client: client, bucket: cfg.Bucket}, nil
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 list %q: %w", prefix, err)
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			// Skip "folder" placeholder keys (zero-byte keys ending in "/").
			if key == "" || strings.HasSuffix(key, "/") {
				continue
			}
			obj := Object{Key: key, Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				obj.LastModified = *o.LastModified
			}
			out = append(out, obj)
		}
	}
	return out, nil
}

func (s *s3Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("s3 get %q: %w", key, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("s3 read %q: %w", key, err)
	}
	return body, aws.ToString(resp.ContentType), nil
}

func (s *s3Store) Put(ctx context.Context, key string, body []byte, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	// Server-side encryption is applied here in #80 PR3.
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("s3 put %q: %w", key, err)
	}
	return nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("s3 delete %q: %w", key, err)
	}
	return nil
}

func (s *s3Store) Copy(ctx context.Context, srcKey, dstKey string) error {
	// CopySource is "bucket/key" with the key URL-encoded. Escape each path
	// segment so special chars are encoded while "/" separators are preserved.
	if _, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		CopySource: aws.String(s.bucket + "/" + escapeKey(srcKey)),
		Key:        aws.String(dstKey),
	}); err != nil {
		return fmt.Errorf("s3 copy %q->%q: %w", srcKey, dstKey, err)
	}
	return nil
}

// escapeKey URL-encodes an object key segment-by-segment, preserving "/".
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
