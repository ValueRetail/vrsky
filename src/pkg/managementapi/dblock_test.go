package managementapi

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	_ "github.com/lib/pq"
)

func TestAdvisoryKey_StableAndPositive(t *testing.T) {
	// Deterministic: same input → same key.
	if advisoryKey("grant-abc") != advisoryKey("grant-abc") {
		t.Fatal("advisoryKey is not deterministic")
	}
	// Distinct inputs → (very likely) distinct keys.
	if advisoryKey("grant-abc") == advisoryKey("grant-xyz") {
		t.Fatal("advisoryKey collided on distinct inputs")
	}
	// Always positive (top bit cleared) so it's a valid bigint advisory key.
	for _, s := range []string{"", "a", "oauth-refresh:" + "ffffffff", "🔒"} {
		if k := advisoryKey(s); k < 0 {
			t.Fatalf("advisoryKey(%q) = %d, want >= 0", s, k)
		}
	}
}

// TestWithAdvisoryLock_MutualExclusion verifies that two callers cannot hold the
// same advisory lock at once, and that releasing (transaction end) lets the next
// caller acquire it. Requires a real Postgres — set MGMT_TEST_DB_URL to run;
// skipped otherwise so the unit suite stays DB-free.
func TestWithAdvisoryLock_MutualExclusion(t *testing.T) {
	dsn := os.Getenv("MGMT_TEST_DB_URL")
	if dsn == "" {
		t.Skip("set MGMT_TEST_DB_URL to run the advisory-lock integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// Need at least 2 real connections so the two locks can be held concurrently.
	db.SetMaxOpenConns(4)
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	const key int64 = 0x7e57_0001 // test-only key

	// Hold the lock in goroutine A until told to release.
	release := make(chan struct{})
	held := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		acquired, err := withAdvisoryLock(ctx, db, key, func(context.Context) error {
			close(held)
			<-release // keep the lock until the test releases it
			return nil
		})
		if err != nil {
			t.Errorf("A: %v", err)
		}
		if !acquired {
			t.Error("A: expected to acquire the lock first")
		}
	}()

	<-held // A now holds the lock

	// B must fail to acquire while A holds it.
	acquired, err := withAdvisoryLock(ctx, db, key, func(context.Context) error {
		t.Error("B: fn ran while A held the lock")
		return nil
	})
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	if acquired {
		t.Fatal("B: acquired the lock while A held it")
	}

	// Release A; the lock should now be free.
	close(release)
	wg.Wait()

	// C should now acquire successfully.
	ran := false
	acquired, err = withAdvisoryLock(ctx, db, key, func(context.Context) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("C: %v", err)
	}
	if !acquired || !ran {
		t.Fatalf("C: expected to acquire after release (acquired=%v ran=%v)", acquired, ran)
	}
}
