package sdk

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestErrorClassification(t *testing.T) {
	base := errors.New("boom")

	if Retriable(nil) != nil || Permanent(nil) != nil || RateLimited(nil, time.Second) != nil {
		t.Error("wrapping nil should return nil")
	}

	if IsPermanent(Retriable(base)) {
		t.Error("Retriable must not report Permanent")
	}
	if !IsPermanent(Permanent(base)) {
		t.Error("Permanent must report Permanent")
	}
	if IsPermanent(base) {
		t.Error("bare error must not report Permanent")
	}

	// Unwrap preserves the cause.
	if !errors.Is(Permanent(base), base) {
		t.Error("Permanent must unwrap to its cause")
	}
	if !errors.Is(Retriable(base), base) {
		t.Error("Retriable must unwrap to its cause")
	}

	d, ok := RetryAfter(RateLimited(base, 5*time.Second))
	if !ok || d != 5*time.Second {
		t.Errorf("RetryAfter = (%v, %v), want (5s, true)", d, ok)
	}
	if _, ok := RetryAfter(base); ok {
		t.Error("RetryAfter on a bare error should be false")
	}
}

func TestEnvInt(t *testing.T) {
	if got := envInt("SDK_TEST_MISSING", 8080); got != 8080 {
		t.Errorf("missing env should return default, got %d", got)
	}
	t.Setenv("SDK_TEST_PORT", "9999")
	if got := envInt("SDK_TEST_PORT", 8080); got != 9999 {
		t.Errorf("env value should win, got %d", got)
	}
	t.Setenv("SDK_TEST_PORT", "not-a-number")
	if got := envInt("SDK_TEST_PORT", 8080); got != 8080 {
		t.Errorf("invalid env should fall back to default, got %d", got)
	}
}

func TestOpenDB_UnreachableFails(t *testing.T) {
	// Port 1 refuses fast; openDB should ping, fail, close, and return an error.
	if _, err := openDB("postgres://u:p@127.0.0.1:1/none?sslmode=disable"); err == nil {
		t.Error("expected openDB to fail against an unreachable database")
	}
}

func TestNewLogger(t *testing.T) {
	_ = os.Setenv("LOG_LEVEL", "debug")
	defer os.Unsetenv("LOG_LEVEL")
	if l := newLogger("svc"); l == nil {
		t.Fatal("newLogger returned nil")
	}
}

func TestBaseKitDefaults(t *testing.T) {
	var p BaseProducer
	p.init("file-producer", nil)
	if p.Name() != "file-producer" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.Version() != "1.0.0" {
		t.Errorf("default Version = %q", p.Version())
	}
	if p.Type() != "producer" {
		t.Errorf("Type = %q", p.Type())
	}
	if p.Health() != "healthy" {
		t.Errorf("default Health = %q", p.Health())
	}
	p.SetUnhealthy()
	if p.Health() != "unhealthy" {
		t.Errorf("after SetUnhealthy Health = %q", p.Health())
	}
	p.RegisterHTTPHandler("/x", nil)
	if len(p.httpRoutes()) != 1 {
		t.Error("RegisterHTTPHandler did not record a route")
	}
}

// Covers the small accessors / aliases that the harness-driven tests don't
// touch directly, keeping the public surface exercised.
func TestAccessorsAndAliases(t *testing.T) {
	// Error wrappers expose Error()/Unwrap().
	for _, e := range []error{Retriable(errors.New("a")), Permanent(errors.New("b")), RateLimited(errors.New("c"), time.Second)} {
		if e.Error() == "" {
			t.Error("wrapped error should have a message")
		}
		if errors.Unwrap(e) == nil {
			t.Error("wrapped error should unwrap")
		}
	}

	// Base structs for every kind report the right Type and shared defaults.
	var bc BaseConsumer
	bc.init("c", nil)
	var bf BaseFilter
	bf.init("f", nil)
	var bv BaseConverter
	bv.init("v", nil)
	if bc.Type() != "consumer" || bf.Type() != "filter" || bv.Type() != "converter" {
		t.Errorf("types: %s/%s/%s", bc.Type(), bf.Type(), bv.Type())
	}
	if bc.Log() == nil {
		t.Error("Log must not be nil")
	}
	if err := bc.Start(context.Background()); err != nil {
		t.Errorf("default Start: %v", err)
	}
	if err := bc.Stop(context.Background()); err != nil {
		t.Errorf("default Stop: %v", err)
	}

	// Explicit version wins over the default.
	var bp BaseProducer
	bp.version = "2.3.4"
	if bp.Version() != "2.3.4" {
		t.Errorf("explicit Version = %q", bp.Version())
	}

	if NewEnvelope() == nil {
		t.Error("NewEnvelope returned nil")
	}

	// healthToggle is nil-safe and forwards to its setter.
	var nilToggle *healthToggle
	nilToggle.SetReady(true) // must not panic
	called := false
	(&healthToggle{setReady: func(bool) { called = true }}).SetReady(true)
	if !called {
		t.Error("SetReady did not invoke the setter")
	}

	// Run options apply.
	var o runOptions
	WithDB(nil)(&o)
	WithoutHealthServer()(&o)
	if !o.disableHealth {
		t.Error("WithoutHealthServer did not set the flag")
	}
}
