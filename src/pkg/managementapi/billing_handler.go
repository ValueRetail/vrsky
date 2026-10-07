package managementapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

// The platform operator (plans/paid-plans.md) is whoever's email is in
// PLATFORM_OPERATORS. It is a role above every workspace, not a workspace
// role: it exists only to set plans and quotas and to see the list of
// workspaces. Nothing a tenant owner can do grants it.

// SetPlatformOperators replaces the operator list (lower-cased, trimmed).
func (h *Handler) SetPlatformOperators(emails []string) {
	ops := map[string]bool{}
	for _, e := range emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			ops[e] = true
		}
	}
	h.platformOperators = ops
}

// PlatformOperatorsFromEnv reads PLATFORM_OPERATORS, a comma-separated list
// of user emails. Empty means nobody: the platform routes answer 403.
func PlatformOperatorsFromEnv() []string {
	return strings.Split(getenvTrim("PLATFORM_OPERATORS"), ",")
}

func (h *Handler) isOperator(u *User) bool {
	return u != nil && h.platformOperators[strings.ToLower(u.Email)]
}

// RequireOperator is middleware for the platform routes; after sessionMW.
func (h *Handler) RequireOperator() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !h.isOperator(GetUserFromContext(r.Context())) {
				_ = writeError(w, http.StatusForbidden, "Forbidden", "platform operators only", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- the customer's side ---

// HandleGetBilling: GET /api/v1/tenants/{tenant_id}/billing — any member.
func (h *Handler) HandleGetBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant := GetTenantFromContext(ctx)
	if tenant == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}
	b := TenantBilling{Plan: tenant.SubscriptionPlan, BillingStatus: tenant.BillingStatus, TrialEndsAt: tenant.TrialEndsAt}
	if limits, err := h.repo.GetPlanLimits(ctx, tenant.SubscriptionPlan); err == nil {
		b.Limits = limits
	}
	if plans, err := h.repo.ListPlanLimits(ctx); err == nil {
		b.Plans = plans
	}
	if open, err := h.repo.GetOpenPlanRequest(ctx, tenant.ID); err == nil {
		b.OpenRequest = open
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: b})
}

type planRequestBody struct {
	Plan    string `json:"plan"`
	Message string `json:"message"`
}

// HandleCreatePlanRequest: POST /api/v1/tenants/{tenant_id}/plan-requests — owner.
// One open request per workspace; asking again returns the open one. The
// operator hears about it through the platform notification targets.
func (h *Handler) HandleCreatePlanRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant := GetTenantFromContext(ctx)
	user := GetUserFromContext(ctx)
	if tenant == nil || user == nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body planRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		return
	}
	if body.Plan != PlanPaid && body.Plan != PlanEnterprise {
		_ = writeError(w, http.StatusBadRequest, "ValidationError", "plan must be paid or enterprise", nil)
		return
	}
	if open, err := h.repo.GetOpenPlanRequest(ctx, tenant.ID); err == nil && open != nil {
		_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: open})
		return
	}
	req := &PlanRequest{TenantID: tenant.ID, RequestedPlan: body.Plan, Message: clipRunes(body.Message, 2000), RequestedBy: &user.ID}
	if err := h.repo.CreatePlanRequest(ctx, req); err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to save the request", nil)
		return
	}
	SetAuditAction(ctx, "plan.request")
	SetAuditDetail(ctx, "plan", body.Plan)

	// To the operator, not the workspace: TenantID empty routes to the
	// platform-flagged targets. Severity warning so a Teams target set to
	// ignore informational alerts still shows it — this one needs a human.
	h.notifyPlatform(ctx, &notify.Alert{
		Name:     notify.PlanRequestedName,
		Status:   "firing",
		Severity: "warning",
		Summary:  fmt.Sprintf("%s asks for the %s plan", tenant.Name, body.Plan),
		Description: fmt.Sprintf("Workspace %q (%s), owner %s, currently %s/%s.\n%s\nActivate it under Platform → Workspaces.",
			tenant.Name, tenant.Slug, user.Email, tenant.SubscriptionPlan, tenant.BillingStatus, req.Message),
		Labels:   map[string]string{"workspace": tenant.Slug, "plan": body.Plan},
		StartsAt: time.Now().UTC(),
	})
	_ = writeJSON(w, http.StatusCreated, SuccessResponse{Data: req})
}

// --- the operator's side ---

// HandlePlatformListTenants: GET /api/v1/platform/tenants — operator.
func (h *Handler) HandlePlatformListTenants(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListTenantsForPlatform(r.Context())
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list workspaces", nil)
		return
	}
	if rows == nil {
		rows = []*PlatformTenant{}
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: rows})
}

// HandlePlatformListPlanRequests: GET /api/v1/platform/plan-requests — operator.
func (h *Handler) HandlePlatformListPlanRequests(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListOpenPlanRequests(r.Context())
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to list requests", nil)
		return
	}
	if rows == nil {
		rows = []*PlanRequest{}
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: rows})
}

