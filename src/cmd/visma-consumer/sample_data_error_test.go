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
	safeurltest.CheckSampleData(t, `{"client_id":"id","client_secret":"sec","base_url":"https://salesorder.visma.net/api/v3","token_url":"https://login.example.com/token","scope":"s","resource":"SalesOrders"}`,
		func(client *http.Client, logger *slog.Logger) http.HandlerFunc {
			client.Transport = &safeurltest.Recorder{Next: client.Transport}
			rec = append(rec, client.Transport.(*safeurltest.Recorder))
			return (&vismaConsumer{allowPrivate: true, httpClient: client, logger: logger}).handleSampleData()
		})
	for _, r := range rec {
		if urls := r.URLs(); len(urls) != 1 || urls[0] != "https://login.example.com/token" {
			t.Errorf("requests = %v, want the token request alone (the failure must hit the token path)", urls)
		}
	}
}
