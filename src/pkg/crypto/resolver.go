package crypto

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// suffix appended to a config key to indicate the plaintext value lives in
// the secrets table. Example: {"password_secret_id": "<uuid>"}.
const SecretIDSuffix = "_secret_id"

// placeholderMarker starts an inline reference, "{secret:<uuid>}". Any string
// holding it counts as a placeholder for the allowlist, well-formed or not.
const placeholderMarker = "{secret:"

// placeholderRE matches a well-formed "{secret:<uuid>}" token, the form that
// is expanded.
var placeholderRE = regexp.MustCompile(`\{secret:([0-9a-fA-F-]{36})\}`)

// errSecretNotFound is the fixed text for a reference to a secret the tenant
// does not have. It says nothing of the id or the tenant: it reaches retry
// errors and the DLQ header (#853).
var errSecretNotFound = errors.New("stored secret not found")

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
		return "", errSecretNotFound
	}
	return ct, err
}

// ResolveNodeSecrets resolves the stored-secret references in one node
// config ({"type": "sftp", "sftp": {…}}), but only in the fields its type
// lists as credentials (secretFields):
//
//   - "<field>_secret_id" becomes "<field>" with the secret's value when
//     <field> is on the list. Anywhere else the reference is dropped,
//     unresolved, so the setting reads as absent.
//   - a "{secret:<uuid>}" placeholder is expanded where the list takes one
//     inline. Anywhere else it is an error that names the path, never a
//     value.
//
// A secret's value can therefore only land in a credential setting, never in
// one a connector quotes in an error or sends on as data (#853).
//
// The type is read from the config. With none, or two keys that differ only
// in case, no field is a credential. A config without references is returned
// as it is; otherwise re-encoded with its keys sorted.
func ResolveNodeSecrets(ctx context.Context, reader SecretReader, tenantID string, nodeConfig json.RawMessage) (json.RawMessage, error) {
	return resolveJSON(ctx, reader, tenantID, nodeConfig, nil, nodeConfigType(nodeConfig), keyHexFromEnv)
}

// ResolveBlockSecrets is ResolveNodeSecrets for a connector's settings block
// on its own (the body of a test or sample-data request): block is what sits
// under configType in a node config.
func ResolveBlockSecrets(ctx context.Context, reader SecretReader, tenantID, configType string, block json.RawMessage) (json.RawMessage, error) {
	return resolveJSON(ctx, reader, tenantID, block, []string{configType}, configType, keyHexFromEnv)
}

// CheckNodeSecrets reports the first placeholder in a node config that
// ResolveNodeSecrets would refuse, without reading any secret. A stray
// "<field>_secret_id" is not reported: it is dropped, never resolved.
func CheckNodeSecrets(nodeConfig json.RawMessage) error {
	_, err := resolveJSON(context.Background(), nil, "", nodeConfig, nil, nodeConfigType(nodeConfig), nil)
	return err
}

// CheckBlockSecrets is CheckNodeSecrets for a settings block on its own.
func CheckBlockSecrets(configType string, block json.RawMessage) error {
	_, err := resolveJSON(context.Background(), nil, "", block, []string{configType}, configType, nil)
	return err
}

// secretFieldError is a placeholder where none may be. It names the setting,
// never the value.
type secretFieldError struct {
	path       string
	credential bool // the path takes a stored secret, as <path>_secret_id
}

func (e *secretFieldError) Error() string {
	if e.credential {
		return fmt.Sprintf("%s holds a {secret:…} placeholder; reference the stored secret as %s%s instead", e.path, e.path, SecretIDSuffix)
	}
	return fmt.Sprintf("%s holds a {secret:…} placeholder; only a credential setting may reference a stored secret (see docs/SECURITY.md)", e.path)
}

// nodeConfigType is the node config's "type" as a connector decoding it into
// a struct would read it, or "" when that is not certain: no such key, a
// value that is not a string, or two keys that encoding/json would both
// match (strings.EqualFold is its rule), since which one the connector reads
// depends on their order.
func nodeConfigType(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	typ, n := "", 0
	for k, v := range m {
		if !strings.EqualFold(k, "type") {
			continue
		}
		n++
		if json.Unmarshal(v, &typ) != nil {
			return ""
		}
	}
	if n != 1 {
		return ""
	}
	return typ
}

