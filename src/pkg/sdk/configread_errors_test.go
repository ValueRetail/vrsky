package sdk

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// ConfigReadError is the not-found vs couldn't-read split every producer
// relies on (#704): only sql.ErrNoRows is a definite "not mine".
func TestConfigReadError(t *testing.T) {
	down := errors.New("pq: the database system is shutting down")
	cases := []struct {
		name            string
		in              error
		wantNil         bool
		wantUnavailable bool
	}{
		{name: "nil", in: nil, wantNil: true},
		{name: "no rows", in: sql.ErrNoRows},
		{name: "wrapped no rows", in: fmt.Errorf("scan: %w", sql.ErrNoRows)},
		{name: "database down", in: down, wantUnavailable: true},
		{name: "connection done", in: sql.ErrConnDone, wantUnavailable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConfigReadError(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("ConfigReadError(nil) = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("ConfigReadError returned nil for an error")
			}
			if IsConfigUnavailable(got) != tc.wantUnavailable {
				t.Errorf("IsConfigUnavailable = %v, want %v (%v)", !tc.wantUnavailable, tc.wantUnavailable, got)
			}
			if IsPermanent(got) {
				t.Errorf("ConfigReadError must never be Permanent: %v", got)
			}
			if !errors.Is(got, tc.in) {
				t.Errorf("the cause is lost: %v", got)
			}
		})
	}
}

func TestConfigUnavailable(t *testing.T) {
	if ConfigUnavailable(nil) != nil {
		t.Error("ConfigUnavailable(nil) must be nil")
	}
	cause := errors.New("secret read failed")
	err := ConfigUnavailable(cause)
	if !IsConfigUnavailable(err) || !errors.Is(err, ErrConfigUnavailable) {
		t.Errorf("not marked: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("cause lost: %v", err)
	}
	if IsPermanent(err) {
		t.Error("ConfigUnavailable must not be Permanent")
	}
	if !IsConfigUnavailable(fmt.Errorf("deliver: %w", err)) {
		t.Error("the mark must survive further wrapping")
	}
	if IsConfigUnavailable(cause) || IsConfigUnavailable(Retriable(cause)) {
		t.Error("a plain or Retriable error is not ConfigUnavailable")
	}
}