// HandlePlatformSetPlan: PUT /api/v1/platform/tenants/{tenant_id}/plan — operator.
//
// paid and enterprise make the workspace paid; trial puts (or keeps) it on a
// clock, by default a fresh TrialLength from now. Either way a suspended
// workspace's pipelines are started again.
func (h *Handler) HandlePlatformSetPlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")
	user := GetUserFromContext(ctx)

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var change TenantPlanChange
	if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse request", nil)
		return
	}
	if _, err := h.repo.GetPlanLimits(ctx, change.Plan); err != nil {
		if errors.Is(err, ErrUnknownPlan) {
			_ = writeError(w, http.StatusBadRequest, "ValidationError", "unknown plan: "+change.Plan, nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to read plans", nil)
		return
	}
	before, err := h.repo.GetTenantByID(ctx, tenantID)
	if err != nil {
		_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
		return
	}
	status := BillingPaid
	if change.Plan == PlanTrial {
		status = BillingTrial
		if change.TrialEndsAt == nil {
			t := time.Now().UTC().Add(TrialLength)
			change.TrialEndsAt = &t
		}
	}
	var handledBy *string
	if user != nil {
		handledBy = &user.ID
	}
	if err := h.repo.SetTenantPlan(ctx, tenantID, change, status, handledBy); err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			_ = writeError(w, http.StatusNotFound, "NotFound", "tenant not found", nil)
			return
		}
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to set the plan", nil)
		return
	}
	SetAuditAction(ctx, "platform.plan.set")
	SetAuditDetail(ctx, "tenant_id", tenantID)
	SetAuditDetail(ctx, "plan", change.Plan)
	SetAuditDetail(ctx, "billing_status", status)

	resumed := 0
	if before.BillingStatus == BillingSuspended {
		resumed = h.resumeBillingStoppedPipelines(ctx, tenantID)
	}
	after, err := h.repo.GetTenantByID(ctx, tenantID)
	if err != nil {
		_ = writeError(w, http.StatusInternalServerError, "DatabaseError", "failed to read the tenant", nil)
		return
	}
	_ = writeJSON(w, http.StatusOK, SuccessResponse{Data: map[string]any{
		"tenant_id": after.ID, "plan": after.SubscriptionPlan, "billing_status": after.BillingStatus,
		"trial_ends_at": after.TrialEndsAt, "pipelines_resumed": resumed,
	}})
}

// --- the gate ---

// planGate answers 402 for a suspended workspace and reports whether the
// request may go on. Reading and editing are never gated; running work is.
func (h *Handler) planGate(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	tenant := GetTenantFromContext(r.Context())
	if tenant == nil || tenant.ID != tenantID {
		t, err := h.repo.GetTenantByID(r.Context(), tenantID)
		if err != nil {
			return true // the handler's own lookup reports this
		}
		tenant = t
	}
	if tenant.BillingStatus != BillingSuspended {
		return true
	}
	_ = writeError(w, http.StatusPaymentRequired, "PlanRequired",
		"this workspace's trial has ended and its pipelines are stopped — ask for a plan under Settings → Plan",
		map[string]interface{}{"request_plan_url": "/settings/plan", "billing_status": tenant.BillingStatus})
	return false
}

// --- suspension and resumption ---

// suspendTenant marks the workspace suspended and stops every running
// pipeline, remembering which ones so activation can start them again.
func (h *Handler) suspendTenant(ctx context.Context, tenant *Tenant, reason string) (stopped int) {
	if err := h.repo.SetTenantBillingStatus(ctx, tenant.ID, BillingSuspended); err != nil {
		slog.Default().Error("billing: suspend tenant", "tenant", tenant.ID, "error", err)
		return 0
	}
	for offset := 0; ; offset += 100 {
		conns, _, err := h.repo.ListConnections(ctx, tenant.ID, &ListFilters{Status: "running", Limit: 100, Offset: offset})
		if err != nil {
			slog.Default().Error("billing: list running pipelines", "tenant", tenant.ID, "error", err)
			break
		}
		for _, c := range conns {
			h.stopPipelineForBilling(ctx, c, reason)
			stopped++
		}
		if len(conns) < 100 {
			break
		}
	}
	slog.Default().Warn("billing: workspace suspended", "tenant", tenant.ID, "slug", tenant.Slug, "reason", reason, "pipelines_stopped", stopped)
	return stopped
}

