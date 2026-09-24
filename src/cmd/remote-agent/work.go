package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// handleWork is the agent's long-poll, and its heartbeat.
//
// GET /agent/v1/work?wait=<seconds>&watches=<version>
//
// It answers at once when there is a delivery to lease, or when the agent's
// watch set has changed since the version it sent; otherwise it holds up to
// wait seconds (capped at PollHoldMax) for either to happen.
func (s *gateway) handleWork(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	s.touch(r.Context(), id)

	wait := agentproto.PollHoldMax
	if v := r.URL.Query().Get("wait"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			wait = time.Duration(n) * time.Second
		}
	}
	if wait > agentproto.PollHoldMax {
		wait = agentproto.PollHoldMax
	}
	known := r.URL.Query().Get("watches")
	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	for {
		resp, notify := s.collectWork(id)
		if len(resp.Deliveries) > 0 || resp.WatchesVersion != known || wait == 0 {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		select {
		case <-notify:
		case <-deadline.C:
			writeJSON(w, http.StatusOK, resp)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// collectWork builds one agent's poll answer — its watch set and any
// deliveries free to lease — and leases those deliveries to it. Everything is
// read from this agent's own state and from sessions naming this agent, so no
// other agent's work can appear in it.
func (s *gateway) collectWork(id agentIdentity) (agentproto.WorkResponse, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agentLocked(id.ID)

	resp := agentproto.WorkResponse{Watches: []agentproto.Watch{}, Deliveries: []agentproto.Delivery{}}
	for _, sess := range s.sessions {
		if sess.tenantID != id.TenantID {
			continue // belt and braces: start already refused cross-tenant agents
		}
		for _, n := range sess.inputs {
			if n.AgentID == id.ID {
				resp.Watches = append(resp.Watches, agentproto.Watch{
					Op: agentproto.OpWatchDir, ConnectionID: sess.connID, Directory: n.Directory, After: n.After,
				})
			}
		}
	}
	sort.Slice(resp.Watches, func(i, j int) bool {
		wi, wj := resp.Watches[i], resp.Watches[j]
		return wi.ConnectionID+"/"+wi.Directory < wj.ConnectionID+"/"+wj.Directory
	})
	resp.WatchesVersion = watchesVersion(resp.Watches)

	now := s.now()
	ids := make([]string, 0, len(a.pending))
	for did := range a.pending {
		ids = append(ids, did)
	}
	sort.Strings(ids)
	for _, did := range ids {
		d := a.pending[did]
		if d.tenantID != id.TenantID {
			continue
		}
		if !d.leasedAt.IsZero() && now.Sub(d.leasedAt) < agentproto.LeaseDuration {
			continue // leased to this agent recently; waiting for its ack
		}
		d.leasedAt = now
		wire := d.wire
		if d.payloadRef == "" && len(d.inline) <= agentproto.InlineWorkBytes {
			wire.InlineBase64 = base64.StdEncoding.EncodeToString(d.inline)
		} else {
			wire.BodyURL = "/agent/v1/deliveries/" + d.wire.ID + "/body"
		}
		resp.Deliveries = append(resp.Deliveries, wire)
	}
	return resp, a.notify
}

func watchesVersion(ws []agentproto.Watch) string {
	h := sha256.New()
	for _, w := range ws {
		fmt.Fprintf(h, "%s|%s|%s\n", w.ConnectionID, w.Directory, w.After)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// handleBody streams one delivery's payload.
//
// GET /agent/v1/deliveries/{id}/body
func (s *gateway) handleBody(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	d := s.pendingFor(id.ID, r.PathValue("id"))
	if d == nil {
		writeErr(w, http.StatusNotFound, agentproto.ErrUnknownDelivery, "no such delivery for this agent")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set(agentproto.HeaderChecksum, d.wire.Checksum)
	if d.payloadRef == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(d.inline)))
		_, _ = w.Write(d.inline)
		return
	}
	rc, _, err := s.store.GetStream(r.Context(), d.payloadRef)
	if err != nil {
		s.logger.Error("Could not read offloaded payload", "delivery_id", d.wire.ID, "error", err)
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "the payload is not readable right now; retry")
		return
	}
	defer rc.Close()
	if d.wire.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(d.wire.Size, 10))
	}
	// The agent verifies the checksum before making the file visible, so a
	// truncated copy here is caught there rather than written.
	_, _ = io.Copy(w, rc)
}

// handleAck records the outcome of a delivery.
//
// POST /agent/v1/deliveries/{id}/ack
func (s *gateway) handleAck(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	var req agentproto.AckRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var result error
	switch req.Status {
	case agentproto.AckOK:
	case agentproto.AckFailed:
		reason := strings.TrimSpace(req.Error)
		if reason == "" {
			reason = "no reason given"
		}
		result = errors.New("agent could not write the file: " + clip(reason, 500))
	default:
		writeErr(w, http.StatusBadRequest, agentproto.ErrBadRequest, "status must be ok or failed")
		return
	}
	if !s.resolve(id.ID, r.PathValue("id"), result) {
		writeErr(w, http.StatusNotFound, agentproto.ErrUnknownDelivery, "no such delivery for this agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
