package main

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// The sample-data response goes to the tenant (#717). A transport error names
// the cluster resolver, the resolved address and the request URL with its key;
// the response carries a fixed text and the detail goes to the server log.
func TestSampleData_TransportErrorsAreFixedTexts(t *testing.T) {
	const reqURL = "https://api.example.com/v1?api_key=s3cr3t-key"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"dns failure", &url.Error{Op: "Get", URL: reqURL, Err: &net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: "api.example.com", Server: "10.0.0.10:53", IsNotFound: true}}},
			"host name could not be resolved"},
		{"connection refused with credential url", &url.Error{Op: "Get", URL: reqURL, Err: &net.OpError{Op: "dial", Net: "tcp",
			Addr: &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 443}, Err: io.ErrUnexpectedEOF}},
			"connection failed"},
		{"unclassified", &url.Error{Op: "Get", URL: reqURL, Err: io.ErrUnexpectedEOF}, "request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs strings.Builder
			s := &apiConsumer{
				allowPrivate: true,
				httpClient:   &http.Client{Transport: failingTransport{tt.err}},
				logger:       slog.New(slog.NewTextHandler(&logs, nil)),
			}
			w := httptest.NewRecorder()
			s.handleSampleData()(w, httptest.NewRequest(http.MethodPost, "/sample-data",
				strings.NewReader(`{"base_url":"https://api.example.com","path":"/v1","params":"api_key=s3cr3t-key"}`)))
			out := w.Body.String()
			if want := `"error":"` + tt.want + `"`; !strings.Contains(out, want) {
				t.Errorf("body = %s, want %s", out, want)
			}
			for _, leak := range []string{"10.0.0.10", "10.1.2.3", "s3cr3t-key", "lookup", "dial tcp", "api.example.com"} {
				if strings.Contains(out, leak) {
					t.Errorf("body %s leaks %q", out, leak)
				}
			}
			if !strings.Contains(logs.String(), "s3cr3t-key") && !strings.Contains(logs.String(), "10.0.0.10") && !strings.Contains(logs.String(), "10.1.2.3") {
				t.Errorf("the detail should be in the server log, got %q", logs.String())
			}
		})
	}
}