func (h *Handler) stopPipelineForBilling(ctx context.Context, conn *Connection, reason string) {
	var orchErr error
	if len(conn.Nodes) > 0 && h.orchestratorFactory != nil {
		if orchErr = h.orchestratorFactory(conn).StopPipeline(ctx, conn); orchErr != nil {
			slog.Default().Error("billing: orchestrator teardown failed", "connection", conn.ID, "error", orchErr)
		}
	}
	msg := reason
	if orchErr != nil {
		msg = reason + " (teardown: " + orchErr.Error() + ")"
	}
	if err := h.repo.UpdateConnectionStatus(ctx, conn.ID, "stopped", &msg); err != nil {
		slog.Default().Error("billing: mark pipeline stopped", "connection", conn.ID, "error", err)
		return
	}
	_ = h.repo.SetConnectionStoppedByBilling(ctx, conn.TenantID, conn.ID, true)
	data, _ := json.Marshal(map[string]any{"status": "stopped", "reason": reason})
	_ = h.repo.CreateConnectionEvent(ctx, NewConnectionEvent(conn.ID, conn.TenantID, "stopped", data))
	if h.publisher != nil {
		_ = h.publisher.PublishConnectionStop(ctx, conn.ID, conn.TenantID)
	}
}

// resumeBillingStoppedPipelines starts what the suspension stopped. A pipeline
// that will not start stays flagged and stopped; the operator sees it in the
// workspace like any other stopped pipeline.
func (h *Handler) resumeBillingStoppedPipelines(ctx context.Context, tenantID string) (resumed int) {
	conns, err := h.repo.ListConnectionsStoppedByBilling(ctx, tenantID)
	if err != nil {
		slog.Default().Error("billing: list pipelines to resume", "tenant", tenantID, "error", err)
		return 0
	}
	for _, c := range conns {
		if len(c.Nodes) > 0 && h.orchestratorFactory != nil {
			if err := h.orchestratorFactory(c).StartPipeline(ctx, c); err != nil {
				slog.Default().Error("billing: resume pipeline failed", "connection", c.ID, "error", err)
				continue
			}
		}
		if err := h.repo.UpdateConnectionStatus(ctx, c.ID, "running", nil); err != nil {
			slog.Default().Error("billing: mark pipeline running", "connection", c.ID, "error", err)
			continue
		}
		_ = h.repo.SetConnectionStoppedByBilling(ctx, tenantID, c.ID, false)
		data, _ := json.Marshal(map[string]any{"status": "running", "reason": "plan activated"})
		_ = h.repo.CreateConnectionEvent(ctx, NewConnectionEvent(c.ID, tenantID, "started", data))
		if h.publisher != nil {
			_ = h.publisher.PublishConnectionStart(ctx, c.ID, tenantID)
		}
		resumed++
	}
	return resumed
}

// --- the sweep ---

// BillingSweep suspends workspaces whose trial has run out: hourly, and once
// at start. With a database handle it is advisory-lock-gated so one replica
// does the work (the same pattern as the usage rollup).
type BillingSweep struct {
	h      *Handler
	db     *sql.DB
	tick   time.Duration
	now    func() time.Time
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBillingSweep wires the sweep; db may be nil (single replica, tests).
func (h *Handler) NewBillingSweep(db *sql.DB) *BillingSweep {
	return &BillingSweep{h: h, db: db, tick: time.Hour, now: time.Now}
}

// Start runs the sweep now and then every tick until Stop.
func (s *BillingSweep) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.tick)
		defer t.Stop()
		s.runOnceGated(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runOnceGated(ctx)
			}
		}
	}()
}

// Stop cancels the loop and waits for an in-flight run.
func (s *BillingSweep) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *BillingSweep) runOnceGated(ctx context.Context) {
	if s.db == nil {
		s.RunOnce(ctx)
		return
	}
	acquired, err := withAdvisoryLock(ctx, s.db, advisoryKeyBillingSweep, func(ctx context.Context) error {
		s.RunOnce(ctx)
		return nil
	})
	if err != nil {
		slog.Default().Warn("billing sweep: advisory lock error; running unguarded", "error", err)
		s.RunOnce(ctx)
		return
	}
	if !acquired {
		slog.Default().Debug("billing sweep: another replica holds the lock; skipping tick")
	}
}

// RunOnce suspends every expired trial and tells the operator about each.
func (s *BillingSweep) RunOnce(ctx context.Context) (suspended int) {
	expired, err := s.h.repo.ListExpiredTrials(ctx, s.now().UTC())
	if err != nil {
		slog.Default().Error("billing sweep: list expired trials", "error", err)
		return 0
	}
	for _, t := range expired {
		stopped := s.h.suspendTenant(ctx, t, "trial ended")
		suspended++
		s.h.notifyPlatform(ctx, &notify.Alert{
			Name:     notify.TrialExpiredName,
			Status:   "firing",
			Severity: "warning",
			Summary:  fmt.Sprintf("Trial ended for %s", t.Name),
			Description: fmt.Sprintf("Workspace %q (%s) is suspended; %d running pipeline(s) were stopped. "+
				"Set a plan under Platform → Workspaces to bring it back.", t.Name, t.Slug, stopped),
			Labels:   map[string]string{"workspace": t.Slug},
			StartsAt: s.now().UTC(),
		})
	}
	return suspended
}

// clipRunes bounds a free-text field in characters, never cutting mid-rune.
func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// getenvTrim is os.Getenv with the whitespace people leave in manifests removed.
func getenvTrim(name string) string { return strings.TrimSpace(osGetenv(name)) }
