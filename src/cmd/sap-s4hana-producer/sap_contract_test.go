package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/test/contract"
)

// Producer half of the SAP contract: replays what cmd/sap-s4hana-consumer
// recorded, through the real CSRF fetch-then-write flow.
func TestContract_SAPProducerSendsWhatWasPublished(t *testing.T) {
	for _, ce := range contract.Load(t, "sap-s4hana") {
		t.Run(ce.Mode, func(t *testing.T) {
			var gotBody []byte
			var gotToken string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet { // CSRF fetch
					http.SetCookie(w, &http.Cookie{Name: "SAP_SESSIONID", Value: "sess1"})
					w.Header().Set("X-CSRF-Token", "csrf-abc")
					w.WriteHeader(http.StatusOK)
					return
				}
				gotToken = r.Header.Get("X-CSRF-Token")
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()

			if !json.Valid(ce.Payload) {
				t.Fatalf("%s payload is not valid JSON — Deliver drops it permanently", ce.Mode)
			}
			cfg := basicCfg(srv.URL)
			p := testProducer()
			if err := p.write(context.Background(), cfg, newAuthorizer(cfg, p.httpClient), ce.Payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			// The CSRF handshake has to survive whatever the consumer produced:
			// a write without the token is rejected by SAP with a 403 that
			// looks like an auth failure rather than a protocol one.
			if gotToken != "csrf-abc" {
				t.Errorf("X-CSRF-Token = %q, want the fetched token", gotToken)
			}
			if string(gotBody) != string(ce.Payload) {
				t.Errorf("body differs from the envelope payload.\ngot:  %s\nwant: %s", gotBody, ce.Payload)
			}
		})
	}
}
