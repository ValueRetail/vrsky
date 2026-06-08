package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// eventMessage is one queue message carrying the object keys it references plus
// an opaque ack handle the source uses to delete it once ingested.
type eventMessage struct {
	objectKeys []string
	ackHandle  string
}

// eventSource is a queue of object-change notifications (e.g. S3 -> SQS). The
// consumer long-polls Receive, ingests the referenced objects, then Acks so the
// message is removed (at-least-once: ack only after a successful publish).
type eventSource interface {
	Receive(ctx context.Context) ([]eventMessage, error)
	Ack(ctx context.Context, handle string) error
}

// eventSourceFactory opens an eventSource for a resolved config. Defaulted to
// newEventSource in Configure; tests inject a fake.
type eventSourceFactory func(ctx context.Context, cfg *cloudConfig) (eventSource, error)

// newEventSource builds the event source for the provider. Only S3 (-> SQS) is
// implemented; Azure Storage Queue / GCS Pub/Sub are tracked follow-ups (poll
// mode covers those providers today).
func newEventSource(ctx context.Context, cfg *cloudConfig) (eventSource, error) {
	provider := cfg.Provider
	if provider == "" {
		provider = objectstore.ProviderS3
	}
	switch provider {
	case objectstore.ProviderS3:
		if cfg.EventQueueURL == "" {
			return nil, fmt.Errorf("event mode (s3) requires event_queue_url (an SQS queue URL)")
		}
		return newSQSEventSource(ctx, cfg)
	default:
		return nil, fmt.Errorf("event-driven mode is not implemented for provider %q yet (use poll mode)", provider)
	}
}

// sqsEventSource long-polls an SQS queue subscribed to S3 bucket notifications.
type sqsEventSource struct {
	client   *sqs.Client
	queueURL string
}

func newSQSEventSource(ctx context.Context, cfg *cloudConfig) (eventSource, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	// Static credentials only when both are set (a single field would build a
	// half-configured provider). Otherwise the AWS default credential chain.
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("sqs: load config: %w", err)
	}
	client := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		// Use the dedicated SQS endpoint override (e.g. LocalStack) — never the
		// S3/MinIO object-store endpoint, which is a different service.
		if cfg.EventEndpoint != "" {
			o.BaseEndpoint = aws.String(cfg.EventEndpoint)
		}
	})
	return &sqsEventSource{client: client, queueURL: cfg.EventQueueURL}, nil
}

func (s *sqsEventSource) Receive(ctx context.Context) ([]eventMessage, error) {
	out, err := s.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(s.queueURL),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     20, // long poll
		VisibilityTimeout:   30,
	})
	if err != nil {
		return nil, fmt.Errorf("sqs receive: %w", err)
	}
	var msgs []eventMessage
	for _, m := range out.Messages {
		msgs = append(msgs, eventMessage{
			objectKeys: parseS3Notification([]byte(aws.ToString(m.Body))),
			ackHandle:  aws.ToString(m.ReceiptHandle),
		})
	}
	return msgs, nil
}

func (s *sqsEventSource) Ack(ctx context.Context, handle string) error {
	if _, err := s.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(s.queueURL),
		ReceiptHandle: aws.String(handle),
	}); err != nil {
		return fmt.Errorf("sqs delete: %w", err)
	}
	return nil
}

// parseS3Notification extracts object keys from an S3 event-notification body.
// Keys are URL-encoded in the notification (spaces as '+'); they are decoded
// here. Non-object messages (e.g. the s3:TestEvent sent at setup) yield no keys.
func parseS3Notification(body []byte) []string {
	var n struct {
		Records []struct {
			S3 struct {
				Object struct {
					Key string `json:"key"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"Records"`
	}
	if err := json.Unmarshal(body, &n); err != nil {
		return nil
	}
	var keys []string
	for _, r := range n.Records {
		k := r.S3.Object.Key
		if k == "" {
			continue
		}
		k = strings.ReplaceAll(k, "+", " ")
		if dec, err := url.QueryUnescape(k); err == nil {
			k = dec
		}
		keys = append(keys, k)
	}
	return keys
}
