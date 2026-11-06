package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// #699: an API that keeps returning @odata.nextLink must not hold a poll for
// ever. The fake never stops; the context timeout is the net that turns a
// missing cap into a failure instead of a hung test.

func lowerPageCap(t *testing.T, n int) {
	t.Helper()
	old := pageCap
	pageCap = n
	t.Cleanup(func() { pageCap = old })
}

func TestFetchAndPublish_PageCap(t *testing.T) {
	const seeded = "2026-09-01T00:00:00Z"
	for _, tc := range []struct {
		name        string
		cap         int
		incremental bool
		stored      string // watermark in the checkpoint before the poll
		// want is the watermark after the poll. An incremental poll reads in
		// cursor order (#733) and advances to the value before the last one
		// it read; one that is not incremental stores none.
		want string
	}{
		{"not incremental, cap 3", 3, false, "", ""},
		{"not incremental, cap 1", 1, false, "", ""},
		{"incremental, no watermark yet", 4, true, "", "2026-10-03T10:00:00Z"},
		{"incremental, watermark advances to where it stopped", 4, true, seeded, "2026-10-03T10:00:00Z"},
		{"incremental, one page read, nothing to advance to", 1, true, seeded, seeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lowerPageCap(t, tc.cap)
			var served atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
					return
				}
				n := served.Add(1)
				// Every record is newer than the stored watermark, so a poll
				// that stored one would move it.
				fmt.Fprintf(w, `{"value":[{"number":"R%d","lastModifiedDateTime":"2026-10-%02dT10:00:00Z"}],"@odata.nextLink":"?page=%d"}`,
					n, n, n+1)
			}))
			defer api.Close()

			c, got, gotMu := newTestConsumer()
			if tc.stored != "" {
				if err := c.checkpoints.Save(context.Background(), &checkpoint.Checkpoint{
					TenantID: "tenant-1", ConnectionID: "conn-1", NodeID: "src",
					LastProcessedMessageID: tc.stored, LastProcessedAt: time.Now().Add(-time.Hour),
				}); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &BCConfig{
				AADTenantID: "t1", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
				Entity: "items", APIBaseURL: api.URL + "/api/v2.0", TokenURL: api.URL + "/token",
				Incremental: tc.incremental, NodeID: "src",
			}
			tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(c.httpClient)
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil)).With("connection_id", "conn-1")

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := c.fetchAndPublish(ctx, "conn-1", "tenant-1", cfg, tok, logger)

			if !errors.Is(err, errPageCap) {
				t.Fatalf("fetchAndPublish = %v, want errPageCap", err)
			}
			if n := int(served.Load()); n != tc.cap {
				t.Errorf("the API served %d pages, want %d", n, tc.cap)
			}
			gotMu.Lock()
			if n := len(*got); n != tc.cap {
				t.Errorf("published %d pages, want %d (the pages fetched stay published)", n, tc.cap)
			}
			gotMu.Unlock()

			cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
			switch {
			case !tc.incremental && cp != nil:
				t.Errorf("a checkpoint was stored (%q) by a poll without incremental", cp.LastProcessedMessageID)
			case tc.want != "" && (cp == nil || cp.LastProcessedMessageID != tc.want):
				t.Errorf("watermark = %+v, want %q", cp, tc.want)
			}

			out := logs.String()
			for _, want := range []string{"level=WARN", "connection_id=conn-1", fmt.Sprintf("page_cap=%d", tc.cap)} {
				if !strings.Contains(out, want) {
					t.Errorf("log %q lacks %q", out, want)
				}
			}
		})
	}
}

// A poll that ends on the last page at the cap is not a capped poll, and its
// watermark is stored.
func TestFetchAndPublish_PageCap_LastPageAtTheCapIsFine(t *testing.T) {
	lowerPageCap(t, 3)
	var served atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		if n := served.Add(1); n < 3 {
			fmt.Fprintf(w, `{"value":[{"number":"R%d","lastModifiedDateTime":"2026-10-01T10:00:00Z"}],"@odata.nextLink":"?page=%d"}`, n, n+1)
			return
		}
		fmt.Fprint(w, `{"value":[{"number":"R3","lastModifiedDateTime":"2026-10-02T10:00:00Z"}]}`)
	}))
	defer api.Close()

	c, got, gotMu := newTestConsumer()
	cfg := &BCConfig{
		AADTenantID: "t1", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: api.URL + "/api/v2.0", TokenURL: api.URL + "/token",
		Incremental: true, NodeID: "src",
	}
	tok := oauthcc.New(cfg.effectiveTokenURL(), cfg.ClientID, cfg.ClientSecret, cfg.effectiveScope()).WithHTTPClient(c.httpClient)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.fetchAndPublish(ctx, "conn-1", "tenant-1", cfg, tok, c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	gotMu.Lock()
	defer gotMu.Unlock()
	if len(*got) != 3 {
		t.Errorf("published %d pages, want 3", len(*got))
	}
	if cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src"); cp == nil || cp.LastProcessedMessageID == "" {
		t.Errorf("no watermark stored after a complete poll: %+v", cp)
	}
}

func TestMaxPagesPerPoll(t *testing.T) {
	if maxPagesPerPoll != 10000 || pageCap != 10000 {
		t.Errorf("maxPagesPerPoll = %d, pageCap = %d, want 10000", maxPagesPerPoll, pageCap)
	}
}
