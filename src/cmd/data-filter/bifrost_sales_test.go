package main

// The filter of the Bifrost sales → Retailhero template (#817), read from
// ui/src/onboarding/testdata/bifrost-sales-filter.json and run on the Basket
// event fixtures. Retailhero refuses a whole batch on an event type its
// profile does not map and on a sale-recorded from a channel that is not a
// POS, so only a till sale may pass.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nats-io/nats.go"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/messaging"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

func readJSONFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return raw
}

func TestBifrostSalesFilter_OnlyATillSaleOrReturnPasses(t *testing.T) {
	var cfg FilterNodeConfig
	if err := json.Unmarshal(readJSONFile(t, "..", "..", "..", "ui", "src", "onboarding", "testdata", "bifrost-sales-filter.json"), &cfg); err != nil {
		t.Fatalf("filter config: %v", err)
	}
	fixture := func(name string) map[string]interface{} {
		var m map[string]interface{}
		if err := json.Unmarshal(readJSONFile(t, "..", "..", "test", "fixtures", "bifrost", name), &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return m
	}

	webReturn := fixture("basket-return-recorded.v1.json")
	webReturn["source"] = map[string]interface{}{"kind": "ecommerce", "instance_id": "web-no", "location_id": "web"}

	webSale := fixture("basket-sale-recorded.v1.json")
	webSale["source"] = map[string]interface{}{"kind": "ecommerce", "instance_id": "web-no", "location_id": "web"}

	noID := fixture("basket-sale-recorded.v1.json")
	delete(noID, "event_id")
	emptyID := fixture("basket-sale-recorded.v1.json")
	emptyID["event_id"] = ""

	cases := []struct {
		name string
		obj  map[string]interface{}
		want bool
	}{
		{"sale-recorded from a till", fixture("basket-sale-recorded.v1.json"), true},
		{"return-recorded from a till", fixture("basket-return-recorded.v1.json"), true},
		{"return-recorded from the web shop", webReturn, false},
		{"another -recorded type", map[string]interface{}{"event_id": "e1", "event_type": "basket.payment-recorded", "source": map[string]interface{}{"kind": "pos"}}, false},
		{"a type that only contains a listed one", map[string]interface{}{"event_id": "e1", "event_type": "basket.sale-recorded.v2", "source": map[string]interface{}{"kind": "pos"}}, false},
		{"sale-recorded from the web shop", webSale, false},
		{"sale-recorded without an event_id", noID, false},
		{"sale-recorded with an empty event_id", emptyID, false},
		{"completed", fixture("basket-completed.v1.json"), false},
		{"cancelled", fixture("basket-cancelled.v1.json"), false},
		{"created", map[string]interface{}{"event_type": "basket.created", "source": map[string]interface{}{"kind": "pos"}}, false},
	}
	for _, c := range cases {
		if got := evaluateRules(c.obj, cfg.Rules, cfg.Logic); got != c.want {
			t.Errorf("%s: passes = %v, want %v", c.name, got, c.want)
		}
	}
}

// "in" passes a field that equals one entry of a comma-separated list, exactly.
func TestEvaluateRule_In(t *testing.T) {
	rule := FilterRule{Field: "t", Operator: "in", Value: "a, b ,c"}
	cases := []struct {
		obj  map[string]interface{}
		want bool
	}{
		{map[string]interface{}{"t": "a"}, true},
		{map[string]interface{}{"t": "b"}, true},
		{map[string]interface{}{"t": "c"}, true},
		{map[string]interface{}{"t": "ab"}, false},
		{map[string]interface{}{"t": ""}, false},
		{map[string]interface{}{"t": nil}, false},
		{map[string]interface{}{}, false},
	}
	for _, c := range cases {
		if got := evaluateRule(c.obj, rule); got != c.want {
			t.Errorf("%v in %q = %v, want %v", c.obj, rule.Value, got, c.want)
		}
	}
	if evaluateRule(map[string]interface{}{"t": ""}, FilterRule{Field: "t", Operator: "in", Value: "a,,b"}) {
		t.Error("an empty entry in the list let an empty value through")
	}
}

// Basket publishes its events into VRSKY_DATA itself (Bifrost ADR 0002): a
// finished VRSky envelope with the event as the base64 payload, the workspace
// as tenant_id and the connection as integration_id, on
// vrsky.data.<tenant>.pipeline.<connection id>. The template's connection
// starts with a "direct" source, which no service serves. The filter has to
// read that connection from the database, see its own input is the consumer,
// and take Basket's message as the consumer's: nothing has processed it yet.
func TestBifrostSalesFilter_TakesTheEnvelopeBasketPublishesIntoADirectSource(t *testing.T) {
	const (
		workspace  = "7d3c2f0a-5a1e-4c8b-9f2d-0e6b1a4c9d17"
		connection = "0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
		subject    = "vrsky.data." + workspace + ".pipeline." + connection
	)
	filterCfg := readJSONFile(t, "..", "..", "..", "ui", "src", "onboarding", "testdata", "bifrost-sales-filter.json")
	event := readJSONFile(t, "..", "..", "test", "fixtures", "bifrost", "basket-sale-recorded.v1.json")
	var eventObj map[string]interface{}
	if err := json.Unmarshal(event, &eventObj); err != nil {
		t.Fatalf("sale fixture: %v", err)
	}

	// The connection as the template deploys it, as the filter reads it.
	nodes, err := json.Marshal([]map[string]interface{}{
		{"id": "src", "type": "consumer", "config": map[string]string{"type": "direct"}},
		{"id": "flt", "type": "filter", "config": json.RawMessage(filterCfg)},
		{"id": "conv", "type": "converter", "config": map[string]interface{}{}},
		{"id": "dst", "type": "producer", "config": map[string]string{"type": "http"}},
	})
	if err != nil {
		t.Fatalf("nodes: %v", err)
	}
	edges := []byte(`[{"source":"src","target":"flt"},{"source":"flt","target":"conv"},{"source":"conv","target":"dst"}]`)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`SELECT nodes, edges FROM connections`).WithArgs(connection).
		WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow(nodes, edges))

	nc, js, cleanup := harness.StartEmbeddedJetStream(t)
	defer cleanup()
	s := newTestFilterService(nil)
	s.db = db
	s.pipelineCache = map[string]*PipelineInfo{}
	s.pipelineCacheTime = map[string]time.Time{}
	s.nc = nc
	s.pub = messaging.NewPublisher(js)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()

	out := make(chan *envelope.Envelope, 4)
	sub, err := nc.Subscribe(subject, func(m *nats.Msg) {
		var env envelope.Envelope
		if json.Unmarshal(m.Data, &env) == nil && env.Metadata != nil {
			if v, _ := env.Metadata["_last_processed_by"].(string); v == "flt" {
				out <- &env
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Written out field by field, the way Basket's publisher puts it on the
	// wire, rather than built with this repo's envelope.New.
	wire, err := json.Marshal(map[string]interface{}{
		"id":             eventObj["event_id"],
		"tenant_id":      workspace,
		"integration_id": connection,
		"payload":        event, // []byte: base64 in JSON, as Basket sends it
		"payload_size":   len(event),
		"content_type":   "application/vnd.valueretail.basket-event+json;version=1",
		"source":         "basket-outbox-publisher",
		"current_step":   0,
		"step_history":   []string{},
		"metadata":       map[string]interface{}{"event_type": eventObj["event_type"], "schema_version": 1},
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"expires_at":     time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339Nano),
		"retry_count":    0,
	})
	if err != nil {
		t.Fatalf("marshal Basket envelope: %v", err)
	}
	if _, err := js.Publish(subject, wire); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var got *envelope.Envelope
	select {
	case got = <-out:
	case <-time.After(5 * time.Second):
		t.Fatal("a till sale Basket published into the direct source never left the filter: the pipeline would send Retailhero nothing")
	}
	if got.TenantID != workspace || got.IntegrationID != connection {
		t.Errorf("the sale left the filter as tenant %q, connection %q; want Basket's %q, %q", got.TenantID, got.IntegrationID, workspace, connection)
	}
	var gotObj map[string]interface{}
	if err := json.Unmarshal(got.Payload, &gotObj); err != nil {
		t.Fatalf("the filter's output is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotObj, eventObj) {
		t.Errorf("the filter changed the Basket event it passed on:\n got: %s\nwant: %s", got.Payload, event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the filter did not read the connection: %v", err)
	}
}
