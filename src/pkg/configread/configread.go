// Package configread tells "this connection is not mine" apart from "I could
// not read this connection's config" (#704). A worker that acks on the second
// loses the message, so a failed read must be retried instead.
//
// It is its own package so workers that don't run on the SDK (data-filter,
// data-converter) can use it without importing pkg/sdk. SDK connectors use
// the same names re-exported from pkg/sdk.
package configread

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrUnavailable means a worker could not read a connection's config or
// secrets: the database query failed (not "no rows"), or a secret could not be
// fetched or decrypted. It is not an answer to "is this connection mine?", and
// a worker that gets it must not ack the message. A connection that does not
// exist, or has no node of the worker's type, is a definite "not mine".
var ErrUnavailable = errors.New("connection config unavailable")

// Unavailable wraps a failed config or secret read so errors.Is matches both
// ErrUnavailable and err.
func Unavailable(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// IsUnavailable reports whether err (or anything it wraps) is a failed config
// or secret read.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrUnavailable)
}

// ReadError classifies the error from reading a connection's row:
// sql.ErrNoRows means the connection is gone, a definite "not mine", and is
// returned as a plain error; any other failure is Unavailable.
func ReadError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("connection not found: %w", err)
	}
	return Unavailable(fmt.Errorf("read connection: %w", err))
}
