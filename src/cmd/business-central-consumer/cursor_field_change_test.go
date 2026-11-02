package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// Changing cursor_field must not carry the old field's watermark over to the
// new field (#610). These tests run the consumer against a Business Central
// that has two cursor candidates on every record, a DateTime
// (lastModifiedDateTime) and an Edm.Date (postingDate), and that answers 400
// to a filter whose literal does not fit its field, as the real one does.

type fieldRec struct{ id, lm, date string }

type twoFieldBC struct {
	srv  *httptest.Server
	tok  *httptest.Server
	mu   sync.Mutex
	recs []fieldRec
	asks []string // $filter of every request
}

var (
	fieldFilter = regexp.MustCompile(`^(lastModifiedDateTime|postingDate) (gt|ge) (\S+)$`)
	dateLiteral = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func newTwoFieldBC(t *testing.T, recs ...fieldRec) *twoFieldBC {
	t.Helper()
	b := &twoFieldBC{recs: recs, tok: bcTestToken(t)}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		f := r.URL.Query().Get("$filter")
		b.asks = append(b.asks, f)
		var field, op, lit string
		if f != "" {
			m := fieldFilter.FindStringSubmatch(f)
			if m == nil {
				http.Error(w, `{"error":{"message":"bad filter"}}`, http.StatusBadRequest)
				return
			}
			field, op, lit = m[1], m[2], m[3]
			// The literal must be of the field's type.
			if _, err := time.Parse(time.RFC3339, lit); (field == "lastModifiedDateTime") != (err == nil) ||
				(field == "postingDate") != dateLiteral.MatchString(lit) {
				http.Error(w, `{"error":{"message":"The type of the literal does not match the type of `+field+`"}}`,
					http.StatusBadRequest)
				return
			}
		}
		recs := append([]fieldRec(nil), b.recs...)
		sort.Slice(recs, func(i, j int) bool { return recs[i].id < recs[j].id })
		var out []string
		for _, rc := range recs {
			v := rc.date
			if field == "lastModifiedDateTime" {
				v = rc.lm
			}
			if f == "" || (op == "gt" && v > lit) || (op == "ge" && v >= lit) {
				out = append(out, fmt.Sprintf(`{"id":%q,"lastModifiedDateTime":%q,"postingDate":%q}`, rc.id, rc.lm, rc.date))
			}
		}
		fmt.Fprintf(w, `{"value":[%s]}`, strings.Join(out, ","))
	}))
	t.Cleanup(func() { b.srv.Close(); b.tok.Close() })
	return b
}

func (b *twoFieldBC) lastAsk() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.asks) == 0 {
		return "<no request>"
	}
	return b.asks[len(b.asks)-1]
}

// cfg is a fresh node config, as after a stop and start of the connection.
func (b *twoFieldBC) cfg(field string) *BCConfig {
	return &BCConfig{
		AADTenantID: "aad", CompanyID: "GUID", ClientID: "cid", ClientSecret: "sec",
		Entity: "items", APIBaseURL: b.srv.URL, TokenURL: b.tok.URL,
		Incremental: true, NodeID: "src", CursorField: field,
	}
}

func twoFieldRecs() []fieldRec {
	return []fieldRec{
		{"a", stamp(-2 * time.Hour), "2026-10-01"},
		{"b", stamp(-time.Hour), "2026-10-20"},
	}
}

const (
	lmField   = "lastModifiedDateTime"
	dateField = "postingDate"
)

