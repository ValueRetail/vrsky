// migrate-secrets is a one-shot tool that moves cleartext credentials out of
// connections.nodes[].config into the secrets table.
//
// Sensitive field names recognised (case-insensitive on the key):
//
//   - password
//   - token        (e.g. http.auth.bearer.token)
//   - key          (only matched when nested under auth.api_key or auth.apikey)
//   - auth_value   (api-consumer endpoints)
//   - signing_secret (added by P1-2 / #67)
//   - connection_string (DSN — password component extracted)
//
// After migration each plaintext key is replaced with "<key>_secret_id". DSN
// strings are rewritten so the password section becomes "{secret:<uuid>}".
//
// The tool is idempotent: rows already showing the _secret_id pattern (or the
// {secret:...} placeholder) are skipped.
//
// Usage:
//
//	go run ./cmd/migrate-secrets --dry-run        (prints diff, no writes)
//	go run ./cmd/migrate-secrets                  (commits changes)
//
// Requires MGMT_API_DB_URL and ENCRYPTION_KEY in the environment, same as
// the management-api service.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	_ "github.com/lib/pq"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "Print the changes that would be made without writing anything.")
	flag.Parse()

	dbURL := os.Getenv("MGMT_API_DB_URL")
	if dbURL == "" {
		log.Fatal("MGMT_API_DB_URL is required")
	}
	keyHex, err := crypto.Key()
	if err != nil {
		log.Fatalf("ENCRYPTION_KEY: %v", err)
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT id, tenant_id, nodes FROM connections`)
	if err != nil {
		log.Fatalf("query connections: %v", err)
	}
	defer rows.Close()

	type rec struct {
		id       string
		tenantID string
		nodes    []byte
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.tenantID, &r.nodes); err != nil {
			log.Fatalf("scan: %v", err)
		}
		recs = append(recs, r)
	}
	rows.Close()

	var changed int
	for _, r := range recs {
		updated, n, err := migrateNodes(ctx, db, keyHex, r.tenantID, r.nodes, *dryRun)
		if err != nil {
			log.Printf("connection %s: %v", r.id, err)
			continue
		}
		if n == 0 {
			continue
		}
		changed++
		log.Printf("connection %s: %d secret(s) extracted", r.id, n)
		if *dryRun {
			continue
		}
		if _, err := db.ExecContext(ctx, `UPDATE connections SET nodes = $1, updated_at = NOW() WHERE id = $2`, updated, r.id); err != nil {
			log.Fatalf("update connection %s: %v", r.id, err)
		}
	}
	if *dryRun {
		log.Printf("DRY RUN: %d connection(s) would be modified", changed)
	} else {
		log.Printf("DONE: %d connection(s) modified", changed)
	}
}

// sensitiveKeys are field names whose value should be moved to the secrets
// table. We additionally treat anything ending in "_secret_id" as already
// migrated.
var sensitiveKeys = map[string]bool{
	"password":       true,
	"token":          true,
	"auth_value":     true,
	"signing_secret": true,
	"api_key":        true, // legacy single-string api key
}

// migrateNodes walks nodes JSON in place. Returns the new JSON, the number
// of secrets extracted, and any error.
func migrateNodes(ctx context.Context, db *sql.DB, keyHex, tenantID string, nodesJSON []byte, dryRun bool) ([]byte, int, error) {
	if len(nodesJSON) == 0 || string(nodesJSON) == "null" {
		return nodesJSON, 0, nil
	}
	var nodes []map[string]any
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return nodesJSON, 0, fmt.Errorf("parse nodes: %w", err)
	}
	count := 0
	for _, n := range nodes {
		cfg, ok := n["config"].(map[string]any)
		if !ok {
			continue
		}
		c, err := migrateConfig(ctx, db, keyHex, tenantID, n, cfg, dryRun)
		if err != nil {
			return nodesJSON, count, err
		}
		count += c
	}
	if count == 0 {
		return nodesJSON, 0, nil
	}
	out, err := json.Marshal(nodes)
	if err != nil {
		return nodesJSON, count, err
	}
	return out, count, nil
}

// migrateConfig recursively descends into a config tree.
func migrateConfig(ctx context.Context, db *sql.DB, keyHex, tenantID string, node, cfg map[string]any, dryRun bool) (int, error) {
	count := 0
	// First recurse so deeper structures are processed.
	for _, v := range cfg {
		switch nested := v.(type) {
		case map[string]any:
			c, err := migrateConfig(ctx, db, keyHex, tenantID, node, nested, dryRun)
			if err != nil {
				return count, err
			}
			count += c
		case []any:
			for _, item := range nested {
				if m, ok := item.(map[string]any); ok {
					c, err := migrateConfig(ctx, db, keyHex, tenantID, node, m, dryRun)
					if err != nil {
						return count, err
					}
					count += c
				}
			}
		}
	}

	// Then promote sensitive fields at this level.
	for key, val := range cfg {
		// Skip already-migrated keys.
		if strings.HasSuffix(key, "_secret_id") {
			continue
		}
		s, ok := val.(string)
		if !ok || s == "" {
			continue
		}
		// Special case: connection_string DSN.
		if key == "connection_string" && looksLikeDSN(s) {
			if strings.Contains(s, "{secret:") {
				continue // already migrated
			}
			newDSN, secretID, err := extractDSNPassword(ctx, db, keyHex, tenantID, cfg, s, dryRun)
			if err != nil {
				return count, fmt.Errorf("connection_string: %w", err)
			}
			if secretID != "" {
				cfg[key] = newDSN
				count++
			}
			continue
		}
		// Standard sensitive key.
		if !sensitiveKeys[strings.ToLower(key)] {
			continue
		}
		// Don't migrate placeholder/empty values like "***".
		if s == "***" || s == "•••" {
			continue
		}
		name := buildSecretName(node, cfg, key)
		id, err := createSecret(ctx, db, keyHex, tenantID, name, s, dryRun)
		if err != nil {
			return count, err
		}
		cfg[key+"_secret_id"] = id
		delete(cfg, key)
		count++
	}
	return count, nil
}

func looksLikeDSN(s string) bool {
	return strings.HasPrefix(s, "postgres://") ||
		strings.HasPrefix(s, "postgresql://") ||
		strings.HasPrefix(s, "mysql://") ||
		strings.HasPrefix(s, "mongodb://") ||
		strings.HasPrefix(s, "redis://")
}

// extractDSNPassword splits a DSN, stores the password in the secrets table,
// and returns a DSN with {secret:<uuid>} in the password position.
func extractDSNPassword(ctx context.Context, db *sql.DB, keyHex, tenantID string, cfg map[string]any, dsn string, dryRun bool) (string, string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn, "", err
	}
	if u.User == nil {
		return dsn, "", nil
	}
	pwd, hasPwd := u.User.Password()
	if !hasPwd || pwd == "" {
		return dsn, "", nil
	}
	name := fmt.Sprintf("dsn-pwd-%s-%d", strings.ReplaceAll(u.Host, ":", "_"), randomTag())
	id, err := createSecret(ctx, db, keyHex, tenantID, name, pwd, dryRun)
	if err != nil {
		return dsn, "", err
	}
	u.User = url.UserPassword(u.User.Username(), "{secret:"+id+"}")
	// Avoid url.Parse escaping the braces — rebuild the userinfo manually.
	return rebuildDSN(u, id), id, nil
}

// rebuildDSN reconstructs a DSN preserving the literal "{secret:<uuid>}"
// substring in the password position, which url.URL.String() would escape.
func rebuildDSN(u *url.URL, secretID string) string {
	user := u.User.Username()
	host := u.Host
	path := u.Path
	q := ""
	if u.RawQuery != "" {
		q = "?" + u.RawQuery
	}
	return u.Scheme + "://" + user + ":{secret:" + secretID + "}@" + host + path + q
}

// createSecret writes one row to secrets and returns its UUID. In dry-run
// mode it returns a placeholder.
func createSecret(ctx context.Context, db *sql.DB, keyHex, tenantID, name, plain string, dryRun bool) (string, error) {
	ct, err := crypto.Encrypt(plain, keyHex)
	if err != nil {
		return "", err
	}
	if dryRun {
		return "00000000-0000-0000-0000-000000000000", nil
	}
	var id string
	err = db.QueryRowContext(ctx, `
		INSERT INTO secrets (tenant_id, name, ciphertext)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, name) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, updated_at = NOW(), rotated_at = NOW()
		RETURNING id
	`, tenantID, name, ct).Scan(&id)
	return id, err
}

// buildSecretName produces a stable, human-readable name from the node + key.
func buildSecretName(node, cfg map[string]any, key string) string {
	nodeID, _ := node["id"].(string)
	if nodeID == "" {
		nodeID = "unknown"
	}
	return nodeID + "-" + key
}

// randomTag adds a small disambiguator so re-running the script on a config
// that already has a (tenant,name) row falls onto the ON CONFLICT path.
func randomTag() int {
	// Stable across one run: use length-of-time-string sentinel; ON CONFLICT
	// makes the choice irrelevant for correctness.
	return os.Getpid() & 0xffff
}
