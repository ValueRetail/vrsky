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

// TestSitooContract_BothModesSendACollection is the assertion that the poll and
// webhook paths agree.
//
// They did not. The consumer's poll path marshalled a page's items into a JSON
// ARRAY; its webhook path forwarded Sitoo's event body verbatim, which is an
// OBJECT. The producer POSTs whichever it receives, unchanged, to the same
// collection endpoint — so a poll-fed and a webhook-fed pipeline sent Sitoo
// materially different bodies and only one of them was a collection write. This
// test used to pin that gap; the consumer now dereferences the event to the
// resource it names, so it pins the fix instead.
func TestSitooContract_BothModesSendACollection(t *testing.T) {
	for _, ce := range contract.Load(t, "sitoo") {
		var arr []json.RawMessage
		if err := json.Unmarshal(ce.Payload, &arr); err != nil {
			t.Errorf("%s payload is not a JSON array (%v) — a Sitoo destination is a collection "+
				"endpoint, so anything else is not a write it can make sense of", ce.Mode, err)
			continue
		}
		if len(arr) == 0 {
			t.Errorf("%s payload is an empty array", ce.Mode)
		}
		// Every element must be a resource, not an event notification. The
		// producer refuses those now (isEventNotification), so one slipping
		// into the golden would be a silent drop at runtime.
		for i, item := range arr {
			var obj map[string]any
			if err := json.Unmarshal(item, &obj); err != nil {
				t.Errorf("%s payload[%d] is not an object: %v", ce.Mode, i, err)
				continue
			}
			if _, isEvent := obj["eventtype"]; isEvent {
				t.Errorf("%s payload[%d] is an event notification, not a resource — the producer "+
					"drops these as Permanent", ce.Mode, i)
			}
		}
	}
}

// TestSitooContract_ProducerRefusesEventNotifications covers the safety net.
//
// The consumer no longer emits raw events, so this fires only when
// sitoo.webhook_raw_event is set on a pipeline whose destination is Sitoo, or
// when an event arrives from elsewhere. Before it existed, that body was POSTed
// to a collection endpoint and whatever Sitoo made of it was the end of it.
func TestSitooContract_ProducerRefusesEventNotifications(t *testing.T) {
	event := []byte(`{"eventid":"evt-77","eventtype":"transaction.created","transactionid":90210}`)

	if !isEventNotification(event) {
		t.Error("an SPI event body was not recognised as one; the producer would POST it as a resource")
	}
	// A collection must never be mistaken for an event — that would drop real data.
	if isEventNotification([]byte(`[{"transactionid":90210}]`)) {
		t.Error("a JSON array was classified as an event notification")
	}
	// Nor a single resource object that merely has other fields.
	if isEventNotification([]byte(`{"transactionid":90210,"total":"1249.00"}`)) {
		t.Error("a resource object was classified as an event notification")
	}
	// Nor anything unparseable — left alone and sent, rather than dropped.
	if isEventNotification([]byte(`not json`)) {
		t.Error("invalid JSON was classified as an event notification")
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
