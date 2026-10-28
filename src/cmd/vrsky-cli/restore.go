package main

import (
	"bufio"
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
	body, _, err := store.GetStream(ctx, key)
	if err != nil {
		return fmt.Errorf("download backup %q: %w", key, err)
	}
	defer body.Close()

	dump, err := openArchive(body, cfg.keyHex)
	if err != nil {
		return err
	}
	n, err := pgRestore(ctx, *targetURL, dump)
	if err != nil {
		return err
	}
	fmt.Printf("restore complete: %s -> %s (%d bytes)\n", key, redactURL(*targetURL), n)
	return nil
}

// openArchive returns a reader of the pg_dump bytes inside a backup archive
// read from r. Chunked archives (#430) are decrypted and gunzipped as they are
// read, so memory stays flat; the reader returns an error rather than EOF if
// the archive is cut off or altered, so read it to the end. Older archives are
// one GCM seal over the whole gzipped dump and are read into memory to open,
// as before.
func openArchive(r io.Reader, keyHex string) (io.Reader, error) {
	br := bufio.NewReader(r)
	var plain io.Reader
	if magic, err := br.Peek(len(backupMagic)); err == nil && string(magic) == backupMagic {
		dec, err := newOpenReader(br, keyHex)
		if err != nil {
			return nil, fmt.Errorf("open backup: %w", err)
		}
		plain = dec
	} else {
		sealed, err := io.ReadAll(br)
		if err != nil {
			return nil, fmt.Errorf("download backup: %w", err)
		}
		gzipped, err := crypto.DecryptBytes(sealed, keyHex)
		if err != nil {
			return nil, fmt.Errorf("decrypt (wrong ENCRYPTION_KEY?): %w", err)
		}
		plain = bytes.NewReader(gzipped)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	return gz, nil
}

// pgRestore pipes a custom-format dump into pg_restore against targetURL,
// cleaning existing objects first (--clean --if-exists) so the restore is
// idempotent. --single-transaction makes it all or nothing: a download that
// fails mid-stream, or a damaged archive, rolls back the --clean drops instead
// of leaving a half-restored database. pg_restore may stop reading at the end
// of the archive, so the rest of dump is drained afterwards: that is where a
// chunked archive's final chunk and the gzip checksum are verified. It returns
// the dump bytes read.
func pgRestore(ctx context.Context, targetURL string, dump io.Reader) (int64, error) {
	in := &countingReader{r: dump}
	cmd := exec.CommandContext(ctx, "pg_restore",
		"--clean", "--if-exists", "--single-transaction", "--no-owner", "--no-privileges",
		"--dbname", targetURL)
	cmd.Stdin = in
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if in.err != nil {
		// The archive failed (decryption, truncation, gzip): that is the
		// cause, whatever pg_restore made of its cut-off input.
		return in.n, fmt.Errorf("read backup: %w", in.err)
	}
	if runErr != nil {
		// A non-zero exit is a real restore failure (e.g. a version-incompatible
		// dump, or DROP errors without --if-exists). Surface pg_restore's stderr
		// so the operator sees exactly what failed.
		return in.n, fmt.Errorf("pg_restore: %w: %s", runErr, errBuf.String())
	}
	if _, err := io.Copy(io.Discard, in); err != nil {
		return in.n, fmt.Errorf("backup archive failed verification after pg_restore finished: %w", err)
	}
	return in.n, nil
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
