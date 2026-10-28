package managementapi

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderConnectorManifest runs deploy-connectors-azure.sh with GENERATE_ONLY=1
// on a copy of the script in a temp tree, so the repo's own connectors.yaml is
// left alone, and returns what it generated. The deploy job does the same
// render before it applies, so this is what production gets.
func renderConnectorManifest(t *testing.T, script []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	azure := filepath.Join(dir, "infrastructure", "azure")
	if err := os.MkdirAll(azure, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(azure, "deploy-connectors-azure.sh")
	if err := os.WriteFile(path, script, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", path)
	// Only what the script needs: its defaults, not whatever REG or FILES_*
	// the caller's shell happens to export.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GENERATE_ONLY=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deploy-connectors-azure.sh with GENERATE_ONLY=1: %v\n%s", err, out)
	}
	out, err := os.ReadFile(filepath.Join(dir, "infrastructure", "kubernetes", "connectors", "connectors.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type connectorStrategy struct {
	Type          string         `yaml:"type"`
	RollingUpdate map[string]any `yaml:"rollingUpdate"`
}

// connectorStrategies maps each Deployment in a connector manifest to its
// strategy. An absent strategy is the zero value (Kubernetes: RollingUpdate).
func connectorStrategies(t *testing.T, manifest []byte) map[string]connectorStrategy {
	t.Helper()
	got := map[string]connectorStrategy{}
	dec := yaml.NewDecoder(strings.NewReader(string(manifest)))
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Strategy connectorStrategy `yaml:"strategy"`
			} `yaml:"spec"`
		}
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("parse connector manifest: %v", err)
		}
		if d.Kind == "Deployment" {
			got[d.Metadata.Name] = d.Spec.Strategy
		}
	}
	return got
}

// file-consumer must never run two pods at once (#425).
//
// It watches folders on a shared volume. With the default RollingUpdate the
// new pod starts, restores every file pipeline and polls before the old one
// stops, and a file that settles in those seconds can be published by both.
// Recreate stops the old pod first; files that land in the gap are published
// by the new pod's first polls, since they are not in the saved done set.
//
// Checked three ways, because each fails on its own:
//   - in a fresh render of the script, since the deploy job regenerates the
//     manifest from it and a hand edit to connectors.yaml alone would be
//     overwritten at deploy time;
//   - in the committed connectors.yaml, which must equal that render;
//   - in the script's apply path: every Recreate connector is patched to
//     Recreate, rollingUpdate dropped, before the apply. The client-side
//     apply would drop that block itself (strategy merges with retainKeys);
//     the patch is a safeguard, the one Grafana needed under helm (#320,
//     kubernetes/kubernetes#24198), and this keeps it from rotting.
func TestFileConsumerRecreates(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	script, err := os.ReadFile(filepath.Join(root, "infrastructure", "azure", "deploy-connectors-azure.sh"))
	if err != nil {
		t.Skipf("connector deploy script not available (%v); strategy guard skipped", err)
	}
	committed, err := os.ReadFile(filepath.Join(root, "infrastructure", "kubernetes", "connectors", "connectors.yaml"))
	if err != nil {
		t.Skipf("generated connector manifest not available (%v); strategy guard skipped", err)
	}
	rendered := renderConnectorManifest(t, script)

	if string(rendered) != string(committed) {
		t.Error("infrastructure/kubernetes/connectors/connectors.yaml differs from what deploy-connectors-azure.sh " +
			"generates, and the deploy applies the generated one. Change the script and regenerate with " +
			"GENERATE_ONLY=1 bash infrastructure/azure/deploy-connectors-azure.sh")
	}

	for _, m := range []struct {
		name     string
		manifest []byte
	}{{"rendered", rendered}, {"committed", committed}} {
		strategies := connectorStrategies(t, m.manifest)
		if len(strategies) < 2 {
			t.Fatalf("%s manifest: found %d Deployments; the layout probably moved", m.name, len(strategies))
		}
		fc, ok := strategies["vrsky-file-consumer"]
		switch {
		case !ok:
			t.Errorf("%s manifest has no vrsky-file-consumer Deployment", m.name)
		case fc.Type != "Recreate":
			t.Errorf("%s manifest: vrsky-file-consumer strategy is %q, want Recreate: a rolling update has "+
				"two pods polling the same folders, and either can publish a file", m.name, fc.Type)
		case fc.RollingUpdate != nil:
			t.Errorf("%s manifest: vrsky-file-consumer is Recreate with a rollingUpdate block; the API server "+
				"rejects a Recreate Deployment that carries one", m.name)
		}
	}

	// Every Recreate connector is switched before the apply, not after it.
	body := string(script)
	apply := strings.Index(body, "\nkubectl apply -f \"$MANIFEST\"")
	if apply < 0 {
		t.Fatal(`deploy-connectors-azure.sh has no 'kubectl apply -f "$MANIFEST"' line; the layout probably moved`)
	}
	// On a line that runs, not in a comment.
	patch := regexp.MustCompile(`(?m)^[ \t]*[^#\s][^\n]*'\{"spec":\{"strategy":\{"rollingUpdate":null,"type":"Recreate"\}\}\}'`)
	if !patch.MatchString(body) {
		t.Error("deploy-connectors-azure.sh no longer patches a Deployment to Recreate with rollingUpdate " +
			"dropped; that pre-apply safeguard is what drop_rolling_update is for")
	}
	for name, s := range connectorStrategies(t, rendered) {
		if s.Type != "Recreate" {
			continue
		}
		call := strings.Index(body, "\ndrop_rolling_update "+name+"\n")
		if call < 0 || call > apply {
			t.Errorf("%s is Recreate in the manifest, but deploy-connectors-azure.sh does not call "+
				"drop_rolling_update %s before its apply, the safeguard for a cluster where it is still "+
				"RollingUpdate", name, name)
		}
	}
}
