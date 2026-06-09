package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	subapi "cloud.google.com/go/pubsub/apiv1"
	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// gcsPubSubEventSource pulls from a Pub/Sub subscription that receives GCS
// object-change notifications (configured via `gcloud storage buckets
// notifications create`). It is the GCS parity of sqsEventSource: Receive pulls
// a batch of messages and parses their object keys; Ack acknowledges them after
// a successful publish.
//
// It uses the low-level SubscriberClient (Pull/Acknowledge) rather than the
// streaming high-level Subscription.Receive, because the pull/ack-handle shape
// maps directly onto the eventSource interface.
type gcsPubSubEventSource struct {
	client *subapi.SubscriberClient
	sub    string // full subscription path: projects/<project>/subscriptions/<sub>
}

// newGCSPubSubEventSource builds a SubscriberClient. Credentials come from a
// service-account JSON when set; an endpoint override targets the Pub/Sub
// emulator (no auth, insecure transport). The subscription may be a full path
// or a bare name (then event_project is required to build the path).
func newGCSPubSubEventSource(ctx context.Context, cfg *cloudConfig) (eventSource, error) {
	sub := cfg.EventSubscription
	if sub == "" {
		return nil, fmt.Errorf("event mode (gcs) requires event_subscription (a Pub/Sub pull subscription)")
	}

	var opts []option.ClientOption
	switch {
	case cfg.EventEndpoint != "":
		opts = append(opts,
			option.WithEndpoint(cfg.EventEndpoint),
			option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		)
	case cfg.CredentialsJSON != "":
		opts = append(opts, option.WithCredentialsJSON([]byte(cfg.CredentialsJSON)))
	}

	client, err := subapi.NewSubscriberClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs pubsub: new client: %w", err)
	}

	subPath := sub
	if !strings.Contains(sub, "/") {
		if cfg.EventProject == "" {
			_ = client.Close()
			return nil, fmt.Errorf("event mode (gcs) requires event_project when event_subscription is a bare name")
		}
		subPath = fmt.Sprintf("projects/%s/subscriptions/%s", cfg.EventProject, sub)
	}
	return &gcsPubSubEventSource{client: client, sub: subPath}, nil
}

func (g *gcsPubSubEventSource) Receive(ctx context.Context) ([]eventMessage, error) {
	// Bound the pull so the event loop can observe ctx cancellation (stop /
	// shutdown) between pulls rather than blocking on the server indefinitely.
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	resp, err := g.client.Pull(cctx, &pubsubpb.PullRequest{Subscription: g.sub, MaxMessages: 10})
	if err != nil {
		// A pull that times out with no messages is normal (not a real error):
		// report no messages so the loop pulls again without backing off.
		if ctx.Err() == nil && cctx.Err() != nil {
			return nil, nil
		}
		return nil, fmt.Errorf("gcs pubsub pull: %w", err)
	}

	var msgs []eventMessage
	for _, rm := range resp.GetReceivedMessages() {
		if rm == nil || rm.GetMessage() == nil {
			continue
		}
		msgs = append(msgs, eventMessage{
			objectKeys: gcsObjectKeys(rm.GetMessage().GetAttributes()),
			ackHandle:  rm.GetAckId(),
		})
	}
	return msgs, nil
}

func (g *gcsPubSubEventSource) Ack(ctx context.Context, handle string) error {
	if err := g.client.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{
		Subscription: g.sub,
		AckIds:       []string{handle},
	}); err != nil {
		return fmt.Errorf("gcs pubsub ack: %w", err)
	}
	return nil
}

// gcsObjectKeys extracts the object key from a GCS notification's Pub/Sub
// attributes. GCS sets objectId (the key), bucketId and eventType. Only
// OBJECT_FINALIZE (new/overwritten object) yields a key; other event types
// (delete, metadata update) yield none so the message drains without ingesting.
func gcsObjectKeys(attrs map[string]string) []string {
	if attrs == nil {
		return nil
	}
	if et := attrs["eventType"]; et != "" && et != "OBJECT_FINALIZE" {
		return nil
	}
	if k := attrs["objectId"]; k != "" {
		return []string{k}
	}
	return nil
}
