package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Sitoo consumer and producer are separate binaries that never call each
// other — they meet only in the envelope one publishes and the other consumes.
// Every existing test exercises one side in isolation, so nothing has ever
// checked that the thing the consumer emits is a thing the producer can send.
//
// These two files are that check. This one runs the REAL consumer against a stub
// Sitoo API serving a committed fixture, and records the envelopes it publishes
// to a golden file. cmd/sitoo-producer/sitoo_contract_test.go reads that file
// and feeds those exact envelopes to the REAL producer.
//
// A consumer-side change that alters the envelope shows up here as a golden diff
// and, if the producer can no longer send it, as a failure over there.

var updateGolden = flag.Bool("update", false, "rewrite the contract golden file from this run")

const (
	fixtureDir = "../../test/fixtures/sitoo"
	goldenName = "envelopes.golden.json"
)

// contractEnvelope is the subset of an envelope that crosses the wire and
// matters to the producer. Volatile fields (ID, timestamps) are deliberately
// excluded: pinning them would make the golden file churn on every run and say
// nothing about the contract.
type contractEnvelope struct {
	Mode          string          `json:"mode"` // how the consumer produced it
	TenantID      string          `json:"tenant_id"`
	IntegrationID string          `json:"integration_id"`
	ContentType   string          `json:"content_type"`
	Source        string          `json:"source"`
	StepHistory   []string        `json:"step_history"`
	Metadata      map[string]any  `json:"metadata"`
	Payload       json.RawMessage `json:"payload"`
}

func TestSitooContract_EnvelopesTheProducerMustAccept(t *testing.T) {
	page, err := os.ReadFile(filepath.Join(fixtureDir, "transactions_page.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	// --- Poll path: the consumer fetches a page and publishes it. ---
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One page only: answer the first request with the fixture and every
		// later one with an empty collection, so pagination terminates.
		if r.URL.Query().Get("start") == "0" || r.URL.Query().Get("start") == "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(page)
			return
		}
		_, _ = w.Write([]byte(`{"totalcount":2,"items":[]}`))
	}))
	defer api.Close()

	c, got, mu := newTestConsumer()
	cfg := &SitooConfig{
		AccountID: 1, SiteID: 2, APIID: "apiid", APIPassword: "secretpw",
		BaseURL: api.URL, Resource: "transactions", PageSize: 1000,
	}
	if err := c.fetchAndPublish(context.Background(), "conn-sitoo", "tenant-vr", cfg, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}

	// --- Webhook path: Sitoo POSTs an SPI event to the aux port. ---
	c.resolveTenant = func(connID string) (string, error) { return "tenant-vr", nil }
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sitoo/events/conn-sitoo",
		strings.NewReader(`{"eventid":"evt-77","eventtype":"transaction.created","transactionid":90210}`))
	req.Header.Set("X-Sitoo-Event", "transaction.created")
	c.handleWebhook()(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want 202", rec.Code)
	}

	mu.Lock()
	envs := make([]contractEnvelope, 0, len(*got))
	for i, env := range *got {
		mode := "poll"
		if i == len(*got)-1 {
			mode = "webhook"
		}
		envs = append(envs, contractEnvelope{
			Mode:          mode,
			TenantID:      env.TenantID,
			IntegrationID: env.IntegrationID,
			ContentType:   env.ContentType,
			Source:        env.Source,
			StepHistory:   env.StepHistory,
			Metadata:      env.Metadata,
			Payload:       json.RawMessage(env.Payload),
		})
	}
	mu.Unlock()

	if len(envs) != 2 {
		t.Fatalf("captured %d envelopes, want 2 (one poll page, one webhook)", len(envs))
	}

	// Invariants the producer depends on. sitooProducer.Deliver drops a message
	// permanently when the payload is not valid JSON, and routes on these two
	// ids — so a consumer that stopped setting either would silently produce
	// undeliverable traffic.
	for _, e := range envs {
		if !json.Valid(e.Payload) {
			t.Errorf("%s payload is not valid JSON; the producer drops it as Permanent", e.Mode)
		}
		if e.TenantID == "" || e.IntegrationID == "" {
			t.Errorf("%s envelope missing routing ids (tenant=%q integration=%q); the producer cannot "+
				"look up a config without them and returns nil, dropping the record silently",
				e.Mode, e.TenantID, e.IntegrationID)
		}
	}

	// The two paths do NOT emit the same shape, and that is the finding this
	// test exists to record rather than hide: polling marshals the page's items
	// into a JSON ARRAY, while the webhook forwards Sitoo's event body verbatim,
	// which is an OBJECT. The producer POSTs whichever it gets, unchanged.
	var pollPayload []json.RawMessage
	if err := json.Unmarshal(envs[0].Payload, &pollPayload); err != nil {
		t.Errorf("poll payload is not a JSON array (%v) — the producer would POST a non-collection "+
			"body to a Sitoo collection endpoint", err)
	}
	if len(pollPayload) != 2 {
		t.Errorf("poll payload carries %d records, want the fixture's 2", len(pollPayload))
	}
	var webhookPayload map[string]any
	if err := json.Unmarshal(envs[1].Payload, &webhookPayload); err != nil {
		t.Errorf("webhook payload is not a JSON object: %v", err)
	}

	writeOrCompareGolden(t, envs)
}

func writeOrCompareGolden(t *testing.T, envs []contractEnvelope) {
	t.Helper()
	path := filepath.Join(fixtureDir, goldenName)

	got, err := json.MarshalIndent(envs, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	got = append(got, '\n')

	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (%v) — regenerate with: go test ./cmd/sitoo-consumer/ -run TestSitooContract -update", err)
	}
	if string(got) != string(want) {
		t.Errorf("the envelopes this consumer publishes no longer match %s.\n\n"+
			"That file is what cmd/sitoo-producer's contract test replays, so this is a change to what "+
			"the two services agree on — not just a test fixture. Check the producer still handles the "+
			"new shape, then regenerate:\n"+
			"  go test ./cmd/sitoo-consumer/ -run TestSitooContract -update\n\ngot:\n%s", path, got)
	}
}
