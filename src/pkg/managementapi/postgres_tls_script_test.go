package managementapi

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// These tests run infrastructure/azure/postgres-tls.sh for real, against a
// kubectl stand-in that only appends its arguments to a log. They never touch
// a cluster: KUBECONFIG is /dev/null, PATH holds the stub first and nothing
// that contains a real kubectl, and the test checks that before it runs the
// script. They exist because the script is the one thing in the repo that
// writes to production by hand, and its safety rules (a confirmation, failing
// closed, cleaning up) are behaviour, not text.

type kubeStub struct {
	dir  string
	log  string
	env  []string
	bash string
}

const kubectlStubScript = `#!/bin/sh
echo "$*" >> "$STUB_LOG"
case "$1" in
  config) echo stub-context ;;
  get)
    case "$2" in
      secret)
        case "$*" in
          *crt*) cat "$STUB_CA_B64" ;;
        esac ;;
      deploy)
        case "$*" in
          *tier=connector*)
            if [ -n "$STUB_FAIL_LIST" ]; then exit 1; fi
            cat "$STUB_CONNECTORS" ;;
          *) echo postgres-ca ;;
        esac ;;
      cronjob) echo postgres-ca ;;
      configmap) echo present ;;
      pod) printf '%s' "${STUB_PHASE:-Succeeded}" ;;
    esac ;;
  exec) echo on ;;
  apply | diff) cat > /dev/null ;;
  logs) echo "ssl=true TLSv1.3" ;;
esac
exit 0
`

func newKubeStub(t *testing.T, connectors int) *kubeStub {
	t.Helper()
	bash := "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		t.Skip("/bin/bash not available")
	}
	opensslPath, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not available")
	}
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(stubPath, []byte(kubectlStubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(opensslPath, filepath.Join(dir, "openssl")); err != nil {
		t.Fatal(err)
	}

	// A real CA certificate, base64 on one line, as the Secret holds it.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "vrsky-postgresql-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caB64 := filepath.Join(dir, "ca.b64")
	if err := os.WriteFile(caB64, []byte(base64.StdEncoding.EncodeToString(caPEM)), 0o644); err != nil {
		t.Fatal(err)
	}

	var names strings.Builder
	for i := 0; i < connectors; i++ {
		fmt.Fprintf(&names, "deployment.apps/vrsky-conn-%02d\n", i)
	}
	conns := filepath.Join(dir, "connectors.txt")
	if err := os.WriteFile(conns, []byte(names.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &kubeStub{dir: dir, log: filepath.Join(dir, "kubectl.log"), bash: bash}
	s.env = []string{
		"PATH=" + dir + ":/usr/bin:/bin",
		"KUBECONFIG=/dev/null",
		"HOME=" + dir,
		"STUB_LOG=" + s.log,
		"STUB_CA_B64=" + caB64,
		"STUB_CONNECTORS=" + conns,
	}

	// The stub must be the only kubectl the script can find.
	probe := exec.Command("/bin/sh", "-c", "command -v kubectl")
	probe.Env = s.env
	out, err := probe.Output()
	if err != nil || strings.TrimSpace(string(out)) != stubPath {
		t.Fatalf("the kubectl stub is not first on PATH (got %q, %v); refusing to run the script", out, err)
	}
	return s
}

// run executes the script with the stub. extra is "KEY=value" pairs.
func (s *kubeStub) run(t *testing.T, extra []string, args ...string) (exit int, stdout, stderr string) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "infrastructure", "azure", "postgres-tls.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("postgres-tls.sh not available (%v)", err)
	}
	_ = os.Remove(s.log)
	cmd := exec.Command(s.bash, append([]string{script}, args...)...)
	cmd.Env = append(append([]string{}, s.env...), extra...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run: %v", err)
		}
		code = ee.ExitCode()
	}
	return code, so.String(), se.String()
}

