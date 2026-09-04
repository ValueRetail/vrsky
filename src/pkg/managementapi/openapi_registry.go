package managementapi

import "reflect"

// OpenAPI route registry (#94). This is the single source of truth for the
// generated /openapi.json: one entry per documented endpoint. The custom
// `cmd/lint-openapi` linter parses handler.go's mux registrations and fails CI
// if any registered route's pattern is absent here — so adding an endpoint
// without documenting it breaks the build.
//
// `Pattern` is the exact string passed to mux.Handle/HandleFunc in
// RegisterRoutes (the lint match key). Method+Path are the OpenAPI coordinates
// (a method-dispatch pattern like "/api/v1/secrets" yields several entries that
// share one Pattern). Request/Response are Go DTOs whose JSON-Schema is
// reflected into components/schemas; nil means an unspecified body.

type apiRoute struct {
	Pattern  string
	Method   string
	Path     string
	Tag      string
	Summary  string
	Request  reflect.Type
	Response reflect.Type
}

func tof(v any) reflect.Type { return reflect.TypeOf(v) }

var apiRoutes = []apiRoute{
	// --- Connections ---
	{"POST /api/v1/connections", "post", "/api/v1/connections", "Connections", "Create a connection", tof(CreateConnectionRequest{}), tof(Connection{})},
	{"GET /api/v1/connections", "get", "/api/v1/connections", "Connections", "List connections", nil, tof(ListResponse{})},
	{"GET /api/v1/connections/{id}", "get", "/api/v1/connections/{id}", "Connections", "Get a connection", nil, tof(Connection{})},
	{"PUT /api/v1/connections/{id}", "put", "/api/v1/connections/{id}", "Connections", "Update a connection", tof(CreateConnectionRequest{}), tof(Connection{})},
	{"DELETE /api/v1/connections/{id}", "delete", "/api/v1/connections/{id}", "Connections", "Delete a connection", nil, nil},
	{"POST /api/v1/connections/{id}/start", "post", "/api/v1/connections/{id}/start", "Connections", "Start (deploy) a connection", nil, tof(Connection{})},
	{"POST /api/v1/connections/{id}/stop", "post", "/api/v1/connections/{id}/stop", "Connections", "Stop a connection", nil, tof(Connection{})},
	{"POST /api/v1/connections/test", "post", "/api/v1/connections/test", "Connections", "Test a connector config without deploying", nil, nil},

	// --- Metrics & sample data ---
	{"GET /api/v1/connections/{id}/metrics", "get", "/api/v1/connections/{id}/metrics", "Metrics", "Point-in-time pipeline metrics for a connection (from Prometheus)", nil, nil},
	{"GET /api/v1/connections/{id}/metrics/stream", "get", "/api/v1/connections/{id}/metrics/stream", "Metrics", "Server-sent stream of live pipeline metrics", nil, nil},
	{"GET /api/v1/connections/{id}/metrics/ws", "get", "/api/v1/connections/{id}/metrics/ws", "Metrics", "WebSocket stream of live pipeline metrics", nil, nil},
	{"GET /api/v1/connections/{id}/sample-data", "get", "/api/v1/connections/{id}/sample-data", "Metrics", "Last payload seen by a deployed connection", nil, nil},
	{"GET /api/v1/sample-data/source", "get", "/api/v1/sample-data/source", "Metrics", "Fetch sample data from a source config", nil, nil},

	// --- Test data generation ---
	{"POST /api/v1/connections/{id}/test-message", "post", "/api/v1/connections/{id}/test-message", "Test data", "Inject a single test message", nil, nil},
	{"POST /api/v1/connections/{id}/auto-generator/start", "post", "/api/v1/connections/{id}/auto-generator/start", "Test data", "Start the synthetic message generator", nil, nil},
	{"POST /api/v1/connections/{id}/auto-generator/stop", "post", "/api/v1/connections/{id}/auto-generator/stop", "Test data", "Stop the synthetic message generator", nil, nil},
	{"GET /api/v1/connections/{id}/auto-generator/status", "get", "/api/v1/connections/{id}/auto-generator/status", "Test data", "Generator status", nil, nil},

	// --- Secrets (method-dispatch patterns) ---
	{"/api/v1/secrets", "get", "/api/v1/secrets", "Secrets", "List secrets (metadata only)", nil, tof(SuccessResponse{})},
	{"/api/v1/secrets", "post", "/api/v1/secrets", "Secrets", "Create an encrypted secret", tof(CreateSecretRequest{}), tof(Secret{})},
	{"/api/v1/secrets/", "get", "/api/v1/secrets/{id}", "Secrets", "Get secret metadata", nil, tof(Secret{})},
	{"/api/v1/secrets/", "put", "/api/v1/secrets/{id}", "Secrets", "Update a secret", nil, tof(Secret{})},
	{"/api/v1/secrets/", "post", "/api/v1/secrets/{id}/rotate", "Secrets", "Rotate a secret", nil, tof(Secret{})},
	{"/api/v1/secrets/", "delete", "/api/v1/secrets/{id}", "Secrets", "Delete a secret", nil, nil},

	// --- Dead-letter queue (DLQRouter dispatches by method/path) ---
	{"GET /api/v1/connections/{id}/dlq", "get", "/api/v1/connections/{id}/dlq", "DLQ", "List dead-lettered messages", nil, nil},
	{"GET /api/v1/connections/{id}/dlq/{seq}", "get", "/api/v1/connections/{id}/dlq/{seq}", "DLQ", "Get a dead-lettered message", nil, nil},
	{"POST /api/v1/connections/{id}/dlq/{seq}/retry", "post", "/api/v1/connections/{id}/dlq/{seq}/retry", "DLQ", "Re-deliver a dead-lettered message", nil, nil},
	{"POST /api/v1/connections/{id}/dlq/{seq}/discard", "post", "/api/v1/connections/{id}/dlq/{seq}/discard", "DLQ", "Discard a dead-lettered message", nil, nil},

	// --- Audit ---
	{"GET /api/v1/audit", "get", "/api/v1/audit", "Audit", "List audit log entries (jsonl export with ?format=jsonl)", nil, tof(AuditEntry{})},

	// --- OAuth 2.0 ---
	{"GET /api/v1/oauth/providers", "get", "/api/v1/oauth/providers", "OAuth", "List OAuth providers", nil, nil},
	{"POST /api/v1/oauth/providers", "post", "/api/v1/oauth/providers", "OAuth", "Create an OAuth provider", nil, nil},
	{"GET /api/v1/oauth/providers/{id}", "get", "/api/v1/oauth/providers/{id}", "OAuth", "Get an OAuth provider", nil, nil},
	{"PUT /api/v1/oauth/providers/{id}", "put", "/api/v1/oauth/providers/{id}", "OAuth", "Update an OAuth provider", nil, nil},
	{"DELETE /api/v1/oauth/providers/{id}", "delete", "/api/v1/oauth/providers/{id}", "OAuth", "Delete an OAuth provider", nil, nil},
	{"POST /api/v1/oauth/providers/{id}/start", "post", "/api/v1/oauth/providers/{id}/start", "OAuth", "Begin the authorization-code flow", nil, nil},
	{"GET /api/v1/oauth/callback", "get", "/api/v1/oauth/callback", "OAuth", "OAuth redirect callback", nil, nil},
	{"GET /api/v1/oauth/grants", "get", "/api/v1/oauth/grants", "OAuth", "List OAuth grants", nil, nil},
	{"GET /api/v1/oauth/grants/{id}", "get", "/api/v1/oauth/grants/{id}", "OAuth", "Get an OAuth grant", nil, nil},
	{"POST /api/v1/oauth/grants/{id}/revoke", "post", "/api/v1/oauth/grants/{id}/revoke", "OAuth", "Revoke an OAuth grant", nil, nil},
	{"GET /api/v1/oauth/grants/{id}/token", "get", "/api/v1/oauth/grants/{id}/token", "OAuth", "Resolve a grant's access token (service-token auth)", nil, nil},

	// --- Notifications ---
	{"GET /api/v1/notifications/targets", "get", "/api/v1/notifications/targets", "Notifications", "List notification targets", nil, tof(NotificationTarget{})},
	{"POST /api/v1/notifications/targets", "post", "/api/v1/notifications/targets", "Notifications", "Create a notification target", tof(NotificationTarget{}), tof(NotificationTarget{})},
	{"PUT /api/v1/notifications/targets/{id}", "put", "/api/v1/notifications/targets/{id}", "Notifications", "Update a notification target", tof(NotificationTarget{}), tof(NotificationTarget{})},
	{"DELETE /api/v1/notifications/targets/{id}", "delete", "/api/v1/notifications/targets/{id}", "Notifications", "Delete a notification target", nil, nil},
	{"POST /api/v1/notifications/targets/{id}/test", "post", "/api/v1/notifications/targets/{id}/test", "Notifications", "Send a test notification", nil, nil},
	{"POST /api/v1/alerts/webhook", "post", "/api/v1/alerts/webhook", "Notifications", "Alertmanager webhook receiver (per-tenant dispatch)", nil, nil},

	// --- Auth ---
	{"POST /api/v1/auth/register", "post", "/api/v1/auth/register", "Auth", "Register a user + first workspace", nil, nil},
	{"POST /api/v1/auth/login", "post", "/api/v1/auth/login", "Auth", "Log in (sets the session cookie)", nil, nil},
	{"GET /api/v1/auth/verify-email", "get", "/api/v1/auth/verify-email", "Auth", "Verify an email address", nil, nil},
	{"POST /api/v1/auth/forgot-password", "post", "/api/v1/auth/forgot-password", "Auth", "Request a password reset", nil, nil},
	{"POST /api/v1/auth/reset-password", "post", "/api/v1/auth/reset-password", "Auth", "Reset a password with a token", nil, nil},
	{"GET /api/v1/auth/me", "get", "/api/v1/auth/me", "Auth", "Current user", nil, nil},
	{"POST /api/v1/auth/logout", "post", "/api/v1/auth/logout", "Auth", "Log out", nil, nil},
	{"POST /api/v1/auth/change-password", "post", "/api/v1/auth/change-password", "Auth", "Change the current user's password", nil, nil},
	{"DELETE /api/v1/auth/me", "delete", "/api/v1/auth/me", "Auth", "Delete the current account", nil, nil},

	// --- OIDC / SSO ---
	{"GET /api/v1/auth/oidc/{slug}/available", "get", "/api/v1/auth/oidc/{slug}/available", "Auth", "Whether SSO is configured for a workspace slug", nil, nil},
	{"GET /api/v1/auth/oidc/{slug}/login", "get", "/api/v1/auth/oidc/{slug}/login", "Auth", "Begin the OIDC login flow", nil, nil},
	{"GET /api/v1/auth/oidc/callback", "get", "/api/v1/auth/oidc/callback", "Auth", "OIDC redirect callback", nil, nil},

	// --- Tenants ---
	{"POST /api/v1/tenants", "post", "/api/v1/tenants", "Tenants", "Create a workspace", nil, tof(Tenant{})},
	{"GET /api/v1/tenants", "get", "/api/v1/tenants", "Tenants", "List the caller's workspaces", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}", "get", "/api/v1/tenants/{tenant_id}", "Tenants", "Get a workspace", nil, tof(Tenant{})},
	{"DELETE /api/v1/tenants/{tenant_id}", "delete", "/api/v1/tenants/{tenant_id}", "Tenants", "Delete a workspace (owner only)", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/oidc", "get", "/api/v1/tenants/{tenant_id}/oidc", "Tenants", "Read the workspace OIDC config", nil, nil},
	{"PUT /api/v1/tenants/{tenant_id}/oidc", "put", "/api/v1/tenants/{tenant_id}/oidc", "Tenants", "Upsert the workspace OIDC config", nil, nil},
	{"DELETE /api/v1/tenants/{tenant_id}/oidc", "delete", "/api/v1/tenants/{tenant_id}/oidc", "Tenants", "Delete the workspace OIDC config", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/members", "get", "/api/v1/tenants/{tenant_id}/members", "Tenants", "List workspace members", nil, tof(TenantMember{})},
	{"POST /api/v1/tenants/{tenant_id}/members", "post", "/api/v1/tenants/{tenant_id}/members", "Tenants", "Add a registered user to the workspace by email (owner only)", tof(addMemberRequest{}), tof(TenantMember{})},
	{"PUT /api/v1/tenants/{tenant_id}/members/{user_id}", "put", "/api/v1/tenants/{tenant_id}/members/{user_id}", "Tenants", "Set a member's role (owner only)", nil, nil},
	{"DELETE /api/v1/tenants/{tenant_id}/members/{user_id}", "delete", "/api/v1/tenants/{tenant_id}/members/{user_id}", "Tenants", "Remove a member (owner only)", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/invites", "get", "/api/v1/tenants/{tenant_id}/invites", "Tenants", "List pending member invites (owner only)", nil, tof(TenantInvite{})},
	{"POST /api/v1/tenants/{tenant_id}/invites", "post", "/api/v1/tenants/{tenant_id}/invites", "Tenants", "Invite a member by email; adds directly if already registered (owner only)", tof(createInviteRequest{}), tof(TenantInvite{})},
	{"POST /api/v1/tenants/{tenant_id}/invites/{invite_id}/resend", "post", "/api/v1/tenants/{tenant_id}/invites/{invite_id}/resend", "Tenants", "Resend an invite — new token + expiry (owner only)", nil, tof(TenantInvite{})},
	{"DELETE /api/v1/tenants/{tenant_id}/invites/{invite_id}", "delete", "/api/v1/tenants/{tenant_id}/invites/{invite_id}", "Tenants", "Revoke a pending invite (owner only)", nil, nil},
	{"POST /api/v1/invites/accept", "post", "/api/v1/invites/accept", "Tenants", "Accept a workspace invite for the signed-in user", tof(acceptInviteRequest{}), tof(TenantMember{})},
	{"GET /api/v1/tenants/{tenant_id}/nats-instances", "get", "/api/v1/tenants/{tenant_id}/nats-instances", "Tenants", "List the tenant's active NATS instances + client URLs (service discovery)", nil, tof(natsInstancesResponse{})},
	{"GET /api/v1/tenants/{tenant_id}/quotas", "get", "/api/v1/tenants/{tenant_id}/quotas", "Tenants", "Get workspace quotas + usage", nil, tof(TenantQuotas{})},
	{"PUT /api/v1/tenants/{tenant_id}/quotas", "put", "/api/v1/tenants/{tenant_id}/quotas", "Tenants", "Update workspace quotas (owner only)", tof(TenantQuotas{}), tof(TenantQuotas{})},
	{"GET /api/v1/tenants/{tenant_id}/usage", "get", "/api/v1/tenants/{tenant_id}/usage", "Usage", "Per-tenant usage (month totals + daily)", nil, tof(UsageResponse{})},
	{"GET /api/v1/tenants/{tenant_id}/usage/export", "get", "/api/v1/tenants/{tenant_id}/usage/export", "Usage", "Usage CSV export", nil, nil},
	{"PUT /api/v1/tenants/{tenant_id}/plan", "put", "/api/v1/tenants/{tenant_id}/plan", "Tenants", "Change the subscription plan (owner only)", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/status/stream", "get", "/api/v1/tenants/{tenant_id}/status/stream", "Tenants", "SSE stream of tenant NATS provisioning status", nil, nil},
	{"POST /api/v1/tenants/{tenant_id}/api-key/rotate", "post", "/api/v1/tenants/{tenant_id}/api-key/rotate", "Tenants", "Rotate the workspace API key (owner only)", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/api-key", "get", "/api/v1/tenants/{tenant_id}/api-key", "Tenants", "Get the workspace API key (admin only)", nil, nil},

	// --- Tenant-to-tenant data sharing ---
	{"POST /api/v1/tenants/{tenant_id}/connection-requests", "post", "/api/v1/tenants/{tenant_id}/connection-requests", "Data sharing", "Request a data-sharing connection", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/connection-requests/incoming", "get", "/api/v1/tenants/{tenant_id}/connection-requests/incoming", "Data sharing", "List incoming requests (admin)", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/connection-requests/outgoing", "get", "/api/v1/tenants/{tenant_id}/connection-requests/outgoing", "Data sharing", "List outgoing requests", nil, nil},
	{"POST /api/v1/tenants/{tenant_id}/connection-requests/{request_id}/approve", "post", "/api/v1/tenants/{tenant_id}/connection-requests/{request_id}/approve", "Data sharing", "Approve a request (owner)", nil, nil},
	{"POST /api/v1/tenants/{tenant_id}/connection-requests/{request_id}/deny", "post", "/api/v1/tenants/{tenant_id}/connection-requests/{request_id}/deny", "Data sharing", "Deny a request (owner)", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/data-connections", "get", "/api/v1/tenants/{tenant_id}/data-connections", "Data sharing", "List active data-sharing connections", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/data-connections/{connection_id}", "get", "/api/v1/tenants/{tenant_id}/data-connections/{connection_id}", "Data sharing", "Get a data-sharing connection", nil, nil},
	{"POST /api/v1/tenants/{tenant_id}/data-connections/{connection_id}/revoke", "post", "/api/v1/tenants/{tenant_id}/data-connections/{connection_id}/revoke", "Data sharing", "Revoke a data-sharing connection (owner)", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/data-connections/{connection_id}/shared-connections", "get", "/api/v1/tenants/{tenant_id}/data-connections/{connection_id}/shared-connections", "Data sharing", "List the connections shared over a link", nil, nil},
	{"GET /api/v1/tenants/{tenant_id}/data-access-log", "get", "/api/v1/tenants/{tenant_id}/data-access-log", "Data sharing", "Data-access audit log (admin)", nil, nil},
	{"POST /api/v1/tenant/{tenant_id}/data", "post", "/api/v1/tenant/{tenant_id}/data", "Data sharing", "Ingest data into a tenant (API-key auth)", nil, nil},

	// --- Status (public; #95) ---
	{"GET /status.json", "get", "/status.json", "Status", "Public platform status + uptime (Prometheus-driven)", nil, tof(StatusResponse{})},

	// --- API consumers (registered by RegisterAPIConsumerRoutes) ---
}
