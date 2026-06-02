// Package harness provides a Docker-less testing harness for VRSky connectors
// built on the sdk package. It runs an in-process NATS+JetStream server and
// drives a connector through the real SDK runner, so connector tests exercise
// the actual subscribe/deliver/DLQ path without a docker-compose stack.
package harness

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// StartEmbeddedJetStream brings up an in-process NATS server with JetStream
// enabled and returns a connected client + JetStream context + cleanup. State
// lives under t.TempDir so each test starts clean. Lifted from
// pkg/messaging/messaging_test.go.
func StartEmbeddedJetStream(t *testing.T) (*nats.Conn, nats.JetStreamContext, func()) {
	t.Helper()
	dir := t.TempDir()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random free port
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  filepath.Join(dir, "js"),
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("harness: new nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("harness: nats server did not come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("harness: connect: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("harness: jetstream: %v", err)
	}
	cleanup := func() {
		_ = nc.Drain()
		srv.Shutdown()
		_ = os.RemoveAll(dir)
	}
	return nc, js, cleanup
}
