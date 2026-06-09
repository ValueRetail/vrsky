package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Aux HTTP handlers served on the SDK auxiliary HTTP port (WORKER_HTTP_PORT,
// 9700 in compose). Registered in Configure via RegisterHTTPHandler. These let
// the UI discover a Salesforce object's field schema (describe) and fetch a
// sample (SOQL) for the visual field-mapping UI (#79 / #81). /health is served
// separately by the SDK on HEALTH_PORT.

// limitClause matches a SOQL LIMIT and captures its count.
var limitClause = regexp.MustCompile(`(?i)\blimit\s+(\d+)`)

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// objectFromSOQL returns the SObject named in a SOQL query's top-level FROM
// clause, or "". It tracks parenthesis depth so subqueries — whether in the
// SELECT (child relationships) or the WHERE (semi-joins) — don't shadow the
// queried object.
func objectFromSOQL(soql string) string {
	depth := 0
	for i := 0; i < len(soql); {
		switch soql[i] {
		case '(':
			depth++
			i++
		case ')':
			if depth > 0 {
				depth--
			}
			i++
		default:
			c := soql[i] | 0x20 // ASCII lower
			if depth == 0 && c == 'f' && i+4 <= len(soql) &&
				(soql[i+1]|0x20) == 'r' && (soql[i+2]|0x20) == 'o' && (soql[i+3]|0x20) == 'm' &&
				(i == 0 || !isIdentByte(soql[i-1])) && (i+4 == len(soql) || !isIdentByte(soql[i+4])) {
				j := i + 4
				for j < len(soql) && (soql[j] == ' ' || soql[j] == '\t' || soql[j] == '\n' || soql[j] == '\r') {
					j++
				}
				start := j
				for j < len(soql) && isIdentByte(soql[j]) {
					j++
				}
				if j > start {
					return soql[start:j]
				}
				i = j
			} else {
				i++
			}
		}
	}
	return ""
}

// schemaRequest is the shared request body for /schema/ and /sample-data/.
type schemaRequest struct {
	TenantID     string `json:"tenant_id"`
	InstanceURL  string `json:"instance_url"`
	OAuthGrantID string `json:"oauth_grant_id"`
	APIVersion   string `json:"api_version"`
	SOQL         string `json:"soql"`
	Object       string `json:"object"` // optional; derived from SOQL when empty
}

type sfSchemaField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

func writeJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// preflight handles CORS preflight + method gating; returns true if the caller
// should stop (preflight or wrong method already handled).
func preflight(w http.ResponseWriter, r *http.Request) bool {
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

func (s *salesforceConsumer) decodeSchemaRequest(w http.ResponseWriter, r *http.Request) (*schemaRequest, bool) {
	var req schemaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
		return nil, false
	}
	if req.InstanceURL == "" || req.OAuthGrantID == "" || req.TenantID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "instance_url, oauth_grant_id and tenant_id are required"})
		return nil, false
	}
	if req.APIVersion == "" {
		req.APIVersion = defaultAPIVersion
	}
	return &req, true
}

// handleSchema returns a Salesforce object's field schema via the describe API,
// so the UI's field-mapping tree shows live fields + types.
func (s *salesforceConsumer) handleSchema() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if preflight(w, r) {
			return
		}
		req, ok := s.decodeSchemaRequest(w, r)
		if !ok {
			return
		}
		object := req.Object
		if object == "" {
			object = objectFromSOQL(req.SOQL)
		}
		if object == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "could not determine the Salesforce object (set a SOQL query with a FROM clause)"})
			return
		}

		base := strings.TrimRight(req.InstanceURL, "/")
		describeURL := fmt.Sprintf("%s/services/data/%s/sobjects/%s/describe", base, req.APIVersion, url.PathEscape(object))
		body, err := s.get(r.Context(), req.TenantID, req.OAuthGrantID, describeURL)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}

		var describe struct {
			Fields []struct {
				Name     string `json:"name"`
				Type     string `json:"type"`
				Nillable bool   `json:"nillable"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(body, &describe); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "parse describe response: " + err.Error()})
			return
		}

		fields := make([]sfSchemaField, 0, len(describe.Fields))
		for _, f := range describe.Fields {
			fields = append(fields, sfSchemaField{Name: f.Name, Type: f.Type, Nullable: f.Nillable})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "object": object, "fields": fields})
	}
}

// handleSampleData runs the configured SOQL (bounded by LIMIT) and returns the
// records, so the Converter's "Fetch from Input" / Test Transform works for a
// Salesforce source.
func (s *salesforceConsumer) handleSampleData() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if preflight(w, r) {
			return
		}
		req, ok := s.decodeSchemaRequest(w, r)
		if !ok {
			return
		}
		if strings.TrimSpace(req.SOQL) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "a SOQL query is required for a sample"})
			return
		}
		// Always bound a preview to a small page, capping any existing LIMIT.
		const sampleLimit = 5
		soql := strings.TrimRight(req.SOQL, "; \t\n")
		if m := limitClause.FindStringSubmatch(soql); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > sampleLimit {
				soql = limitClause.ReplaceAllString(soql, fmt.Sprintf("LIMIT %d", sampleLimit))
			}
		} else {
			soql += fmt.Sprintf(" LIMIT %d", sampleLimit)
		}

		base := strings.TrimRight(req.InstanceURL, "/")
		queryURL := fmt.Sprintf("%s/services/data/%s/query/?q=%s", base, req.APIVersion, url.QueryEscape(soql))
		body, err := s.get(r.Context(), req.TenantID, req.OAuthGrantID, queryURL)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		var qr queryResponse
		if err := json.Unmarshal(body, &qr); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "parse query response: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": qr.Records})
	}
}
