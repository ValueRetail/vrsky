package managementapi

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Live-event streams for the pipeline builder's test panels.
//
// Each of these workers serves an unauthenticated SSE stream at /events/{id} on
// its auxiliary HTTP port, and the builder's panels read it directly. That
// worked in local compose, where the browser can reach localhost:<port>, and
// nowhere else: in a deployment those ports are not routable from a browser, so
// EventSource opened, failed, and retried forever behind an onerror comment
// that reads "// Will auto-reconnect". The panel showed an empty list and said
// nothing — the same silent-no-op shape as #205 (edge workers) and #221 (the
// webhook URL), one layer further out.
//
// The fix is to route them through this API instead of exposing worker ports.
// That is not merely a routing convenience: the worker endpoints have NO
// AUTHENTICATION AND NO TENANT CHECK of their own. Anyone who could reach one
// could stream any connection's live payloads by guessing an ID. Publishing
// those ports through an ingress would have made that reachable from the
// internet. This handler is the only thing that enforces the tenant boundary
// on them, so the ownership check below is the whole security model, not a
// formality.

// workerEventSource is one worker whose live-event stream the builder shows.
//
// This is an ALLOWLIST, and must stay one. The proxy builds an upstream URL
// from these values; accepting a caller-supplied host or port instead would
// turn the endpoint into an SSRF primitive pointed at the cluster network,
// where the management API can reach the database, NATS, MinIO and the
// Kubernetes API.
type workerEventSource struct {
	service string // service name, per docker-compose and the k8s Service
	port    int    // the worker's WORKER_HTTP_PORT
}

var workerEventSources = map[string]workerEventSource{
	"file-consumer":  {"file-consumer", 9200},
	"http-producer":  {"http-producer", 9400},
	"db-producer":    {"db-producer", 9500},
	"data-converter": {"data-converter", 9600},
	"data-filter":    {"data-filter", 9700},
}

// workerAddrTemplateEnv names the printf template used to reach a worker,
// taking the service name and port. It differs per environment — compose
// resolves bare service names on its own network, while in Kubernetes the
// services carry a "vrsky-" prefix and live in another namespace — so it is
// configuration rather than something derivable here.
//
//	compose (default): http://%s:%d
//	AKS:               http://vrsky-%s.vrsky-platform.svc.cluster.local:%d
const workerAddrTemplateEnv = "WORKER_ADDR_TEMPLATE"

func workerAddrTemplate() string {
	if t := os.Getenv(workerAddrTemplateEnv); t != "" {
		return t
	}
	return "http://%s:%d"
}

// ProxyWorkerEvents streams a worker's SSE event feed for one connection,
// after checking that the connection belongs to the caller's tenant.
//
// GET /api/v1/connections/{id}/workers/{worker}/events
func (h *Handler) ProxyWorkerEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	worker := r.PathValue("worker")
	src, ok := workerEventSources[worker]
	if !ok {
		// Deliberately does not echo the requested name back into a list of
		// what exists; the allowlist is enumerated in the docs, not in a 404.
		_ = writeError(w, http.StatusNotFound, "UnknownWorker",
			"no live event stream is published for that worker", nil)
		return
	}

	// Ownership check, and the source of the ID used upstream. Reading the id
	// back from the row rather than reusing the path parameter means the value
	// we interpolate into the upstream URL is one the database produced for
	// this tenant — a caller cannot smuggle path segments through it.
	var connID string
	err = h.db.QueryRowContext(ctx,
		`SELECT id::text FROM connections WHERE id::text = $1 AND tenant_id::text = $2`,
		r.PathValue("id"), tenantID).Scan(&connID)
	if err != nil {
		// Same response whether the connection is missing or belongs to
		// someone else: distinguishing them would confirm the existence of
		// another tenant's connection IDs.
		_ = writeError(w, http.StatusNotFound, "ConnectionNotFound",
			"connection not found", nil)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		_ = writeError(w, http.StatusInternalServerError, "StreamingUnsupported",
			"the server cannot stream responses", nil)
		return
	}

	upstream := fmt.Sprintf(workerAddrTemplate(), src.service, src.port) +
		"/events/" + url.PathEscape(connID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "BadUpstream", err.Error(), nil)
		return
	}
	req.Header.Set("Accept", "text/event-stream")

	// No client timeout: an SSE stream is meant to stay open. The request
	// context ends it when the browser disconnects or the server shuts down.
	// http.DefaultClient would be wrong here for the opposite reason — it also
	// has no timeout, but shares state with every other caller in the process.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		// The panel's own error handling is a silent retry, so say something
		// useful in the stream itself rather than closing without explanation.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: error\ndata: {\"error\":%q}\n\n",
			"cannot reach the "+worker+" service")
		flusher.Flush()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = writeError(w, http.StatusBadGateway, "UpstreamError",
			fmt.Sprintf("%s returned %d", worker, resp.StatusCode), nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// The stream passes through a proxy in every deployment; without this,
	// nginx buffers it and events arrive in batches or not at all.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Copy and flush per chunk. io.Copy alone would buffer, which for a stream
	// whose entire purpose is liveness defeats the feature without failing.
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return // client went away
			}
			flusher.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				// Upstream died mid-stream. EventSource will reconnect; this
				// line is what makes the reason visible if anyone looks.
				fmt.Fprintf(w, "event: error\ndata: {\"error\":%q}\n\n",
					"stream from "+worker+" ended: "+sanitizeStreamErr(readErr))
				flusher.Flush()
			}
			return
		}
	}
}

// sanitizeStreamErr keeps upstream addresses out of a message that reaches the
// browser. The internal service DNS name is not a secret worth much, but it is
// not the client's business either, and it appears verbatim in net/http errors.
func sanitizeStreamErr(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}
