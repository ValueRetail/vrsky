// Command mock-sap is a tiny stand-in for SAP S/4HANA OData, for local pipeline
// testing of the sap-s4hana connectors (no real SAP tenant needed). It is
// path-agnostic and accepts any auth header, so point a connector's
// api_base_url at it (e.g. http://mock-sap:8099/sap/opu/odata/sap/API_SALES_ORDER_SRV).
//
//   - GET with "X-CSRF-Token: Fetch"  → returns a CSRF token + session cookie
//   - other GET                        → OData v2 page of sample sales orders,
//     paginated via a relative __next link
//   - POST/PATCH                       → requires the CSRF token, returns 201
//   - GET /__test/received             → what has been POSTed to it so far
//   - DELETE /__test/received          → forget it
//
// The two /__test routes exist so an automated end-to-end can assert that a
// record actually arrived, rather than a human reading the log. Everything the
// pipeline writes lands there; see test/integration/sap_e2e_test.go.
//
// NOT for production — it validates nothing and returns canned data.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

// received holds the bodies POSTed to this mock, for the end-to-end test to
// assert against. Unbounded is fine: the process is a test fixture with a
// lifetime measured in minutes.
var received struct {
	mu     sync.Mutex
	bodies []json.RawMessage
}

func main() {
	addr := ":" + env("PORT", "8099")
	http.HandleFunc("/__test/received", handleReceived)
	http.HandleFunc("/", handle)
	log.Printf("mock-sap listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// handleReceived reports, or clears, what the pipeline has written here.
func handleReceived(w http.ResponseWriter, r *http.Request) {
	received.mu.Lock()
	defer received.mu.Unlock()

	switch r.Method {
	case http.MethodDelete:
		received.bodies = nil
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		out := received.bodies
		if out == nil {
			out = []json.RawMessage{}
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// CSRF token fetch (the producer does this before a write).
		if r.Header.Get("X-CSRF-Token") == "Fetch" {
			http.SetCookie(w, &http.Cookie{Name: "MOCK_SESSION", Value: "s1"})
			w.Header().Set("X-CSRF-Token", "mock-csrf")
			w.WriteHeader(http.StatusOK)
			log.Printf("GET csrf-fetch %s", r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("$skiptoken") == "" {
			// Page 1: two sales orders + a relative next link.
			log.Printf("GET page1 %s", r.URL.RequestURI())
			fmt.Fprint(w, `{"d":{"results":[`+
				`{"SalesOrder":"5001","SalesOrderType":"OR","SoldToParty":"0017100001","TotalNetAmount":"1200.00","TransactionCurrency":"EUR"},`+
				`{"SalesOrder":"5002","SalesOrderType":"OR","SoldToParty":"0017100002","TotalNetAmount":"640.00","TransactionCurrency":"EUR"}`+
				`],"__next":"?$skiptoken=P2"}}`)
			return
		}
		// Page 2: one more, no next link (end of collection).
		log.Printf("GET page2 %s", r.URL.RequestURI())
		fmt.Fprint(w, `{"d":{"results":[`+
			`{"SalesOrder":"5003","SalesOrderType":"OR","SoldToParty":"0017100003","TotalNetAmount":"85.50","TransactionCurrency":"EUR"}`+
			`]}}`)

	case http.MethodPost, http.MethodPatch:
		if t := r.Header.Get("X-CSRF-Token"); t == "" || t == "Fetch" {
			w.Header().Set("X-CSRF-Token", "Required")
			w.WriteHeader(http.StatusForbidden)
			log.Printf("%s rejected: missing CSRF token", r.Method)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		log.Printf("%s accepted %d bytes: %s", r.Method, len(body), string(body))
		if json.Valid(body) {
			received.mu.Lock()
			received.bodies = append(received.bodies, json.RawMessage(body))
			received.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"d":{"SalesOrder":"6001"}}`)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
