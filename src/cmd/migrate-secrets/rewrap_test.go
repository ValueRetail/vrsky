package main

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Two distinct valid 32-byte keys. Rewrap is the one place in the codebase
// where two master keys are live at once, so the tests need both.
const (
	oldKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	newKeyHex = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

func sealed(t *testing.T, plain, keyHex string) string {
	t.Helper()
	ct, err := crypto.Encrypt(plain, keyHex)
	if err != nil {
		t.Fatalf("seal %q: %v", plain, err)
	}
	return ct
}

// setKeys puts both keys in the environment for one test.
func setKeys(t *testing.T, oldKey, newKey string) {
	t.Helper()
	t.Setenv(crypto.KeyEnvVar, newKey)
	t.Setenv(keyPrevEnvVar, oldKey)
}

// capture is a sqlmock argument matcher that accepts anything and records what
// it saw, so a test can assert on the ciphertext that was actually written
// rather than on sqlmock.AnyArg().
type capture struct{ got *[]string }

func (c capture) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	*c.got = append(*c.got, s)
	return true
}

func secretRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "tenant_id", "ciphertext"})
}

// TestRecoverPlaintext covers the classification that decides whether a row is
// rewrapped, skipped, or fatal.
func TestRecoverPlaintext(t *testing.T) {
	oldCT := sealed(t, "sitoo-api-token", oldKeyHex)
	newCT := sealed(t, "sitoo-api-token", newKeyHex)

	t.Run("sealed with the old key is rewrappable", func(t *testing.T) {
		plain, kind, err := recoverPlaintext(oldCT, oldKeyHex, newKeyHex)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if kind != sealedWithOld {
			t.Errorf("kind = %v, want sealedWithOld", kind)
		}
		if plain != "sitoo-api-token" {
			t.Errorf("plain = %q, want the original secret", plain)
		}
	})

	t.Run("sealed with the new key is already done", func(t *testing.T) {
		// This is what a re-run after a partial failure sees. It must NOT be
		// treated as old-key material and encrypted a second time.
		_, kind, err := recoverPlaintext(newCT, oldKeyHex, newKeyHex)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if kind != sealedWithNew {
			t.Errorf("kind = %v, want sealedWithNew", kind)
		}
	})

	t.Run("unprefixed plaintext is not mistaken for rotated", func(t *testing.T) {
		// The trap this guards: crypto.Decrypt returns a non-prefixed value
		// as-is with a nil error, so a plaintext row "decrypts" under the new
		// key. Without the IsCiphertext check in recoverPlaintext this returns
		// sealedWithNew, rewrap skips the row, and a cleartext credential
		// survives a rotation that reports success.
		plain, kind, err := recoverPlaintext("hunter2", oldKeyHex, newKeyHex)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if kind != wasPlaintext {
			t.Fatalf("kind = %v, want wasPlaintext — a cleartext row would be silently skipped", kind)
		}
		if plain != "hunter2" {
			t.Errorf("plain = %q, want the value as stored", plain)
		}
	})

	t.Run("readable by neither key is an error", func(t *testing.T) {
		thirdKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		orphan := sealed(t, "written-under-a-lost-key", thirdKey)
		if _, _, err := recoverPlaintext(orphan, oldKeyHex, newKeyHex); err == nil {
			t.Fatal("want an error for a row neither key opens, got nil")
		}
	})
}

