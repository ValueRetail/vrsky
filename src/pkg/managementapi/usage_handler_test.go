package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedUsage(t *testing.T, repo *MockRepository, tenant string) {
	t.Helper()
	ctx := context.Background()
	// Two days in June 2026.
	d1 := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	if err := repo.UpsertUsageDaily(ctx, tenant, d1, 100, 2, 5000); err != nil {
		t.Fatalf("seed d1: %v", err)
	}
	if err := repo.UpsertUsageDaily(ctx, tenant, d2, 50, 1, 6000); err != nil {
		t.Fatalf("seed d2: %v", err)
	}
}

func TestHandleGetUsage(t *testing.T) {
	repo := NewMockRepository()
	seedUsage(t, repo, "t-1")
	// A second tenant must not leak into t-1's totals.
	seedUsage(t, repo, "t-2")
	h := NewHandler(repo, NewValidator())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/t-1/usage?from=2026-06-01&to=2026-06-30", nil)
	req.SetPathValue("tenant_id", "t-1")
	rec := httptest.NewRecorder()
	h.HandleGetUsage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data UsageResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Month.MessagesPublished != 150 {
		t.Errorf("messages = %d, want 150", resp.Data.Month.MessagesPublished)
	}
	if resp.Data.Month.Deploys != 3 {
		t.Errorf("deploys = %d, want 3", resp.Data.Month.Deploys)
	}
	// Storage is the latest day's snapshot, not a sum.
	if resp.Data.Month.StorageBytes != 6000 {
		t.Errorf("storage = %d, want 6000 (latest day)", resp.Data.Month.StorageBytes)
	}
	if len(resp.Data.Daily) != 2 {
		t.Fatalf("daily rows = %d, want 2", len(resp.Data.Daily))
	}
	if resp.Data.Daily[0].Day != "2026-06-10" {
		t.Errorf("first day = %q, want 2026-06-10 (ascending)", resp.Data.Daily[0].Day)
	}
}

func TestHandleExportUsageCSV(t *testing.T) {
	repo := NewMockRepository()
	seedUsage(t, repo, "t-1")
	h := NewHandler(repo, NewValidator())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/t-1/usage/export?from=2026-06-01&to=2026-06-30", nil)
	req.SetPathValue("tenant_id", "t-1")
	rec := httptest.NewRecorder()
	h.HandleExportUsage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("content-type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("content-disposition = %q, want attachment", cd)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 3 { // header + 2 days
		t.Fatalf("csv lines = %d, want 3; body=%q", len(lines), rec.Body.String())
	}
	if lines[0] != "day,messages_published,deploys,storage_bytes" {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "2026-06-10,100,2,5000") {
		t.Errorf("row 1 = %q", lines[1])
	}
}

func TestUsageRangeDefaultsToCurrentMonth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/t-1/usage", nil)
	from, to := usageRange(req)
	now := time.Now().UTC()
	if from.Day() != 1 || from.Month() != now.Month() {
		t.Errorf("from = %v, want first of current month", from)
	}
	if to.Before(from) {
		t.Errorf("to %v before from %v", to, from)
	}
}
