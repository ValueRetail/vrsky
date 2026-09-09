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

// Producer half: replays what cmd/front-systems-consumer recorded and asserts
// what reaches Front Systems.
func TestContract_FrontSystemsProducerSendsWhatWasPublished(t *testing.T) {
	for _, ce := range contract.Load(t, "front-systems") {
		t.Run(ce.Mode, func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			if !json.Valid(ce.Payload) {
				t.Fatalf("%s payload is not valid JSON — Deliver drops it permanently", ce.Mode)
			}
			if err := testProducer().write(context.Background(), cfgFor(srv.URL), ce.Payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			// The producer does not transform the payload, so anything but a
			// byte-for-byte match is a change nobody intended.
			if string(gotBody) != string(ce.Payload) {
				t.Errorf("body differs from the envelope payload.\ngot:  %s\nwant: %s", gotBody, ce.Payload)
			}
		})
	}
}
