package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// #795: Stop waits for every poller's done channel (its last checkpoint save
// included) until its context ends, instead of sleeping 2 s. Ordering is
// forced with gates, never with sleeps; every wait is bounded by srWait.

// swReturns starts f.c.Stop and reports its result on a channel.
func swReturns(c *bcConsumer, ctx context.Context) <-chan error {
	out := make(chan error, 1)
	go func() { out <- c.Stop(ctx) }()
	return out
}

func swNotYet(t *testing.T, ch <-chan error, what string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("Stop returned (%v) %s", err, what)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStop_WaitsForPollerDone(t *testing.T) {
	exited := &poller{cancel: func() {}, done: make(chan struct{})}
	close(exited.done)
	gated := &poller{cancel: func() {}, done: make(chan struct{})}
	stopping := &poller{cancel: func() {}, done: make(chan struct{})}

	c, _, _ := newTestConsumer()
	c.active = map[string]*poller{"a": exited, "b": gated, "c": {cancel: func() {}}} // "c": no done channel
	c.stopping = map[string]*poller{"d": stopping}

	ctx, cancel := context.WithTimeout(context.Background(), srWait)
	defer cancel()
	res := swReturns(c, ctx)

	swNotYet(t, res, "before the active poller finished")
	close(gated.done)
	swNotYet(t, res, "before the poller an earlier stop cancelled finished")
	close(stopping.done)
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("Stop = %v, want nil once every poller is done", err)
		}
	case <-time.After(srWait):
		t.Fatal("Stop did not return after every poller was done")
	}
}

func TestStop_NoPollersReturnsAtOnce(t *testing.T) {
	c, _, _ := newTestConsumer()
	select {
	case err := <-swReturns(c, context.Background()):
		if err != nil {
			t.Fatalf("Stop = %v, want nil", err)
		}
	case <-time.After(srWait):
		t.Fatal("Stop with no pollers did not return at once")
	}
}

func TestStop_ReturnsWhenContextEnds(t *testing.T) {
	c, _, _ := newTestConsumer()
	c.active = map[string]*poller{"a": {cancel: func() {}, done: make(chan struct{})}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	select {
	case err := <-swReturns(c, ctx):
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop = %v, want the context's deadline error", err)
		}
	case <-time.After(srWait):
		t.Fatal("Stop did not return when its context ended")
	}
}

// swScenario starts a real poller that is stopped mid-poll and is left
// storing its checkpoint through a store that holds the save (see heldStore).
// release lets the save through; the returned func is the test's cleanup.
func swScenario(t *testing.T) (f *srFixture, store *heldStore) {
	t.Helper()
	recs := map[string]string{"a": mins(-90), "b": mins(-80), "c": mins(-70), "d": mins(-60)}
	f = newSRFixture(t)
	bc := newOrderedBC(t, lmField, 2, recs)
	testDone := make(chan struct{})
	t.Cleanup(func() { close(testDone) })
	atPage2 := make(chan struct{})
	var holdOnce sync.Once
	bc.hold = func(token string, r *http.Request) {
		if token == "" {
			return
		}
		first := false
		holdOnce.Do(func() { first = true })
		if !first {
			return
		}
		close(atPage2)
		select { // until Stop cancels the request
		case <-r.Context().Done():
		case <-testDone:
		}
	}
	f.nodes = fmt.Sprintf(`[{"id":"c1","type":"consumer","config":{"type":"business_central","business_central":{`+
		`"aad_tenant_id":"aad","company_id":"GUID","client_id":"cid","client_secret":"sec","entity":"items",`+
		`"api_base_url":%q,"token_url":%q,"incremental":true,"cursor_field":%q,"poll_interval_seconds":3600}}}]`,
		bc.srv.URL+"/api/v2.0", bc.tok.URL, lmField)
	store = &heldStore{
		Store: checkpoint.NewInMemoryStore(), held: mins(-90),
		entered: make(chan struct{}), returned: make(chan struct{}), release: make(chan struct{}),
	}
	f.c.checkpoints = store
	f.c.saveTimeout = srWait
	f.c.httpClient = http.DefaultClient
	f.c.now = func() time.Time { return aheadNow }
	f.expectLookup(nil)
	f.c.startConnection(context.Background(), srConn, srTenant)
	srAwait(t, atPage2, "the poller to ask for page 2")
	return f, store
}

func TestStop_ReturnsOnlyAfterTheLastSave(t *testing.T) {
	f, store := swScenario(t)
	res := swReturns(f.c, context.Background())
	srAwait(t, store.entered, "the cancelled poller to store its checkpoint")
	swNotYet(t, res, "while the poller's save was still held")

	close(store.release)
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("Stop = %v, want nil", err)
		}
	case <-time.After(srWait):
		t.Fatal("Stop did not return after the save finished")
	}
	select {
	case <-store.returned:
	default:
		t.Fatal("Stop returned before the poller's save did")
	}
	if !store.stored(mins(-90)) {
		t.Error("the cancelled poll's checkpoint was not stored")
	}
	gfAwaitPollers(t, 0) // nothing leaks
}

func TestStop_BoundedByContextWhenSaveHangs(t *testing.T) {
	f, store := swScenario(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := swReturns(f.c, ctx)
	srAwait(t, store.entered, "the cancelled poller to store its checkpoint")
	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop = %v, want the context's deadline error", err)
		}
	case <-time.After(srWait):
		t.Fatal("Stop did not return when its context ended")
	}
	close(store.release) // let the poller finish, then check nothing leaks
	gfAwaitPollers(t, 0)
}
