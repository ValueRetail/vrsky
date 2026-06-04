// Command sftp-consumer watches a remote SFTP directory per active connection,
// fetches new files, publishes their contents into the pipeline, and applies an
// after-action (delete / move / leave). It supports password and private-key
// auth. It is an SDK Consumer: the runner owns NATS/DB/health/signals/shutdown;
// this binary subscribes to the connection command subjects and drives a poller
// per active connection.
//
// PR 1 of #76 (SFTP connector): the consumer (watch + fetch). The SFTP producer
// (upload) lands in PR 2.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ValueRetail/vrsky/pkg/sdk"
)

func main() {
	if err := sdk.RunConsumer(context.Background(), "sftp-consumer", &sftpConsumer{}); err != nil {
		slog.Error("sftp-consumer exited", "error", err)
		os.Exit(1)
	}
}
