package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// runBackup dumps the management DB, gzips + encrypts it, and uploads it to
// object storage under <prefix>management_db-<timestamp>.dump.gz.enc.
func runBackup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	stamp := fs.String("stamp", "", "override the timestamp segment of the object key (default: current UTC time as 20060102T150405Z)")
	_ = fs.Parse(args)

	cfg, err := backupConfigFromEnv()
	if err != nil {
		return err
	}

	// pg_dump in custom format (-Fc) so restore can use pg_restore with
	// --clean --if-exists. Streamed straight into memory — the management DB is
	// small (tenants/connections/secrets), so this is fine.
	dump, err := pgDump(ctx, cfg.dbURL)
	if err != nil {
		return err
	}

	gzipped, err := gzipBytes(dump)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	sealed, err := crypto.EncryptBytes(gzipped, cfg.keyHex)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	store, err := objectstore.New(ctx, cfg.store)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}
	ts := *stamp
	if ts == "" {
		ts = time.Now().UTC().Format("20060102T150405Z")
	}
	key := cfg.prefix + "management_db-" + ts + ".dump.gz.enc"
	if err := store.Put(ctx, key, sealed, "application/octet-stream"); err != nil {
		return fmt.Errorf("upload backup: %w", err)
	}

	fmt.Printf("backup complete: %s (%d bytes encrypted, %d raw)\n", key, len(sealed), len(dump))
	return nil
}

// runList prints the available backups (most recent first).
func runList(ctx context.Context, args []string) error {
	cfg, err := backupConfigFromEnv()
	if err != nil {
		return err
	}
	store, err := objectstore.New(ctx, cfg.store)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}
	objs, err := store.List(ctx, cfg.prefix)
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	if len(objs) == 0 {
		fmt.Printf("no backups under %q\n", cfg.prefix)
		return nil
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key > objs[j].Key })
	for _, o := range objs {
		age := ""
		if !o.LastModified.IsZero() {
			age = time.Since(o.LastModified).Round(time.Minute).String() + " ago"
		}
		fmt.Printf("%-60s %10d bytes  %s\n", o.Key, o.Size, age)
	}
	return nil
}

// pgDump runs pg_dump -Fc against dbURL and returns the dump bytes.
func pgDump(ctx context.Context, dbURL string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--dbname", dbURL)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pg_dump: %w: %s", err, errBuf.String())
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("pg_dump produced no output: %s", errBuf.String())
	}
	return out.Bytes(), nil
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
