package crypto

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeReader struct {
	store map[string]string // id -> ciphertext
	reads *[]string         // ids asked for, when not nil
}

func (f fakeReader) GetCiphertext(ctx context.Context, tenantID, id string) (string, error) {
	if f.reads != nil {
		*f.reads = append(*f.reads, id)
	}
	ct, ok := f.store[id]
	if !ok {
		return "", errSecretNotFound
	}
	return ct, nil
}

func newReader(t *testing.T, entries map[string]string) fakeReader {
	t.Helper()
	t.Setenv(KeyEnvVar, testKey)
	store := make(map[string]string, len(entries))
	for id, plain := range entries {
		ct, err := Encrypt(plain, testKey)
		if err != nil {
			t.Fatalf("setup encrypt: %v", err)
		}
		store[id] = ct
	}
	return fakeReader{store: store, reads: new([]string)}
}

const (
	idA = "00000000-0000-0000-0000-00000000000a"
	idB = "00000000-0000-0000-0000-00000000000b"
	// The secret every refusal case must keep out of its answer.
	secretA = "s3cret-value-A"
	secretB = "s3cret-value-B"
)

// decode returns the resolved config as a generic tree for path lookups.
func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("resolved config does not parse: %v (%s)", err, raw)
	}
	return m
}

func at(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[p]
		case []any:
			i := int(p[0] - '0')
			if i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

// TestResolveNodeSecrets is the allowlist: what resolves, what is refused
// and what is dropped. A refusal names the path and never carries a value.
func TestResolveNodeSecrets(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		wantErr string         // substring; "" means success
		want    map[string]any // path ("a.b.c") -> expected value
		absent  []string       // paths that must not exist afterwards
		reads   []string       // secret ids read, in order (nil: not checked)
		check   func(*testing.T, json.RawMessage)
	}{
		{
			name:   "credential reference resolves",
			config: `{"type":"sftp","sftp":{"host":"h","password_secret_id":"` + idA + `"}}`,
			want:   map[string]any{"sftp.password": secretA, "sftp.host": "h"},
			absent: []string{"sftp.password_secret_id"},
		},
		{
			name:   "nested credential reference resolves",
			config: `{"type":"http","http":{"signature":{"header":"X-Sig","secret_secret_id":"` + idA + `"}}}`,
			want:   map[string]any{"http.signature.secret": secretA},
		},
		{
			name:   "tls block beside http resolves",
			config: `{"type":"http","http":{"url":"https://x"},"tls":{"cert_secret_id":"` + idA + `","key_secret_id":"` + idB + `"}}`,
			want:   map[string]any{"tls.cert": secretA, "tls.key": secretB},
		},
		{
			name: "endpoint array references resolve",
			config: `{"type":"api","api":{"endpoints":[{"path":"/a","auth_value_secret_id":"` + idA + `"},` +
				`{"path":"/b","auth_value_secret_id":"` + idB + `"}]}}`,
			want: map[string]any{"api.endpoints.0.auth_value": secretA, "api.endpoints.1.auth_value": secretB},
		},
		{
			name:   "header placeholder expands inline",
			config: `{"type":"http","http":{"headers":{"Authorization":"Bearer {secret:` + idA + `}"}}}`,
			want:   map[string]any{"http.headers.Authorization": "Bearer " + secretA},
		},
		{
			name:   "header reference resolves",
			config: `{"type":"http","http":{"headers":{"X-Api-Key_secret_id":"` + idA + `"}}}`,
			want:   map[string]any{"http.headers.X-Api-Key": secretA},
		},
		{
			// Its endpoint parts become the service URL, which transport
			// errors quote (#868 review).
			name:    "connection string placeholder is refused",
			config:  `{"type":"cloud_storage","cloud_storage":{"connection_string":"BlobEndpoint=http://127.0.0.1:1/{secret:` + idA + `};SharedAccessSignature=sv=x"}}`,
			wantErr: "cloud_storage.connection_string holds a {secret:…} placeholder; reference the stored secret as cloud_storage.connection_string_secret_id instead",
			reads:   []string{},
		},
		{
			name:   "connection string reference resolves",
			config: `{"type":"cloud_storage","cloud_storage":{"connection_string_secret_id":"` + idA + `"}}`,
			want:   map[string]any{"cloud_storage.connection_string": secretA},
		},
		{
			name:   "key spelled in another case still matches its credential",
			config: `{"type":"sftp","SFTP":{"Password_secret_id":"` + idA + `"}}`,
			want:   map[string]any{"SFTP.Password": secretA},
		},
		{
			name:    "placeholder in an enum setting is refused",
			config:  `{"type":"nats","nats":{"auth_type":"{secret:` + idA + `}"}}`,
			wantErr: "nats.auth_type holds a {secret:…} placeholder; only a credential setting",
			reads:   []string{},
		},
		{
			name:    "placeholder in an enum setting in another case is refused",
			config:  `{"type":"nats","nats":{"Auth_Type":"{secret:` + idA + `}"}}`,
			wantErr: "nats.Auth_Type holds",
			reads:   []string{},
		},
		{
			name:    "placeholder inside a filename template is refused",
			config:  `{"type":"sftp","sftp":{"password_secret_id":"` + idB + `","filename_template":"out-{secret:` + idA + `}.json"}}`,
			wantErr: "sftp.filename_template holds",
		},
		{
			name:    "placeholder in a credential that takes only a reference is refused",
			config:  `{"type":"sftp","sftp":{"password":"{secret:` + idA + `}"}}`,
			wantErr: "sftp.password holds a {secret:…} placeholder; reference the stored secret as sftp.password_secret_id instead",
			reads:   []string{},
		},
		{
			name:    "placeholder in an array of strings is refused",
			config:  `{"type":"kafka","kafka":{"brokers":["{secret:` + idA + `}"]}}`,
			wantErr: "kafka.brokers[0] holds",
		},
		{
			name:    "placeholder nested under a header value is refused",
			config:  `{"type":"http","http":{"headers":{"X":{"y":"{secret:` + idA + `}"}}}}`,
			wantErr: "http.headers.X.y holds",
		},
		{
			name:    "placeholder beside the block is refused",
			config:  `{"type":"http","name":"{secret:` + idA + `}","http":{}}`,
			wantErr: "name holds",
		},
		{
			name:    "malformed placeholder is refused too",
			config:  `{"type":"sftp","sftp":{"mode":"{secret:not-a-uuid}"}}`,
			wantErr: "sftp.mode holds",
		},
		{
			name:    "type spelled twice: nothing is a credential",
			config:  `{"type":"http","Type":"sftp","http":{"headers":{"A":"{secret:` + idA + `}"}}}`,
			wantErr: "http.headers.A holds",
		},
		{
			name:    "unknown type: nothing is a credential",
			config:  `{"type":"nope","nope":{"password":"x {secret:` + idA + `}"}}`,
			wantErr: "nope.password holds",
		},
		{
			name:    "type not a string: nothing is a credential",
			config:  `{"type":7,"http":{"headers":{"A":"{secret:` + idA + `}"}}}`,
			wantErr: "http.headers.A holds",
		},
		{
			name:   "reference in an enum setting is dropped, not resolved",
			config: `{"type":"nats","nats":{"auth_type_secret_id":"` + idA + `","password_secret_id":"` + idB + `"}}`,
			want:   map[string]any{"nats.password": secretB},
			absent: []string{"nats.auth_type", "nats.auth_type_secret_id"},
			reads:  []string{idB},
		},
		{
			name:   "reference beside the block is dropped",
			config: `{"type":"sftp","name_secret_id":"` + idA + `","sftp":{}}`,
			absent: []string{"name", "name_secret_id"},
			reads:  []string{},
		},
		{
			name:   "reference under a type spelled twice is dropped",
			config: `{"type":"sftp","TYPE":"sftp","sftp":{"password_secret_id":"` + idA + `"}}`,
			absent: []string{"sftp.password"},
			reads:  []string{},
		},
		{
			name:    "missing secret: fixed text, no id",
			config:  `{"type":"sftp","sftp":{"password_secret_id":"00000000-0000-0000-0000-deadbeef0000"}}`,
			wantErr: "resolve sftp.password_secret_id: stored secret not found",
		},
		{
			name:   "a resolved value is not expanded again",
			config: `{"type":"http","http":{"headers":{"A_secret_id":"` + idA + `"}}}`,
			reads:  []string{idA},
		},
		{
			name:   "no references: the bytes come back as they were",
			config: `{"type": "sftp",  "sftp": {"host": "h", "port": 22}}`,
			check: func(t *testing.T, out json.RawMessage) {
				if string(out) != `{"type": "sftp",  "sftp": {"host": "h", "port": 22}}` {
					t.Errorf("unchanged config was rewritten: %s", out)
				}
			},
		},
		{
			name:   "numbers survive re-encoding exactly",
			config: `{"type":"sitoo","sitoo":{"account_id":9007199254740993,"api_password_secret_id":"` + idA + `"}}`,
			check: func(t *testing.T, out json.RawMessage) {
				if !strings.Contains(string(out), `"account_id":9007199254740993`) {
					t.Errorf("large integer changed: %s", out)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := newReader(t, map[string]string{idA: secretA, idB: secretB})
			if tc.name == "a resolved value is not expanded again" {
				reader = newReader(t, map[string]string{idA: "{secret:" + idB + "}", idB: secretB})
			}
			out, err := ResolveNodeSecrets(context.Background(), reader, "t1", json.RawMessage(tc.config))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("err = nil, want %q; out %s", tc.wantErr, out)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %q, want it to contain %q", err, tc.wantErr)
				}
				for _, leak := range []string{secretA, secretB, "deadbeef", "t1"} {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("error %q carries %q", err, leak)
					}
				}
				if string(out) != tc.config {
					t.Errorf("refused config came back changed: %s", out)
				}
			} else if err != nil {
				t.Fatalf("err = %v", err)
			}
			if tc.reads != nil && !reflect.DeepEqual(*reader.reads, tc.reads) && !(len(tc.reads) == 0 && len(*reader.reads) == 0) {
				t.Errorf("secrets read = %v, want %v", *reader.reads, tc.reads)
			}
			if tc.check != nil {
				tc.check(t, out)
			}
			if tc.wantErr != "" {
				return
			}
			m := decode(t, out)
			for p, want := range tc.want {
				if got := at(m, strings.Split(p, ".")...); got != want {
					t.Errorf("%s = %v, want %v", p, got, want)
				}
			}
			for _, p := range tc.absent {
				if got := at(m, strings.Split(p, ".")...); got != nil {
					t.Errorf("%s = %v, want it absent", p, got)
				}
			}
			if tc.name == "a resolved value is not expanded again" {
				if got := at(m, "http", "headers", "A"); got != "{secret:"+idB+"}" {
					t.Errorf("header = %v, want the stored text as it is", got)
				}
			}
		})
	}
}

