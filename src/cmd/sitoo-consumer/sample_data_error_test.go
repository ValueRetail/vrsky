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
	safeurltest.CheckSampleData(t, `{"api_id":"id","api_password":"pw","account_id":1,"site_id":2,"base_url":"https://api.example.com/v2","resource":"transactions"}`,
		func(client *http.Client, logger *slog.Logger) http.HandlerFunc {
			return (&sitooConsumer{allowPrivate: true, httpClient: client, logger: logger}).handleSampleData()
		})
}
