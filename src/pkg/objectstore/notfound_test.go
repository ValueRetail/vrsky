package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Tests for #609: Get and GetStream of every backend return an error wrapping
// ErrNotFound when the object is missing, and only then. Each backend runs its
// real SDK client against an httptest server that answers the way the provider
// does. A missing bucket or container, and a denied request, are not
// ErrNotFound: the event-mode consumer counts a not-found key as already
// handled, so a configuration error must not look like one.

// nfBackend builds a store of one provider against a fake whose answer to the
// object read is given by respond.
type nfBackend struct {
	name  string
	build func(t *testing.T, respond http.HandlerFunc) ObjectStore
}

// nfCase is what the fake says to the read, and whether that is a missing
// object.
type nfCase struct {
	name         string
	respond      map[string]http.HandlerFunc // by backend name
	wantNotFound bool
}

func nfStore(t *testing.T, cfg *Config, h http.HandlerFunc) ObjectStore {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.Endpoint = srv.URL
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New(%s): %v", cfg.Provider, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var nfBackends = []nfBackend{
	{"s3", func(t *testing.T, h http.HandlerFunc) ObjectStore {
		return nfStore(t, &Config{
			Provider: ProviderS3, Bucket: "b", Region: "us-east-1",
			AccessKeyID: "k", SecretAccessKey: "s",
		}, h)
	}},
	{"azure", func(t *testing.T, h http.HandlerFunc) ObjectStore {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		// Zm9v is base64 for "foo": the shared key only has to decode.
		s, err := New(context.Background(), &Config{
			Provider: ProviderAzure, Bucket: "c",
			ConnectionString: "DefaultEndpointsProtocol=http;AccountName=acct;AccountKey=Zm9v;BlobEndpoint=" + srv.URL + "/acct;",
		})
		if err != nil {
			t.Fatalf("New(azure): %v", err)
		}
		return s
	}},
	{"gcs", func(t *testing.T, h http.HandlerFunc) ObjectStore {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		s, err := New(context.Background(), &Config{
			Provider: ProviderGCS, Bucket: "b", Endpoint: srv.URL + "/storage/v1/",
		})
		if err != nil {
			t.Fatalf("New(gcs): %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}},
}

func nfS3Error(status int, code string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+code+`</Code><Message>m</Message></Error>`)
	}
}

func nfAzureError(status int, code string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("x-ms-error-code", code)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><Error><Code>`+code+`</Code><Message>m</Message></Error>`)
	}
}

func nfGCSError(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"code":`+strconv.Itoa(status)+`,"message":"m"}}`)
	}
}

var nfCases = []nfCase{
	{
		name: "object missing",
		respond: map[string]http.HandlerFunc{
			"s3":    nfS3Error(404, "NoSuchKey"),
			"azure": nfAzureError(404, "BlobNotFound"),
			"gcs":   nfGCSError(404),
		},
		wantNotFound: true,
	},
	{
		// A missing bucket or container is a configuration error. GCS has no
		// case here: its read API answers 404 for both and the client reports
		// both as a missing object.
		name: "bucket missing",
		respond: map[string]http.HandlerFunc{
			"s3":    nfS3Error(404, "NoSuchBucket"),
			"azure": nfAzureError(404, "ContainerNotFound"),
		},
		wantNotFound: false,
	},
	{
		name: "access denied",
		respond: map[string]http.HandlerFunc{
			"s3":    nfS3Error(403, "AccessDenied"),
			"azure": nfAzureError(403, "AuthorizationFailure"),
			"gcs":   nfGCSError(403),
		},
		wantNotFound: false,
	},
}

func TestGetNotFound_PerProvider(t *testing.T) {
	for _, be := range nfBackends {
		for _, tc := range nfCases {
			h, ok := tc.respond[be.name]
			if !ok {
				continue
			}
			t.Run(be.name+"/"+tc.name, func(t *testing.T) {
				store := be.build(t, h)

				_, _, err := store.Get(context.Background(), "in/gone.csv")
				checkNotFound(t, "Get", err, tc.wantNotFound)

				rc, _, err := store.GetStream(context.Background(), "in/gone.csv")
				if rc != nil {
					_ = rc.Close()
				}
				checkNotFound(t, "GetStream", err, tc.wantNotFound)
			})
		}
	}
}

func checkNotFound(t *testing.T, op string, err error, want bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want an error, got nil", op)
	}
	if got := errors.Is(err, ErrNotFound); got != want {
		t.Errorf("%s: errors.Is(err, ErrNotFound) = %v, want %v (err: %v)", op, got, want, err)
	}
	// The key stays in the message and the provider's own error stays in the
	// chain, so an operator reading the log sees what the provider said.
	if !strings.Contains(err.Error(), "in/gone.csv") {
		t.Errorf("%s: error %q does not name the key", op, err)
	}
}

// A present object still reads: the mapping touches errors only.
func TestGetFound_PerProvider(t *testing.T) {
	ok := map[string]http.HandlerFunc{
		"s3": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") },
		"azure": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "5")
			_, _ = io.WriteString(w, "hello")
		},
		"gcs": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") },
	}
	for _, be := range nfBackends {
		t.Run(be.name, func(t *testing.T) {
			store := be.build(t, ok[be.name])
			body, _, err := store.Get(context.Background(), "in/here.csv")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(body) != "hello" {
				t.Errorf("body = %q, want hello", body)
			}
		})
	}
}