// TestCursorFieldChange_DropsTheWatermark — each step is one poll with a
// node config freshly built (a restart), reading the checkpoint the earlier
// steps left.
func TestCursorFieldChange_DropsTheWatermark(t *testing.T) {
	lmMark := lmField + " gt " + stamp(-time.Hour)
	dateMark := dateField + " ge 2026-10-20"
	type step struct {
		field   string
		wantAsk string
		wantIDs string
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"same field keeps the watermark", []step{
			{lmField, "", "a,b"},
			{lmField, lmMark, ""},
			{lmField, lmMark, ""},
		}},
		{"same date field keeps the watermark", []step{
			{dateField, "", "a,b"},
			{dateField, dateMark, ""},
		}},
		{"the default field name and the spelled-out one are the same field", []step{
			{"", "", "a,b"},
			{lmField, lmMark, ""},
		}},
		{"DateTime to date: no gt on the old value, everything read once", []step{
			{lmField, "", "a,b"},
			{dateField, "", "a,b"},
			{dateField, dateMark, ""},
		}},
		{"date to DateTime", []step{
			{dateField, "", "a,b"},
			{lmField, "", "a,b"},
			{lmField, lmMark, ""},
		}},
		{"switched back reads from the start again", []step{
			{lmField, "", "a,b"},
			{dateField, "", "a,b"},
			{lmField, "", "a,b"},
			{lmField, lmMark, ""},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc := newTwoFieldBC(t, twoFieldRecs()...)
			c, got, mu := newTestConsumer()
			c.now = func() time.Time { return aheadNow }
			for i, s := range tc.steps {
				ids := poll(t, c, got, mu, bc.cfg(s.field))
				if bc.lastAsk() != s.wantAsk {
					t.Errorf("step %d (%s): asked %q, want %q", i+1, s.field, bc.lastAsk(), s.wantAsk)
				}
				if sorted(ids) != s.wantIDs {
					t.Errorf("step %d (%s): published %q, want %q", i+1, s.field, sorted(ids), s.wantIDs)
				}
			}
		})
	}
}

// TestCursorFieldChange_SameConfigObject — the field changed on a config the
// poller already holds, not through a restart: what the process remembers in
// memory is dropped too.
func TestCursorFieldChange_SameConfigObject(t *testing.T) {
	bc := newTwoFieldBC(t, append(twoFieldRecs(), fieldRec{"f", "2099-01-01T00:00:00Z", "2026-10-25"})...)
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	cfg := bc.cfg(lmField)
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,b,f" {
		t.Fatalf("poll 1 published %q", sorted(ids))
	}
	if len(cfg.aheadSeen) != 1 {
		t.Fatalf("setup: f should be remembered as ahead, got %v", cfg.aheadSeen)
	}
	cfg.CursorField = dateField
	if ids := poll(t, c, got, mu, cfg); sorted(ids) != "a,b,f" || bc.lastAsk() != "" {
		t.Errorf("poll 2 asked %q and published %q, want an unfiltered read of a,b,f", bc.lastAsk(), sorted(ids))
	}
	if ids := poll(t, c, got, mu, cfg); len(ids) != 0 || bc.lastAsk() != dateField+" ge 2026-10-25" {
		t.Errorf("poll 3 asked %q and published %q", bc.lastAsk(), sorted(ids))
	}
}

// TestCursorFieldChange_StoresTheFieldWithTheWatermark — the checkpoint row
// names the field, and a change replaces both.
func TestCursorFieldChange_StoresTheFieldWithTheWatermark(t *testing.T) {
	bc := newTwoFieldBC(t, twoFieldRecs()...)
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	for _, tc := range []struct{ field, wantMark string }{
		{lmField, stamp(-time.Hour)},
		{dateField, "2026-10-20"},
	} {
		poll(t, c, got, mu, bc.cfg(tc.field))
		cp, err := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
		if err != nil || cp == nil {
			t.Fatalf("checkpoint: %v, %v", cp, err)
		}
		if cp.LastProcessedMessageID != tc.wantMark || storedCursorField(cp) != tc.field {
			t.Errorf("after %s: stored watermark %q, field %q; want %q, %q",
				tc.field, cp.LastProcessedMessageID, storedCursorField(cp), tc.wantMark, tc.field)
		}
	}
}

// TestCursorFieldChange_NoWatermarkOnTheNewFieldClearsTheOld — the first read
// on the new field finds only a record dated after now, so there is no new
// watermark; the old field's value must not stay in the row.
func TestCursorFieldChange_NoWatermarkOnTheNewFieldClearsTheOld(t *testing.T) {
	bc := newTwoFieldBC(t, fieldRec{"a", stamp(-time.Hour), "2099-01-01"})
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	poll(t, c, got, mu, bc.cfg(lmField))
	if ids := poll(t, c, got, mu, bc.cfg(dateField)); sorted(ids) != "a" {
		t.Fatalf("published %q, want a", sorted(ids))
	}
	cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
	if cp == nil || cp.LastProcessedMessageID != "" || storedCursorField(cp) != dateField {
		t.Errorf("row = %+v, want no watermark and field %s", cp, dateField)
	}
	if ids := poll(t, c, got, mu, bc.cfg(dateField)); len(ids) != 0 || bc.lastAsk() != "" {
		t.Errorf("poll 3 asked %q and published %q, want an unfiltered read that sends nothing new", bc.lastAsk(), sorted(ids))
	}
}

