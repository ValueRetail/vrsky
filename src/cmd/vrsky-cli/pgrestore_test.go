package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for pgRestore (#523), written against the precedence documented in
// plans/issue-523-pgrestore-test.md:
//
//  1. the archive read failed           -> "read backup: ..."
//  2. else pg_restore exited non-zero   -> "pg_restore: ..." with its stderr
//  3. else drain the rest of the dump; a read error there is
//     "backup archive failed verification after pg_restore finished: ..."
//
// pg_restore is replaced by a shell script on PATH whose behaviour comes from
// environment variables the child inherits.

const (
	errReadBackup   = "read backup:"
	errPgRestore    = "pg_restore:"
	errVerification = "backup archive failed verification after pg_restore finished"

	testChunk     = 64 << 10 // the default chunk size, given explicitly so layout() works
	testTargetURL = "postgres://restore:secret@db.test:5432/vrsky_restore?sslmode=disable"
)

const fakePgRestoreScript = `#!/bin/sh
if [ -n "$FAKE_PGR_ARGS" ]; then
	: > "$FAKE_PGR_ARGS"
	for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_PGR_ARGS"; done
fi
case "$FAKE_PGR_READ" in
	all) cat >/dev/null ;;
	*) head -c "$FAKE_PGR_READ" >/dev/null ;;
esac
if [ -n "$FAKE_PGR_STDERR" ]; then
	printf '%s\n' "$FAKE_PGR_STDERR" >&2
fi
exit "${FAKE_PGR_EXIT:-0}"
`

// fakePgRestore describes what the fake pg_restore does with its stdin.
type fakePgRestore struct {
	read   int    // bytes of stdin to read before exiting; 0 means all of it
	exit   int    // exit code
	stderr string // written to stderr before exiting
}

func (f fakePgRestore) String() string {
	read := "reads all"
	if f.read > 0 {
		read = "reads " + strconv.Itoa(f.read) + " bytes"
	}
	return read + ", exits " + strconv.Itoa(f.exit)
}

// installFakePgRestore puts the fake first on PATH and returns the file it
// writes its argv to, one argument per line.
func installFakePgRestore(t *testing.T) (argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake pg_restore is a /bin/sh script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pg_restore"), []byte(fakePgRestoreScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	argsFile = filepath.Join(t.TempDir(), "argv")
	t.Setenv("FAKE_PGR_ARGS", argsFile)
	return argsFile
}

// behave sets the fake's behaviour for the next pgRestore call.
func (f fakePgRestore) behave(t *testing.T) {
	t.Helper()
	read := "all"
	if f.read > 0 {
		read = strconv.Itoa(f.read)
	}
	t.Setenv("FAKE_PGR_READ", read)
	t.Setenv("FAKE_PGR_EXIT", strconv.Itoa(f.exit))
	t.Setenv("FAKE_PGR_STDERR", f.stderr)
}

// buildArchive returns a chunked backup archive of dump, as `vrsky-cli backup`
// writes it: gzip, then sealed in 64 KiB chunks.
func buildArchive(t *testing.T, dump []byte, key string) []byte {
	t.Helper()
	return seal(t, gzipBytes(t, dump), key, testChunk)
}

// finalChunkStart returns the offset of the archive's final (shorter) chunk.
func finalChunkStart(t *testing.T, archive []byte, key string) int {
	t.Helper()
	header, record := layout(t, key, testChunk)
	start := header + (len(archive)-header)/record*record
	if start >= len(archive) {
		t.Fatalf("archive of %d bytes ends on a chunk boundary; pick another dump size", len(archive))
	}
	return start
}

// runPgRestore opens archive the way runRestore does and hands it to pgRestore.
func runPgRestore(t *testing.T, archive []byte, key string) (int64, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dump, _, err := openArchive(bytes.NewReader(archive), key, testObjectKey, false)
	if err != nil {
		t.Fatalf("openArchive: %v", err)
	}
	return pgRestore(ctx, testTargetURL, dump)
}

func wantErrPrefix(t *testing.T, err error, prefixes ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("pgRestore succeeded, want an error starting with one of %q", prefixes)
	}
	for _, p := range prefixes {
		if strings.HasPrefix(err.Error(), p) {
			return
		}
	}
	t.Fatalf("pgRestore error = %q, want it to start with one of %q", err, prefixes)
}

// AC 1: a good archive restores, whether pg_restore reads all of its input or
// stops early, and the whole dump is counted either way.
func TestPgRestoreGoodArchive(t *testing.T) {
	installFakePgRestore(t)
	key := randomKey(t)
	dump := randomBytes(t, 300<<10) // several chunks
	archive := buildArchive(t, dump, key)

	for _, fake := range []fakePgRestore{
		{read: 0, exit: 0},
		{read: 64, exit: 0},
	} {
		t.Run(fake.String(), func(t *testing.T) {
			fake.behave(t)
			n, err := runPgRestore(t, archive, key)
			if err != nil {
				t.Fatalf("pgRestore: %v", err)
			}
			if n != int64(len(dump)) {
				t.Errorf("pgRestore read %d bytes, want the full dump of %d", n, len(dump))
			}
		})
	}
}

