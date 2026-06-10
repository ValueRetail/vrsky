package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// runRestore downloads a backup, decrypts + gunzips it, and pg_restores it into
// the target database. Restoring is destructive (it drops + recreates objects),
// so it requires an explicit --target-db-url and --confirm, and refuses to
// clobber the configured backup source DB without a deliberate override.
func runRestore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	targetURL := fs.String("target-db-url", "", "destination database URL to restore INTO (required)")
	confirm := fs.Bool("confirm", false, "required: acknowledge that restore overwrites the target database")
	allowSource := fs.Bool("allow-source-db", false, "permit restoring over BACKUP_DB_URL/MGMT_API_DB_URL (dangerous)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := fs.Arg(0)
	if key == "" {
		return fmt.Errorf("usage: vrsky-cli restore --target-db-url <url> --confirm <backup-key>")
	}
	if *targetURL == "" {
		return fmt.Errorf("--target-db-url is required (the database to restore INTO)")
	}
	if !*confirm {
		return fmt.Errorf("restore is destructive — re-run with --confirm to proceed")
	}

	cfg, err := backupConfigFromEnv()
	if err != nil {
		return err
	}
	// Guard against accidentally overwriting the live management DB. Compare by
	// host:port/dbname so equivalent URLs that differ only in credentials or
	// query params (e.g. sslmode) still trip the guard.
	if !*allowSource && sameDatabase(*targetURL, cfg.dbURL) {
		return fmt.Errorf("refusing to restore over the source database (%s); pass --allow-source-db to override", redactURL(*targetURL))
	}

	store, err := objectstore.New(ctx, cfg.store)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}
	sealed, _, err := store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("download backup %q: %w", key, err)
	}

	gzipped, err := crypto.DecryptBytes(sealed, cfg.keyHex)
	if err != nil {
		return fmt.Errorf("decrypt (wrong ENCRYPTION_KEY?): %w", err)
	}
	dump, err := gunzipBytes(gzipped)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}

	if err := pgRestore(ctx, *targetURL, dump); err != nil {
		return err
	}
	fmt.Printf("restore complete: %s -> %s (%d bytes)\n", key, redactURL(*targetURL), len(dump))
	return nil
}

// pgRestore pipes a custom-format dump into pg_restore against targetURL,
// cleaning existing objects first (--clean --if-exists) so the restore is
// idempotent.
func pgRestore(ctx context.Context, targetURL string, dump []byte) error {
	cmd := exec.CommandContext(ctx, "pg_restore",
		"--clean", "--if-exists", "--no-owner", "--no-privileges",
		"--dbname", targetURL)
	cmd.Stdin = bytes.NewReader(dump)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// A non-zero exit is a real restore failure (e.g. a version-incompatible
		// dump, or DROP errors without --if-exists). Surface pg_restore's stderr
		// so the operator sees exactly what failed.
		return fmt.Errorf("pg_restore: %w: %s", err, errBuf.String())
	}
	return nil
}

func gunzipBytes(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// sameDatabase reports whether two postgres URLs point at the same database —
// compared by host:port + database name, ignoring credentials and query params
// (sslmode, etc.) so cosmetically-different URLs don't bypass the source guard.
// Falls back to exact string match if either URL can't be parsed.
func sameDatabase(a, b string) bool {
	pa, errA := url.Parse(a)
	pb, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return strings.EqualFold(pa.Hostname(), pb.Hostname()) &&
		pa.Port() == pb.Port() &&
		strings.TrimPrefix(pa.Path, "/") == strings.TrimPrefix(pb.Path, "/")
}

// redactURL hides the password in a postgres URL for log output.
func redactURL(url string) string {
	at := bytes.IndexByte([]byte(url), '@')
	slashes := bytes.Index([]byte(url), []byte("//"))
	if at < 0 || slashes < 0 || slashes+2 >= at {
		return url
	}
	return url[:slashes+2] + "***@" + url[at+1:]
}
