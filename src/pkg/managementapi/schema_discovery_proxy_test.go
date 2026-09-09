package managementapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func newDiscoveryRequest(t *testing.T, tenantID, source, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/schema-discovery/"+source, strings.NewReader(body))
	r.SetPathValue("source", source)
	return r.WithContext(ContextWithTenantID(context.Background(), tenantID))
}

// pointSourceAt redirects one allowlist entry at a test server for one test.
func pointSourceAt(t *testing.T, source string, srv *httptest.Server, path string) {
	t.Helper()
	host, port := splitTestServer(t, srv)
	orig, existed := schemaSources[source]
	schemaSources[source] = schemaSource{service: host, port: port, path: path}
	t.Cleanup(func() {
		if existed {
			schemaSources[source] = orig
		} else {
			delete(schemaSources, source)
		}
	})
}

// TestDiscoverSchema_ForwardsAndOverridesTenant is the security property: the
// connector is told which workspace to resolve secrets for, and that value
// comes from the session — never from the caller, who is talking to a port that
// has no authentication of its own.
func TestDiscoverSchema_ForwardsAndOverridesTenant(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"a":1}}`))
	}))
	defer upstream.Close()
	pointSourceAt(t, "kafka", upstream, "/sample-data/")

	// The caller claims another workspace. It must not survive.
	body := `{"brokers":"b:9092","topic":"t","password":"hunter2","tenant_id":"tenant-victim"}`
	rec := httptest.NewRecorder()
	(&Handler{}).DiscoverSchema(rec, newDiscoveryRequest(t, "tenant-caller", "kafka", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := gotBody["tenant_id"]; got != "tenant-caller" {
		t.Errorf("connector received tenant_id %v, want the session's tenant — a caller could otherwise "+
			"make the connector resolve another workspace's stored secrets", got)
	}
	if gotBody["topic"] != "t" {
		t.Errorf("the rest of the config did not survive the proxy: %v", gotBody)
	}
	if gotPath != "/sample-data/" {
		t.Errorf("upstream path = %q, want the source's own discovery endpoint", gotPath)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("connector response was not passed through: %s", rec.Body.String())
	}
}

// TestDiscoverSchema_RejectsSourceNotOnAllowlist: the upstream address is built
// from the table, never from the request. This endpoint forwards a body, which
// makes a caller-chosen target a considerably more useful SSRF primitive than a
// bare GET would be.
func TestDiscoverSchema_RejectsSourceNotOnAllowlist(t *testing.T) {
	for _, source := range []string{
		"nats",
		"tenant", // deliberately absent: served by /api/v1/sample-data/source
		"../../secrets",
		"http://169.254.169.254",
		"",
	} {
		t.Run(source, func(t *testing.T) {
			rec := httptest.NewRecorder()
			(&Handler{}).DiscoverSchema(rec, newDiscoveryRequest(t, "tenant-a", source, `{}`))

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for source %q", rec.Code, source)
			}
			if !strings.Contains(rec.Body.String(), "UnknownSource") {
				t.Errorf("source %q was not rejected by the allowlist; body: %s", source, rec.Body.String())
			}
		})
	}
}

func TestDiscoverSchema_RequiresTenant(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/schema-discovery/kafka", strings.NewReader(`{}`))
	r.SetPathValue("source", "kafka")
	rec := httptest.NewRecorder()
	(&Handler{}).DiscoverSchema(rec, r) // no tenant in context

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when the session carries no workspace", rec.Code)
	}
}

// TestSchemaSourcesMatchUI pins the Go allowlist to the source types the
// builder actually asks for. A case in the UI with no entry here is a
// "Discover fields" button that 404s; an entry here with no caller is dead
// surface. Neither looks wrong in its own file.
func TestSchemaSourcesMatchUI(t *testing.T) {
	src, err := readRepoFile("ui", "src", "components", "Pipeline", "schemaDiscovery.ts")
	if err != nil {
		t.Skipf("UI client not available (%v) — allowlist drift guard skipped", err)
	}

	uiSources := map[string]bool{}
	for _, m := range regexp.MustCompile(`/api/v1/schema-discovery/([a-z0-9_]+)`).FindAllStringSubmatch(src, -1) {
		uiSources[m[1]] = true
	}
	if len(uiSources) == 0 {
		t.Fatal("parsed no discovery calls from schemaDiscovery.ts — it changed shape; update this test")
	}

	for name := range uiSources {
		if _, ok := schemaSources[name]; !ok {
			t.Errorf("the builder discovers %q, which is not on the API allowlist — that button would 404", name)
		}
	}
	for name := range schemaSources {
		if !uiSources[name] {
			t.Errorf("the API proxies %q, which the builder never asks for — dead surface", name)
		}
	}
}
