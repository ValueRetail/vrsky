package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Remote-agent management endpoints (#266). All are X-Tenant-ID routes gated by
// RequireTenantRoleFromHeader, so the tenant in context is one the caller has
// been proven a member of — and every store call below passes that tenant, never
// one from the path or body.

// maxAgentNameLen matches agents.name VARCHAR(255), counted in characters.
const maxAgentNameLen = 255

type createAgentTokenRequest struct {
	// SuggestedName pre-fills the agent's name when it registers. Optional: the
	// agent can pass its own, and falls back to its hostname.
	SuggestedName string `json:"suggested_name"`
}

type renameAgentRequest struct {
	Name string `json:"name"`
}

func (h *Handler) agentStore() (AgentStore, bool) {
	s, ok := h.repo.(AgentStore)
	return s, ok
}

// validAgentName trims and checks a user-supplied agent name.
func validAgentName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxAgentNameLen {
		return "", false
	}
	return name, true
}

// decodeOptionalJSON decodes a small JSON body, treating an empty body as the
// zero value. Returns false after writing the error response.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return false
	}
	return true
}

// CreateAgentRegistrationToken mints a one-time token for registering a new
// agent in this workspace. The raw token appears in this response only.
//
// POST /api/v1/agents/registration-tokens
func (h *Handler) CreateAgentRegistrationToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store, ok := h.agentStore()
	if !ok {
		_ = writeError(w, http.StatusNotImplemented, "NotSupported", "remote agents are not available", nil)
		return
	}
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	var req createAgentTokenRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	suggested := ""
	if strings.TrimSpace(req.SuggestedName) != "" {
		name, ok := validAgentName(req.SuggestedName)
		if !ok {
			_ = writeError(w, http.StatusBadRequest, "InvalidName", "suggested_name must be 1-255 characters", nil)
			return
		}
		suggested = name
	}

	createdBy := ""
	if u := GetUserFromContext(ctx); u != nil {
		createdBy = u.ID
	}
	tok, err := store.CreateAgentRegistrationToken(ctx, tenantID, suggested, createdBy)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	SetAuditAction(ctx, "agent.token.create")
	SetAuditDetail(ctx, "token_id", tok.ID)
	_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: tok})
}

// ListAgents lists the workspace's agents, revoked ones included.
//
// GET /api/v1/agents
func (h *Handler) ListAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store, ok := h.agentStore()
	if !ok {
		_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: []*Agent{}})
		return
	}
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	agents, err := store.ListAgents(ctx, tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: agents})
}

// RenameAgent renames a live agent.
//
// PATCH /api/v1/agents/{id}
func (h *Handler) RenameAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store, ok := h.agentStore()
	if !ok {
		_ = writeError(w, http.StatusNotFound, "AgentNotFound", "agent not found", nil)
		return
	}
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	var req renameAgentRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	name, ok := validAgentName(req.Name)
	if !ok {
		_ = writeError(w, http.StatusBadRequest, "InvalidName", "name must be 1-255 characters", nil)
		return
	}

	agent, err := store.RenameAgent(ctx, tenantID, r.PathValue("id"), name)
	switch {
	case errors.Is(err, ErrAgentNotFound):
		// Same answer for "no such agent", "another tenant's agent" and
		// "revoked": distinguishing them would confirm which IDs exist.
		_ = writeError(w, http.StatusNotFound, "AgentNotFound", "agent not found", nil)
		return
	case errors.Is(err, ErrAgentNameTaken):
		_ = writeError(w, http.StatusConflict, "NameTaken", "another agent in this workspace already has that name", nil)
		return
	case err != nil:
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	SetAuditAction(ctx, "agent.rename")
	SetAuditDetail(ctx, "agent_id", agent.ID)
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: agent})
}

// RevokeAgent revokes an agent. Its credential stops working on its next
// request to the gateway, and its name is released.
//
// DELETE /api/v1/agents/{id}
func (h *Handler) RevokeAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store, ok := h.agentStore()
	if !ok {
		_ = writeError(w, http.StatusNotFound, "AgentNotFound", "agent not found", nil)
		return
	}
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	agentID := r.PathValue("id")
	if err := store.RevokeAgent(ctx, tenantID, agentID); err != nil {
		if errors.Is(err, ErrAgentNotFound) {
			_ = writeError(w, http.StatusNotFound, "AgentNotFound", "agent not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", err.Error(), nil)
		return
	}
	SetAuditAction(ctx, "agent.revoke")
	SetAuditDetail(ctx, "agent_id", agentID)
	w.WriteHeader(http.StatusNoContent)
}

// checkRemoteAgentNodes reports, for each remote_agent node, anything that
// would stop the gateway from running it: an agent that is not one of this
// workspace's live agents, or a folder it has not reported in the needed mode.
// Returns nothing when the repository has no AgentStore (narrow test mocks).
func (h *Handler) checkRemoteAgentNodes(ctx context.Context, tenantID string, nodes []*Node) []string {
	store, ok := h.agentStore()
	if !ok {
		return nil
	}
	var problems []string
	for _, n := range nodes {
		var cfg struct {
			Type        string `json:"type"`
			RemoteAgent struct {
				AgentID   string `json:"agent_id"`
				Directory string `json:"directory"`
			} `json:"remote_agent"`
		}
		if json.Unmarshal(n.Config, &cfg) != nil || cfg.Type != "remote_agent" {
			continue
		}
		mode := "write"
		if n.Type == "consumer" {
			mode = "read"
		}
		agent, err := store.GetAgent(ctx, tenantID, strings.TrimSpace(cfg.RemoteAgent.AgentID))
		if err != nil || agent.RevokedAt != nil {
			problems = append(problems, fmt.Sprintf(
				"node %s: the chosen remote agent is not registered in this workspace, or has been revoked", n.ID))
			continue
		}
		found := false
		for _, d := range agent.Directories {
			if d.Name == cfg.RemoteAgent.Directory {
				found = true
				if d.Mode != mode {
					problems = append(problems, fmt.Sprintf(
						"node %s: folder %q on %s is %s-only", n.ID, d.Name, agent.Name, d.Mode))
				}
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf(
				"node %s: %s has no folder named %q", n.ID, agent.Name, cfg.RemoteAgent.Directory))
		}
	}
	return problems
}