// AC 2 and 3: a truncated archive fails. When pg_restore read to the end of
// the cut-off input the read error wins, even over a non-zero exit.
func TestPgRestoreTruncatedArchive(t *testing.T) {
	installFakePgRestore(t)
	key := randomKey(t)
	dump := randomBytes(t, 300<<10)
	archive := buildArchive(t, dump, key)
	final := finalChunkStart(t, archive, key)

	cuts := []struct {
		name    string
		archive []byte
	}{
		{"final chunk dropped", archive[:final]},
		{"cut mid-chunk", archive[:final-testChunk/2]},
	}
	fakes := []struct {
		fake fakePgRestore
		want []string
	}{
		// pg_restore saw the whole input, so the copy into it hit the read
		// error before it exited.
		{fakePgRestore{read: 0, exit: 0}, []string{errReadBackup}},
		{fakePgRestore{read: 0, exit: 1, stderr: "pg_restore: error: could not read input file: end of file"}, []string{errReadBackup}},
		// pg_restore stopped early, so the copy stopped short of the cut and
		// the drain afterwards finds it. Seen from outside, the copy reaching
		// the cut first would also be correct, so both are accepted; with a
		// 300 KiB dump it is the drain in practice.
		{fakePgRestore{read: 64, exit: 0}, []string{errReadBackup, errVerification}},
	}

	for _, c := range cuts {
		for _, f := range fakes {
			t.Run(c.name+"/"+f.fake.String(), func(t *testing.T) {
				f.fake.behave(t)
				n, err := runPgRestore(t, c.archive, key)
				wantErrPrefix(t, err, f.want...)
				if n >= int64(len(dump)) {
					t.Errorf("pgRestore read %d bytes from a truncated %d-byte dump", n, len(dump))
				}
			})
		}
	}
}

// AC 4: pg_restore failing on a good archive surfaces its exit and stderr.
func TestPgRestoreExitNonZero(t *testing.T) {
	installFakePgRestore(t)
	key := randomKey(t)
	dump := randomBytes(t, 300<<10)
	archive := buildArchive(t, dump, key)
	const stderr = `pg_restore: error: could not execute query: ERROR:  relation "tenants" already exists`

	for _, fake := range []fakePgRestore{
		{read: 0, exit: 1, stderr: stderr},
		{read: 64, exit: 1, stderr: stderr},
	} {
		t.Run(fake.String(), func(t *testing.T) {
			fake.behave(t)
			_, err := runPgRestore(t, archive, key)
			wantErrPrefix(t, err, errPgRestore)
			if !strings.Contains(err.Error(), stderr) {
				t.Errorf("pgRestore error = %q, want it to contain pg_restore's stderr %q", err, stderr)
			}
			if !strings.Contains(err.Error(), "exit status 1") {
				t.Errorf("pgRestore error = %q, want it to report the exit status", err)
			}
		})
	}
}

// AC 5: a damaged final chunk is caught even when pg_restore exits 0 before
// the copy into its stdin could reach that chunk. The dump is a few MB of
// random bytes, far past the 32 KiB copy buffer plus the OS pipe buffer, so
// the early-exit case can only fail through the drain.
func TestPgRestoreDamagedFinalChunk(t *testing.T) {
	installFakePgRestore(t)
	key := randomKey(t)
	dump := randomBytes(t, 4<<20)
	archive := buildArchive(t, dump, key)
	archive[len(archive)-1] ^= 0xff // last byte of the final chunk's GCM tag

	for _, tc := range []struct {
		fake fakePgRestore
		want string
	}{
		{fakePgRestore{read: 0, exit: 0}, errReadBackup},
		{fakePgRestore{read: 64, exit: 0}, errVerification},
		// pg_restore failed before the damage was reached: its own error
		// wins over the drain, so the operator sees its stderr.
		{fakePgRestore{read: 64, exit: 1, stderr: "pg_restore: error: could not execute query"}, errPgRestore},
	} {
		t.Run(tc.fake.String(), func(t *testing.T) {
			tc.fake.behave(t)
			_, err := runPgRestore(t, archive, key)
			wantErrPrefix(t, err, tc.want)
		})
	}
}

// The drain also checks the gzip trailer: a dump whose CRC is wrong, sealed
// into an otherwise valid archive, fails after pg_restore exits 0 early.
func TestPgRestoreBadGzipChecksum(t *testing.T) {
	installFakePgRestore(t)
	key := randomKey(t)
	gz := gzipBytes(t, randomBytes(t, 4<<20))
	gz[len(gz)-8] ^= 0xff // first byte of the CRC-32 in the gzip trailer
	archive := seal(t, gz, key, testChunk)
	fakePgRestore{read: 64, exit: 0}.behave(t)

	_, err := runPgRestore(t, archive, key)
	wantErrPrefix(t, err, errVerification)
}

// AC 6: pg_restore is run all-or-nothing and cleans the target first.
func TestPgRestoreArgs(t *testing.T) {
	argsFile := installFakePgRestore(t)
	key := randomKey(t)
	archive := buildArchive(t, randomBytes(t, 1<<10), key)
	fakePgRestore{read: 0, exit: 0}.behave(t)

	if _, err := runPgRestore(t, archive, key); err != nil {
		t.Fatalf("pgRestore: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake pg_restore did not record its argv: %v", err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")

	for _, flag := range []string{"--single-transaction", "--clean", "--if-exists"} {
		if !containsArg(args, flag) {
			t.Errorf("pg_restore args %q lack %s", args, flag)
		}
	}
	dbname := -1
	for i, a := range args {
		if a == "--dbname" {
			dbname = i
		}
	}
	switch {
	case dbname < 0:
		t.Errorf("pg_restore args %q lack --dbname", args)
	case dbname+1 >= len(args) || args[dbname+1] != testTargetURL:
		t.Errorf("pg_restore args %q: --dbname not followed by the target URL", args)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
