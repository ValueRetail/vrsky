package managementapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// These tests run infrastructure/azure/smoke-prod.sh for real against a
// kubectl stand-in that models one deployment mid-rollout: an old
// ReplicaSet (revision 1) whose pod is still terminating and a new one
// (revision 2). `kubectl logs deploy/…` answers with the old pod's logs, as
// it did in run 41621931134 (#741). KUBECONFIG is /dev/null and PATH holds
// the stub first and nothing that contains a real kubectl; the test checks
// that before it runs the script.

const (
	smokeDeploy = "vrsky-demo-consumer"
	// What the stub answers; TestSmokeProdTemplates checks the script's
	// go-templates produce exactly this from real-shaped objects.
	smokeDeployOut = "2 app=" + smokeDeploy + ","
	smokeRSOut     = "1 old1111\n2 new2222\n"
	restoredLine   = `{"level":"INFO","msg":"Restored running pipelines","count":3}`
)

const smokeKubectlStub = `#!/bin/sh
echo "$*" >> "$STUB_DIR/calls"
for a in "$@"; do
  case "$a" in go-template=*) printf '%s' "${a#go-template=}" > "$STUB_DIR/tpl-$2" ;; esac
done
case "$1" in
  get)
    case "$2" in
      --raw) echo '{"status":"success","data":{"groups":[]}}' ;;
      deploy) [ "$5" = "$STUB_DEPLOY" ] || exit 1; printf '%s' "$STUB_DEPLOY_OUT" ;;
      rs) case "$*" in *"-l app=$STUB_DEPLOY "*) printf '%s' "$STUB_RS_OUT" ;; *) exit 1 ;; esac ;;
      *) exit 1 ;;
    esac ;;
  logs)
    case "$*" in
      *"deploy/$STUB_DEPLOY"*) f=old ;;
      *"-l app=$STUB_DEPLOY,pod-template-hash=new2222 "*) f=new ;;
      *"-l app=$STUB_DEPLOY,pod-template-hash=old1111 "*) f=old ;;
      *) exit 1 ;;
    esac
    # kubectl logs -l keeps only the last 10 lines per pod unless told otherwise.
    case "$*" in
      *"-l "*) case "$*" in *--tail=-1*) cat "$STUB_DIR/logs-$f" ;; *) tail -n 10 "$STUB_DIR/logs-$f" ;; esac ;;
      *) cat "$STUB_DIR/logs-$f" ;;
    esac ;;
  *) exit 1 ;;
esac
`

// podLogs is what `kubectl logs --prefix` prints for one pod: the restore
// line (when restored) followed by enough later lines that a default
// --tail of 10 would cut it off.
func podLogs(pod string, restored bool) string {
	var b strings.Builder
	if restored {
		b.WriteString("[pod/" + pod + "/consumer] " + restoredLine + "\n")
	}
	for i := 0; i < 15; i++ {
		b.WriteString("[pod/" + pod + "/consumer] {\"level\":\"INFO\",\"msg\":\"delivered\"}\n")
	}
	return b.String()
}

type smokeRun struct {
	out   string
	err   error
	calls []string
	dir   string
}

func runSmokeRestore(t *testing.T, oldRestored, newRestored bool) smokeRun {
	t.Helper()
	bash := "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		t.Skip("/bin/bash not available")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "infrastructure", "azure", "smoke-prod.sh"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	stub := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(stub, []byte(smokeKubectlStub), 0o700); err != nil { //nolint:gosec // an executable stub in a temp dir
		t.Fatal(err)
	}
	for f, body := range map[string]string{
		"logs-old": podLogs(smokeDeploy+"-old1111-abcde", oldRestored),
		"logs-new": podLogs(smokeDeploy+"-new2222-fghij", newRestored),
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{
		"PATH=" + dir + ":/usr/bin:/bin",
		"KUBECONFIG=/dev/null",
		"HOME=" + dir,
		"PUBLIC_URL=" + srv.URL,
		"SMOKE_RESTORE_TRIES=2",
		"STUB_DIR=" + dir,
		"STUB_DEPLOY=" + smokeDeploy,
		"STUB_DEPLOY_OUT=" + smokeDeployOut,
		"STUB_RS_OUT=" + smokeRSOut,
	}

	// The stub must be the only kubectl the script can find.
	probe := exec.Command("/bin/sh", "-c", "command -v kubectl")
	probe.Env = env
	got, err := probe.Output()
	if err != nil || strings.TrimSpace(string(got)) != stub {
		t.Fatalf("the kubectl stub is not first on PATH (got %q, %v); refusing to run the script", got, err)
	}

	cmd := exec.Command(bash, script, smokeDeploy)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			t.Fatalf("run: %v", runErr)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "calls")) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatalf("the kubectl stub was never called: %v\n%s", err, out.String())
	}
	var calls []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			calls = append(calls, l)
		}
	}
	return smokeRun{out: out.String(), err: runErr, calls: calls, dir: dir}
}

