package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ValueRetail/vrsky/pkg/crypto"
	"github.com/ValueRetail/vrsky/pkg/safeurl"
	"github.com/ValueRetail/vrsky/pkg/sdk"
)

// Aux HTTP handler on the SDK auxiliary port (WORKER_HTTP_PORT, 9340 in
// compose). The management API's POST /api/v1/connections/test forwards a
// draft node config here (#82), with the tenant from the session.

// testTimeout keeps the answer inside the management API's 10 s budget for
// the call, so a slow server gets a reason rather than a cut connection.
// The dial and API waits are shorter than the whole, so a server that does
// not answer still leaves time for the reply.
const (
	testTimeout  = 8 * time.Second
	testDialWait = 3 * time.Second
	testAPIWait  = 4 * time.Second
)

// userText is what a tenant sees for an error: the connector's own texts for
// a permanent error (none of them carries an address or a credential), and
// safeurl's fixed texts for a transport error, whose raw text names the
// resolver and the resolved address (#717).
func userText(err error) string {
	if sdk.IsPermanent(err) {
		return err.Error()
	}
	if msg, ok := safeurl.UserMessage(err); ok {
		return "could not reach the NATS server: " + msg
	}
	return "could not reach the NATS server"
}

func writeTestJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// handleTestConnection connects with the draft config, looks up the durable
// (consumer info only, no stream info), and disconnects. It creates nothing:
// a missing durable is reported as fine when the first start may create it,
// and as an error when create_durable is off.
//
// The body is the nats block, plus tenant_id. With a tenant, *_secret_id
// references are resolved, so a saved node can be tested again. A draft typed
// a moment ago carries its password in the clear; it is used for this one
// test and stored nowhere.
func (c *natsConsumer) handleTestConnection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "could not read the request body"})
			return
		}
		var meta struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.Unmarshal(raw, &meta)
		cfgJSON := json.RawMessage(raw)
		if meta.TenantID != "" && c.db != nil {
			resolved, rerr := crypto.ResolveSecretsInJSON(r.Context(), crypto.NewSQLSecretReader(c.db), meta.TenantID, onlyCredentialSecrets(cfgJSON))
			if rerr != nil {
				writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "could not read the stored credentials"})
				return
			}
			cfgJSON = resolved
		}
		var cfg NATSConfig
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			writeTestJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "invalid JSON request body"})
			return
		}
		cfg.normalise()
		if err := cfg.validate(c.allowPrivate); err != nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
		defer cancel()
		s, err := open(ctx, &cfg, openOpts{allowPrivate: c.allowPrivate, apiWait: testAPIWait, dialWait: testDialWait})
		if errors.Is(err, errDurableMissing) {
			if !cfg.mayCreate() {
				writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": durableMissingError(&cfg).Error()})
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "note": "the durable does not exist yet; the first start creates it (whether the account may create it is not checked here)"})
			return
		}
		if err != nil {
			if c.logger != nil {
				c.logger.Info("NATS connection test failed", "error", safeurl.Detail(err))
			}
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": userText(err)})
			return
		}
		s.close()
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	}
}
