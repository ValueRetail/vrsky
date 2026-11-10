package main

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/safeurl/safeurltest"
)

// The sample-data response goes to the tenant (#717). A transport error names
// the cluster resolver, the resolved address and the request URL with its key;
// the response carries a fixed text and the detail, without the key, goes to
// the server log.
func TestSampleData_TransportErrorsAreFixedTexts(t *testing.T) {
	safeurltest.CheckSampleData(t, `{"base_url":"https://api.example.com","path":"/v1","params":"api_key=`+safeurltest.Secret+`"}`,
		func(client *http.Client, logger *slog.Logger) http.HandlerFunc {
			return (&apiConsumer{allowPrivate: true, httpClient: client, logger: logger}).handleSampleData()
		})
}
