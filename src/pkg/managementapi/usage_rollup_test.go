package managementapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/promquery"
)

// fakeProm returns a Prometheus-shaped vector keyed by tenant_id, choosing the
// payload by whether the query is for messages or deploys.
func fakeProm(t *testing.T, messages, deploys map[string]string) *httptest.Server {
	t.Helper()
	vec := func(vals map[string]string) string {
		var b strings.Builder
		b.WriteString(`{"status":"success","data":{"resultType":"vector","result":[`)
		first := true
		for tid, v := range vals {
			if !first {
				b.WriteString(",")
			}
			first = false
			b.WriteString(`{"metric":{"tenant_id":"` + tid + `"},"value":[1718000000,"` + v + `"]}`)
		}
		b.WriteString(`]}}`)
		return b.String()
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(q, "deploys") {
			_, _ = w.Write([]byte(vec(deploys)))
		} else {
			_, _ = w.Write([]byte(vec(messages)))
		}
	}))
}

func quietRollup(repo Repository, prom *promquery.Client) *UsageRollup {
	return NewUsageRollup(repo, prom,
		WithRollupLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

func TestUsageRollup_RunOnce(t *testing.T) {
	ctx := context.Background()
	repo := NewMockRepository()
	// Two tenants with storage rows.
	_, _ = repo.GetTenantQuotas(ctx, "t-1")
	_ = repo.SetTenantStorageUsage(ctx, "t-1", 4096)
	_, _ = repo.GetTenantQuotas(ctx, "t-2")
	_ = repo.SetTenantStorageUsage(ctx, "t-2", 0)

	srv := fakeProm(t,
		map[string]string{"t-1": "42.4", "t-2": "0"}, // messages (fractional → rounds to 42)
		map[string]string{"t-1": "3"},                // deploys (only t-1)
	)
	defer srv.Close()

	quietRollup(repo, promquery.New(srv.URL, srv.Client())).runOnce(ctx)

	today := time.Now().UTC()
	rows, _ := repo.ListUsageDaily(ctx, "t-1", today, today)
	if len(rows) != 1 {
		t.Fatalf("t-1 rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.MessagesPublished != 42 {
		t.Errorf("messages = %d, want 42 (rounded)", got.MessagesPublished)
	}
	if got.Deploys != 3 {
		t.Errorf("deploys = %d, want 3", got.Deploys)
	}
	if got.StorageBytes != 4096 {
		t.Errorf("storage = %d, want 4096", got.StorageBytes)
	}

	// t-2 had storage but no deploys → messages 0, deploys 0, storage 0.
	rows2, _ := repo.ListUsageDaily(ctx, "t-2", today, today)
	if len(rows2) != 1 || rows2[0].Deploys != 0 {
		t.Errorf("t-2 row = %+v, want one row with deploys=0", rows2)
	}
}

func TestUsageRollup_StorageOnlyWhenNoProm(t *testing.T) {
	ctx := context.Background()
	repo := NewMockRepository()
	_, _ = repo.GetTenantQuotas(ctx, "t-1")
	_ = repo.SetTenantStorageUsage(ctx, "t-1", 9999)

	quietRollup(repo, nil).runOnce(ctx) // nil prom → storage-only

	today := time.Now().UTC()
	rows, _ := repo.ListUsageDaily(ctx, "t-1", today, today)
	if len(rows) != 1 || rows[0].StorageBytes != 9999 || rows[0].MessagesPublished != 0 {
		t.Errorf("row = %+v, want storage=9999 messages=0", rows)
	}
}
