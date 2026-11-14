package sdk_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Start retries are kept per tenant (#725 review). Every consumer sees every
// start and stop command, so a start or stop naming another tenant for a
// connection can be in flight beside the right tenant's retry; neither may
// end it.

// A start for tenant B holds a claim on A's connection (its lookup will find
// the connection is not B's) and B's stop lands meanwhile. A's pending retry
// is not B's to end: it runs and starts the connection.
func TestStartRetrying_OtherTenantsStopKeepsRetry(t *testing.T) {
	var mu sync.Mutex
	var g sdk.StartGuard
	active := map[string]bool{}
	var attempts atomic.Int32
	var activated atomic.Bool
	pol := sdk.RetryPolicy{Base: 200 * time.Millisecond, Max: 200 * time.Millisecond}
	isActive := func() bool { return active["c1"] }
	startA := func(ctx context.Context) error {
		claim, ok := g.ReserveOrRetryContext(ctx, &mu, rtTenantA, "c1", isActive, nil)
		if !ok {
			return nil
		}
		defer claim.Release()
		if attempts.Add(1) == 1 {
			return sdk.TransientStart(errDBDown)
		}
		if claim.Activate(func() { active["c1"] = true }) {
			activated.Store(true)
		}
		return nil
	}
	g.StartRetrying(context.Background(), sdk.RetryOptions{Mu: &mu, Logger: quietLogger, Policy: pol}, rtTenantA, "c1", startA)
	if sdk.PendingRetriesForTest(&g, &mu) != 1 {
		t.Fatal("no retry pending")
	}
	claimB, ok := g.ReserveOrRetryContext(context.Background(), &mu, rtTenantB, "c1", isActive, nil)
	if !ok {
		t.Fatal("B could not reserve")
	}
	mu.Lock()
	_, err := g.StopForTenantLocked("vrsky.commands."+rtTenantB+".connection.stop", rtTenantB, "c1")
	mu.Unlock()
	claimB.Release()
	if err != nil {
		t.Fatalf("B's stop of B's own claim was refused: %v", err)
	}
	eventually(t, "A's retry to start the connection", activated.Load)
	if !sdk.WaitRetriesForTest(&g, 5*time.Second) {
		t.Fatal("a retry loop is still running")
	}
	if n := attempts.Load(); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

// A's retry fires while another tenant's start holds the claim, so its
// attempt is refused and queues itself on that claim. When the other start
// gives up, A's queued start runs and the connection comes up: the retry
// loop does not end while a claim it stands behind is in flight.
func TestStartRetrying_RetryBehindAnotherClaim(t *testing.T) {
	var mu sync.Mutex
	var g sdk.StartGuard
	active := map[string]bool{}
	var attemptsA atomic.Int32
	var activated atomic.Bool
	pol := sdk.RetryPolicy{Base: 50 * time.Millisecond, Max: 50 * time.Millisecond}
	isActive := func() bool { return active["c1"] }
	opts := sdk.RetryOptions{Mu: &mu, Logger: quietLogger, Policy: pol}

	bHolds := make(chan struct{})
	bRelease := make(chan struct{})
	var startConn func(ctx context.Context, tenant string)
	try := func(ctx context.Context, tenant string) error {
		claim, ok := g.ReserveOrRetryContext(ctx, &mu, tenant, "c1", isActive, func() { startConn(ctx, tenant) })
		if !ok {
			return nil
		}
		defer claim.Release()
		if tenant == rtTenantB {
			close(bHolds)
			<-bRelease
			return nil // not B's
		}
		if attemptsA.Add(1) == 1 {
			return sdk.TransientStart(errors.New("db down"))
		}
		if claim.Activate(func() { active["c1"] = true }) {
			activated.Store(true)
		}
		return nil
	}
	startConn = func(ctx context.Context, tenant string) {
		g.StartRetrying(ctx, opts, tenant, "c1", func(ctx context.Context) error { return try(ctx, tenant) })
	}
	startConn(context.Background(), rtTenantA)
	done := make(chan struct{})
	go func() {
		defer close(done)
		startConn(context.Background(), rtTenantB)
	}()
	<-bHolds
	// Several of A's retries fire, and are refused, while B holds the claim.
	time.Sleep(6 * pol.Max)
	if activated.Load() {
		t.Fatal("A activated while B held the claim")
	}
	close(bRelease)
	<-done
	eventually(t, "A to start once B gave up", activated.Load)
	if !sdk.WaitRetriesForTest(&g, 5*time.Second) {
		t.Fatal("a retry loop is still running")
	}
}

// B's Release drops its claim and runs A's queued start
// only after unlocking. A's loop, deciding in between, sees no claim; it must
// not end and cancel the context that queued start runs under (#725 review).
// Forced here by holding the queued start until A's loop has decided.
func TestStartRetrying_QueuedStartAfterClaimDrop(t *testing.T) {
	var mu sync.Mutex
	var g sdk.StartGuard
	active := map[string]bool{}
	var attemptsA atomic.Int32
	var activated atomic.Bool
	pol := sdk.RetryPolicy{Base: 50 * time.Millisecond, Max: 50 * time.Millisecond}
	isActive := func() bool { return active["c1"] }
	opts := sdk.RetryOptions{Mu: &mu, Logger: quietLogger, Policy: pol}

	bHolds := make(chan struct{})
	bRelease := make(chan struct{})
	closureStarted := make(chan struct{})
	var releaseOnce, closureOnce sync.Once
	var startConn func(ctx context.Context, tenant string)
	try := func(ctx context.Context, tenant string) error {
		claim, ok := g.ReserveOrRetryContext(ctx, &mu, tenant, "c1", isActive, func() {
			if tenant == rtTenantA {
				first := false
				closureOnce.Do(func() { first = true; close(closureStarted) })
				if first {
					// Hold the queued start until A's loop has decided.
					deadline := time.Now().Add(500 * time.Millisecond)
					for sdk.PendingRetriesForTest(&g, &mu) != 0 && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
				}
			}
			startConn(ctx, tenant)
		})
		if !ok {
			if tenant == rtTenantA {
				// Refused behind B: let B give up, and return only once B's
				// claim is dropped and its Release is running our closure.
				releaseOnce.Do(func() { close(bRelease) })
				select {
				case <-closureStarted:
				case <-time.After(time.Second):
				}
			}
			return nil
		}
		defer claim.Release()
		if tenant == rtTenantB {
			close(bHolds)
			<-bRelease
			return nil // not B's
		}
		if attemptsA.Add(1) == 1 {
			return sdk.TransientStart(errors.New("db down"))
		}
		if claim.Activate(func() { active["c1"] = true }) {
			activated.Store(true)
		}
		return nil
	}
	startConn = func(ctx context.Context, tenant string) {
		g.StartRetrying(ctx, opts, tenant, "c1", func(ctx context.Context) error { return try(ctx, tenant) })
	}
	startConn(context.Background(), rtTenantA)
	go startConn(context.Background(), rtTenantB)
	<-bHolds
	time.Sleep(time.Second)
	if !activated.Load() {
		t.Fatalf("A never started (A attempts=%d, pending=%d)", attemptsA.Load(), sdk.PendingRetriesForTest(&g, &mu))
	}
}