// TestSecretFieldsResolve: every listed credential resolves from a
// reference, and every inline one from a placeholder too. A path dropped
// from the table, or matched wrongly, fails here.
func TestSecretFieldsResolve(t *testing.T) {
	for typ, fields := range secretFields {
		for _, f := range fields {
			name := typ + ":" + strings.Join(f.path, ".")
			t.Run(name, func(t *testing.T) {
				concrete := make([]string, len(f.path))
				for i, seg := range f.path {
					if seg == "*" {
						seg = "X-Any"
					}
					concrete[i] = seg
				}
				build := func(leaf string, value string) json.RawMessage {
					root := map[string]any{"type": typ}
					cur := root
					for _, seg := range concrete[:len(concrete)-1] {
						next := map[string]any{}
						cur[seg] = next
						cur = next
					}
					cur[leaf] = value
					b, _ := json.Marshal(root)
					return b
				}
				last := concrete[len(concrete)-1]

				reader := newReader(t, map[string]string{idA: secretA})
				out, err := ResolveNodeSecrets(context.Background(), reader, "t1", build(last+SecretIDSuffix, idA))
				if err != nil {
					t.Fatalf("reference: %v", err)
				}
				if got := at(decode(t, out), concrete...); got != secretA {
					t.Errorf("reference: %s = %v, want the secret", strings.Join(concrete, "."), got)
				}

				out, err = ResolveNodeSecrets(context.Background(), reader, "t1", build(last, "pre-{secret:"+idA+"}-post"))
				switch {
				case f.inline && err != nil:
					t.Fatalf("placeholder: %v", err)
				case f.inline:
					if got := at(decode(t, out), concrete...); got != "pre-"+secretA+"-post" {
						t.Errorf("placeholder: %s = %v", strings.Join(concrete, "."), got)
					}
				case err == nil:
					t.Errorf("placeholder in a reference-only credential resolved: %s", out)
				}
			})
		}
	}
}

