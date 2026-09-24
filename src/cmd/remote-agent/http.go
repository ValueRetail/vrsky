package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/auth"
)

// agentRoutes is the agent-facing API, served on the aux port and exposed at
// /agent through its own Ingress (infrastructure/kubernetes/ingress/agent-ingress.yaml).
func (s *gateway) agentRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /agent/v1/register", s.protocol(http.HandlerFunc(s.handleRegister)))
	mux.Handle("POST /agent/v1/announce", s.protocol(s.authenticated(s.handleAnnounce)))
	mux.Handle("GET /agent/v1/work", s.protocol(s.authenticated(s.handleWork)))
	mux.Handle("GET /agent/v1/deliveries/{id}/body", s.protocol(s.authenticated(s.handleBody)))
	mux.Handle("POST /agent/v1/deliveries/{id}/ack", s.protocol(s.authenticated(s.handleAck)))
	mux.Handle("POST /agent/v1/uploads", s.protocol(s.authenticated(s.handleUpload)))
	mux.HandleFunc("/agent/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusNotFound, agentproto.ErrBadRequest, "no such agent endpoint")
	})
	return mux
}

// protocol refuses a peer speaking another protocol major version. A missing
// header is read as the current version, so a hand-written curl still works.
func (s *gateway) protocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get(agentproto.HeaderProto); v != "" && v != strconv.Itoa(agentproto.ProtoVersion) {
			writeErr(w, http.StatusUpgradeRequired, agentproto.ErrUnsupportedProtocol,
				"this VRSky speaks agent protocol "+strconv.Itoa(agentproto.ProtoVersion)+"; update the agent")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// agentIdentity is who an authenticated request is from. The tenant comes from
// the agent's row, never from anything the request says.
type agentIdentity struct {
	ID       string
	TenantID string
	Name     string
}

type ctxKey struct{}

func identityFrom(ctx context.Context) agentIdentity {
	id, _ := ctx.Value(ctxKey{}).(agentIdentity)
	return id
}

// authenticated resolves the bearer credential to one agent. Revocation is
// read from the database on every request — one indexed lookup — so revoking
// in the UI takes effect on the agent's very next call, with no cache to
// invalidate.
func (s *gateway) authenticated(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		raw = strings.TrimSpace(raw)
		if !ok || !strings.HasPrefix(raw, agentproto.CredentialPrefix) {
			writeErr(w, http.StatusUnauthorized, agentproto.ErrUnauthorized, "an agent credential is required")
			return
		}
		id, revoked, err := s.lookupAgent(r.Context(), auth.HashToken(raw))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeErr(w, http.StatusUnauthorized, agentproto.ErrUnauthorized, "unknown agent credential")
			return
		case err != nil:
			s.logger.Error("Agent credential lookup failed", "error", err)
			writeErr(w, http.StatusServiceUnavailable, "unavailable", "try again shortly")
			return
		case revoked:
			writeErr(w, http.StatusForbidden, agentproto.ErrAgentRevoked,
				"this agent was revoked in VRSky; register the machine again to reconnect")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// dbLookupAgent resolves a credential hash to its agent, and whether that agent
// is revoked. sql.ErrNoRows means no agent has this credential.
func (s *gateway) dbLookupAgent(ctx context.Context, credentialHash string) (agentIdentity, bool, error) {
	var id agentIdentity
	var revokedAt sql.NullTime
	// lint:tenant-ok — lookup by unique credential hash; the tenant is recovered from the row.
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, tenant_id::text, name, revoked_at FROM agents WHERE credential_hash = $1`,
		credentialHash).Scan(&id.ID, &id.TenantID, &id.Name, &revokedAt)
	return id, revokedAt.Valid, err
}

// touch records that an agent was heard from, at most once per
// lastSeenWriteEvery.
func (s *gateway) touch(ctx context.Context, id agentIdentity) {
	s.mu.Lock()
	a := s.agentLocked(id.ID)
	due := s.now().Sub(a.lastSeenWrite) >= lastSeenWriteEvery
	if due {
		a.lastSeenWrite = s.now()
	}
	s.mu.Unlock()
	if !due {
		return
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE agents SET last_seen_at = NOW() WHERE id::text = $1 AND tenant_id::text = $2`,
		id.ID, id.TenantID); err != nil {
		s.logger.Warn("Could not record agent last-seen", "agent_id", id.ID, "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, agentproto.ErrorResponse{Error: code, Message: msg})
}

// decodeJSON reads a small JSON body, writing the error response itself.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, agentproto.ErrBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// validDirectories checks and de-duplicates a reported directory list.
func validDirectories(in []agentproto.Directory) ([]agentproto.Directory, string) {
	seen := map[string]bool{}
	out := make([]agentproto.Directory, 0, len(in))
	for _, d := range in {
		if !agentproto.ValidDirectoryName(d.Name) {
			return nil, "invalid directory name " + strconv.Quote(d.Name)
		}
		if !agentproto.ValidMode(d.Mode) {
			return nil, "directory " + d.Name + ": mode must be read or write"
		}
		if seen[d.Name] {
			return nil, "directory " + d.Name + " is listed twice"
		}
		seen[d.Name] = true
		out = append(out, d)
	}
	return out, ""
}

// clip bounds a free-text field the agent reports about itself, in
// characters, so a multi-byte name is never cut mid-rune.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}
