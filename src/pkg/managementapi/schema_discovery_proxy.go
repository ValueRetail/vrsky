package managementapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Schema discovery for the mapping UI ("Discover fields").
//
// Every source type but one used to POST straight at a worker's auxiliary port
// on localhost — thirteen hardcoded http://localhost:<port> calls in
// ui/src/components/Pipeline/schemaDiscovery.ts. That worked in compose and
// nowhere else, so the whole feature was dead in the deployed builder: the same
// shape as #205, #221 and #224, at the widest scale yet.
//
// Reachability is the smaller half. Those bodies carry credentials — Kafka's
// username/password and client key, RabbitMQ's URL and password, the API
// source's auth_value, SFTP's and SAP's whole config object — and the worker
// ports have no authentication at all. Publishing them through an ingress to
// fix reachability would have put a credential-accepting endpoint on the
// internet, which is exactly why #224 declined to do that for the event
// streams.
//
// So discovery goes through this API instead: authenticated, and with the
// tenant taken from the session rather than from the request.
//
// WHAT THIS STILL DOES NOT FIX. The browser continues to hold and send the
// plaintext credential, because discovery runs while a node is being
// configured, often before the connection is saved — there is no connection ID
// to resolve secrets against. Moving credential resolution server-side means
// keying discovery on a saved connection, which is a change to the builder
// flow, not just to transport. This closes the reachability and authentication
// halves and leaves that one deliberately.

// schemaSource is one source type the builder can discover a schema for.
//
// An ALLOWLIST, for the same reason as workerEventSources in
// worker_events_proxy.go: the upstream address is built from these values and
// never from the request. A caller-supplied target would be an SSRF primitive
// pointed at the cluster network — and this endpoint forwards a request body,
// which makes it a considerably more useful one.
type schemaSource struct {
	service string // service name, per docker-compose and the k8s Service
	port    int    // the worker's WORKER_HTTP_PORT
	path    string // the worker's discovery endpoint
}

// Keyed by the node config's `type`, which is what the builder already has in
// hand. TestSchemaSourcesMatchUI pins this table to the UI's switch.
var schemaSources = map[string]schemaSource{
	"database":         {"db-consumer", 9300, "/schema/"},
	"file":             {"file-consumer", 9200, "/sample-data/"},
	"api":              {"api-consumer", 9800, "/sample-data/"},
	"salesforce":       {"salesforce-consumer", 9250, "/schema/"},
	"kafka":            {"kafka-consumer", 9220, "/sample-data/"},
	"rabbitmq":         {"rabbitmq-consumer", 9230, "/sample-data/"},
	"sap_s4hana":       {"sap-s4hana-consumer", 9290, "/sample-data/"},
	"sftp":             {"sftp-consumer", 9210, "/sample-data/"},
	"cloud_storage":    {"cloud-storage-consumer", 9240, "/sample-data/"},
	"sitoo":            {"sitoo-consumer", 9260, "/sample-data/"},
	"business_central": {"business-central-consumer", 9310, "/sample-data/"},
	"visma":            {"visma-consumer", 9320, "/sample-data/"},
	"brightpearl":      {"brightpearl-consumer", 9280, "/sample-data/"},
	// "tenant" is absent on purpose: it already goes through
	// GET /api/v1/sample-data/source, which reads from this platform's own
	// database rather than a worker.
}

// maxDiscoveryBody bounds what is forwarded. Discovery bodies are connector
// config — a few kilobytes with certificates in the largest case.
const maxDiscoveryBody = 256 << 10

// DiscoverSchema forwards a schema-discovery request to the worker that owns
// that source type.
//
// POST /api/v1/schema-discovery/{source}
func (h *Handler) DiscoverSchema(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	sourceType := r.PathValue("source")
	src, ok := schemaSources[sourceType]
	if !ok {
		_ = writeError(w, http.StatusNotFound, "UnknownSource",
			"schema discovery is not available for that source type", nil)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDiscoveryBody+1))
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "BadRequest", "could not read the request body", nil)
		return
	}
	if len(raw) > maxDiscoveryBody {
		_ = writeError(w, http.StatusRequestEntityTooLarge, "BodyTooLarge",
			"discovery config is too large", nil)
		return
	}

	body := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			_ = writeError(w, http.StatusBadRequest, "BadRequest", "body must be a JSON object", nil)
			return
		}
	}

	// The tenant comes from the session, never from the caller.
	//
	// Several of these calls already sent tenant_id, and the workers use it to
	// resolve stored secrets. A browser could put any workspace's id there and
	// the worker had no way to know better — it is an unauthenticated port. The
	// overwrite is the point of routing through here.
	body["tenant_id"] = tenantID

	out, err := json.Marshal(body)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", err.Error(), nil)
		return
	}

	upstream := fmt.Sprintf(workerAddrTemplate(), src.service, src.port) + src.path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(out))
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "BadUpstream", err.Error(), nil)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// Discovery reaches out to the customer's system — a slow SFTP or a cold
	// SAP gateway is normal — so the budget is generous, but bounded: without
	// one a stuck connector would hold a management-api request open forever.
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		_ = writeError(w, http.StatusBadGateway, "ConnectorUnreachable",
			"could not reach the "+sourceType+" connector", nil)
		return
	}
	defer resp.Body.Close()

	// The worker's own JSON is passed through unchanged: it carries the {ok,
	// data, fields, columns, error} shape the builder already understands, and
	// re-wrapping it here would mean two places to keep in step.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxDiscoveryBody))
}
