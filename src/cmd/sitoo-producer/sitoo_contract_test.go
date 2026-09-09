package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/test/contract"
)

// The other half of the consumer→producer contract. See the header of
// cmd/sitoo-consumer/sitoo_contract_test.go: that test runs the real consumer
// and records what it publishes; this one replays exactly those envelopes
// through the real producer and asserts what reaches Sitoo.
//
// The two binaries never call each other, so this file and that one are the
// only place the join is checked. Before them, both sides were tested in
// isolation against payloads their own tests invented — which is the shape of
// an integration that passes everywhere and fails in production.

// TestSitooContract_ProducerSendsWhatTheConsumerPublished replays every envelope
// the consumer emits and checks the resulting Sitoo request.
func TestSitooContract_ProducerSendsWhatTheConsumerPublished(t *testing.T) {
	for _, ce := range contract.Load(t, "sitoo") {
		t.Run(ce.Mode, func(t *testing.T) {
			var gotBody []byte
			var gotPath, gotAuth, gotContentType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = readAll(r)
				gotPath = r.URL.Path
				gotContentType = r.Header.Get("Content-Type")
				if u, p, ok := r.BasicAuth(); ok {
					gotAuth = u + ":" + p
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			p := testProducer()
			// getSitooConfig reads the connection from the database; the
			// contract under test is the envelope, not the lookup, so the
			// config is supplied directly and write() is driven the way
			// Deliver drives it.
			cfg := cfgFor(srv.URL)

			// Deliver's own precondition, asserted here because failing it is
			// silent: an invalid payload is dropped as Permanent and the record
			// never reaches Sitoo.
			if !json.Valid(ce.Payload) {
				t.Fatalf("%s payload is not valid JSON — Deliver would drop it permanently", ce.Mode)
			}

			if err := p.write(context.Background(), cfg, ce.Payload); err != nil {
				t.Fatalf("write: %v", err)
			}

			if gotAuth != "id:pw" {
				t.Errorf("basic auth = %q, want id:pw", gotAuth)
			}
			if gotContentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", gotContentType)
			}
			if want := "/accounts/1/sites/2/warehouseitems"; gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
			// The body must be the consumer's payload, byte for byte. The
			// producer does not transform it, so anything else here means a
			// change nobody intended.
			if string(gotBody) != string(ce.Payload) {
				t.Errorf("body sent to Sitoo differs from the envelope payload.\ngot:  %s\nwant: %s",
					gotBody, ce.Payload)
			}
		})
	}
}

// TestSitooContract_PollAndWebhookSendDifferentShapes records a real asymmetry
// rather than papering over it.
//
// The consumer's poll path marshals a page's items into a JSON ARRAY; its
// webhook path forwards Sitoo's event body verbatim, which is an OBJECT. The
// producer POSTs whichever it receives, unchanged, to the same collection
// endpoint. So a poll-fed pipeline and a webhook-fed pipeline send Sitoo
// materially different bodies, and only one of them looks like a collection
// write.
//
// This is not a bug in either binary — each does what it says. It is a gap in
// what the pipeline promises, and it will surface the first time someone points
// a Sitoo webhook at a Sitoo destination. Pinning it here means the next person
// meets it in a test rather than in a partner's error log.
func TestSitooContract_PollAndWebhookSendDifferentShapes(t *testing.T) {
	byMode := map[string]json.RawMessage{}
	for _, ce := range contract.Load(t, "sitoo") {
		byMode[ce.Mode] = ce.Payload
	}

	poll, ok := byMode["poll"]
	if !ok {
		t.Fatal("golden file has no poll envelope")
	}
	webhook, ok := byMode["webhook"]
	if !ok {
		t.Fatal("golden file has no webhook envelope")
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(poll, &arr); err != nil {
		t.Errorf("poll payload is no longer a JSON array (%v) — if the consumer changed, the producer "+
			"is now POSTing something else entirely to a Sitoo collection endpoint", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(webhook, &obj); err != nil {
		t.Errorf("webhook payload is no longer a JSON object: %v", err)
	}
	// The webhook body is Sitoo's event notification, not a resource. Sending it
	// to a write endpoint is what the current wiring does; asserting it keeps
	// the fact visible.
	if _, isEvent := obj["eventtype"]; !isEvent {
		t.Logf("webhook payload has no eventtype field; the fixture may no longer represent a Sitoo SPI event")
	}
}

// TestSitooContract_DeliverDropsSilently pins the two ways a record is lost with
// no error surfaced anywhere.
//
// Both are deliberate — a producer that subscribes to ALL pipeline data has to
// ignore traffic that is not its own — but both mean "record gone, nothing
// logged above Debug", so they are worth holding still.
func TestSitooContract_DeliverDropsSilently(t *testing.T) {
	// No integration id: returns before any lookup. A consumer that stopped
	// setting it would drop every record, quietly.
	if err := testProducer().Deliver(context.Background(), &envelope.Envelope{TenantID: "t"}); err != nil {
		t.Errorf("Deliver with no integration id = %v, want nil (it is a silent no-op today)", err)
	}

	// No matching connection row: also nil. This is the common case for a
	// non-Sitoo pipeline, and it is why a misrouted envelope disappears rather
	// than dead-lettering.
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT nodes FROM connections").
		WithArgs("conn-unknown", "tenant-vr").
		WillReturnError(sql.ErrNoRows)

	p := testProducer()
	p.db = db
	env := &envelope.Envelope{TenantID: "tenant-vr", IntegrationID: "conn-unknown", Payload: []byte(`[]`)}
	if err := p.Deliver(context.Background(), env); err != nil {
		t.Errorf("Deliver with no config = %v, want nil", err)
	}

	// NOTE the ordering this exercises: Deliver looks the config up BEFORE
	// validating the payload, so the json.Valid gate — and its Permanent
	// classification — is only reachable for a connection that really is a
	// Sitoo destination. Testing that path needs a full node fixture, which
	// belongs with the config-parsing tests rather than here.
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
