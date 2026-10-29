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
	"os"
	"os/exec"
	"strings"
	"time"

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
	allowUnbound := fs.Bool("allow-unbound", false, "accept a VRSKYBK1 or older archive, which does not name its object key (backups made before VRSKYBK2)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := fs.Arg(0)
	if key == "" || fs.NArg() > 1 {
		// Flags after the key are not parsed; refuse rather than drop them.
		return fmt.Errorf("usage: vrsky-cli restore --target-db-url <url> --confirm [--allow-unbound] <backup-key> (flags go before the key)")
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

	dump, info, err := openArchive(body, cfg.keyHex, key, *allowUnbound)
	if err != nil {
		return err
	}
	if !info.bound() {
		fmt.Fprintln(os.Stderr, unboundWarning(key, info))
	}
	n, err := pgRestore(ctx, *targetURL, dump)
	if err != nil {
		return err
	}
	fmt.Printf("restore complete: %s -> %s (%d bytes; %s)\n", key, redactURL(*targetURL), n, describeArchive(info))
	return nil
}

// openArchive returns a reader of the pg_dump bytes inside the backup archive
// stored under objectKey, read from r, and what its header says.
//
// A VRSKYBK2 archive (#549) must name objectKey in its header, or it is
// refused before any chunk is decrypted: it is another backup, copied or
// renamed. VRSKYBK1 archives (#430) and older single-seal ones name nothing,
// so anyone who can write to the bucket could have put them there in place of
// a newer backup; they are read only when allowUnbound is set, and the caller
// should warn. A VRSKYBK prefix with an unknown version is refused.
//
// Chunked archives are decrypted and gunzipped as they are read, so memory
// stays flat; the reader returns an error rather than EOF if the archive is
// cut off or altered, so read it to the end. Single-seal archives are read
// into memory to open, as before.
func openArchive(r io.Reader, keyHex, objectKey string, allowUnbound bool) (io.Reader, archiveInfo, error) {
	if objectKey == "" {
		return nil, archiveInfo{}, fmt.Errorf("open backup: no object key to check the archive against")
	}
	br := bufio.NewReader(r)
	// Anything valid, a legacy archive included, is longer than a magic, so a
	// shorter object is damaged, not unbound: don't point at --allow-unbound.
	magic, err := br.Peek(len(backupMagic))
	if err != nil {
		return nil, archiveInfo{}, fmt.Errorf("read backup: object too short to be a backup archive: %w", err)
	}
	format := formatLegacy
	switch {
	case string(magic) == backupMagicV2 || string(magic) == backupMagicV1:
		format = string(magic)
	case bytes.HasPrefix(magic, []byte(backupMagicPrefix)) && len(magic) == len(backupMagic):
		return nil, archiveInfo{}, fmt.Errorf("open backup: unsupported archive version %q; use a newer vrsky-cli", magic)
	}
	if format != backupMagicV2 && !allowUnbound {
		return nil, archiveInfo{format: format}, fmt.Errorf(
			"backup %q is a %s archive, which is not bound to its object key, so nothing shows it is the backup made under that name. "+
				"Backups made since VRSKYBK2 was deployed never need --allow-unbound; if this key is newer than that, the bucket has been tampered with. "+
				"Pass --allow-unbound only to read a backup from before that", objectKey, format)
	}

	var plain io.Reader
	var info archiveInfo
	if format == formatLegacy {
		sealed, err := io.ReadAll(br)
		if err != nil {
			return nil, archiveInfo{}, fmt.Errorf("download backup: %w", err)
		}
		gzipped, err := crypto.DecryptBytes(sealed, keyHex)
		if err != nil {
			return nil, archiveInfo{}, fmt.Errorf("decrypt (wrong ENCRYPTION_KEY?): %w", err)
		}
		plain, info = bytes.NewReader(gzipped), archiveInfo{format: formatLegacy}
	} else {
		dec, hdr, err := newOpenReader(br, keyHex)
		if err != nil {
			return nil, archiveInfo{}, fmt.Errorf("open backup: %w", err)
		}
		if hdr.bound() && hdr.key != objectKey {
			// The header is not authenticated yet, so it only claims a key.
			return nil, hdr, fmt.Errorf("backup %q is not the backup made under that key: its header names %q (copied, renamed or forged)", objectKey, hdr.key)
		}
		plain, info = dec, hdr
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return nil, info, fmt.Errorf("gunzip: %w", err)
	}
	return gz, info, nil
}

// unboundWarning is the line verify and restore print to stderr when they read
// an archive that does not name its object key.
func unboundWarning(objectKey string, info archiveInfo) string {
	return fmt.Sprintf("warning: %q is a %s archive and is not bound to its object key; "+
		"nothing shows it is the backup made under that name rather than an older one copied over it. "+
		"Backups made since VRSKYBK2 was deployed are never in this format", objectKey, info.format)
}

// describeArchive is the format and, for a bound archive, the time it was
// made, for verify's and restore's last line.
func describeArchive(info archiveInfo) string {
	if info.bound() {
		return fmt.Sprintf("%s, made %s", info.format, info.created.Format(time.RFC3339))
	}
	return info.format + ", not bound to its key"
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
