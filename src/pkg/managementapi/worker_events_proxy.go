package managementapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
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
	"remote-agent":   {"remote-agent", 9330},
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

// sseHeartbeat is how often an idle stream carries an SSE comment. Every
// proxy in front of this API (the UI's nginx, ingress-nginx) closes an
// upstream that has been silent for 60 s; a pipeline with no traffic is
// silent for much longer than that. A var so tests can shorten it.
var sseHeartbeat = 25 * time.Second

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

	upstreams, err := workerUpstreams(ctx, src)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "BadUpstream", err.Error(), nil)
		return
	}
	path := "/events/" + url.PathEscape(connID)

	// No client timeout: an SSE stream is meant to stay open. The request
	// context ends it when the browser disconnects or the server shuts down.
	// http.DefaultClient would be wrong here for the opposite reason — it also
	// has no timeout, but shares state with every other caller in the process.
	client := &http.Client{}
	frames := make(chan string)
	ended := make(chan error, len(upstreams))
	opened := 0
	badStatus := false
	var lastErr error
	for i, base := range upstreams {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			_ = writeError(w, http.StatusInternalServerError, "BadUpstream", err.Error(), nil)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s returned %d", worker, resp.StatusCode)
			badStatus = true
			continue
		}
		opened++
		// Every replica greets with its own "connected" frame; the panel
		// needs one.
		go forwardFrames(ctx, resp.Body, i > 0, frames, ended)
	}

	if opened == 0 {
		if badStatus {
			_ = writeError(w, http.StatusBadGateway, "UpstreamError", lastErr.Error(), nil)
			return
		}
		// The panel's own error handling is a retry, so say something
		// useful in the stream itself rather than closing without explanation.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: error\ndata: {\"error\":%q}\n\n",
			"cannot reach the "+worker+" service")
		flusher.Flush()
		return
	}

	// The server's WriteTimeout (30 s) covers the whole response, and a live
	// stream is meant to outlive it. Past the deadline every write fails, but
	// the handler only notices on the next upstream event and the connection
	// stays open meanwhile, so the browser sat on a dead stream: it showed the
	// first frame and nothing after, and never reconnected. Clearing the
	// deadline needs every ResponseWriter wrapper in the chain to Unwrap; if
	// one does not, this fails and the stream behaves as before.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// The stream passes through a proxy in every deployment; without this,
	// nginx buffers it and events arrive in batches or not at all.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	if opened < len(upstreams) {
		// Partial: the panel will miss what the unreachable replicas handle.
		fmt.Fprintf(w, "event: error\ndata: {\"error\":%q}\n\n",
			fmt.Sprintf("cannot reach %d of %d %s instances: %s",
				len(upstreams)-opened, len(upstreams), worker, sanitizeStreamErr(lastErr)))
		flusher.Flush()
	}

	// Whole frames only, from any replica, plus a heartbeat on an idle
	// stream: every proxy in front of this API (the UI's nginx, ingress-nginx)
	// closes an upstream that has been silent for 60 s, and a pipeline with
	// no traffic is silent for much longer than that.
	tick := time.NewTicker(sseHeartbeat)
	defer tick.Stop()
	for opened > 0 {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case f := <-frames:
			if _, err := io.WriteString(w, f); err != nil {
				return // client went away
			}
			flusher.Flush()
		case err := <-ended:
			opened--
			if err != io.EOF {
				// A replica died mid-stream. EventSource reconnects once all
				// are gone; this line is what makes the reason visible.
				fmt.Fprintf(w, "event: error\ndata: {\"error\":%q}\n\n",
					"stream from "+worker+" ended: "+sanitizeStreamErr(err))
				flusher.Flush()
			}
		}
	}
}

// workerUpstreams returns one base URL per instance of a worker.
//
// The transforms run two replicas, each with its own in-memory event hub, so
// their Services are headless and the name resolves to every pod; the panel
// must hear all of them or it shows half the traffic. A single-instance worker
// or a compose container resolves to one address, and a ClusterIP Service to
// its VIP — both are the one-upstream case. A var so tests can stand two
// servers in for one worker.
var workerUpstreams = func(ctx context.Context, src workerEventSource) ([]string, error) {
	base := fmt.Sprintf(workerAddrTemplate(), src.service, src.port)
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(addrs) <= 1 {
		// One instance — or a lookup failure, which the dial then reports
		// in the words the panel already knows.
		return []string{base}, nil
	}
	urls := make([]string, 0, len(addrs))
	for _, a := range addrs {
		v := *u
		v.Host = net.JoinHostPort(a.IP.String(), u.Port())
		urls = append(urls, v.String())
	}
	sort.Strings(urls)
	return urls, nil
}

// forwardFrames reads one upstream and hands over complete SSE frames, so a
// heartbeat or another replica's frame never lands inside one. dropHello
// skips the "connected" greeting the first replica already supplied.
func forwardFrames(ctx context.Context, body io.ReadCloser, dropHello bool, frames chan<- string, ended chan<- error) {
	defer body.Close()
	r := bufio.NewReader(body)
	var frame strings.Builder
	flush := func() bool {
		if frame.Len() == 0 {
			return true
		}
		f := frame.String()
		frame.Reset()
		if !strings.HasSuffix(f, "\n\n") {
			f = strings.TrimRight(f, "\n") + "\n\n"
		}
		if dropHello && strings.Contains(f, `"type":"connected"`) {
			return true
		}
		select {
		case frames <- f:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			frame.WriteString(line)
		}
		if line == "\n" || err != nil {
			if !flush() {
				return
			}
		}
		if err != nil {
			ended <- err
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
