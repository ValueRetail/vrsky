package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Failing cases from review round 4 of #472.

// Raising cursor_overlap must not re-send the orders between the new and the
// old window: the stored set only covers the old one.
func TestReview_RaisingOverlapResendsOrders(t *testing.T) {
	for _, fm := range filterModes {
		t.Run(fm.name, func(t *testing.T) {
			_, srv := newFakeSitoo(t, fm.exclusive, ords(idRange(1, 3000)...)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, ov(1000), 1000)
			poll(t, c, incTenant, incConn, cfg)
			assertOnce(t, "poll 1", published(t, got, mu, incTenant, incConn), v1(idRange(1, 3000)...)...)
			cfg = incConfig(srv.URL, ov(2000), 1000) // operator raises the overlap
			poll(t, c, incTenant, incConn, cfg)
			assertOnce(t, "poll 2 after raising cursor_overlap", published(t, got, mu, incTenant, incConn))
		})
	}
}

// Sitoo may return fewer orders than num asks for (a server-side cap). A short
// page is not the end of the data.
func TestReview_NumCapStallsSilently(t *testing.T) {
	const limit = 100
	for _, fm := range filterModes {
		t.Run(fm.name, func(t *testing.T) {
			f := &fakeSitoo{orders: ords(idRange(1, 150)...), exclusive: fm.exclusive}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if n, _ := strconv.Atoi(q.Get("num")); n > limit {
					q.Set("num", strconv.Itoa(limit))
					r.URL.RawQuery = q.Encode()
				}
				f.serve(w, r)
			}))
			t.Cleanup(srv.Close)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, nil, 0) // defaults: page size 1000, overlap 1000
			var all []pubRec
			for p := 0; p < 5; p++ {
				poll(t, c, incTenant, incConn, cfg)
				all = append(all, published(t, got, mu, incTenant, incConn)...)
			}
			assertOnce(t, "5 polls", all, v1(idRange(1, 150)...)...)
		})
	}
}

// A resource query string that doesn't parse must fail, not be dropped.
func TestReview_MalformedResourceQueryIsNotSilentlyDropped(t *testing.T) {
	for _, inc := range []bool{false, true} {
		t.Run("incremental="+strconv.FormatBool(inc), func(t *testing.T) {
			f, srv := newFakeSitoo(t, false, ords(1, 2, 3)...)
			c, _, _ := newIncConsumer(checkpoint.NewInMemoryStore())
			cfg := incConfig(srv.URL, nil, 0)
			cfg.Incremental = inc
			cfg.Resource = "orders?orderstate=10&note=50%off"
			if err := c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger); err != nil {
				return // refused: fine
			}
			for _, r := range f.take() {
				if !strings.Contains(r.uri, "orderstate=10") {
					t.Errorf("request %s dropped the resource filter orderstate=10 without an error", r.uri)
				}
			}
		})
	}
}
