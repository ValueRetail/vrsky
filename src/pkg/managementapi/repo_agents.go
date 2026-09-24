package managementapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/auth"
)

// Remote agents (#266). An agent is a small binary on a customer machine that
// dials out to VRSky and makes that machine usable as a pipeline input or
// output. This file is the management side: minting one-time registration
// tokens, and listing / renaming / revoking the agents they produced. The
// agent-facing side — consuming a token, authenticating with the credential —
// lives in the remote-agent connector service.
//
// Accessed through the narrow AgentStore interface, type-asserted from h.repo
// (the InviteStore pattern), so the broad Repository interface and its mocks
// stay untouched.

// AgentRegistrationTokenTTL is how long a freshly minted registration token can
// be used. Short, because it is a bearer capability that creates a credential
// in the tenant: long enough to copy onto a machine and run one command.
const AgentRegistrationTokenTTL = 1 * time.Hour

// AgentRegistrationTokenPrefix marks a registration token so one pasted into
// the wrong place is recognisable, and cannot be mistaken for the long-lived
// agent credential ("vrsky_agent_…") it is exchanged for.
const AgentRegistrationTokenPrefix = "vrsky_reg_"

// AgentOnlineWindow is how recently an agent must have been heard from to count
// as online. The agent long-polls every ≤25 s and the gateway throttles its
// last_seen_at write to once per 30 s, so 90 s tolerates one missed write plus
// a slow poll without flapping.
const AgentOnlineWindow = 90 * time.Second

// ErrAgentNotFound is returned when no agent matches within the tenant —
// including when the agent exists but belongs to another tenant, which callers
// must not be able to tell apart.
var ErrAgentNotFound = errors.New("agent not found")

// ErrAgentNameTaken is returned when a live (unrevoked) agent in the tenant
// already has the requested name.
var ErrAgentNameTaken = errors.New("an agent with that name already exists")

// AgentDirectory is one directory an agent reported. Name and mode only: the
// path is defined in the agent's own config file and never leaves the machine.
type AgentDirectory struct {
	Name string `json:"name"`
	Mode string `json:"mode"` // read | write
}

// Agent mirrors one row of agents, minus the credential hash.
type Agent struct {
	ID           string           `json:"id"`
	TenantID     string           `json:"tenant_id"`
	Name         string           `json:"name"`
	Hostname     string           `json:"hostname"`
	OS           string           `json:"os"`
	Arch         string           `json:"arch"`
	AgentVersion string           `json:"agent_version"`
	Directories  []AgentDirectory `json:"directories"`
	LastSeenAt   *time.Time       `json:"last_seen_at,omitempty"`
	Online       bool             `json:"online"`
	RegisteredAt time.Time        `json:"registered_at"`
	RevokedAt    *time.Time       `json:"revoked_at,omitempty"`
}

