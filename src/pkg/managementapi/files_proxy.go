package managementapi

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// The pipeline builder's file manager and its file-upload panel.
//
// Both used to talk to a worker's auxiliary port straight from the browser —
// `${config.fileProducerUrl}/files` and `${config.fileConsumerUrl}/upload/{id}`
// — with the host baked into the bundle at build time. That works in compose,
// where the browser really can reach localhost:9900, and nowhere else: those
// ports are not routable from a browser in a deployment, and the VITE_* values
// never reach a static nginx bundle anyway. In production the page asked for
// localhost and the panel said "Load failed". Same shape as the worker event
// streams (see worker_events_proxy.go), one layer further out.
//
// Routing them through this API is again not merely a routing convenience. The
// worker endpoints had NO TENANT CHECK: /files took an absolute path checked
// only against the output root, so any caller could list and DELETE another
// tenant's files on the shared volume, and /upload took any running connection
// ID, so any caller could inject arbitrary bytes into another tenant's
// pipeline. Publishing those ports through an ingress — the obvious-looking fix
// for the broken panel — would have put both on the internet.
//
// The ownership check in each handler below is half of the security model. The
// other half is in the workers, which derive the tenant from the connection row
// themselves rather than trusting anything this proxy sends. Neither half
// trusts a header.

// fileWorkers is an ALLOWLIST, for the same reason workerEventSources is one:
// the upstream URL is built from these values, and accepting a caller-supplied
// host or port would turn the endpoint into an SSRF primitive aimed at the
// cluster network.
var fileWorkers = struct {
	producer workerEventSource
	consumer workerEventSource
}{
	producer: workerEventSource{"file-producer", 9900},
	consumer: workerEventSource{"file-consumer", 9200},
}

// fileWorkerTokenEnv names the shared bearer token the workers require on their
// file endpoints. It is what stops anything else on the cluster network from
// reaching past this proxy; unset (the compose default) leaves them open.
const (
	fileProducerTokenEnv = "FILE_PRODUCER_AUTH_TOKEN"
	fileConsumerTokenEnv = "FILE_CONSUMER_AUTH_TOKEN"
)

// ownedConnectionID returns the connection id when it belongs to the caller's
// tenant, having written the error response itself when it does not.
//
// The id is read back out of the database rather than reused from the path, so
// the value interpolated into an upstream URL is one the database produced for
// this tenant and cannot carry smuggled path segments.
func (h *Handler) ownedConnectionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenantID, err := GetTenantIDFromContext(r.Context())
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return "", false
	}
	var connID string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id::text FROM connections WHERE id::text = $1 AND tenant_id::text = $2`,
		r.PathValue("id"), tenantID).Scan(&connID)
	if err != nil {
		// Same response whether the connection is missing or belongs to
		// someone else: distinguishing them would confirm the existence of
		// another tenant's connection IDs.
		_ = writeError(w, http.StatusNotFound, "ConnectionNotFound", "connection not found", nil)
		return "", false
	}
	return connID, true
}

// ProxyListFiles lists a connection's produced files.
//
// GET /api/v1/connections/{id}/files?path=…
func (h *Handler) ProxyListFiles(w http.ResponseWriter, r *http.Request) {
	h.proxyFileRequest(w, r, http.MethodGet)
}

// ProxyDeleteFile deletes one produced file or directory.
//
// DELETE /api/v1/connections/{id}/files?path=…
func (h *Handler) ProxyDeleteFile(w http.ResponseWriter, r *http.Request) {
	h.proxyFileRequest(w, r, http.MethodDelete)
}

// proxyFileRequest forwards a list or delete to the file-producer for a
// connection the caller owns.
func (h *Handler) proxyFileRequest(w http.ResponseWriter, r *http.Request, method string) {
	connID, ok := h.ownedConnectionID(w, r)
	if !ok {
		return
	}

	// connection_id is what the worker resolves the tenant (and therefore the
	// directory) from. `path` passes through as the caller typed it: the worker
	// re-homes it into that tenant's subtree and refuses anything outside, so
	// it is never trusted here either.
	q := url.Values{}
	q.Set("connection_id", connID)
	q.Set("path", r.URL.Query().Get("path"))

	upstream := fmt.Sprintf(workerAddrTemplate(), fileWorkers.producer.service, fileWorkers.producer.port) +
		"/files?" + q.Encode()

	req, err := http.NewRequestWithContext(r.Context(), method, upstream, nil)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "BadUpstream", err.Error(), nil)
		return
	}
	if token := os.Getenv(fileProducerTokenEnv); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := fileProxyClient.Do(req)
	if err != nil {
		_ = writeError(w, http.StatusBadGateway, "UpstreamUnreachable",
			"cannot reach the file-producer service", nil)
		return
	}
	defer resp.Body.Close()

	copyJSONResponse(w, resp)
}

// ProxyUploadFile forwards a multipart upload to the file-consumer for a
// connection the caller owns.
//
// POST /api/v1/connections/{id}/files/upload
func (h *Handler) ProxyUploadFile(w http.ResponseWriter, r *http.Request) {
	connID, ok := h.ownedConnectionID(w, r)
	if !ok {
		return
	}

	upstream := fmt.Sprintf(workerAddrTemplate(), fileWorkers.consumer.service, fileWorkers.consumer.port) +
		"/upload/" + url.PathEscape(connID)

	// Streamed rather than buffered: uploads are capped at 32 MiB upstream, and
	// reading them into this process first would spend that per concurrent
	// request for no benefit.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, r.Body)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "BadUpstream", err.Error(), nil)
		return
	}
	// The multipart boundary lives in Content-Type; without it the upstream
	// cannot parse the form.
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	req.ContentLength = r.ContentLength
	if token := os.Getenv(fileConsumerTokenEnv); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := fileProxyClient.Do(req)
	if err != nil {
		_ = writeError(w, http.StatusBadGateway, "UpstreamUnreachable",
			"cannot reach the file-consumer service", nil)
		return
	}
	defer resp.Body.Close()

	copyJSONResponse(w, resp)
}

// copyJSONResponse relays an upstream response verbatim. The worker's own
// status and JSON body are the useful answer — a 403 for a path outside the
// tenant's directory, a 404 for an inactive connection — so they are passed
// through rather than flattened into one of this API's error shapes.
func copyJSONResponse(w http.ResponseWriter, resp *http.Response) {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	// Bounded: these are directory listings and short status bodies, and an
	// unbounded copy from an upstream is how a worker fault becomes this
	// process's memory problem.
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}

// fileProxyClient is shared across file-proxy requests so connections are
// reused. Unlike the SSE proxy these are ordinary bounded requests, so they get
// a timeout; http.DefaultClient would have none and shares state with every
// other caller in the process.
var fileProxyClient = &http.Client{Timeout: fileProxyTimeout}

// Generous because it also covers a 32 MiB upload over the cluster network.
const fileProxyTimeout = 2 * time.Minute
