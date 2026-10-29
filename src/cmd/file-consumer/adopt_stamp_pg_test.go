package main

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// #500 against Postgres (acceptance criterion 7): the two-pod case of
// adopt_stamp_test.go with both pods on checkpoint.PostgresStore, so the
// snapshot and changes rows (#426) A reads are the ones in
// connection_node_checkpoints. Skipped without VRSKY_TEST_POSTGRES_URL; CI
// sets it.
func TestAdoptStampPG_TwoPodsPublishTheReplacementOnce(t *testing.T) {
	for _, tc := range twoPodCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newPGRestartEnv(t)
			tenant := e.tenant()
			connID := uuid.New().String()
			e.connection(connID, tenant, "inbox")
			runTwoPods(t, checkpoint.NewPostgresStore(e.db), tenant, connID, tc)
		})
	}
}