func keyHexFromEnv() (string, error) {
	return Key()
}

// walker carries one resolution. reader nil means check only: nothing is
// read or changed, and only refusals are reported.
type walker struct {
	ctx      context.Context
	reader   SecretReader
	tenantID string
	fields   []secretField
	keyFn    func() (string, error)
	changed  bool
}

func resolveJSON(ctx context.Context, reader SecretReader, tenantID string, raw json.RawMessage, root []string, configType string, keyFn func() (string, error)) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers go back out exactly as they came in
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return raw, err
	}
	// One value only, as json.Unmarshal reads it: anything after it would be
	// checked by nobody.
	if _, err := dec.Token(); err != io.EOF {
		return raw, errors.New("invalid character after top-level value")
	}
	w := &walker{ctx: ctx, reader: reader, tenantID: tenantID, fields: secretFields[configType], keyFn: keyFn}
	if err := w.node(generic, root); err != nil {
		return raw, err
	}
	if !w.changed {
		return raw, nil
	}
	out, err := json.Marshal(generic)
	if err != nil {
		return raw, err
	}
	return out, nil
}

// node walks one value at path.
func (w *walker) node(v any, path []string) error {
	switch t := v.(type) {
	case map[string]any:
		return w.object(t, path)
	case []any:
		for i, item := range t {
			p := with(path, "["+strconv.Itoa(i)+"]")
			if s, ok := item.(string); ok {
				out, err := w.inline(s, p)
				if err != nil {
					return err
				}
				t[i] = out
				continue
			}
			if err := w.node(item, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// object walks a JSON object, in sorted key order so the first refusal is
// always the same one. Values written here are never walked again.
func (w *walker) object(m map[string]any, path []string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := m[key]
		if strings.HasSuffix(key, SecretIDSuffix) {
			id, ok := val.(string)
			if !ok || id == "" {
				continue // not a reference the resolver ever followed
			}
			plainKey := strings.TrimSuffix(key, SecretIDSuffix)
			if w.reader == nil {
				continue // check only: a stray reference is dropped, not refused
			}
			w.changed = true
			delete(m, key)
			if _, ok := w.match(with(path, plainKey)); !ok {
				continue // not a credential here: ignored, never resolved
			}
			plain, err := w.fetch(id)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", display(with(path, key)), err)
			}
			m[plainKey] = plain
			continue
		}
		if s, ok := val.(string); ok {
			out, err := w.inline(s, with(path, key))
			if err != nil {
				return err
			}
			m[key] = out
			continue
		}
		if err := w.node(val, with(path, key)); err != nil {
			return err
		}
	}
	return nil
}

// inline expands the placeholders in s when path takes them inline, and
// refuses them anywhere else. A string without one comes back unchanged.
func (w *walker) inline(s string, path []string) (string, error) {
	if !strings.Contains(s, placeholderMarker) {
		return s, nil
	}
	f, ok := w.match(path)
	if !ok || !f.inline {
		return s, &secretFieldError{path: display(path), credential: ok}
	}
	if w.reader == nil {
		return s, nil
	}
	matches := placeholderRE.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		plain, err := w.fetch(s[m[2]:m[3]])
		if err != nil {
			return s, fmt.Errorf("expand placeholder in %s: %w", display(path), err)
		}
		b.WriteString(plain)
		last = m[1]
	}
	b.WriteString(s[last:])
	w.changed = true
	return b.String(), nil
}

func (w *walker) match(path []string) (secretField, bool) {
	for _, f := range w.fields {
		if f.matches(path) {
			return f, true
		}
	}
	return secretField{}, false
}

func (w *walker) fetch(id string) (string, error) {
	ct, err := w.reader.GetCiphertext(w.ctx, w.tenantID, id)
	if err != nil {
		return "", err
	}
	keyHex, err := w.keyFn()
	if err != nil {
		return "", err
	}
	return Decrypt(ct, keyHex)
}

// with returns path plus one segment, without sharing path's backing array.
func with(path []string, seg string) []string {
	out := make([]string, len(path), len(path)+1)
	copy(out, path)
	return append(out, seg)
}

// display renders a path the way the docs write it: sftp.password,
// api.endpoints[0].auth_value.
func display(path []string) string {
	var b strings.Builder
	for i, seg := range path {
		if i > 0 && !strings.HasPrefix(seg, "[") {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}
