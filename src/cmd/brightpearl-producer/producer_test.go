package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func testProducer() *brightpearlProducer {
	return &brightpearlProducer{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: http.DefaultClient,
	}
}

func cfgFor(url string) *BrightpearlProducerConfig {
	return &BrightpearlProducerConfig{BaseURL: url, AppRef: "app", StaffToken: "tok", Resource: "/order-service/order"}
}

func TestWrite_Success(t *testing.T) {
	var appRef, staffTok, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appRef, staffTok, path = r.Header.Get("brightpearl-app-ref"), r.Header.Get("brightpearl-staff-token"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`{"orderTypeCode":"SO"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if appRef != "app" || staffTok != "tok" {
		t.Errorf("headers = %q / %q", appRef, staffTok)
	}
	if path != "/order-service/order" {
		t.Errorf("path = %q", path)
	}
	if body != `{"orderTypeCode":"SO"}` {
		t.Errorf("body = %q", body)
	}
}

func TestWrite_Classification(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
		wantErr   bool
	}{
		{http.StatusOK, false, false},
		{http.StatusBadRequest, true, true},
		{http.StatusUnauthorized, true, true},
		{http.StatusInternalServerError, false, true},
		{http.StatusServiceUnavailable, false, true},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		err := testProducer().write(context.Background(), cfgFor(srv.URL), []byte(`{}`))
		srv.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("status %d: err=%v wantErr=%v", tc.status, err, tc.wantErr)
			continue
		}
		if tc.wantErr && sdk.IsPermanent(err) != tc.permanent {
			t.Errorf("status %d: IsPermanent=%v want %v", tc.status, sdk.IsPermanent(err), tc.permanent)
		}
	}
}
