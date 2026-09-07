package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Master-key rotation (docs/SECURITY.md, "Rotate the master key itself").
//
// Every credential the platform stores is AES-256-GCM ciphertext in ONE column,
// secrets.ciphertext. Everything else — oauth_grants.access_token_secret_id,
// oauth_providers.client_secret_id, notification_targets.secret_id, the OIDC
// client secret, the `<field>_secret_id` refs inside connections.nodes — holds
// a UUID pointing at that row, not the ciphertext itself. So rotating the master
// key means re-encrypting exactly one column, and rewrap does not need to walk
// the rest of the schema. (Verified against every crypto.Encrypt call site; the
// 000009/000013/000015 migrations say the same in their headers.)
//
// Usage:
//
//	ENCRYPTION_KEY_PREVIOUS=<old> ENCRYPTION_KEY=<new> \
//	    go run ./cmd/migrate-secrets --rewrap --dry-run
//	ENCRYPTION_KEY_PREVIOUS=<old> ENCRYPTION_KEY=<new> \
//	    go run ./cmd/migrate-secrets --rewrap
//
// Run it BEFORE the new key reaches any service: a worker holding the new key
// cannot read rows still sealed with the old one. The safe order is
//
//  1. --rewrap --dry-run, confirm the counts
//  2. --rewrap
//  3. roll the new ENCRYPTION_KEY out to management-api and every connector
//
// CAVEAT the tool cannot fix: `vrsky-cli backup` seals its archives with the
// master key too (crypto.EncryptBytes). Backups taken before a rotation are
// readable only with the OLD key, so keep it until those backups have aged out.

// keyPrevEnvVar names the old key. Deliberately distinct from crypto.KeyEnvVar
// so a rewrap can never be run with one key supplied twice.
const keyPrevEnvVar = "ENCRYPTION_KEY_PREVIOUS"

// rewrapStats reports what a run did, so --dry-run and a live run print the
// same shape and can be compared.
type rewrapStats struct {
	Total      int // rows examined
	Rewrapped  int // decrypted with the old key, re-sealed with the new
	AlreadyNew int // already readable with the new key — a resumed run
	Plaintext  int // no aes256: prefix; sealed with the new key
	Skipped    int // empty ciphertext — nothing to rotate
}

// runRewrap re-encrypts every secret from the previous master key to the
// current one, inside a single transaction: a partial rewrap would leave some
// rows readable only by the old key and some only by the new, with no single
// key that opens the whole table.
func runRewrap(ctx context.Context, db *sql.DB, dryRun bool) error {
	newKey, err := crypto.Key()
	if err != nil {
		return fmt.Errorf("%s: %w", crypto.KeyEnvVar, err)
	}
	oldKey := os.Getenv(keyPrevEnvVar)
	if oldKey == "" {
		return fmt.Errorf("%s is required for --rewrap (the key the rows are currently sealed with)", keyPrevEnvVar)
	}
	if oldKey == newKey {
		return fmt.Errorf("%s and %s are identical — nothing to rewrap; if you meant to rotate, generate a new key first",
			keyPrevEnvVar, crypto.KeyEnvVar)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	// FOR UPDATE holds the rows for the transaction's life, so a secret written
	// through the API mid-rewrap cannot be sealed with the old key after we have
	// already passed it.
	rows, err := tx.QueryContext(ctx, `SELECT id, tenant_id, ciphertext FROM secrets ORDER BY id FOR UPDATE`)
	if err != nil {
		return fmt.Errorf("select secrets: %w", err)
	}

	type row struct{ id, tenantID, ct string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.tenantID, &r.ct); err != nil {
			rows.Close()
			return fmt.Errorf("scan: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate secrets: %w", err)
	}
	rows.Close()

	var st rewrapStats
	st.Total = len(all)

	for _, r := range all {
		plain, kind, err := recoverPlaintext(r.ct, oldKey, newKey)
		if err != nil {
			// Readable by neither key. Abort rather than skip: continuing would
			// commit a table in two states, and the operator needs to know
			// before the old key is discarded.
			return fmt.Errorf("secret %s (tenant %s) cannot be decrypted with either key: %w — "+
				"aborting with no changes written; do NOT discard %s until this is resolved",
				r.id, r.tenantID, err, keyPrevEnvVar)
		}

		switch kind {
		case sealedWithNew:
			// A previous run already moved this row. Leave it alone: re-sealing
			// would churn the nonce for no gain.
			st.AlreadyNew++
			continue
		case wasPlaintext:
			if r.ct == "" {
				// An empty ciphertext column is a data bug, not a secret.
				// crypto.Encrypt("") returns "", so "sealing" it would write
				// the same empty string back and report a rotation that did
				// not happen. Surface it instead.
				st.Skipped++
				log.Printf("secret %s (tenant %s): ciphertext is EMPTY — left untouched; there is nothing to rotate, investigate separately",
					r.id, r.tenantID)
				continue
			}
			st.Plaintext++
			log.Printf("secret %s (tenant %s): stored WITHOUT the %s prefix — sealing it now",
				r.id, r.tenantID, crypto.Prefix)
		case sealedWithOld:
			st.Rewrapped++
		}

		if dryRun {
			continue
		}

		resealed, err := crypto.Encrypt(plain, newKey)
		if err != nil {
			return fmt.Errorf("re-encrypt secret %s: %w", r.id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE secrets SET ciphertext = $1, rotated_at = NOW(), updated_at = NOW() WHERE id = $2`,
			resealed, r.id,
		); err != nil {
			return fmt.Errorf("update secret %s: %w", r.id, err)
		}
	}

	if dryRun {
		log.Printf("DRY RUN: %d secret(s) examined — %d would be rewrapped, %d already on the new key, %d plaintext would be sealed, %d empty and skipped",
			st.Total, st.Rewrapped, st.AlreadyNew, st.Plaintext, st.Skipped)
		return nil // deferred Rollback discards the (empty) transaction
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	log.Printf("DONE: %d secret(s) examined — %d rewrapped, %d already on the new key, %d plaintext sealed, %d empty and skipped",
		st.Total, st.Rewrapped, st.AlreadyNew, st.Plaintext, st.Skipped)
	log.Printf("Now roll %s out to management-api and every connector; until then they cannot read these rows.", crypto.KeyEnvVar)
	return nil
}

// ctKind describes which key a stored value turned out to need.
type ctKind int

const (
	sealedWithOld ctKind = iota
	sealedWithNew
	wasPlaintext
)

// recoverPlaintext returns the plaintext behind a stored value and how it was
// stored.
//
// The NEW key is tried first so a re-run after an interrupted rotation is a
// no-op rather than a double-encrypt. GCM authenticates, so a wrong key fails
// rather than returning garbage — which is what makes "try one, then the other"
// safe here.
func recoverPlaintext(stored, oldKey, newKey string) (string, ctKind, error) {
	// crypto.Decrypt passes non-prefixed values straight through, so an
	// unprefixed row would "decrypt" under any key. Check the prefix first,
	// or plaintext would be misreported as already-rotated.
	if !crypto.IsCiphertext(stored) {
		return stored, wasPlaintext, nil
	}
	if plain, err := crypto.Decrypt(stored, newKey); err == nil {
		return plain, sealedWithNew, nil
	}
	plain, err := crypto.Decrypt(stored, oldKey)
	if err != nil {
		return "", 0, err
	}
	return plain, sealedWithOld, nil
}
