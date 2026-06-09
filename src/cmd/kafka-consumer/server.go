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
		n, err := k.ping(r.Context(), &cfg)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "partitions": n, "topic": cfg.Topic})
	}
}