// TestCursorFieldChange_LegacyCheckpointWithoutAFieldName — a row saved
// before #610 has a watermark and no field. It is taken to belong to the
// configured field: an upgrade does not read everything again, and the next
// save records the field.
func TestCursorFieldChange_LegacyCheckpointWithoutAFieldName(t *testing.T) {
	cases := []struct {
		name, field, mark, state string
		wantAsk                  string
	}{
		{"DateTime, no state", lmField, stamp(-time.Hour), "", lmField + " gt " + stamp(-time.Hour)},
		{"DateTime, pictures state", lmField, stamp(-time.Hour), `{"pictures":null}`, lmField + " gt " + stamp(-time.Hour)},
		{"date, ahead state", dateField, "2026-10-20", `{"pictures":null,"ahead":{"b":"2026-10-20"}}`, dateField + " ge 2026-10-20"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc := newTwoFieldBC(t, twoFieldRecs()...)
			c, got, mu := newTestConsumer()
			c.now = func() time.Time { return aheadNow }
			row := &checkpoint.Checkpoint{
				TenantID: "tenant-1", ConnectionID: "conn-1", NodeID: "src",
				LastProcessedMessageID: tc.mark, LastProcessedAt: aheadNow.Add(-time.Minute),
			}
			if tc.state != "" {
				row.State = json.RawMessage(tc.state)
			}
			if err := c.checkpoints.Save(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			if ids := poll(t, c, got, mu, bc.cfg(tc.field)); len(ids) != 0 {
				t.Errorf("published %q, want nothing: the watermark is still good", sorted(ids))
			}
			if bc.lastAsk() != tc.wantAsk {
				t.Errorf("asked %q, want %q", bc.lastAsk(), tc.wantAsk)
			}
			cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
			if cp == nil || cp.LastProcessedMessageID != tc.mark || storedCursorField(cp) != tc.field {
				t.Errorf("row = %+v, want the same watermark %q and the field %q recorded", cp, tc.mark, tc.field)
			}
			// With the field recorded, a later change is seen.
			other := dateField
			if tc.field == dateField {
				other = lmField
			}
			if ids := poll(t, c, got, mu, bc.cfg(other)); bc.lastAsk() != "" || sorted(ids) != "a,b" {
				t.Errorf("after switching to %s: asked %q, published %q, want an unfiltered read of a,b", other, bc.lastAsk(), sorted(ids))
			}
		})
	}
}

// TestCursorFieldChange_OtherStateIsKept — pictures remembered in the same
// row belong to the records, not to the cursor field.
func TestCursorFieldChange_OtherStateIsKept(t *testing.T) {
	bc := newTwoFieldBC(t, twoFieldRecs()...)
	c, got, mu := newTestConsumer()
	c.now = func() time.Time { return aheadNow }
	row := &checkpoint.Checkpoint{
		TenantID: "tenant-1", ConnectionID: "conn-1", NodeID: "src",
		LastProcessedMessageID: stamp(-time.Hour), LastProcessedAt: aheadNow.Add(-time.Minute),
		State: json.RawMessage(`{"pictures":{"a":{"picture_id":"p1","number":"N1"}},"cursor_field":"lastModifiedDateTime"}`),
	}
	if err := c.checkpoints.Save(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	cfg := bc.cfg(dateField)
	cfg.pictureSeen = map[string]seenPicture{"a": {PictureID: "p1", Number: "N1"}}
	poll(t, c, got, mu, cfg)
	cp, _ := c.checkpoints.Get(context.Background(), "tenant-1", "conn-1", "src")
	var st pictureState
	if err := json.Unmarshal(cp.State, &st); err != nil {
		t.Fatal(err)
	}
	if want := map[string]seenPicture{"a": {PictureID: "p1", Number: "N1"}}; !reflect.DeepEqual(st.Pictures, want) || st.CursorField != dateField {
		t.Errorf("state = %s, want the pictures kept and the field %s", cp.State, dateField)
	}
}
