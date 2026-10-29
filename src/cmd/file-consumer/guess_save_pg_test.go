package main

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// #536 against Postgres (acceptance criterion 6): the two-pod case of
// guess_save_test.go with both pods on checkpoint.PostgresStore, so the rows
// A's start can't read, and later settles its guess from, are the ones in
// connection_node_checkpoints. Skipped without VRSKY_TEST_POSTGRES_URL; CI
// sets it.
func TestGuessSavePG_ReplacementOfAnotherPodsFilePublishedOnce(t *testing.T) {
	for _, tc := range guessTwoPodCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newPGRestartEnv(t)
			tenant := e.tenant()
			connID := uuid.New().String()
			e.connection(connID, tenant, "inbox")
			runGuessTwoPods(t, checkpoint.NewPostgresStore(e.db), tenant, connID, tc)
		})
	}
}
