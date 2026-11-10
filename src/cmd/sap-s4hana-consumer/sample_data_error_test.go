package main

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/safeurl/safeurltest"
)

// The sample-data response goes to the tenant (#717): a transport error, on
// the API path (basic auth) and on the token path (OAuth2), shows a fixed text
// and logs the detail without the key.
func TestSampleData_TransportErrorsAreFixedTexts(t *testing.T) {
	newHandler := func(client *http.Client, logger *slog.Logger) http.HandlerFunc {
		return (&sapConsumer{allowPrivate: true, httpClient: client, logger: logger}).handleSampleData()
	}
	t.Run("basic auth", func(t *testing.T) {
		safeurltest.CheckSampleData(t, `{"username":"u","password":"p","api_base_url":"https://api.example.com/odata","entity_set":"A_SalesOrder"}`, newHandler)
	})
	t.Run("oauth2 token", func(t *testing.T) {
		safeurltest.CheckSampleData(t, `{"api_base_url":"https://api.example.com/odata","auth_type":"oauth2","client_id":"id","client_secret":"s","token_url":"https://login.example.com/token","entity_set":"A_SalesOrder"}`, newHandler)
	})
}