// After a rollout `kubectl logs deploy/…` can pick the old pod that is still
// terminating, whose restore line is older than the window. The check reads
// the current ReplicaSet's pods only and passes if any of them restored
// (#741, plans/issue-741-smoke-current-pods.md).
func TestSmokeProdReadsCurrentPods(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		oldRestored, newRestored bool
		pass                     bool
	}{
		{"new pod restored, old pod did not", false, true, true},
		{"no current pod restored", false, false, false},
		{"only the old pod restored in the window", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runSmokeRestore(t, tc.oldRestored, tc.newRestored)
			if tc.pass {
				if r.err != nil || !strings.Contains(r.out, smokeDeploy+": restored its pipelines") || !strings.Contains(r.out, "smoke: ok") {
					t.Fatalf("want a pass; err=%v\n%s", r.err, r.out)
				}
			} else {
				want := smokeDeploy + ": no 'Restored running pipelines' line within 60 s"
				if r.err == nil || !strings.Contains(r.out, want) || !strings.Contains(r.out, "smoke: FAILED") {
					t.Fatalf("want a failure with %q; err=%v\n%s", want, r.err, r.out)
				}
			}
			var logCalls int
			for _, c := range r.calls {
				if strings.HasPrefix(c, "logs ") {
					logCalls++
					if strings.Contains(c, "deploy/") || strings.Contains(c, "old1111") {
						t.Errorf("read logs beyond the current pods: %q", c)
					}
				}
			}
			if logCalls == 0 {
				t.Errorf("never read any logs; calls:\n%s", strings.Join(r.calls, "\n"))
			}
		})
	}
}

// The stub answers with fixed strings, so check that the go-templates the
// script hands kubectl produce those strings from real-shaped objects.
// kubectl renders -o go-template with text/template over the object decoded
// from JSON, as here.
func TestSmokeProdTemplates(t *testing.T) {
	r := runSmokeRestore(t, false, true)
	if r.err != nil {
		t.Fatalf("smoke failed: %v\n%s", r.err, r.out)
	}
	for _, tc := range []struct{ kind, object, want string }{
		{"deploy", `{"apiVersion":"apps/v1","kind":"Deployment",
			"metadata":{"name":"` + smokeDeploy + `","namespace":"vrsky-platform",
				"annotations":{"deployment.kubernetes.io/revision":"2","kubectl.kubernetes.io/last-applied-configuration":"{}"},
				"labels":{"app":"` + smokeDeploy + `","tier":"connector"}},
			"spec":{"replicas":1,"selector":{"matchLabels":{"app":"` + smokeDeploy + `"}}}}`, smokeDeployOut},
		{"rs", `{"apiVersion":"v1","kind":"List","items":[
			{"kind":"ReplicaSet","metadata":{"name":"` + smokeDeploy + `-old1111",
				"annotations":{"deployment.kubernetes.io/desired-replicas":"1","deployment.kubernetes.io/revision":"1"},
				"labels":{"app":"` + smokeDeploy + `","pod-template-hash":"old1111"}}},
			{"kind":"ReplicaSet","metadata":{"name":"` + smokeDeploy + `-new2222",
				"annotations":{"deployment.kubernetes.io/desired-replicas":"1","deployment.kubernetes.io/revision":"2"},
				"labels":{"app":"` + smokeDeploy + `","pod-template-hash":"new2222"}}}]}`, smokeRSOut},
	} {
		raw, err := os.ReadFile(filepath.Join(r.dir, "tpl-"+tc.kind)) //nolint:gosec // the test's own temp file
		if err != nil {
			t.Fatalf("the script never passed a go-template to `kubectl get %s`: %v", tc.kind, err)
		}
		tpl, err := template.New(tc.kind).Parse(string(raw))
		if err != nil {
			t.Fatalf("%s template does not parse: %v\n%s", tc.kind, err, raw)
		}
		var obj any
		if err := json.Unmarshal([]byte(tc.object), &obj); err != nil {
			t.Fatal(err)
		}
		var got strings.Builder
		if err := tpl.Execute(&got, obj); err != nil {
			t.Fatalf("%s template: %v", tc.kind, err)
		}
		if got.String() != tc.want {
			t.Errorf("%s template renders %q, want %q", tc.kind, got.String(), tc.want)
		}
	}
}
