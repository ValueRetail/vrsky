package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/lib/pq"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/auth"
)

// handleRegister exchanges a one-time registration token for a credential.
//
// POST /agent/v1/register
//
// The token is consumed by a single UPDATE that only matches an unused,
// unexpired token, so two machines racing on one token cannot both register;
// the loser sees token_expired_or_used. The agent is created in the tenant the
// TOKEN belongs to, in the same transaction — nothing in the request can name a
// tenant.
func (s *gateway) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req agentproto.RegisterRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.RegistrationToken)
	if !strings.HasPrefix(token, agentproto.RegTokenPrefix) {
		writeErr(w, http.StatusUnauthorized, agentproto.ErrInvalidToken,
			"registration_token must be the vrsky_reg_… token from Settings → Remote agents")
		return
	}
	dirs, bad := validDirectories(req.Directories)
	if bad != "" {
		writeErr(w, http.StatusBadRequest, agentproto.ErrBadRequest, bad)
		return
	}
	dirsJSON, _ := json.Marshal(dirs)

	credential, err := newCredential()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not generate a credential")
		return
	}

	ctx := r.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "try again shortly")
		return
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	var tokenID, tenantID, suggested, createdBy string
	var tokenGroups []string
	// lint:tenant-ok — lookup by unique one-time token hash; the tenant is recovered from the row.
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_registration_tokens SET used_at = NOW()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()
		RETURNING id::text, tenant_id::text, COALESCE(suggested_name, ''), COALESCE(created_by::text, ''), suggested_groups`,
		auth.HashToken(token)).Scan(&tokenID, &tenantID, &suggested, &createdBy, pq.Array(&tokenGroups))
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusUnauthorized, agentproto.ErrTokenExpiredOrUsed,
			"this registration token is unknown, already used, or expired — generate a new one in Settings → Remote agents")
		return
	}
	if err != nil {
		s.logger.Error("Registration token lookup failed", "error", err)
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "try again shortly")
		return
	}

	name := firstNonEmpty(clip(req.Name, 255), clip(suggested, 255), clip(req.Hostname, 255), "agent")
	// The machine's own --groups win over the token's; both were typed by
	// someone with access to this workspace. Validated here too: the token's
	// were checked when minted, the request's were not.
	groups := tokenGroups
	if len(req.Groups) > 0 {
		groups = req.Groups
	}
	groups, badGroup := validGroups(groups)
	if badGroup != "" {
		writeErr(w, http.StatusBadRequest, agentproto.ErrBadRequest, badGroup)
		return
	}
	var createdByArg any
	if createdBy != "" {
		createdByArg = createdBy
	}
	var agentID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agents (tenant_id, name, hostname, os, arch, agent_version, credential_hash, directories, created_by, last_seen_at, groups)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), $10)
		RETURNING id::text`,
		tenantID, name, clip(req.Hostname, 255), clip(req.OS, 32), clip(req.Arch, 32), clip(req.Version, 64),
		auth.HashToken(credential), dirsJSON, createdByArg, pq.Array(groups)).Scan(&agentID)
	if err != nil {
		// The rollback also un-consumes the token, so the user can retry with
		// --name rather than generate a new one.
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict, agentproto.ErrNameTaken,
				"an agent called "+name+" already exists in this workspace — register with --name to choose another")
			return
		}
		s.logger.Error("Could not create agent", "error", err)
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "try again shortly")
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_registration_tokens SET used_by_agent = $1 WHERE id::text = $2 AND tenant_id::text = $3`,
		agentID, tokenID, tenantID); err != nil {
		s.logger.Error("Could not link token to agent", "error", err)
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "try again shortly")
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "try again shortly")
		return
	}

	s.logger.Info("Agent registered", "agent_id", agentID, "tenant_id", tenantID, "name", name, "hostname", req.Hostname)
	writeJSON(w, http.StatusCreated, agentproto.RegisterResponse{AgentID: agentID, Name: name, Credential: credential})
}

// handleAnnounce refreshes what an agent reports about itself.
//
// POST /agent/v1/announce
func (s *gateway) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	var req agentproto.AnnounceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	dirs, bad := validDirectories(req.Directories)
	if bad != "" {
		writeErr(w, http.StatusBadRequest, agentproto.ErrBadRequest, bad)
		return
	}
	dirsJSON, _ := json.Marshal(dirs)
	if _, err := s.db.ExecContext(r.Context(), `
		UPDATE agents SET hostname = $3, os = $4, arch = $5, agent_version = $6, directories = $7, last_seen_at = NOW()
		WHERE id::text = $1 AND tenant_id::text = $2`,
		id.ID, id.TenantID, clip(req.Hostname, 255), clip(req.OS, 32), clip(req.Arch, 32),
		clip(req.Version, 64), dirsJSON); err != nil {
		s.logger.Error("Could not record agent announce", "agent_id", id.ID, "error", err)
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "try again shortly")
		return
	}
	s.mu.Lock()
	s.agentLocked(id.ID).lastSeenWrite = s.now()
	s.mu.Unlock()
	// A machine that just (re)announced may have joined a group or gained a
	// folder: refresh its tenant's group sessions now, not at the next tick.
	go s.reconcileTenant(context.Background(), id.TenantID)
	w.WriteHeader(http.StatusNoContent)
}

func newCredential() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return agentproto.CredentialPrefix + hex.EncodeToString(b), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isUniqueViolation matches Postgres SQLSTATE 23505, as pkg/managementapi does.
func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "unique constraint")
}

// validGroups trims, validates, de-duplicates and sorts group names, the same
// rule as the management API's. Returns a user-facing message when one is
// unusable.
func validGroups(raw []string) ([]string, string) {
	seen := map[string]bool{}
	out := []string{}
	for _, g := range raw {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if !agentproto.ValidGroupName(g) {
			return nil, fmt.Sprintf("group %q: use letters, digits, - and _ (1-64 characters), e.g. all-tills", g)
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	if len(out) > 32 {
		return nil, "an agent can be in at most 32 groups"
	}
	sort.Strings(out)
	return out, ""
}
