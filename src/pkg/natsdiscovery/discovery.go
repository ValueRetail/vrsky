// Package natsdiscovery lets a worker resolve the set of NATS instances for its
// tenant from the management-api service-discovery endpoint (#21), instead of
// relying on a single hardcoded NATS_URL. The returned comma-separated server
// list is handed to nats.Connect, which load-balances and fails over across all
// servers and reconnects automatically.
package natsdiscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Resolver fetches a tenant's NATS instance URLs from the management-api.
type Resolver struct {
	BaseURL    string // management-api base, e.g. http://management-api:3000
	TenantID   string
	AuthToken  string // optional service token (Bearer)
	HTTPClient *http.Client
}

type discoveryResponse struct {
	Data struct {
		URLs []string `json:"urls"`
	} `json:"data"`
}

// New builds a Resolver. baseURL and tenantID are required for discovery to be
// attempted; an empty baseURL disables it (caller falls back to NATS_URL).
func New(baseURL, tenantID, authToken string) *Resolver {
	return &Resolver{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		TenantID:   tenantID,
		AuthToken:  authToken,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Enabled reports whether discovery is configured.
func (r *Resolver) Enabled() bool {
	return r.BaseURL != "" && r.TenantID != ""
}

// Resolve returns the tenant's active NATS server URLs. Returns an empty slice
// (no error) when discovery is disabled, so callers can fall back to NATS_URL.
func (r *Resolver) Resolve(ctx context.Context) ([]string, error) {
	if !r.Enabled() {
		return nil, nil
	}
	url := fmt.Sprintf("%s/api/v1/tenants/%s/nats-instances", r.BaseURL, r.TenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if r.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+r.AuthToken)
	}
	req.Header.Set("X-Tenant-ID", r.TenantID)
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: management-api returned %d", resp.StatusCode)
	}
	var dr discoveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return nil, err
	}
	return dr.Data.URLs, nil
}

// ResolveJoined returns the discovered URLs joined for nats.Connect (comma-
// separated). Empty string when discovery is disabled or finds no instances.
func (r *Resolver) ResolveJoined(ctx context.Context) (string, error) {
	urls, err := r.Resolve(ctx)
	if err != nil {
		return "", err
	}
	return strings.Join(urls, ","), nil
}
