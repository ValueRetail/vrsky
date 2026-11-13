package harness

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// LeakPlaceholder is a {secret:} placeholder for LeakSecretID, for a test that
// puts it in a setting that is not a credential (#853).
const LeakPlaceholder = "{secret:" + LeakSecretID + "}"

// LeakSecretsDB is a mock management database whose secrets table hands out
// LeakSecret for LeakSecretID, as often as asked, with the encryption key set.
// A connector that resolved a placeholder or reference it should refuse or
// drop would get the value, so a test can check it shows up nowhere.
// Expectations are matched in any order; add the connection query the
// connector makes.
func LeakSecretsDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyHex := hex.EncodeToString(key)
	t.Setenv(crypto.KeyEnvVar, keyHex)
	ct, err := crypto.Encrypt(LeakSecret, keyHex)
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 8; i++ {
		mock.ExpectQuery(`FROM secrets`).WithArgs(LeakSecretID, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"ciphertext"}).AddRow(ct))
	}
	return db, mock
}
