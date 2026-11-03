package configread

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// Only sql.ErrNoRows is a definite "not mine"; every other read error is
// Unavailable and must not be acked (#704).
func TestReadError(t *testing.T) {
	cases := []struct {
		name            string
		in              error
		wantNil         bool
		wantUnavailable bool
	}{
		{name: "nil", in: nil, wantNil: true},
		{name: "no rows", in: sql.ErrNoRows},
		{name: "wrapped no rows", in: fmt.Errorf("scan: %w", sql.ErrNoRows)},
		{name: "database down", in: errors.New("pq: the database system is shutting down"), wantUnavailable: true},
		{name: "connection done", in: sql.ErrConnDone, wantUnavailable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadError(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("ReadError(nil) = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("ReadError returned nil for an error")
			}
			if IsUnavailable(got) != tc.wantUnavailable {
				t.Errorf("IsUnavailable = %v, want %v (%v)", !tc.wantUnavailable, tc.wantUnavailable, got)
			}
			if !errors.Is(got, tc.in) {
				t.Errorf("the cause is lost: %v", got)
			}
		})
	}
}

func TestUnavailable(t *testing.T) {
	if Unavailable(nil) != nil {
		t.Error("Unavailable(nil) must be nil")
	}
	cause := errors.New("secret read failed")
	err := Unavailable(cause)
	if !IsUnavailable(err) || !errors.Is(err, ErrUnavailable) || !errors.Is(err, cause) {
		t.Errorf("Unavailable(%v) = %v: mark or cause lost", cause, err)
	}
	if !IsUnavailable(fmt.Errorf("deliver: %w", err)) {
		t.Error("the mark must survive further wrapping")
	}
	if IsUnavailable(cause) {
		t.Error("a plain error is not Unavailable")
	}
}
