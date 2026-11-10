package main

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/safeurl/safeurltest"
)

// The sample-data response goes to the tenant (#717): a transport error, on
// the token request path, shows a fixed text and logs the detail without the key.
func TestSampleData_TransportErrorsAreFixedTexts(t *testing.T) {
	var rec []*safeurltest.Recorder
	safeurltest.CheckSampleData(t, `{"aad_tenant_id":"t1","company_id":"GUID","client_id":"cid","client_secret":"sec","entity":"items","api_base_url":"https://api.businesscentral.example.com/v2.0","token_url":"https://login.example.com/token"}`,
		func(client *http.Client, logger *slog.Logger) http.HandlerFunc {
			client.Transport = &safeurltest.Recorder{Next: client.Transport}
			rec = append(rec, client.Transport.(*safeurltest.Recorder))
			return (&bcConsumer{allowPrivate: true, httpClient: client, logger: logger}).handleSampleData()
		})
	for _, r := range rec {
		if urls := r.URLs(); len(urls) != 1 || urls[0] != "https://login.example.com/token" {
			t.Errorf("requests = %v, want the token request alone (the failure must hit the token path)", urls)
		}
	}
}
