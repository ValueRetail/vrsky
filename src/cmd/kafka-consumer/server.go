package main

import (
	"encoding/json"
	"net/http"
)

// Aux HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9220
// in compose). Lets the management-api's POST /api/v1/connections/test verify a
// draft Kafka config (#82).

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

// handleTestConnection dials the draft Kafka brokers and reads partition metadata.
func (k *kafkaConsumer) handleTestConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if testGate(w, r) {
			return
		}
		var cfg KafkaConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
			return
		}
		if len(cfg.Brokers) == 0 {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "at least one broker is required"})
			return
		}
		if cfg.Topic == "" {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "topic is required"})
			return
		}
		n, err := k.ping(r.Context(), &cfg)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "partitions": n, "topic": cfg.Topic})
	}
}

// handleSampleData peeks the earliest message on the topic and returns it as
// parsed JSON for the UI schema-preview ("Show Data Structure") flow (#144).
// Response: {"ok":true,"data":<parsed JSON | {"value":"…"}>} or {"ok":false,"error":"…"}.
func (k *kafkaConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if testGate(w, r) {
			return
		}
		var cfg KafkaConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
			return
		}
		if len(cfg.Brokers) == 0 || cfg.Topic == "" {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "brokers and topic are required"})
			return
		}
		raw, err := k.sample(r.Context(), &cfg)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		// Prefer parsed JSON so the field picker can infer a schema; fall back to
		// the raw string for non-JSON payloads.
		var parsed interface{}
		if json.Unmarshal(raw, &parsed) != nil {
			parsed = map[string]interface{}{"value": string(raw)}
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": parsed})
	}
}
