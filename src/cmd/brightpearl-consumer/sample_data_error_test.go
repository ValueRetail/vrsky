package main

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/safeurl/safeurltest"
)

// The sample-data response goes to the tenant (#717): a transport error, on
// the API request path, shows a fixed text and logs the detail without the key.
func TestSampleData_TransportErrorsAreFixedTexts(t *testing.T) {
	safeurltest.CheckSampleData(t, `{"app_ref":"app","staff_token":"tok","base_url":"https://api.example.com/public-api/acme","resource":"/order-service/order"}`,
		func(client *http.Client, logger *slog.Logger) http.HandlerFunc {
			return (&brightpearlConsumer{allowPrivate: true, httpClient: client, logger: logger}).handleSampleData()
		})
}
