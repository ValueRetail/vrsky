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
)

// #699: a gateway that keeps returning next links must not hold a poll for
// ever. The fakes below never stop; the context timeout is the net that turns
// a missing cap into a failure instead of a hung test.

func lowerPageCap(t *testing.T, n int) {
	t.Helper()
	old := pageCap
	pageCap = n
	t.Cleanup(func() { pageCap = old })
}

func TestFetchAndPublish_PageCap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		cap     int
		body    func(next string) string
	}{
		{"v2, cap 3", "v2", 3, func(next string) string {
			return fmt.Sprintf(`{"d":{"results":[{"SalesOrder":"x"}],"__next":%q}}`, next)
		}},
		{"v2, cap 1", "v2", 1, func(next string) string {
			return fmt.Sprintf(`{"d":{"results":[{"SalesOrder":"x"}],"__next":%q}}`, next)
		}},
		{"v4, cap 5", "v4", 5, func(next string) string {
			return fmt.Sprintf(`{"value":[{"SalesOrder":"x"}],"@odata.nextLink":%q}`, next)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lowerPageCap(t, tc.cap)
			var served atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := served.Add(1)
				fmt.Fprint(w, tc.body(fmt.Sprintf("A_SalesOrder?$skiptoken=P%d", n+1)))
			}))
			defer srv.Close()

			c, published := collectingConsumer()
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil)).With("connection_id", "conn-699")
			cfg := &SAPConfig{APIBaseURL: srv.URL + svcPath, EntitySet: "A_SalesOrder", Username: "u", Password: "p", ODataVersion: tc.version}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := c.fetchAndPublish(ctx, "conn-699", "tenant", cfg, newAuthorizer(cfg, c.httpClient), logger)

			if !errors.Is(err, errPageCap) {
				t.Fatalf("fetchAndPublish = %v, want errPageCap", err)
			}
			if n := int(served.Load()); n != tc.cap {
				t.Errorf("the server handled %d requests, want %d", n, tc.cap)
			}
			if n := int(published.Load()); n != tc.cap {
				t.Errorf("published %d pages, want %d (the pages fetched stay published)", n, tc.cap)
			}
			out := logs.String()
			for _, want := range []string{"level=WARN", "connection_id=conn-699", fmt.Sprintf("page_cap=%d", tc.cap)} {
				if !strings.Contains(out, want) {
					t.Errorf("log %q lacks %q", out, want)
				}
			}
		})
	}
}

// A poll that ends on the last page just under the cap is not a capped poll.
func TestFetchAndPublish_PageCap_LastPageAtTheCapIsFine(t *testing.T) {
	lowerPageCap(t, 3)
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := served.Add(1); n < 3 {
			fmt.Fprintf(w, `{"d":{"results":[{"SalesOrder":"x"}],"__next":"A_SalesOrder?$skiptoken=P%d"}}`, n+1)
			return
		}
		fmt.Fprint(w, `{"d":{"results":[{"SalesOrder":"last"}]}}`)
	}))
	defer srv.Close()

	c, published := collectingConsumer()
	cfg := &SAPConfig{APIBaseURL: srv.URL + svcPath, EntitySet: "A_SalesOrder", Username: "u", Password: "p"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.fetchAndPublish(ctx, "conn", "tenant", cfg, newAuthorizer(cfg, c.httpClient), c.logger); err != nil {
		t.Fatalf("fetchAndPublish: %v", err)
	}
	if published.Load() != 3 {
		t.Errorf("published %d pages, want 3", published.Load())
	}
}

func TestMaxPagesPerPoll(t *testing.T) {
	if maxPagesPerPoll != 10000 || pageCap != 10000 {
		t.Errorf("maxPagesPerPoll = %d, pageCap = %d, want 10000", maxPagesPerPoll, pageCap)
	}
}