// calls returns the logged kubectl invocations, one per line.
func (s *kubeStub) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(s.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

var kubectlWriteVerb = regexp.MustCompile(`^(apply|create|patch|delete|replace|label|annotate|scale|set|rollout (restart|undo|pause|resume))\b`)

func writes(calls []string) []string {
	var w []string
	for _, c := range calls {
		if kubectlWriteVerb.MatchString(c) {
			w = append(w, c)
		}
	}
	return w
}

// A pasted command must not change a cluster by itself: every writing
// subcommand stops at the gate, before any kubectl write.
func TestPostgresTLS_WritesNeedConfirm(t *testing.T) {
	s := newKubeStub(t, 31)
	for _, args := range [][]string{
		{"server"}, {"ca-sync"}, {"clients"}, {"urls", "verify-full"}, {"urls", "disable"}, {"canary"}, {"restart"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, out, errOut := s.run(t, nil, args...)
			if code == 0 {
				t.Errorf("%v exited 0 without CONFIRM=1", args)
			}
			if !strings.Contains(errOut, "CONFIRM=1") {
				t.Errorf("%v did not say it needs CONFIRM=1: %q", args, errOut)
			}
			if !strings.Contains(out, "kube context: stub-context") {
				t.Errorf("%v did not print the kube context before stopping: %q", args, out)
			}
			if w := writes(s.calls(t)); len(w) > 0 {
				t.Errorf("%v attempted kubectl writes without CONFIRM: %v", args, w)
			}
		})
	}
	// Anything but exactly 1 is not a confirmation.
	if code, _, _ := s.run(t, []string{"CONFIRM=yes"}, "restart"); code == 0 || len(writes(s.calls(t))) > 0 {
		t.Error("CONFIRM=yes confirmed a restart")
	}
}

// DRY_RUN and check need no confirmation and write nothing.
func TestPostgresTLS_DryRunAndCheckWriteNothing(t *testing.T) {
	s := newKubeStub(t, 31)
	for _, args := range [][]string{
		{"check"}, {"server"}, {"ca-sync"}, {"clients"}, {"canary"}, {"restart"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			extra := []string{"DRY_RUN=1"}
			if args[0] == "check" {
				extra = nil
			}
			code, _, errOut := s.run(t, extra, args...)
			if code != 0 {
				t.Errorf("%v exited %d: %s", args, code, errOut)
			}
			if w := writes(s.calls(t)); len(w) > 0 {
				t.Errorf("%v wrote to the cluster: %v", args, w)
			}
		})
	}
}

// On bash 3.2 (the macOS default) a RETURN trap in ca-sync fired again when
// main returned and aborted with "tmp: unbound variable", which would have
// stopped rotate-secrets.sh after ALTER ROLE. Run it under /bin/bash.
func TestPostgresTLS_CASyncExitsCleanly(t *testing.T) {
	s := newKubeStub(t, 31)
	code, out, errOut := s.run(t, []string{"CONFIRM=1"}, "ca-sync")
	if code != 0 {
		t.Fatalf("ca-sync exited %d under %s\nstdout: %s\nstderr: %s", code, s.bash, out, errOut)
	}
	if strings.Contains(errOut, "unbound variable") {
		t.Errorf("ca-sync reported an unbound variable: %s", errOut)
	}
	applies := 0
	for _, c := range s.calls(t) {
		if strings.HasPrefix(c, "apply") {
			applies++
		}
	}
	if applies != 2 {
		t.Errorf("ca-sync applied %d ConfigMaps, want 2 (vrsky-platform, vrsky-database)", applies)
	}
}

