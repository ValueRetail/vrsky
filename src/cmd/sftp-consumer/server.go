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

// Aux HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9210
// in compose). Registered in Configure via RegisterHTTPHandler. Lets the
// management-api's POST /api/v1/connections/test verify SFTP credentials against
// a draft config (#82).

func writeTestJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// testGate applies CORS + method gating; returns true if the caller should stop.
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

// handleTestConnection dials the draft SFTP config and lists the remote dir.
func (s *sftpConsumer) handleTestConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if testGate(w, r) {
			return
		}
		var cfg SFTPConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
			return
		}
		if cfg.Host == "" || cfg.Username == "" {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "host and username are required"})
			return
		}
		if cfg.Password == "" && cfg.PrivateKey == "" {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "set a password or a private key"})
			return
		}
		conn, err := s.dial(&cfg)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		defer conn.Close()

		dir := cfg.RemoteDir
		if dir == "" {
			dir = "."
		}
		files, err := conn.List(dir)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "connected, but listing " + dir + " failed: " + err.Error()})
			return
		}
		names := make([]string, 0, len(files))
		for i, f := range files {
			if i >= 20 {
				break
			}
			names = append(names, f.Name)
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "sample": names})
	}
}

// handleSampleData dials the draft SFTP config, reads the first file in the
// remote dir (matching the optional glob), parses it, and returns a structure
// preview — so the UI's filter/converter can show fields before the pipeline is
// deployed. tenant_id (optional) resolves <field>_secret_id references.
func (s *sftpConsumer) handleSampleData() http.HandlerFunc {
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
		var cfg SFTPConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "invalid SFTP config: " + err.Error()})
			return
		}
		if cfg.Host == "" || cfg.Username == "" {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "host and username are required"})
			return
		}
		conn, err := s.dial(&cfg)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		defer conn.Close()

		dir := cfg.RemoteDir
		if dir == "" {
			dir = "."
		}
		files, err := conn.List(dir)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "listing " + dir + " failed: " + err.Error()})
			return
		}
		var chosen string
		for _, f := range files {
			if cfg.FilePattern != "" {
				if ok, _ := path.Match(cfg.FilePattern, f.Name); !ok {
					continue
				}
			}
			chosen = f.Name
			break
		}
		if chosen == "" {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "no files in " + dir + " to sample yet"})
			return
		}
		data, err := conn.Read(path.Join(dir, chosen))
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "read " + chosen + ": " + err.Error()})
			return
		}
		if len(data) > 256*1024 {
			data = data[:256*1024]
		}
		writeTestJSON(w, http.StatusOK, sampleFromFileBytes(chosen, data))
	}
}

// sampleFromFileBytes turns raw file bytes into a {ok,filename,data|columns}
// preview: parsed JSON when it looks like JSON, header+first-row for CSV/TSV,
// else a plain-text envelope. Shared by the SFTP + cloud-storage samplers.
func sampleFromFileBytes(filename string, data []byte) map[string]interface{} {
	resp := map[string]interface{}{"ok": true, "filename": filename}
	trimmed := bytes.TrimSpace(data)
	ct := detectContentType(filename, data)
	if ct == "application/json" || (len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')) {
		var parsed interface{}
		if err := json.Unmarshal(trimmed, &parsed); err == nil {
			resp["data"] = parsed
			return resp
		}
	}
	ext := strings.ToLower(path.Ext(filename))
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
	resp["data"] = map[string]string{"text": string(data), "content_type": ct}
	return resp
}

// parseCSVSample reads the header row + first data row, returning the header
// names and a header→value map (empty strings when there is no data row).
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
