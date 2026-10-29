package main

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ValueRetail/vrsky/pkg/checkpoint"
)

// #563 against Postgres (acceptance criterion 9): the start of
// checkpoint_decode_test.go with the undecodable rows in
// connection_node_checkpoints, where state is jsonb, through
// checkpoint.PostgresStore. Skipped without VRSKY_TEST_POSTGRES_URL; CI sets
// it.
func TestCheckpointDecodePG_StartReplacesUndecodableRow(t *testing.T) {
	for _, tc := range undecodableCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newPGRestartEnv(t)
			tenant := e.tenant()
			connID := uuid.New().String()
			e.connection(connID, tenant, "inbox")
			runUndecodableStart(t, checkpoint.NewPostgresStore(e.db), tenant, connID, tc)
		})
	}
}
