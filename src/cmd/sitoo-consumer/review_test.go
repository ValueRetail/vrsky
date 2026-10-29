package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Failing cases from review rounds 4 and 5 of #472.

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

// A poll that stops at its page budget after getting past the watermark must
// not let the next poll step past an order that shows up late inside the
// overlap behind that watermark.
func TestReview5_LateOrderBehindNextIsLost(t *testing.T) {
	for _, fm := range filterModes {
		t.Run(fm.name, func(t *testing.T) {
			var visible, hidden []int64
			for id := int64(1); id <= 400; id++ {
				if id%10 == 7 {
					hidden = append(hidden, id)
				} else {
					visible = append(visible, id)
				}
			}
			f, srv := newFakeSitoo(t, fm.exclusive, ords(visible...)...)
			store := checkpoint.NewInMemoryStore()
			c, got, mu := newIncConsumer(store)
			cfg := incConfig(srv.URL, ov(10), 2)
			_ = c.fetchAndPublish(context.Background(), incConn, incTenant, cfg, c.logger) // stops at the budget
			w, _ := strconv.ParseInt(watermark(t, store, incTenant, incConn, incNode), 10, 64)
			var late []int64
			for _, h := range hidden {
				if h < w && w-h < 10 { // within the overlap of the stored watermark
					late = append(late, h)
				}
			}
			if len(late) == 0 {
				t.Fatalf("no hidden order within 10 of watermark %d", w)
			}
			f.add(ords(late...)...) // a till comes back online
			for p := 0; p < 10; p++ {
				poll(t, c, incTenant, incConn, cfg)
			}
			cnt := countRecs(published(t, got, mu, incTenant, incConn))
			for _, l := range late {
				if cnt[pubRec{ID: l, V: 1}] != 1 {
					t.Errorf("late order %d (watermark %d, overlap 10) published %d times, want 1", l, w, cnt[pubRec{ID: l, V: 1}])
				}
			}
		})
	}
}

// The sample-data endpoint refuses a resource the poller would refuse,
// instead of showing a sample without the filter (review round 5).
func TestReview5_SampleDataRefusesABadResource(t *testing.T) {
	for _, res := range []string{"orders?orderstate=10&note=50%off", "orders#x"} {
		t.Run(res, func(t *testing.T) {
			hit := false
			apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hit = true
				fmt.Fprint(w, `{"totalcount":1,"items":[{"orderid":1}]}`)
			}))
			defer apiSrv.Close()
			c := &sitooConsumer{httpClient: http.DefaultClient}
			body := fmt.Sprintf(`{"api_id":"id","api_password":"pw","account_id":1,"site_id":2,"base_url":%q,"resource":%q}`, apiSrv.URL, res)
			req := httptest.NewRequest(http.MethodPost, "/sample-data/", strings.NewReader(body))
			w := httptest.NewRecorder()
			c.handleSampleData()(w, req)
			var resp struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.OK || hit {
				t.Fatalf("sample for %q: ok=%v, Sitoo called=%v; want a refusal before any request", res, resp.OK, hit)
			}
		})
	}
}
