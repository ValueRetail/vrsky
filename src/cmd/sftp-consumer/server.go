package main

import (
	"encoding/json"
	"net/http"
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
