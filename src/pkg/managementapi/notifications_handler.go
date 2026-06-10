package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

// Phase 3A (#84): notification-target CRUD + the Alertmanager webhook receiver.
// Layout mirrors oauth_admin_handler.go — tenant-from-context extraction,
// audit calls, writeError/writeJSON. Routing model: Alertmanager has ONE
// webhook receiver pointing at POST /api/v1/alerts/webhook; this handler fans
// each alert out to the owning tenant's targets (tenant_id label) or to
// platform-flagged targets (no tenant_id label).

// notificationTargetRequest is the JSON shape of a create / update request.
type notificationTargetRequest struct {
	Name        string `json:"name"`
	Type        string `json:"type"`             // slack | email | pagerduty | webhook
	Secret      string `json:"secret,omitempty"` // Slack webhook URL / PD routing key / webhook HMAC key; PUT may omit
	Email       string `json:"email,omitempty"`
	URL         string `json:"url,omitempty"`
	Platform    bool   `json:"platform,omitempty"`
	MinSeverity string `json:"min_severity,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"` // nil -> true on create / unchanged on update
}

// notificationTargetResponse is the safe (secret-less) shape returned to clients.
type notificationTargetResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Email       string    `json:"email,omitempty"`
	URL         string    `json:"url,omitempty"`
	Platform    bool      `json:"platform"`
	MinSeverity string    `json:"min_severity,omitempty"`
	Enabled     bool      `json:"enabled"`
	HasSecret   bool      `json:"has_secret"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toNotificationTargetResponse(t *NotificationTarget) notificationTargetResponse {
	return notificationTargetResponse{
		ID:          t.ID,
		Name:        t.Name,
		Type:        t.Type,
		Email:       t.Config.Email,
		URL:         t.Config.URL,
		Platform:    t.Config.Platform,
		MinSeverity: t.Config.MinSeverity,
		Enabled:     t.Enabled,
		HasSecret:   t.SecretID != "",
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

// validateTargetRequest enforces the per-type required fields. create governs
// whether the secret itself is mandatory (PUT may omit it to keep the old one).
func validateTargetRequest(req *notificationTargetRequest, create bool) error {
	if req.Name == "" {
		return errors.New("name is required")
	}
	switch req.Type {
	case "slack":
		if create && req.Secret == "" {
			return errors.New("slack targets need the incoming-webhook URL (secret)")
		}
	case "email":
		if req.Email == "" || !strings.Contains(req.Email, "@") {
			return errors.New("email targets need a valid recipient address")
		}
	case "pagerduty":
		if create && req.Secret == "" {
			return errors.New("pagerduty targets need the Events API routing key (secret)")
		}
	case "webhook":
		if req.URL == "" || !strings.HasPrefix(req.URL, "http") {
			return errors.New("webhook targets need a destination URL")
		}
	default:
		return fmt.Errorf("unknown target type %q (slack | email | pagerduty | webhook)", req.Type)
	}
	switch req.MinSeverity {
	case "", "info", "warning", "critical":
	default:
		return fmt.Errorf("invalid min_severity %q (info | warning | critical)", req.MinSeverity)
	}
	return nil
}

func targetFromRequest(tenantID string, req *notificationTargetRequest) *NotificationTarget {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return &NotificationTarget{
		TenantID: tenantID,
		Name:     req.Name,
		Type:     req.Type,
		Enabled:  enabled,
		Config: NotificationTargetConfig{
			Email:       req.Email,
			URL:         req.URL,
			Platform:    req.Platform,
			MinSeverity: req.MinSeverity,
		},
	}
}

// ListNotificationTargets handles GET /api/v1/notifications/targets.
func (h *Handler) ListNotificationTargets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	targets, err := h.repo.ListNotificationTargets(ctx, tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to list targets", nil)
		return
	}
	out := make([]notificationTargetResponse, 0, len(targets))
	for _, t := range targets {
		out = append(out, toNotificationTargetResponse(t))
	}
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"targets": out})
}

// CreateNotificationTarget handles POST /api/v1/notifications/targets.
func (h *Handler) CreateNotificationTarget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	var req notificationTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	if err := validateTargetRequest(&req, true); err != nil {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		return
	}
	t := targetFromRequest(tenantID, &req)
	if err := h.repo.CreateNotificationTarget(ctx, t, req.Secret); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to create target", nil)
		return
	}
	SetAuditAction(ctx, "notification.target.create")
	SetAuditResource(ctx, "notification_target", t.ID)
	SetAuditDetail(ctx, "type", t.Type)
	SetAuditDetail(ctx, "name", t.Name)
	_ = writeJSON(w, http.StatusCreated, toNotificationTargetResponse(t))
}

// UpdateNotificationTarget handles PUT /api/v1/notifications/targets/{id}.
func (h *Handler) UpdateNotificationTarget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	id := r.PathValue("id")
	existing, err := h.repo.GetNotificationTarget(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, ErrNotificationTargetNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "target not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to load target", nil)
		return
	}
	var req notificationTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}
	if err := validateTargetRequest(&req, false); err != nil {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", err.Error(), nil)
		return
	}
	t := targetFromRequest(tenantID, &req)
	t.ID = existing.ID
	t.SecretID = existing.SecretID // kept unless a new secret re-encrypts below
	if req.Enabled == nil {
		t.Enabled = existing.Enabled
	}
	if err := h.repo.UpdateNotificationTarget(ctx, t, req.Secret); err != nil {
		if errors.Is(err, ErrNotificationTargetNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "target not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to update target", nil)
		return
	}
	SetAuditAction(ctx, "notification.target.update")
	SetAuditResource(ctx, "notification_target", t.ID)
	_ = writeJSON(w, http.StatusOK, toNotificationTargetResponse(t))
}

// DeleteNotificationTarget handles DELETE /api/v1/notifications/targets/{id}.
func (h *Handler) DeleteNotificationTarget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	id := r.PathValue("id")
	if err := h.repo.DeleteNotificationTarget(ctx, tenantID, id); err != nil {
		if errors.Is(err, ErrNotificationTargetNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "target not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to delete target", nil)
		return
	}
	SetAuditAction(ctx, "notification.target.delete")
	SetAuditResource(ctx, "notification_target", id)
	w.WriteHeader(http.StatusNoContent)
}

// TestNotificationTarget handles POST /api/v1/notifications/targets/{id}/test —
// sends a synthetic alert through the target so the user can confirm delivery.
func (h *Handler) TestNotificationTarget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}
	t, err := h.repo.GetNotificationTarget(ctx, tenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotificationTargetNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "target not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "InternalError", "failed to load target", nil)
		return
	}
	n, err := h.buildNotifier(ctx, t)
	if err != nil {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	alert := &notify.Alert{
		Name:        "TestNotification",
		Status:      "firing",
		Severity:    "info",
		Summary:     "test notification from VRSky",
		Description: fmt.Sprintf("Target %q (%s) is wired up correctly.", t.Name, t.Type),
		TenantID:    tenantID,
		StartsAt:    time.Now().UTC(),
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := n.Send(sctx, alert); err != nil {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	SetAuditAction(ctx, "notification.target.test")
	SetAuditResource(ctx, "notification_target", t.ID)
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// --- Alertmanager webhook receiver + dispatch ---

// amWebhookPayload is the Alertmanager webhook_config payload (version "4").
type amWebhookPayload struct {
	Status string `json:"status"`
	Alerts []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    time.Time         `json:"startsAt"`
	} `json:"alerts"`
}

// AlertsWebhook handles POST /api/v1/alerts/webhook — the single Alertmanager
// receiver. Authenticated by the ALERTS_WEBHOOK_TOKEN bearer token (the route
// is exempt from tenant middleware since Alertmanager can't send X-Tenant-ID).
func (h *Handler) AlertsWebhook(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("ALERTS_WEBHOOK_TOKEN")
	if token == "" {
		_ = writeError(w, http.StatusServiceUnavailable, "NotConfigured", "ALERTS_WEBHOOK_TOKEN is not set", nil)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		_ = writeError(w, http.StatusUnauthorized, "Unauthorized", "invalid alerts webhook token", nil)
		return
	}
	var payload amWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", err.Error(), nil)
		return
	}

	ctx := r.Context()
	delivered, failed := 0, 0
	for _, a := range payload.Alerts {
		alert := &notify.Alert{
			Name:        a.Labels["alertname"],
			Status:      a.Status,
			Severity:    a.Labels["severity"],
			Summary:     a.Annotations["summary"],
			Description: a.Annotations["description"],
			TenantID:    a.Labels["tenant_id"],
			Labels:      a.Labels,
			StartsAt:    a.StartsAt,
		}
		d, f := h.dispatchAlert(ctx, alert)
		delivered += d
		failed += f
	}
	_ = writeJSON(w, http.StatusOK, map[string]interface{}{"delivered": delivered, "failed": failed})
}

// severityRank orders severities for min_severity filtering.
func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	default: // info / ""
		return 1
	}
}

// dispatchAlert fans one alert out to its targets (the tenant's, or platform
// ones for tenant-less alerts), honouring per-target min_severity. Send errors
// are logged and counted, never fatal — one broken webhook must not block the
// other targets.
func (h *Handler) dispatchAlert(ctx context.Context, alert *notify.Alert) (delivered, failed int) {
	targets, err := h.repo.ListNotificationTargetsForDispatch(ctx, alert.TenantID)
	if err != nil {
		slog.Error("alerts: list dispatch targets", "error", err, "alert", alert.Name)
		return 0, 0
	}
	for _, t := range targets {
		if t.Config.MinSeverity != "" && severityRank(alert.Severity) < severityRank(t.Config.MinSeverity) {
			continue
		}
		n, err := h.buildNotifier(ctx, t)
		if err != nil {
			slog.Error("alerts: build notifier", "target", t.Name, "type", t.Type, "error", err)
			failed++
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = n.Send(sctx, alert)
		cancel()
		if err != nil {
			slog.Error("alerts: send failed", "target", t.Name, "type", t.Type, "error", err)
			failed++
			continue
		}
		delivered++
	}
	return delivered, failed
}

// buildNotifier constructs the pkg/notify sender for a target, resolving its
// encrypted secret. Email uses the platform SMTP_* env identity.
func (h *Handler) buildNotifier(ctx context.Context, t *NotificationTarget) (notify.Notifier, error) {
	secret, err := h.repo.ResolveNotificationSecret(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("resolve secret: %w", err)
	}
	switch t.Type {
	case "slack":
		return &notify.Slack{WebhookURL: secret}, nil
	case "email":
		return &notify.Email{SMTP: smtpFromEnv(), To: t.Config.Email}, nil
	case "pagerduty":
		return &notify.PagerDuty{RoutingKey: secret}, nil
	case "webhook":
		return &notify.Webhook{URL: t.Config.URL, Secret: secret}, nil
	default:
		return nil, fmt.Errorf("unknown target type %q", t.Type)
	}
}

// smtpFromEnv reads the platform mail identity (compose/k8s env).
func smtpFromEnv() notify.SMTPConfig {
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	return notify.SMTPConfig{
		Host:     os.Getenv("SMTP_HOST"),
		Port:     port,
		From:     os.Getenv("SMTP_FROM"),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
	}
}
