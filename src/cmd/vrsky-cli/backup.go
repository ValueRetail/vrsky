package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"time"

	"github.com/ValueRetail/vrsky/pkg/objectstore"
)

// runBackup dumps the management DB, gzips + encrypts it, and uploads it to
// object storage under <prefix>management_db-<timestamp>.dump.gz.enc. The dump
// is streamed from pg_dump through gzip and encryption into the upload, so
// memory use does not grow with the size of the database (#430).
func runBackup(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "verify" {
		return runVerify(ctx, args[1:])
	}
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	stamp := fs.String("stamp", "", "override the timestamp segment of the object key (default: current UTC time as 20060102T150405Z)")
	_ = fs.Parse(args)

	cfg, err := backupConfigFromEnv()
	if err != nil {
		return err
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

	// Cancelling ctx kills pg_dump if the upload fails before the dump ends.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// pg_dump in custom format (-Fc) so restore can use pg_restore with
	// --clean --if-exists.
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--dbname", cfg.dbURL)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	waited := false
	wait := func() error {
		waited = true
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("pg_dump: %w: %s", err, errBuf.String())
		}
		return nil
	}

	res, err := streamBackup(ctx, store, key, stdout, wait, cfg.keyHex)
	if !waited {
		// The stream failed before pg_dump's output ended: stop pg_dump and
		// reap it. Its exit status is then the cancellation, not news.
		cancel()
		_ = cmd.Wait()
	}
	if err != nil {
		return err
	}
	fmt.Printf("backup complete: %s (%d bytes encrypted, %d raw)\n", key, res.sealed, res.raw)
	return nil
}

// streamPutter is the part of objectstore.ObjectStore the backup needs.
type streamPutter interface {
	PutStream(ctx context.Context, key string, body io.Reader, contentType string) error
}

type backupResult struct {
	raw    int64 // bytes read from the dump
	sealed int64 // bytes handed to the store
}

// streamBackup gzips and encrypts dump into store under key without buffering
// it whole. wait is called once dump reaches EOF and returns the producer's
// exit status (pg_dump's); the archive's final chunk is written only after
// wait succeeds. Any failure, an empty dump included, reaches the store as a
// read error rather than a clean end of stream, so the store does not commit
// a valid object.
//
// If the upload fails first and dump is an io.Closer, dump is closed so a
// blocked read returns. streamBackup returns only after its goroutine has
// exited; when it returns an error, wait may not have been called and the
// caller must stop the producer itself.
func streamBackup(ctx context.Context, store streamPutter, key string, dump io.Reader, wait func() error, keyHex string) (backupResult, error) {
	pr, pw := io.Pipe()
	sealed := &countingReader{r: pr}
	var raw int64
	done := make(chan error, 1)

	go func() {
		err := sealDump(pw, dump, wait, keyHex, &raw)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		done <- err
	}()

	putErr := store.PutStream(ctx, key, sealed, "application/octet-stream")
	if putErr != nil {
		// The store stopped reading: fail the writer's next write and its
		// next read from dump.
		_ = pr.CloseWithError(putErr)
		if c, ok := dump.(io.Closer); ok {
			_ = c.Close()
		}
	}
	sealErr := <-done
	if putErr == nil && sealErr == nil && !sealed.eof {
		// A store that returns success without reading to the end did not
		// store the whole archive.
		putErr = errors.New("store returned before reading the whole archive")
	}

	res := backupResult{raw: raw, sealed: sealed.n}
	if sealErr != nil {
		return res, sealErr
	}
	if putErr != nil {
		return res, fmt.Errorf("upload backup: %w", putErr)
	}
	return res, nil
}

// sealDump copies dump through gzip and the chunked encryption into w and
// writes the final chunk only after wait reports success.
func sealDump(w io.Writer, dump io.Reader, wait func() error, keyHex string, raw *int64) error {
	enc, err := newSealWriter(w, keyHex)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	gz := gzip.NewWriter(enc)
	n, copyErr := io.Copy(gz, dump)
	*raw = n
	if copyErr != nil {
		return fmt.Errorf("stream dump: %w", copyErr)
	}
	if err := wait(); err != nil {
		return err
	}
	if n == 0 {
		return errors.New("pg_dump produced no output")
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	return nil
}

// countingReader counts the bytes read through it and records whether it hit
// EOF and the first other error.
type countingReader struct {
	r   io.Reader
	n   int64
	eof bool
	err error
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if err == io.EOF {
		c.eof = true
	} else if err != nil && c.err == nil {
		c.err = err
	}
	return n, err
}

// runVerify downloads a backup and decrypts and gunzips it to nowhere, so a
// damaged archive is caught before restore's --clean touches a database.
func runVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup verify", flag.ExitOnError)
	_ = fs.Parse(args)
	key := fs.Arg(0)
	if key == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: vrsky-cli backup verify <backup-key>")
	}
	cfg, err := storeConfigFromEnv()
	if err != nil {
		return err
	}
	store, err := objectstore.New(ctx, cfg.store)
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}
	n, err := verifyBackup(ctx, store, key, cfg.keyHex)
	if err != nil {
		return fmt.Errorf("backup %q is damaged or unreadable: %w", key, err)
	}
	fmt.Printf("backup ok: %s (%d bytes of dump)\n", key, n)
	return nil
}

// streamGetter is the part of objectstore.ObjectStore that reading a backup
// needs.
type streamGetter interface {
	GetStream(ctx context.Context, key string) (io.ReadCloser, string, error)
}

// verifyBackup reads the backup under key to the end, through decryption and
// gunzip, and returns the dump size. Any damage is an error.
func verifyBackup(ctx context.Context, store streamGetter, key, keyHex string) (int64, error) {
	body, _, err := store.GetStream(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}
	defer body.Close()
	dump, err := openArchive(body, keyHex)
	if err != nil {
		return 0, err
	}
	return io.Copy(io.Discard, dump)
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