// However the canary ends, its pod is deleted last.
func TestPostgresTLS_CanaryAlwaysDeletesItsPod(t *testing.T) {
	s := newKubeStub(t, 31)
	for _, phase := range []string{"Succeeded", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			code, _, _ := s.run(t, []string{"CONFIRM=1", "STUB_PHASE=" + phase}, "canary")
			if (phase == "Succeeded") != (code == 0) {
				t.Errorf("phase %s: exit %d", phase, code)
			}
			calls := s.calls(t)
			if len(calls) == 0 || !strings.HasPrefix(calls[len(calls)-1], "delete pod pg-tls-canary") {
				t.Errorf("the last kubectl call was not the pod delete: %v", calls)
			}
		})
	}
	// The script is also told to clean up on INT and TERM.
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "postgres-tls.sh"))
	if err != nil {
		t.Skip(err)
	}
	for _, want := range []string{"trap canary_cleanup EXIT", "trap 'exit 130' INT", "trap 'exit 143' TERM"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("postgres-tls.sh lacks %q", want)
		}
	}
}

// restart and urls trust the connector list. A short, empty or failed list
// must stop them before anything changes.
func TestPostgresTLS_ConnectorListFailsClosed(t *testing.T) {
	cases := []struct {
		name       string
		connectors int
		extra      []string
		wantOK     bool
	}{
		{"all 31", 31, nil, true},
		{"empty", 0, nil, false},
		{"one short", 30, nil, false},
		{"listing fails", 31, []string{"STUB_FAIL_LIST=1"}, false},
	}
	for _, c := range cases {
		t.Run("restart "+c.name, func(t *testing.T) {
			s := newKubeStub(t, c.connectors)
			code, _, errOut := s.run(t, append([]string{"CONFIRM=1"}, c.extra...), "restart")
			restarts := 0
			for _, call := range s.calls(t) {
				if strings.HasPrefix(call, "rollout restart") {
					restarts++
				}
			}
			if c.wantOK {
				if code != 0 || restarts != 3+31 {
					t.Errorf("exit %d, %d restarts (want 0 and %d): %s", code, restarts, 3+31, errOut)
				}
				return
			}
			if code == 0 || restarts != 0 {
				t.Errorf("exit %d with %d restarts; it must stop before restarting anything: %s", code, restarts, errOut)
			}
		})
		if c.wantOK {
			continue
		}
		t.Run("urls verify-full "+c.name, func(t *testing.T) {
			s := newKubeStub(t, c.connectors)
			code, _, errOut := s.run(t, append([]string{"CONFIRM=1"}, c.extra...), "urls", "verify-full")
			if code == 0 {
				t.Errorf("urls verify-full succeeded with a bad connector list: %s", errOut)
			}
			for _, call := range s.calls(t) {
				if strings.HasPrefix(call, "patch") {
					t.Errorf("urls patched a Secret with a bad connector list: %s", call)
				}
			}
		})
	}
}

// The count the script insists on is the fleet the generator emits.
func TestPostgresTLS_ExpectedConnectorsMatchesTheManifest(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(k8sRoot(), "connectors", "connectors.yaml"))
	n := 0
	for _, d := range docs {
		if str(d["kind"]) == "Deployment" && str(at(d, "metadata", "labels", "tier")) == "connector" {
			n++
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "postgres-tls.sh"))
	if err != nil {
		t.Skip(err)
	}
	want := fmt.Sprintf(`EXPECTED_CONNECTORS="${EXPECTED_CONNECTORS:-%d}"`, n)
	if !strings.Contains(string(raw), want) {
		t.Errorf("connectors.yaml has %d connector Deployments but postgres-tls.sh does not say %s", n, want)
	}
}

// rotate-secrets.sh calls ca-sync itself, so it passes the confirmation: the
// human already ran the rotation on purpose.
func TestPostgresTLS_RotateSecretsConfirmsItsOwnCASync(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "infrastructure", "azure", "rotate-secrets.sh"))
	if err != nil {
		t.Skip(err)
	}
	if !strings.Contains(string(raw), "CONFIRM=1 bash infrastructure/azure/postgres-tls.sh ca-sync") {
		t.Error("rotate-secrets.sh calls postgres-tls.sh ca-sync without CONFIRM=1; the call would stop at the gate mid-rotation")
	}
}
