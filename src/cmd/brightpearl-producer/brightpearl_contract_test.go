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

func TestContract_BrightpearlProducerSendsWhatWasPublished(t *testing.T) {
	for _, ce := range contract.Load(t, "brightpearl") {
		t.Run(ce.Mode, func(t *testing.T) {
			var gotBody []byte
			var gotAppRef, gotStaffTok string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				gotAppRef = r.Header.Get("brightpearl-app-ref")
				gotStaffTok = r.Header.Get("brightpearl-staff-token")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			if !json.Valid(ce.Payload) {
				t.Fatalf("%s payload is not valid JSON — Deliver drops it permanently", ce.Mode)
			}
			if err := testProducer().write(context.Background(), cfgFor(srv.URL), ce.Payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			if gotAppRef != "app" || gotStaffTok != "tok" {
				t.Errorf("auth headers = %q/%q, want app/tok", gotAppRef, gotStaffTok)
			}
			if string(gotBody) != string(ce.Payload) {
				t.Errorf("body differs from the envelope payload.\ngot:  %s\nwant: %s", gotBody, ce.Payload)
			}
		})
	}
}
