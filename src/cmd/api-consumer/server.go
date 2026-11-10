package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// HTTP handler served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT, 9800 in
// compose). Registered in Configure via RegisterHTTPHandler. /health is served
// separately by the SDK on HEALTH_PORT.

type SampleDataRequest struct {
	BaseURL   string `json:"base_url"`
	Path      string `json:"path"`
	Params    string `json:"params"`
	AuthType  string `json:"auth_type"`
	AuthValue string `json:"auth_value"`
}

// handleSampleData fetches a small preview from a configured endpoint, used by
// the UI's filter/converter preview before a pipeline is deployed. The request
// carries the (typed, not-yet-persisted) auth value directly — nothing here is
// stored, so no secret resolution is involved.
func (s *apiConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var req SampleDataRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "Invalid request"})
			return
		}

		// The caller is a tenant and the URL is theirs: refuse what the poller
		// would refuse before anything is sent (#639).
		url := safeurl.EndpointURL(req.BaseURL, req.Path, req.Params)
		if err := safeurl.CheckRequestURL(url, s.allowPrivate); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			// A parse error quotes the URL, query and key included.
			if s.logger != nil {
				s.logger.Warn("sample-data request not built", "error", safeurl.Detail(err))
			}
			writeJSON(w, map[string]interface{}{"ok": false, "error": "request failed"})
			return
		}
		httpReq.Header.Set("User-Agent", "VRSky-API-Consumer/1.0")
		httpReq.Header.Set("Accept", "*/*")
		applyAuth(httpReq, req.AuthType, req.AuthValue)

		resp, err := s.client().Do(httpReq)
		if err != nil {
			var msg string
			if m, ok := safeurl.UserMessage(err); ok {
				// A transport error names resolvers, resolved addresses and
				// the request URL: log it, hand the caller a fixed text.
				if s.logger != nil {
					s.logger.Warn("sample-data request failed", "error", safeurl.Detail(err))
				}
				msg = m
			} else {
				msg = "request failed"
			}
			writeJSON(w, map[string]interface{}{"ok": false, "error": msg})
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
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
