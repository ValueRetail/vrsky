// Command sftp-producer uploads pipeline messages as files to a remote SFTP
// directory, naming each file from a configurable template (e.g.
// order_{{.id}}_{{.timestamp}}.json). It supports password and private-key
// auth. It is an SDK Producer: the runner owns NATS/JetStream/health/signals/
// shutdown; this binary implements Configure + Deliver.
//
// PR 2 of #76 (SFTP connector): the producer (upload). The SFTP consumer
// (watch + fetch) shipped in PR 1.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunProducer(context.Background(), "sftp-producer", &sftpProducer{}); err != nil {
		slog.Error("sftp-producer exited", "error", err)
		os.Exit(1)
	}
}
