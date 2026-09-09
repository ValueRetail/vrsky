package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/oauthcc"
	"github.com/ValueRetail/vrsky/test/contract"
)

func TestContract_BusinessCentralProducerSendsWhatWasPublished(t *testing.T) {
	for _, ce := range contract.Load(t, "business-central") {
		t.Run(ce.Mode, func(t *testing.T) {
			tokenSrv := tokenServer(t)
			defer tokenSrv.Close()

			var gotBody []byte
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			cfg := cfgFor(srv.URL, tokenSrv.URL)
			tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).
				WithHTTPClient(http.DefaultClient)

			if !json.Valid(ce.Payload) {
				t.Fatalf("%s payload is not valid JSON — Deliver drops it permanently", ce.Mode)
			}
			if err := testProducer().write(context.Background(), cfg, tok, ce.Payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			if gotAuth == "" {
				t.Error("no Authorization header reached Business Central")
			}
			if string(gotBody) != string(ce.Payload) {
				t.Errorf("body differs from the envelope payload.\ngot:  %s\nwant: %s", gotBody, ce.Payload)
			}
		})
	}
}
