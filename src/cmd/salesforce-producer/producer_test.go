package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/sdk/harness"
)

func mockConn(mock sqlmock.Sqlmock, connID, instanceURL, operation string) {
	nodes := fmt.Sprintf(
		`[{"id":"p1","type":"producer","config":{"type":"salesforce","salesforce":{"instance_url":%q,"oauth_grant_id":"g1","object":"Account","operation":%q,"external_id_field":"ExtId__c"}}}]`,
		instanceURL, operation)
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("FROM connections WHERE id").
			WithArgs(connID).
			WillReturnRows(sqlmock.NewRows([]string{"nodes", "edges"}).AddRow([]byte(nodes), []byte(`[]`)))
	}
}

// TestSalesforceProducer_RESTInsert: a small batch goes through the REST sObject
// API with a Bearer token.
func TestSalesforceProducer_RESTInsert(t *testing.T) {
	const connID = "sfp-rest"
	var posted atomic.Int32
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sobjects/Account") {
			posted.Add(1)
			auth.Store(r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"001","success":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mockConn(mock, connID, srv.URL, "insert")

	p := &salesforceProducer{
		resolveToken: func(context.Context, string, string, bool) (string, error) { return "tok", nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "salesforce-producer", DB: db})

	env := envelope.New()
	env.ID = "sfp-1"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.Payload = []byte(`{"Name":"Acme"}`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "record POSTed to Salesforce REST", func() bool {
		return posted.Load() == 1
	})
	if a := auth.Load(); a == nil || a.(string) != "Bearer tok" {
		t.Errorf("auth = %v, want Bearer tok", a)
	}
}

// TestSalesforceProducer_BulkAPI: a batch ≥ threshold uses Bulk API 2.0
// (create job → upload CSV → close), not per-record REST.
func TestSalesforceProducer_BulkAPI(t *testing.T) {
	const connID = "sfp-bulk"
	var create, upload, closed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs/ingest"):
			create.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"job-1","state":"Open"}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/jobs/ingest/job-1/batches"):
			upload.Store(true)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/jobs/ingest/job-1"):
			closed.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"job-1","state":"UploadComplete"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mockConn(mock, connID, srv.URL, "insert")

	p := &salesforceProducer{
		bulkThreshold: 2, // force bulk for a tiny batch
		resolveToken:  func(context.Context, string, string, bool) (string, error) { return "tok", nil },
	}
	h := harness.NewProducerHarness(t, p, harness.Options{Name: "salesforce-producer", DB: db})

	env := envelope.New()
	env.ID = "sfp-bulk-1"
	env.IntegrationID = connID
	env.TenantID = "tenant-1"
	env.Payload = []byte(`[{"Name":"Acme"},{"Name":"Globex"}]`)
	h.Publish(t, env)

	harness.Eventually(t, 5*time.Second, "bulk job created → uploaded → closed", func() bool {
		return create.Load() && upload.Load() && closed.Load()
	})
}
