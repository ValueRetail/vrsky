package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
)

// azureQueueEventSource long-polls an Azure Storage Queue that receives Blob
// change notifications (Blob events routed through Event Grid into the queue).
// It is the Azure parity of sqsEventSource: Receive dequeues messages and parses
// the object keys; Ack deletes the message after a successful publish.
type azureQueueEventSource struct {
	client *azqueue.QueueClient
}

// azureAckHandle carries the two values Azure needs to delete a dequeued
// message. It is JSON-encoded into eventMessage.ackHandle (a single string) so
// the provider-agnostic event loop stays unchanged.
type azureAckHandle struct {
	ID         string `json:"id"`
	PopReceipt string `json:"pr"`
}

// newAzureQueueEventSource builds a queue client from a connection string (also
// the way to target the Azurite emulator) or an account name + shared key. The
// queue name is the per-node event_queue_name.
func newAzureQueueEventSource(_ context.Context, cfg *cloudConfig) (eventSource, error) {
	queue := cfg.EventQueueName
	if queue == "" {
		return nil, fmt.Errorf("event mode (azure) requires event_queue_name (the Storage Queue receiving Blob events via Event Grid)")
	}

	var (
		client *azqueue.QueueClient
		err    error
	)
	switch {
	case cfg.ConnectionString != "":
		client, err = azqueue.NewQueueClientFromConnectionString(cfg.ConnectionString, queue, nil)
	case cfg.AccountName != "" && cfg.AccountKey != "":
		cred, cerr := azqueue.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
		if cerr != nil {
			return nil, fmt.Errorf("azure queue: shared key credential: %w", cerr)
		}
		// The queue service lives on a different host than blob; build the queue
		// service URL (or use the explicit endpoint override for Azurite).
		base := cfg.EventEndpoint
		if base == "" {
			base = fmt.Sprintf("https://%s.queue.core.windows.net", cfg.AccountName)
		}
		queueURL := strings.TrimRight(base, "/") + "/" + queue
		client, err = azqueue.NewQueueClientWithSharedKeyCredential(queueURL, cred, nil)
	default:
		return nil, errors.New("azure queue: set connection_string, or account_name + account_key")
	}
	if err != nil {
		return nil, fmt.Errorf("azure queue: new client: %w", err)
	}
	return &azureQueueEventSource{client: client}, nil
}

func (a *azureQueueEventSource) Receive(ctx context.Context) ([]eventMessage, error) {
	n := int32(10)
	vt := int32(30) // visibility timeout: redelivered if not deleted (ack) in time
	resp, err := a.client.DequeueMessages(ctx, &azqueue.DequeueMessagesOptions{
		NumberOfMessages:  &n,
		VisibilityTimeout: &vt,
	})
	if err != nil {
		return nil, fmt.Errorf("azure queue dequeue: %w", err)
	}
	var msgs []eventMessage
	for _, m := range resp.Messages {
		if m == nil {
			continue
		}
		text := ""
		if m.MessageText != nil {
			text = *m.MessageText
		}
		h := azureAckHandle{}
		if m.MessageID != nil {
			h.ID = *m.MessageID
		}
		if m.PopReceipt != nil {
			h.PopReceipt = *m.PopReceipt
		}
		hb, _ := json.Marshal(h)
		msgs = append(msgs, eventMessage{
			objectKeys: parseAzureNotification([]byte(text)),
			ackHandle:  string(hb),
		})
	}
	return msgs, nil
}

func (a *azureQueueEventSource) Ack(ctx context.Context, handle string) error {
	var h azureAckHandle
	if err := json.Unmarshal([]byte(handle), &h); err != nil {
		return fmt.Errorf("azure queue ack: bad handle: %w", err)
	}
	if _, err := a.client.DeleteMessage(ctx, h.ID, h.PopReceipt, nil); err != nil {
		return fmt.Errorf("azure queue delete: %w", err)
	}
	return nil
}

// azureEvent is one Event Grid notification (the Storage Queue message body).
type azureEvent struct {
	EventType string `json:"eventType"`
	Subject   string `json:"subject"`
	Data      struct {
		URL string `json:"url"`
	} `json:"data"`
}

// parseAzureNotification extracts blob keys from an Event Grid Blob notification
// delivered into a Storage Queue. Event Grid base64-encodes the message body by
// default, so it is base64-decoded first (falling back to raw text). The body
// may be a single event or an array. Only *Created events yield keys; other
// events (deletes, test events) yield none so they drain without ingesting.
func parseAzureNotification(body []byte) []string {
	raw := body
	if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body))); err == nil && json.Valid(dec) {
		raw = dec
	}

	var events []azureEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		var single azureEvent
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil
		}
		events = []azureEvent{single}
	}

	var keys []string
	for _, e := range events {
		// Only ingest blob-created notifications. A missing/other eventType
		// (e.g. Event Grid subscription-validation or a delete event) is skipped
		// so it drains without ingesting.
		if !strings.Contains(e.EventType, "BlobCreated") {
			continue
		}
		if k := azureKeyFromEvent(e); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// azureKeyFromEvent derives the blob path (the object key) from an event. The
// subject is "/blobServices/default/containers/<container>/blobs/<key>"; the
// data.url is "https://acct.blob.core.windows.net/<container>/<key>". The key is
// everything after the container, URL-decoded.
func azureKeyFromEvent(e azureEvent) string {
	if i := strings.Index(e.Subject, "/blobs/"); i >= 0 {
		return decodeAzureKey(e.Subject[i+len("/blobs/"):])
	}
	if e.Data.URL != "" {
		if u, err := url.Parse(e.Data.URL); err == nil {
			// Path is /<container>/<key...>; drop the leading slash + container.
			p := strings.TrimPrefix(u.Path, "/")
			if j := strings.Index(p, "/"); j >= 0 {
				return decodeAzureKey(p[j+1:])
			}
		}
	}
	return ""
}

func decodeAzureKey(k string) string {
	if dec, err := url.PathUnescape(k); err == nil {
		return dec
	}
	return k
}
