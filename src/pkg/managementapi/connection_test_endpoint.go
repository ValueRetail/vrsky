package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// testHTTPClient is the client the management-api uses to reach worker aux
// /test-connection (and equivalent) endpoints. The 10s timeout backs the #82
// acceptance criterion ("returns within 10s or times out gracefully").
var testHTTPClient = &http.Client{Timeout: 10 * time.Second}

func testWorkerURL(envKey, def string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return def
}

func jsonString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// buildTestTarget maps a draft node config to the worker aux endpoint that can
// test it, returning the URL + the request body to POST. supported is false for
// connectors with no pre-deploy test path (inbound webhook, unknown types).
func buildTestTarget(typ, role string, raw map[string]json.RawMessage, tenantID string) (url string, body []byte, supported bool, hint string) {
	sub := func(key string) []byte {
		if b, ok := raw[key]; ok && len(b) > 0 {
			return b
		}
		return []byte("{}")
	}

	switch typ {
	case "database":
		if role == "producer" {
			return testWorkerURL("TEST_URL_DB_PRODUCER", "http://db-producer:9500") + "/test-connection/", sub("database"), true, ""
		}
		return testWorkerURL("TEST_URL_DB_CONSUMER", "http://db-consumer:9300") + "/test-connection/", sub("database"), true, ""
	case "sftp":
		return testWorkerURL("TEST_URL_SFTP", "http://sftp-consumer:9210") + "/test-connection/", sub("sftp"), true, ""
	case "kafka":
		return testWorkerURL("TEST_URL_KAFKA", "http://kafka-consumer:9220") + "/test-connection/", sub("kafka"), true, ""
	case "rabbitmq":
		return testWorkerURL("TEST_URL_RABBITMQ", "http://rabbitmq-consumer:9230") + "/test-connection/", sub("rabbitmq"), true, ""
	case "cloud_storage":
		return testWorkerURL("TEST_URL_CLOUD", "http://cloud-storage-consumer:9240") + "/test-connection/", sub("cloud_storage"), true, ""
	case "file":
		var fc struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(sub("file"), &fc)
		b, _ := json.Marshal(map[string]string{"path": fc.Path})
		return testWorkerURL("TEST_URL_FILE", "http://file-consumer:9200") + "/sample-data/", b, true, ""
	case "api":
		var ac struct {
			BaseURL   string                   `json:"base_url"`
			Endpoints []map[string]interface{} `json:"endpoints"`
		}
		_ = json.Unmarshal(sub("api"), &ac)
		ep := map[string]interface{}{}
		if len(ac.Endpoints) > 0 {
			ep = ac.Endpoints[0]
		}
		// Match the UI's sample-data defaults so "Test" behaves consistently even
		// when no endpoint is configured yet.
		path := jsonString(ep["path"])
		if path == "" {
			path = "/"
		}
		authType := jsonString(ep["auth_type"])
		if authType == "" {
			authType = "none"
		}
		b, _ := json.Marshal(map[string]interface{}{
			"base_url":   ac.BaseURL,
			"path":       path,
			"params":     jsonString(ep["params"]),
			"auth_type":  authType,
			"auth_value": jsonString(ep["auth_value"]),
		})
		return testWorkerURL("TEST_URL_API", "http://api-consumer:9800") + "/sample-data/", b, true, ""
	case "salesforce":
		var sf struct {
			InstanceURL  string `json:"instance_url"`
			OAuthGrantID string `json:"oauth_grant_id"`
			APIVersion   string `json:"api_version"`
			SOQL         string `json:"soql"`
		}
		_ = json.Unmarshal(sub("salesforce"), &sf)
		b, _ := json.Marshal(map[string]interface{}{
			"tenant_id":      tenantID,
			"instance_url":   sf.InstanceURL,
			"oauth_grant_id": sf.OAuthGrantID,
			"api_version":    sf.APIVersion,
			"soql":           sf.SOQL,
		})
		return testWorkerURL("TEST_URL_SALESFORCE", "http://salesforce-consumer:9250") + "/schema/", b, true, ""
	case "http":
		return "", nil, false, "An inbound HTTP webhook can't be tested before deploy — deploy the pipeline and send it a request."
	default:
		return "", nil, false, fmt.Sprintf("connection testing isn't supported for %q", typ)
	}
}

// TestConnection tests a DRAFT connector config without persisting it, by
// dispatching to the relevant worker's aux test endpoint and relaying the
// {ok,error,sample,…} result. Never writes to the database. (#82)
//
// Body: the draft node config, e.g. { "type":"database", "role":"consumer",
// "database": { … } }.
func (h *Handler) TestConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID, err := GetTenantIDFromContext(ctx)
	if err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidTenant", err.Error(), nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		_ = writeError(w, http.StatusBadRequest, "InvalidJSON", "failed to parse draft config", nil)
		return
	}
	var typ, role string
	if b, ok := raw["type"]; ok {
		_ = json.Unmarshal(b, &typ)
	}
	if b, ok := raw["role"]; ok {
		_ = json.Unmarshal(b, &role)
	}
	if typ == "" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "config type is required", nil)
		return
	}
	if role != "" && role != "consumer" && role != "producer" {
		_ = writeError(w, http.StatusBadRequest, "InvalidRequest", "role must be 'consumer' or 'producer'", nil)
		return
	}
	if role == "" {
		role = "consumer"
	}

	url, body, supported, hint := buildTestTarget(typ, role, raw, tenantID)
	if !supported {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": hint})
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := testHTTPClient.Do(req)
	if err != nil {
		msg := "could not reach the connector worker"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "connection test timed out after 10s"
		}
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": msg})
		return
	}
	defer resp.Body.Close()

	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]interface{}
	if err := json.Unmarshal(rb, &out); err != nil {
		_ = writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": fmt.Sprintf("connector worker returned status %d", resp.StatusCode)})
		return
	}
	// Relay the worker's normalized {ok,error,sample,tables,fields,data,…}.
	_ = writeJSON(w, http.StatusOK, out)
}
