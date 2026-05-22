package crypto

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// suffix appended to a config key to indicate the plaintext value lives in
// the secrets table. Example: {"password_secret_id": "<uuid>"}.
const SecretIDSuffix = "_secret_id"

// dsnPlaceholder matches "{secret:<uuid>}" tokens embedded in connection
// strings. We use this for DB DSNs where extracting the password into a
// separate field would require schema changes across every DB connector.
var dsnPlaceholder = regexp.MustCompile(`\{secret:([0-9a-fA-F-]{36})\}`)

// SecretReader is the minimum surface a worker needs from the management DB
// to resolve secret references. It is satisfied by *sql.DB through the
// helper sqlSecretReader.
type SecretReader interface {
	GetCiphertext(ctx context.Context, tenantID, id string) (string, error)
}

// NewSQLSecretReader wraps *sql.DB to satisfy SecretReader. Workers already
// hold a *sql.DB to read connections; reusing it avoids a Management-API
// round-trip on each pipeline start.
func NewSQLSecretReader(db *sql.DB) SecretReader {
	return sqlSecretReader{db: db}
}

type sqlSecretReader struct {
	db *sql.DB
}

func (s sqlSecretReader) GetCiphertext(ctx context.Context, tenantID, id string) (string, error) {
	var ct string
	err := s.db.QueryRowContext(ctx, `
		SELECT ciphertext FROM secrets WHERE id = $1 AND tenant_id = $2
	`, id, tenantID).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("secret %s not found for tenant %s", id, tenantID)
	}
	return ct, err
}

// ResolveSecrets walks an arbitrary config tree (decoded JSON: maps, slices,
// primitives) and replaces every "<key>_secret_id" entry with a plaintext
// "<key>" entry. DSN placeholders like "{secret:<uuid>}" inside string values
// are also expanded.
//
// The function mutates config in place. Calling it twice is a no-op the
// second time (the _secret_id keys are removed once resolved).
func ResolveSecrets(ctx context.Context, reader SecretReader, tenantID string, config any) error {
	return resolveNode(ctx, reader, tenantID, config, keyHexFromEnv)
}

// ResolveSecretsWithKey is like ResolveSecrets but lets the caller pass an
// explicit master key (useful in tests).
func ResolveSecretsWithKey(ctx context.Context, reader SecretReader, tenantID string, config any, keyHex string) error {
	return resolveNode(ctx, reader, tenantID, config, func() (string, error) {
		return keyHex, nil
	})
}

// ResolveSecretsInJSON is a convenience for workers that operate on
// json.RawMessage (the shape of nodes[].config in the connections table).
// It parses, resolves, and re-serialises; workers can then unmarshal into
// their typed configs as usual and find plaintext fields populated.
//
// If the input is null or doesn't decode into an object/array, it is
// returned unchanged.
func ResolveSecretsInJSON(ctx context.Context, reader SecretReader, tenantID string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return raw, err
	}
	if err := ResolveSecrets(ctx, reader, tenantID, generic); err != nil {
		return raw, err
	}
	out, err := json.Marshal(generic)
	if err != nil {
		return raw, err
	}
	return out, nil
}

func keyHexFromEnv() (string, error) {
	return Key()
}

func resolveNode(ctx context.Context, reader SecretReader, tenantID string, node any, keyFn func() (string, error)) error {
	switch v := node.(type) {
	case map[string]any:
		return resolveMap(ctx, reader, tenantID, v, keyFn)
	case []any:
		for _, item := range v {
			if err := resolveNode(ctx, reader, tenantID, item, keyFn); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveMap(ctx context.Context, reader SecretReader, tenantID string, m map[string]any, keyFn func() (string, error)) error {
	for key, val := range m {
		// Recurse into nested objects and arrays first.
		switch v := val.(type) {
		case map[string]any:
			if err := resolveMap(ctx, reader, tenantID, v, keyFn); err != nil {
				return err
			}
		case []any:
			for _, item := range v {
				if err := resolveNode(ctx, reader, tenantID, item, keyFn); err != nil {
					return err
				}
			}
		}

		// Resolve `<field>_secret_id` references.
		if strings.HasSuffix(key, SecretIDSuffix) {
			id, ok := val.(string)
			if !ok || id == "" {
				continue
			}
			plain, err := fetchAndDecrypt(ctx, reader, tenantID, id, keyFn)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", key, err)
			}
			plainKey := strings.TrimSuffix(key, SecretIDSuffix)
			m[plainKey] = plain
			delete(m, key)
			continue
		}

		// Expand any {secret:<uuid>} placeholders embedded in strings.
		if s, ok := val.(string); ok && strings.Contains(s, "{secret:") {
			expanded, err := expandPlaceholders(ctx, reader, tenantID, s, keyFn)
			if err != nil {
				return fmt.Errorf("expand placeholder in %s: %w", key, err)
			}
			m[key] = expanded
		}
	}
	return nil
}

func fetchAndDecrypt(ctx context.Context, reader SecretReader, tenantID, id string, keyFn func() (string, error)) (string, error) {
	ct, err := reader.GetCiphertext(ctx, tenantID, id)
	if err != nil {
		return "", err
	}
	keyHex, err := keyFn()
	if err != nil {
		return "", err
	}
	return Decrypt(ct, keyHex)
}

func expandPlaceholders(ctx context.Context, reader SecretReader, tenantID, in string, keyFn func() (string, error)) (string, error) {
	matches := dsnPlaceholder.FindAllStringSubmatchIndex(in, -1)
	if len(matches) == 0 {
		return in, nil
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		idStart, idEnd := m[2], m[3]
		b.WriteString(in[last:start])
		id := in[idStart:idEnd]
		plain, err := fetchAndDecrypt(ctx, reader, tenantID, id, keyFn)
		if err != nil {
			return "", err
		}
		b.WriteString(plain)
		last = end
	}
	b.WriteString(in[last:])
	return b.String(), nil
}