func TestResolveBlockSecrets(t *testing.T) {
	reader := newReader(t, map[string]string{idA: secretA, idB: secretB})
	ctx := context.Background()

	// The nats test endpoint's reproduction (#853): auth_type held a
	// placeholder and the answer quoted the secret.
	_, err := ResolveBlockSecrets(ctx, reader, "t1", "nats", json.RawMessage(`{"tenant_id":"t1","auth_type":"{secret:`+idA+`}"}`))
	if err == nil || !strings.Contains(err.Error(), "nats.auth_type holds") || strings.Contains(err.Error(), secretA) {
		t.Fatalf("placeholder in auth_type: err = %v", err)
	}

	out, err := ResolveBlockSecrets(ctx, reader, "t1", "nats", json.RawMessage(
		`{"tenant_id":"t1","auth_type":"user_password","password_secret_id":"`+idA+`","inbox_prefix_secret_id":"`+idB+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out)
	if m["password"] != secretA {
		t.Errorf("password = %v", m["password"])
	}
	if _, ok := m["inbox_prefix"]; ok || strings.Contains(string(out), secretB) {
		t.Errorf("stray reference resolved: %s", out)
	}
}

func TestCheckSecrets(t *testing.T) {
	cases := []struct {
		name, config, wantErr string
	}{
		{"credential reference", `{"type":"sftp","sftp":{"password_secret_id":"x"}}`, ""},
		{"stray reference is not reported", `{"type":"sftp","sftp":{"mode_secret_id":"x"}}`, ""},
		{"header placeholder", `{"type":"http","http":{"headers":{"A":"Bearer {secret:` + idA + `}"}}}`, ""},
		{"enum placeholder", `{"type":"nats","nats":{"deliver_policy":"{secret:` + idA + `}"}}`, "nats.deliver_policy holds"},
		{"credential placeholder", `{"type":"rabbitmq","rabbitmq":{"password":"{secret:` + idA + `}"}}`, "rabbitmq.password_secret_id instead"},
		{"no type", `{"sftp":{"password":"{secret:` + idA + `}"}}`, "sftp.password holds"},
		{"empty", ``, ""},
		{"null", `null`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckNodeSecrets(json.RawMessage(tc.config))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}

	if err := CheckBlockSecrets("cloud_storage", json.RawMessage(`{"bucket":"{secret:`+idA+`}"}`)); err == nil ||
		!strings.Contains(err.Error(), "cloud_storage.bucket holds") {
		t.Errorf("block check: err = %v", err)
	}
	if err := CheckBlockSecrets("cloud_storage", json.RawMessage(`{"connection_string":"AccountName={secret:`+idA+`}"}`)); err == nil ||
		!strings.Contains(err.Error(), "cloud_storage.connection_string holds") {
		t.Errorf("block check of a connection string placeholder: err = %v", err)
	}
	if err := CheckBlockSecrets("cloud_storage", json.RawMessage(`{"connection_string_secret_id":"`+idA+`"}`)); err != nil {
		t.Errorf("block check of a connection string reference: %v", err)
	}
	// Header values are the only inline settings left.
	inline := 0
	for _, fields := range secretFields {
		for _, f := range fields {
			if f.inline {
				inline++
				if strings.Join(f.path, ".") != "http.headers.*" {
					t.Errorf("%s takes a placeholder inline", strings.Join(f.path, "."))
				}
			}
		}
	}
	if inline != 1 {
		t.Errorf("%d inline settings, want 1 (http.headers.*)", inline)
	}
	if err := CheckNodeSecrets(json.RawMessage(`{"type":`)); err == nil {
		t.Error("broken JSON passed the check")
	}
	// A second value after the first is not looked at by anyone else.
	if err := CheckNodeSecrets(json.RawMessage(`{"type":"sftp"} {"sftp":{"mode":"{secret:` + idA + `}"}}`)); err == nil {
		t.Error("a trailing value passed the check")
	}
}

func TestResolve_Idempotent(t *testing.T) {
	reader := newReader(t, map[string]string{idA: secretA})
	cfg := json.RawMessage(`{"type":"database","database":{"password_secret_id":"` + idA + `"}}`)
	for i := 0; i < 3; i++ {
		out, err := ResolveNodeSecrets(context.Background(), reader, "t1", cfg)
		if err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		if at(decode(t, out), "database", "password") != secretA {
			t.Fatalf("pass %d: %s", i, out)
		}
		cfg = out
	}
}

func TestResolve_ReaderErrorPassesThrough(t *testing.T) {
	down := errors.New("pq: the database system is shutting down")
	_, err := ResolveNodeSecrets(context.Background(), errReader{down}, "t1",
		json.RawMessage(`{"type":"database","database":{"password_secret_id":"`+idA+`"}}`))
	if !errors.Is(err, down) {
		t.Fatalf("err = %v, want it to wrap the reader's error", err)
	}
}

type errReader struct{ err error }

func (e errReader) GetCiphertext(context.Context, string, string) (string, error) { return "", e.err }
