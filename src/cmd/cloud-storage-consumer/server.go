package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/crypto"
)

// Aux HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9240
// in compose). Lets the management-api's POST /api/v1/connections/test verify a
// draft cloud-storage config (#82).

func writeTestJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func testGate(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	return false
}

// handleTestConnection opens the draft object store and lists under the prefix.
func (s *cloudConsumer) handleTestConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if testGate(w, r) {
			return
		}
		var cfg cloudConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
			return
		}
		if cfg.Bucket == "" {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "bucket is required"})
			return
		}
		store, err := s.newStore(r.Context(), &cfg.Config)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		objs, err := store.List(r.Context(), cfg.Prefix)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		keys := make([]string, 0, len(objs))
		for i, o := range objs {
			if i >= 20 {
				break
			}
			keys = append(keys, o.Key)
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "sample": keys})
	}
}

// handleSampleData opens the draft object store, reads the first object under
// the prefix (matching the optional glob), parses it, and returns a structure
// preview for the UI's filter/converter — before the pipeline is deployed.
// tenant_id (optional) resolves <field>_secret_id references.
func (s *cloudConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if testGate(w, r) {
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "read body: " + err.Error()})
			return
		}
		var meta struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.Unmarshal(raw, &meta)
		cfgJSON := json.RawMessage(raw)
		if meta.TenantID != "" && s.db != nil {
			if resolved, rerr := crypto.ResolveSecretsInJSON(r.Context(), crypto.NewSQLSecretReader(s.db), meta.TenantID, cfgJSON); rerr == nil {
				cfgJSON = resolved
			}
		}
		var cfg cloudConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "invalid cloud storage config: " + err.Error()})
			return
		}
		if cfg.Bucket == "" {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "bucket is required"})
			return
		}
		store, err := s.newStore(r.Context(), &cfg.Config)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		objs, err := store.List(r.Context(), cfg.Prefix)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		var chosen string
		for _, o := range objs {
			if strings.HasSuffix(o.Key, "/") {
				continue // skip "directory" placeholders
			}
			if cfg.FilePattern != "" {
				if match, _ := path.Match(cfg.FilePattern, path.Base(o.Key)); !match {
					continue
				}
			}
			chosen = o.Key
			break
		}
		if chosen == "" {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "no objects under the prefix to sample yet"})
			return
		}
		body, ct, err := store.Get(r.Context(), chosen)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "read " + chosen + ": " + err.Error()})
			return
		}
		if len(body) > 256*1024 {
			body = body[:256*1024]
		}
		writeTestJSON(w, http.StatusOK, sampleFromObject(chosen, body, ct))
	}
}

// sampleFromObject turns raw object bytes into a {ok,filename,data|columns}
// preview: parsed JSON when it looks like JSON, header+first-row for CSV/TSV,
// else a plain-text envelope.
func sampleFromObject(key string, data []byte, contentType string) map[string]interface{} {
	resp := map[string]interface{}{"ok": true, "filename": path.Base(key)}
	trimmed := bytes.TrimSpace(data)
	if strings.Contains(contentType, "json") || (len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')) {
		var parsed interface{}
		if err := json.Unmarshal(trimmed, &parsed); err == nil {
			resp["data"] = parsed
			return resp
		}
	}
	ext := strings.ToLower(path.Ext(key))
	if ext == ".csv" || ext == ".tsv" {
		delim := ','
		if ext == ".tsv" {
			delim = '\t'
		}
		if headers, row := parseCSVSample(data, delim); len(headers) > 0 {
			resp["columns"] = headers
			resp["data"] = row
			return resp
		}
	}
	if contentType == "" {
		contentType = "text/plain"
	}
	resp["data"] = map[string]string{"text": string(data), "content_type": contentType}
	return resp
}

// parseCSVSample reads the header row + first data row, returning header names
// and a header→value map (empty strings when there is no data row).
func parseCSVSample(data []byte, delim rune) ([]string, map[string]string) {
	rd := csv.NewReader(bytes.NewReader(data))
	rd.Comma = delim
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	header, err := rd.Read()
	if err != nil || len(header) == 0 {
		return nil, nil
	}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	row := make(map[string]string, len(header))
	for _, h := range header {
		row[h] = ""
	}
	if rec, err := rd.Read(); err == nil {
		for i, h := range header {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
	}
	return header, row
}