// TestRunRewrap_ResealsEveryRow is the happy path: every row moves from the old
// key to the new one, in one committed transaction.
func TestRunRewrap_ResealsEveryRow(t *testing.T) {
	setKeys(t, oldKeyHex, newKeyHex)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	var written []string
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id, ciphertext FROM secrets").
		WillReturnRows(secretRows().
			AddRow("sec-1", "tenant-a", sealed(t, "db-password", oldKeyHex)).
			AddRow("sec-2", "tenant-b", sealed(t, "bearer-token", oldKeyHex)))
	mock.ExpectExec("UPDATE secrets SET ciphertext").
		WithArgs(capture{&written}, "sec-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE secrets SET ciphertext").
		WithArgs(capture{&written}, "sec-2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := runRewrap(context.Background(), db, false); err != nil {
		t.Fatalf("runRewrap: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}

	if len(written) != 2 {
		t.Fatalf("wrote %d ciphertexts, want 2", len(written))
	}
	for i, want := range []string{"db-password", "bearer-token"} {
		// Readable with the new key...
		got, err := crypto.Decrypt(written[i], newKeyHex)
		if err != nil {
			t.Fatalf("row %d: new key cannot decrypt what was written: %v", i, err)
		}
		if got != want {
			t.Errorf("row %d: decrypted %q, want %q — the plaintext changed during rewrap", i, got, want)
		}
		// ...and no longer with the old one. Without this half, a rewrap that
		// wrote the row back untouched would pass the check above.
		if _, err := crypto.Decrypt(written[i], oldKeyHex); err == nil {
			t.Errorf("row %d: still decrypts with the OLD key — it was not actually rewrapped", i)
		}
	}
}

// TestRunRewrap_DryRunWritesNothing: --dry-run must report the same counts
// without touching a row. sqlmock fails the test if an unexpected Exec fires.
func TestRunRewrap_DryRunWritesNothing(t *testing.T) {
	setKeys(t, oldKeyHex, newKeyHex)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id, ciphertext FROM secrets").
		WillReturnRows(secretRows().
			AddRow("sec-1", "tenant-a", sealed(t, "db-password", oldKeyHex)))
	// No ExpectExec, and a rollback rather than a commit.
	mock.ExpectRollback()

	if err := runRewrap(context.Background(), db, true); err != nil {
		t.Fatalf("runRewrap dry-run: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestRunRewrap_ResumeSkipsRowsAlreadyOnTheNewKey covers re-running after an
// interrupted rotation: rows the previous run moved are left alone, and only
// the stragglers are written.
func TestRunRewrap_ResumeSkipsRowsAlreadyOnTheNewKey(t *testing.T) {
	setKeys(t, oldKeyHex, newKeyHex)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	var written []string
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id, ciphertext FROM secrets").
		WillReturnRows(secretRows().
			AddRow("sec-done", "tenant-a", sealed(t, "already-moved", newKeyHex)).
			AddRow("sec-todo", "tenant-a", sealed(t, "still-old", oldKeyHex)))
	// Only sec-todo is updated. An expectation on sec-done would be unmet, and
	// an Exec for it would be unexpected — either way the test fails.
	mock.ExpectExec("UPDATE secrets SET ciphertext").
		WithArgs(capture{&written}, "sec-todo").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := runRewrap(context.Background(), db, false); err != nil {
		t.Fatalf("runRewrap: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("wrote %d rows, want 1 (the already-rotated row must not be re-sealed)", len(written))
	}
}

// TestRunRewrap_UndecryptableRowAbortsWholeRun: a row neither key opens must
// roll the transaction back, not commit a half-rotated table.
func TestRunRewrap_UndecryptableRowAbortsWholeRun(t *testing.T) {
	setKeys(t, oldKeyHex, newKeyHex)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id, ciphertext FROM secrets").
		WillReturnRows(secretRows().
			AddRow("sec-bad", "tenant-a", crypto.Prefix+"!!!not-base64!!!").
			AddRow("sec-good", "tenant-a", sealed(t, "fine", oldKeyHex)))
	// No Exec, no Commit — the deferred Rollback is the only DB call left.
	mock.ExpectRollback()

	err = runRewrap(context.Background(), db, false)
	if err == nil {
		t.Fatal("want an error for an undecryptable row, got nil")
	}
	// The operator has to be able to find the row and must not throw the old
	// key away while it is unresolved.
	if !strings.Contains(err.Error(), "sec-bad") || !strings.Contains(err.Error(), keyPrevEnvVar) {
		t.Errorf("error does not name the row and the old-key var: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestRunRewrap_RejectsBadKeyPairs: every guard must fire before the
// transaction opens. sqlmock has no expectations registered, so any DB call
// these paths make is itself a failure.
func TestRunRewrap_RejectsBadKeyPairs(t *testing.T) {
	cases := []struct {
		name        string
		oldKey      string
		newKey      string
		wantErrPart string
	}{
		{"no previous key", "", newKeyHex, keyPrevEnvVar},
		{"no current key", oldKeyHex, "", crypto.KeyEnvVar},
		// The dangerous one: rotating "to" the key already in use would report
		// a successful rotation while the compromised key still opens every row.
		{"both keys identical", oldKeyHex, oldKeyHex, "identical"},
		{"malformed current key", oldKeyHex, "not-hex", crypto.KeyEnvVar},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setKeys(t, tc.oldKey, tc.newKey)

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			err = runRewrap(context.Background(), db, false)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrPart) {
				t.Errorf("error %q does not mention %q", err, tc.wantErrPart)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("the DB was touched before the keys were validated: %v", err)
			}
		})
	}
}

// TestRunRewrap_EmptyCiphertextIsReportedNotSealed: crypto.Encrypt("") returns
// "", so a blank ciphertext column cannot be rotated. It must be surfaced, not
// written back as another empty string and counted as sealed.
func TestRunRewrap_EmptyCiphertextIsReportedNotSealed(t *testing.T) {
	setKeys(t, oldKeyHex, newKeyHex)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id, ciphertext FROM secrets").
		WillReturnRows(secretRows().AddRow("sec-empty", "tenant-a", ""))
	mock.ExpectCommit()

	if err := runRewrap(context.Background(), db, false); err != nil {
		t.Fatalf("runRewrap: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
