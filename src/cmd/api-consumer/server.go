package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type SampleDataRequest struct {
	BaseURL   string `json:"base_url"`
	Path      string `json:"path"`
	Params    string `json:"params"`
	AuthType  string `json:"auth_type"`
	AuthValue string `json:"auth_value"`
}

func startHTTPServer(port string, logger *slog.Logger) {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/sample-data/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var req SampleDataRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "Invalid request"})
			return
		}

		// Build URL
		var url string
		if strings.HasPrefix(req.Path, "http://") || strings.HasPrefix(req.Path, "https://") {
			url = req.Path
		} else {
			url = strings.TrimSuffix(req.BaseURL, "/") + req.Path
		}
		if req.Params != "" {
			if strings.Contains(url, "?") {
				url += "&" + req.Params
			} else {
				url += "?" + req.Params
			}
		}

		// Fetch
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		httpReq.Header.Set("User-Agent", "VRSky-API-Consumer/1.0")
		httpReq.Header.Set("Accept", "*/*")
		applyAuth(httpReq, req.AuthType, req.AuthValue)

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "Failed to read response"})
			return
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "HTTP " + resp.Status, "body": string(body[:min(len(body), 500)])})
			return
		}

		// Try to parse as JSON for structured response
		var parsed interface{}
		if json.Unmarshal(body, &parsed) == nil {
			writeJSON(w, map[string]interface{}{"ok": true, "data": parsed})
		} else {
			writeJSON(w, map[string]interface{}{"ok": true, "data": string(body)})
		}
	})

	server := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		logger.Info("API Consumer HTTP server started", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
