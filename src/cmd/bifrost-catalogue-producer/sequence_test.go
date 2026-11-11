package main

// Bifrost's ordering guard (#858; Bifrost #253): the producer sends the
// message's sequence on begin, and a commit Bifrost refuses with 409 because it
// already has that sequence or a newer one is dropped and logged, not retried.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

// syncBuffer is a log sink the producer may write to from its own goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func loggingProducer() (*catalogueProducer, *syncBuffer) {
	p := testProducer()
	buf := &syncBuffer{}
	p.logger = slog.New(slog.NewTextHandler(buf, nil))
	return p, buf
}

// sequenced wraps one article at a price as {"items": [...], "sequence": seq}.
func sequenced(seq, price string) string {
	return fmt.Sprintf(`{"items":[{"article_no":"4711","name":"Melk","price_incl_vat":%q,"vat_code":"F"}],"sequence":%s}`, price, seq)
}

func livePrice(t *testing.T, stub *bifrostStub) string {
	t.Helper()
	var it struct {
		Price string `json:"price_incl_vat"`
	}
	if err := json.Unmarshal([]byte(stub.liveSnapshot()["4711"]), &it); err != nil {
		t.Fatalf("article 4711 is not live: %v", err)
	}
	return it.Price
}

func TestSequence_IsSentOnBegin(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string // the begin body's sequence, "" for none
	}{
		{"whole number", sequenced("42", "10.00"), "42"},
		{"largest whole number", sequenced("18446744073709551615", "10.00"), "18446744073709551615"},
		{"timestamp in UTC", sequenced(`"2026-11-11T12:00:00.123Z"`, "10.00"), "1794398400123"},
		{"timestamp with offset", sequenced(`"2026-11-11T13:00:00.123+01:00"`, "10.00"), "1794398400123"},
		{"timestamp below a millisecond", sequenced(`"2026-11-11T12:00:00.123999Z"`, "10.00"), "1794398400123"},
		{"null", sequenced("null", "10.00"), ""},
		{"absent", `{"items":[{"article_no":"4711","name":"Melk","price_incl_vat":"10.00","vat_code":"F"}]}`, ""},
		{"a list has none", `[{"article_no":"4711","name":"Melk","price_incl_vat":"10.00","vat_code":"F"}]`, ""},
		// A lone object in delta mode is the item itself; its fields are not read.
		{"a single item has none", `{"article_no":"4711","name":"Melk","price_incl_vat":"10.00","vat_code":"F","sequence":7}`, ""},
		// Keys are exact, as for "items".
		{"a capitalised key is not the sequence", `{"items":[{"article_no":"4711","name":"Melk","price_incl_vat":"10.00","vat_code":"F"}],"Sequence":7}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newBifrostStub(t)
			if err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(tt.payload)}, `[]`, nodeJSON(stub.URL(), `"mode":"delta"`)); err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			b := stub.beginBodies()
			if len(b) != 1 {
				t.Fatalf("%d begins, want 1", len(b))
			}
			var raw struct {
				Sequence json.RawMessage `json:"sequence"`
			}
			if err := json.Unmarshal([]byte(b[0].raw), &raw); err != nil {
				t.Fatal(err)
			}
			if got := string(raw.Sequence); got != tt.want {
				t.Errorf("begin sent sequence %q, want %q (body %s)", got, tt.want, b[0].raw)
			}
			if tt.want == "" && strings.Contains(b[0].raw, "sequence") {
				t.Errorf("begin body %s names a sequence; an unnumbered import must leave it out", b[0].raw)
			}
		})
	}
}

func TestSequence_OlderDeliveryAfterNewerIsDroppedAndLogged(t *testing.T) {
	stub := newBifrostStub(t)
	cfg := nodeJSON(stub.URL(), `"mode":"delta"`)

	if err := deliver(t, testProducer(), &envelope.Envelope{ID: "newer", Payload: []byte(sequenced("11", "12.00"))}, `[]`, cfg); err != nil {
		t.Fatalf("newer delivery: %v", err)
	}
	p, logs := loggingProducer()
	// The retry of an older list arrives last.
	if err := deliver(t, p, &envelope.Envelope{ID: "older", Payload: []byte(sequenced("10", "11.00"))}, `[]`, cfg); err != nil {
		t.Fatalf("older delivery returned %v; a 409 at commit must be acked (nil), not retried or dead-lettered", err)
	}
	if got := livePrice(t, stub); got != "12.00" {
		t.Errorf("the tills sell 4711 at %s, want 12.00: the older list overwrote the newer one", got)
	}
	if got := stub.kinds(); got != "begin items commit begin items commit" {
		t.Errorf("calls = %q; the older import must be tried once, with no abort after Bifrost took it", got)
	}
	if n := stub.openImports(); n != 0 {
		t.Errorf("%d imports left open", n)
	}
	out := logs.String()
	// Delta: the dropped rows may be lost, so it is an error to alert on.
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "dropped the delivery") || !strings.Contains(out, "sequence=10") {
		t.Errorf("the dropped delta is not logged as an error with its sequence:\n%s", out)
	}
	if strings.Contains(out, stubToken) {
		t.Error("the log holds the token")
	}
}

func TestSequence_SameSequenceTwiceIsAppliedOnce(t *testing.T) {
	for _, mode := range []string{"full", "delta"} {
		t.Run(mode, func(t *testing.T) {
			stub := newBifrostStub(t)
			cfg := nodeJSON(stub.URL(), fmt.Sprintf(`"mode":%q`, mode))
			env := &envelope.Envelope{Payload: []byte(sequenced(`"2026-11-11T12:00:00Z"`, "10.00"))}
			if err := deliver(t, testProducer(), env, `[]`, cfg); err != nil {
				t.Fatal(err)
			}
			// A crash between commit and ack: the same message again.
			p, logs := loggingProducer()
			if err := deliver(t, p, env, `[]`, cfg); err != nil {
				t.Fatalf("redelivery returned %v, want nil: it is already applied", err)
			}
			if got := livePrice(t, stub); got != "10.00" {
				t.Errorf("price = %s, want 10.00", got)
			}
			if !strings.Contains(logs.String(), "dropped the delivery") {
				t.Errorf("the second delivery is not logged as dropped:\n%s", logs.String())
			}
			// A full list that is already newer loses nothing: a warning.
			if want := map[string]string{"full": "level=WARN", "delta": "level=ERROR"}[mode]; !strings.Contains(logs.String(), want) {
				t.Errorf("%s mode drop not logged at %s:\n%s", mode, want, logs.String())
			}
		})
	}
}

func TestSequence_NewerDeliveryStillCommits(t *testing.T) {
	stub := newBifrostStub(t)
	cfg := nodeJSON(stub.URL(), `"mode":"delta"`)
	for i, d := range []struct{ seq, price string }{{"10", "11.00"}, {"11", "12.00"}} {
		if err := deliver(t, testProducer(), &envelope.Envelope{ID: fmt.Sprintf("e%d", i), Payload: []byte(sequenced(d.seq, d.price))}, `[]`, cfg); err != nil {
			t.Fatalf("delivery %s: %v", d.seq, err)
		}
	}
	if got := livePrice(t, stub); got != "12.00" {
		t.Errorf("price = %s, want 12.00 from the newer list", got)
	}
}

func TestSequence_UnusableValueIsPermanentAndSendsNothing(t *testing.T) {
	tests := []struct{ name, seq, want string }{
		{"zero", "0", "1 or more"},
		{"negative", "-5", "neither a whole number"},
		{"fraction", "10.5", "neither a whole number"},
		{"exponent", "1e3", "neither a whole number"},
		{"too large", "18446744073709551616", "too large"},
		{"bool", "true", "neither a whole number"},
		{"object", `{"n":1}`, "neither a whole number"},
		{"not a timestamp", `"yesterday"`, "not an RFC 3339 timestamp"},
		{"number as a string", `"42"`, "not an RFC 3339 timestamp"},
		{"before 1970", `"1969-12-31T23:59:59Z"`, "not after 1970"},
		{"the epoch itself", `"1970-01-01T00:00:00Z"`, "not after 1970"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newBifrostStub(t)
			err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(sequenced(tt.seq, "10.00"))}, `[]`, nodeJSON(stub.URL(), `"mode":"delta"`))
			if err == nil || !sdk.IsPermanent(err) {
				t.Fatalf("err = %v, want Permanent: the same message would be refused again", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not say %q", err, tt.want)
			}
			if got := stub.kinds(); got != "" {
				t.Errorf("Bifrost received %q, want nothing", got)
			}
		})
	}
}

// A 409 when no sequence was sent is not Bifrost's ordering guard (a proxy, or
// an API change): it stays a refusal, not a silent success.
func TestSequence_ConflictWithoutSequenceIsARefusal(t *testing.T) {
	stub := newBifrostStub(t)
	stub.failOnce("commit", 1, fault{status: 409, body: `{"error":"conflict"}`})
	err := deliver(t, testProducer(), &envelope.Envelope{Payload: []byte(items(2))}, `[]`, nodeJSON(stub.URL(), `"mode":"delta"`))
	if err == nil || !sdk.IsPermanent(err) || !strings.Contains(err.Error(), "409") {
		t.Fatalf("err = %v, want a Permanent refusal naming the 409", err)
	}
}

// Through the real runner: the dropped delivery is acked, so it is neither
// redelivered nor copied to the DLQ.
func TestSequence_RunnerAcksTheDroppedDelivery(t *testing.T) {
	stub := newBifrostStub(t)
	newer := uint64(20)
	stub.newest = &newer
	h, _ := runThroughRunner(t, stub, `"mode":"delta"`, sequenced("19", "11.00"))
	harness.Eventually(t, 5*time.Second, "commit tried", func() bool { return strings.Contains(stub.kinds(), "commit") })
	h.ExpectNoDLQ(t, time.Second)
	if got := stub.kinds(); got != "begin items commit" {
		t.Errorf("calls = %q, want one attempt", got)
	}
	if _, ok := stub.liveSnapshot()["4711"]; ok {
		t.Error("the older list reached the catalogue")
	}
}
