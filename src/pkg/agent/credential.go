package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credential is what registration leaves on disk. The credential string is
// the agent's only secret: whoever holds it can upload into, and receive files
// from, this agent's pipelines. It is written readable by administrators and
// the service account only (see restrictFile), and never logged.
type Credential struct {
	AgentID      string    `json:"agent_id"`
	Name         string    `json:"name"`
	ServerURL    string    `json:"server_url"`
	Credential   string    `json:"credential"`
	RegisteredAt time.Time `json:"registered_at"`
	// Revoked is set when VRSky refuses the credential as revoked, so the
	// service does not keep retrying one that will never work again.
	Revoked bool `json:"revoked,omitempty"`
}

// ErrNotRegistered means there is no credential yet.
var ErrNotRegistered = errors.New("this agent is not registered — run: vrsky-agent register --url <VRSky URL> --token <registration token>")

func credentialPath(dataDir string) string { return filepath.Join(dataDir, "credential.json") }

// LoadCredential reads the stored credential.
func LoadCredential(dataDir string) (*Credential, error) {
	raw, err := os.ReadFile(credentialPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotRegistered
	}
	if err != nil {
		return nil, fmt.Errorf("read credential: %w", err)
	}
	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("read credential: %w", err)
	}
	if c.Credential == "" || c.ServerURL == "" {
		return nil, ErrNotRegistered
	}
	return &c, nil
}

// SaveCredential writes the credential atomically, locked down before it is
// moved into place so it is never readable by others even briefly.
func SaveCredential(dataDir string, c *Credential) error {
	if err := ensurePrivateDir(dataDir); err != nil {
		return fmt.Errorf("create %s: %w", dataDir, err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(credentialPath(dataDir), raw)
}

// writePrivateFile writes via a temp file in the same folder, restricts it,
// then renames it over the target.
func writePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op after a successful rename
	if err := restrictFile(name); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
