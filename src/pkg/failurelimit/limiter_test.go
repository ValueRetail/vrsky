package failurelimit

import (
	"net/netip"
	"testing"
	"time"
)

const refill = time.Minute

func TestLimiter_ForgetsIdleKeys(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	l := New(2, refill)
	l.MaxKeys = 3

	for _, key := range []string{"a", "b", "c"} {
		if _, ok, _ := l.Take(key, now); !ok {
			t.Fatalf("first attempt for %s refused", key)
		}
	}
	// At the cap, keys not seen before share one bucket instead of growing
	// the map: two attempts between them, then refusal.
	for i, key := range []string{"d", "e", "f", "g"} {
		_, ok, _ := l.Take(key, now)
		if want := i < 2; ok != want {
			t.Fatalf("attempt for new key %s at the cap: ok = %v, want %v", key, ok, want)
		}
	}
	if l.Tracked() != 4 { // a, b, c and the shared one
		t.Fatalf("limiter tracks %d keys, want 4 (the cap of 3 plus the shared bucket)", l.Tracked())
	}
	// A known key is still judged on its own.
	if _, ok, _ := l.Take("a", now); !ok {
		t.Fatal("a tracked key was refused because strangers filled the shared bucket")
	}

	// Once every allowance has refilled, nothing is remembered.
	now = now.Add(10 * refill)
	if _, ok, _ := l.Take("z", now); !ok {
		t.Fatal("a fresh key was refused after the limiter emptied")
	}
	if l.Tracked() != 1 {
		t.Fatalf("limiter still tracks %d keys after they all refilled, want only the newcomer", l.Tracked())
	}
}

// A refund returns the attempt Take spent and nothing more: however long the
// genuine request took, a key never holds more than the allowance.
func TestLimiter_RefundNeverBanksMoreThanTheAllowance(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	l := New(2, refill)

	if _, ok, _ := l.Take("a", now); !ok {
		t.Fatal("first attempt refused")
	}
	// The request was slow: half of the spent attempt has refilled by the
	// time it is handed back. A refund for a key the limiter never saw is
	// nothing at all.
	now = now.Add(refill / 2)
	l.Refund("a", now)
	l.Refund("never-seen", now)

	for i := 0; i < 2; i++ {
		if _, ok, _ := l.Take("a", now); !ok {
			t.Fatalf("attempt %d of the allowance refused", i+1)
		}
	}
	// Exactly the allowance was there: the wait for the next attempt is a
	// whole refill period, not the half a banked surplus would leave.
	if wait, ok, first := l.Take("a", now); ok || !first || wait != refill {
		t.Fatalf("third attempt: ok=%v first=%v wait=%v, want a first refusal with %v to wait", ok, first, wait, refill)
	}
	if _, ok, first := l.Take("a", now); ok || first {
		t.Fatalf("fourth attempt: ok=%v first=%v, want a refusal that is not the first of its block", ok, first)
	}
	if _, ok, _ := l.Take("never-seen", now); !ok {
		t.Fatal("a key that was only ever refunded was refused its first attempt")
	}
}

func TestLimiter_ZeroMeansOff(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	l := New(0, refill)
	for i := 0; i < 100; i++ {
		if _, ok, _ := l.Take("a", now); !ok {
			t.Fatalf("attempt %d refused by a disabled limiter", i+1)
		}
	}
	var nilLimiter *Limiter
	if _, ok, _ := nilLimiter.Take("a", now); !ok {
		t.Fatal("a nil limiter must allow")
	}
	nilLimiter.Refund("a", now)
}

func TestAddressKey(t *testing.T) {
	key := func(s string) string { return AddressKey(netip.MustParseAddr(s)) }
	if a, b := key("2001:db8:1:2::1"), key("2001:db8:1:2:ffff:abcd:0:9"); a != b {
		t.Errorf("two addresses in one /64 are counted apart: %q and %q", a, b)
	}
	if a, b := key("2001:db8:1:2::1"), key("2001:db8:1:3::1"); a == b {
		t.Errorf("two different /64s share a key: %q", a)
	}
	if a, b := key("::ffff:203.0.113.9"), key("203.0.113.9"); a != b {
		t.Errorf("an IPv4-mapped address is counted apart from the IPv4 one: %q and %q", a, b)
	}
	if a, b := key("203.0.113.9"), key("203.0.113.10"); a == b {
		t.Errorf("two IPv4 addresses share a key: %q", a)
	}
	if got := key("fe80::1%eth0"); got != "fe80::/64" {
		t.Errorf("a zoned address keyed as %q, want fe80::/64", got)
	}
}
