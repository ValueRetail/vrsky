package main

import (
	"encoding/json"
	"net/http"
)

// Aux HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9230
// in compose). Lets the management-api's POST /api/v1/connections/test verify a
// draft RabbitMQ config (#82).

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

// handleTestConnection dials the draft RabbitMQ config (opens a connection +
// channel) and closes it.
func (c *rabbitConsumer) handleTestConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if testGate(w, r) {
			return
		}
		var cfg RabbitMQConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
			return
		}
		if cfg.URL == "" {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "AMQP URL is required"})
			return
		}
		src, err := c.dial(&cfg)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		_ = src.Close()
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}
}

// handleSampleData peeks one message off the queue (non-destructively) and
// returns it as parsed JSON for the UI schema-preview flow (#144).
// Response: {"ok":true,"data":<parsed JSON | {"value":"…"}>} or {"ok":false,"error":"…"}.
func (c *rabbitConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if testGate(w, r) {
			return
		}
		var cfg RabbitMQConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
			return
		}
		if cfg.URL == "" || cfg.Queue == "" {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "AMQP URL and queue are required"})
			return
		}
		raw, err := c.sample(&cfg)
		if err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		var parsed interface{}
		if json.Unmarshal(raw, &parsed) != nil {
			parsed = map[string]interface{}{"value": string(raw)}
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": parsed})
	}
}
