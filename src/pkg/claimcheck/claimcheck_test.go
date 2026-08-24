package claimcheck

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// The offload/rehydrate round-trip, caps, and checksum behavior are covered end
// to end by pkg/sdk's tests, which exercise this package through the SDK's
// delegating wrappers. This file tests the env-configuration surface directly.

// captureLogs returns a logger writing into buf, so a test can assert that a
// misconfiguration was actually reported and not just quietly corrected.
func captureLogs(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestEnvBytes_MissingUsesDefault(t *testing.T) {
	var buf bytes.Buffer
	if got := envBytes("CLAIMCHECK_TEST_ABSENT_BYTES", 4096, captureLogs(&buf)); got != 4096 {
		t.Fatalf("envBytes = %d, want 4096", got)
	}
	if buf.Len() != 0 {
		t.Errorf("an unset variable should not warn, logged: %s", buf.String())
	}
}

// A cap an operator cannot express as an int32 is the whole reason this parses
// as int64. 8 GiB is a plausible value for a worker with the memory to match.
func TestEnvBytes_ParsesValuesBeyondInt32(t *testing.T) {
	const eightGiB = int64(8) * 1024 * 1024 * 1024

	var buf bytes.Buffer
	t.Setenv(EnvRehydrateMax, "8589934592")

	if got := RehydrateMaxFromEnv(captureLogs(&buf)); got != eightGiB {
		t.Fatalf("RehydrateMaxFromEnv = %d, want %d", got, eightGiB)
	}
	if buf.Len() != 0 {
		t.Errorf("a valid value should not warn, logged: %s", buf.String())
	}
}

// The failure mode this guards against: an operator raises the cap, fat-fingers
// the value, and the worker goes on rejecting payloads at the old limit with
// nothing in the logs to explain why.
func TestEnvBytes_InvalidValueWarnsAndFallsBack(t *testing.T) {
	for _, bad := range []string{"128MiB", "not-a-number", "12.5", "99999999999999999999999"} {
		t.Run(bad, func(t *testing.T) {
			var buf bytes.Buffer
			t.Setenv(EnvRehydrateMax, bad)

			got := RehydrateMaxFromEnv(captureLogs(&buf))
			if got != DefaultRehydrateMaxBytes {
				t.Fatalf("got %d, want the default %d", got, int64(DefaultRehydrateMaxBytes))
			}

			logged := buf.String()
			if !strings.Contains(logged, EnvRehydrateMax) || !strings.Contains(logged, bad) {
				t.Errorf("warning should name the variable and the offending value, got: %q", logged)
			}
		})
	}
}

func TestInlineMaxFromEnv_InvalidValueWarnsAndFallsBack(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv(EnvInlineMax, "256KiB")

	if got := InlineMaxFromEnv(captureLogs(&buf)); got != DefaultInlineMaxBytes {
		t.Fatalf("got %d, want the default %d", got, DefaultInlineMaxBytes)
	}
	if !strings.Contains(buf.String(), EnvInlineMax) {
		t.Errorf("warning should name the variable, got: %q", buf.String())
	}
}

// A negative value is a deliberate "switch this off", not a mistake, so it is
// passed through rather than warned about.
func TestEnvBytes_NegativeIsHonoured(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv(EnvRehydrateMax, "-1")

	if got := RehydrateMaxFromEnv(captureLogs(&buf)); got != -1 {
		t.Fatalf("got %d, want -1 (cap disabled)", got)
	}
	if buf.Len() != 0 {
		t.Errorf("disabling the cap should not warn, logged: %s", buf.String())
	}
}