// AgentRegistrationToken is a freshly minted token. Token carries the raw value
// and is populated only on the create response; it is never stored or listed.
type AgentRegistrationToken struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Token         string    `json:"token,omitempty"`
	SuggestedName string    `json:"suggested_name,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// AgentStore is the narrow persistence surface the agent handlers need.
// Every method takes the tenant and scopes by it; there is deliberately no
// method that finds an agent without one.
type AgentStore interface {
	CreateAgentRegistrationToken(ctx context.Context, tenantID, suggestedName, createdBy string) (*AgentRegistrationToken, error)
	ListAgents(ctx context.Context, tenantID string) ([]*Agent, error)
	GetAgent(ctx context.Context, tenantID, agentID string) (*Agent, error)
	RenameAgent(ctx context.Context, tenantID, agentID, name string) (*Agent, error)
	RevokeAgent(ctx context.Context, tenantID, agentID string) error
}

func newAgentRegistrationToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return AgentRegistrationTokenPrefix + hex.EncodeToString(b), nil
}

// CreateAgentRegistrationToken stores the hash of a new one-time token and
// returns the raw value, which is not recoverable afterwards.
func (r *PostgresRepository) CreateAgentRegistrationToken(ctx context.Context, tenantID, suggestedName, createdBy string) (*AgentRegistrationToken, error) {
	raw, err := newAgentRegistrationToken()
	if err != nil {
		return nil, err
	}
	var createdByArg, suggestedArg any
	if strings.TrimSpace(createdBy) != "" {
		createdByArg = createdBy
	}
	if s := strings.TrimSpace(suggestedName); s != "" {
		suggestedArg = s
	}
	tok := &AgentRegistrationToken{Token: raw}
	var suggested sql.NullString
	// lint:tenant-ok — INSERT carries tenant_id in the row.
	err = r.db.QueryRowContext(ctx, `
		INSERT INTO agent_registration_tokens (tenant_id, token_hash, suggested_name, created_by, expires_at)
		VALUES ($1, $2, $3, $4, NOW() + ($5 || ' seconds')::interval)
		RETURNING id, tenant_id::text, suggested_name, expires_at
	`, tenantID, auth.HashToken(raw), suggestedArg, createdByArg,
		fmt.Sprintf("%d", int(AgentRegistrationTokenTTL.Seconds()))).Scan(
		&tok.ID, &tok.TenantID, &suggested, &tok.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	tok.SuggestedName = suggested.String
	return tok, nil
}

type rowScanner interface{ Scan(dest ...any) error }

// scanAgent reads the column list both agent reads select, in order. The list
// is spelled out in each query rather than shared through a constant: the
// tenant-filter linter reads only the first string literal of a query, so a
// concatenated SELECT would hide these reads from it. "online" is computed in
// SQL so it uses the database's clock, not each replica's.
func scanAgent(row rowScanner) (*Agent, error) {
	a := &Agent{}
	var dirs []byte
	if err := row.Scan(&a.ID, &a.TenantID, &a.Name, &a.Hostname, &a.OS, &a.Arch,
		&a.AgentVersion, &dirs, &a.LastSeenAt, &a.RegisteredAt, &a.RevokedAt, &a.Online); err != nil {
		return nil, err
	}
	a.Directories = []AgentDirectory{}
	if len(dirs) > 0 {
		if err := json.Unmarshal(dirs, &a.Directories); err != nil {
			return nil, fmt.Errorf("decode directories for agent %s: %w", a.ID, err)
		}
	}
	return a, nil
}

func onlineWindowArg() string { return fmt.Sprintf("%d", int(AgentOnlineWindow.Seconds())) }

// ListAgents returns every agent in the tenant, revoked ones included (the UI
// shows them greyed out), live ones first.
func (r *PostgresRepository) ListAgents(ctx context.Context, tenantID string) ([]*Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id::text, name, hostname, os, arch, agent_version, directories,
		       last_seen_at, registered_at, revoked_at,
		       (revoked_at IS NULL AND last_seen_at IS NOT NULL
		        AND last_seen_at > NOW() - ($2 || ' seconds')::interval) AS online
		FROM agents
		WHERE tenant_id = $1
		ORDER BY (revoked_at IS NOT NULL), lower(name)
	`, tenantID, onlineWindowArg())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgent fetches one agent scoped to its tenant.
func (r *PostgresRepository) GetAgent(ctx context.Context, tenantID, agentID string) (*Agent, error) {
	a, err := scanAgent(r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id::text, name, hostname, os, arch, agent_version, directories,
		       last_seen_at, registered_at, revoked_at,
		       (revoked_at IS NULL AND last_seen_at IS NOT NULL
		        AND last_seen_at > NOW() - ($2 || ' seconds')::interval) AS online
		FROM agents
		WHERE tenant_id = $1 AND id::text = $3
	`, tenantID, onlineWindowArg(), agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAgentNotFound
	}
	return a, err
}

// RenameAgent renames a live agent. A revoked agent cannot be renamed — its
// name was released and may already belong to its replacement.
func (r *PostgresRepository) RenameAgent(ctx context.Context, tenantID, agentID, name string) (*Agent, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agents SET name = $3
		WHERE tenant_id = $1 AND id::text = $2 AND revoked_at IS NULL
	`, tenantID, agentID, name)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAgentNameTaken
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrAgentNotFound
	}
	return r.GetAgent(ctx, tenantID, agentID)
}

// RevokeAgent revokes a live agent. Its next request to the gateway is refused,
// and its name is released. Revoking twice reports ErrAgentNotFound, so the
// caller sees that the second call changed nothing.
func (r *PostgresRepository) RevokeAgent(ctx context.Context, tenantID, agentID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agents SET revoked_at = NOW()
		WHERE tenant_id = $1 AND id::text = $2 AND revoked_at IS NULL
	`, tenantID, agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentNotFound
	}
	return nil
}
