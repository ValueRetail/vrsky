package managementapi

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run infrastructure/azure/budget-check.sh, the Production
// check's Budget step (#744), against an `az` stand-in. The stand-in answers
// `az rest` per resource group from a list of canned responses and logs
// every call; `az aks stop` is only logged. PATH holds the stub's directory
// and /usr/bin:/bin, and the test refuses to run unless `az` resolves to the
// stub, so a missing stub can never reach a real az.

const azStubScript = `#!/bin/sh
echo "$*" >> "$STUB_LOG"
case "$1" in
  rest) ;;
  aks) exit 0 ;;
  *) echo "stub az: unexpected command: $*" >&2; exit 99 ;;
esac
url="" body=""
while [ $# -gt 0 ]; do
  case "$1" in
    --url) url=$2; shift ;;
    --body) body=$2; shift ;;
  esac
  shift
done
case "$body" in
  *filter*) echo 'ERROR: Bad Request({"error":{"code":"BadRequest","message":"stub: no filter expected at resource-group scope"}})' >&2; exit 1 ;;
esac
case "$url" in
  */resourceGroups/*/providers/Microsoft.CostManagement/query\?api-version=2023-11-01) ;;
  *) echo 'ERROR: Forbidden({"error":{"code":"AuthorizationFailed","message":"stub: the identity holds the role per resource group only"}})' >&2; exit 1 ;;
esac
rg=${url#*/resourceGroups/}
rg=${rg%%/*}
f="$STUB_DIR/resp-$rg"
[ -f "$f" ] || { echo "stub az: no responses for $rg" >&2; exit 98; }
line=$(head -n 1 "$f")
# Pop the first response; the last one repeats.
if [ "$(wc -l < "$f")" -gt 1 ]; then
  tail -n +2 "$f" > "$f.tmp" && mv "$f.tmp" "$f"
fi
case "$line" in
  ok*) v=${line#ok}; v=${v# }; [ -n "$v" ] && echo "$v"; exit 0 ;;
  429) echo 'ERROR: Too Many Requests({"error":{"code":"429","message":"Too many requests. Please retry."}})' >&2; exit 1 ;;
  403) echo 'ERROR: Forbidden({"error":{"code":"AuthorizationFailed","message":"The client does not have authorization to perform action Microsoft.CostManagement/query/action."}})' >&2; exit 1 ;;
esac
echo "stub az: bad canned response $line" >&2
exit 97
`

const (
	budgetRG = "vrsky-prod"
	budgetMC = "MC_vrsky-prod_vrsky-prod_norwayeast"
)

type budgetRun struct {
	code    int
	out     string
	summary string
	calls   []string
}

// runBudgetCheck runs the script with canned responses per resource group
// (one per line: "ok <cost>", "ok" for no rows, "429", "403").
func runBudgetCheck(t *testing.T, budget string, responses map[string][]string) budgetRun {
	t.Helper()
	bash := "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		t.Skip("/bin/bash not available")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "infrastructure", "azure", "budget-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "az")
	if err := os.WriteFile(stub, []byte(azStubScript), 0o755); err != nil { //nolint:gosec // the stub must be executable
		t.Fatal(err)
	}
	for rg, lines := range responses {
		if err := os.WriteFile(filepath.Join(dir, "resp-"+rg), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "az.log")
	summary := filepath.Join(dir, "summary.md")
	env := []string{
		"PATH=" + dir + ":/usr/bin:/bin",
		"HOME=" + dir,
		"KUBECONFIG=/dev/null",
		"AZURE_CONFIG_DIR=" + filepath.Join(dir, "azure"),
		"STUB_LOG=" + log,
		"STUB_DIR=" + dir,
		"GITHUB_STEP_SUMMARY=" + summary,
		"MONTHLY_BUDGET_NOK=" + budget,
		"AKS_RESOURCE_GROUP=" + budgetRG,
		"BUDGET_RETRY_DELAYS=0 0 0",
	}
	probe := exec.Command("/bin/sh", "-c", "command -v az")
	probe.Env = env
	got, err := probe.Output()
	if err != nil || strings.TrimSpace(string(got)) != stub {
		t.Fatalf("az resolves to %q (%v), not the stub; refusing to run the script", got, err)
	}

	cmd := exec.Command(bash, script, "00000000-0000-0000-0000-000000000000") //nolint:gosec // fixed script inside the repo
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	r := budgetRun{}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run: %v", err)
		}
		r.code = ee.ExitCode()
	}
	r.out = out.String()
	if b, err := os.ReadFile(summary); err == nil { //nolint:gosec // temp file the test made
		r.summary = string(b)
	}
	b, err := os.ReadFile(log) //nolint:gosec // temp file the test made
	if err != nil {
		t.Fatalf("the az stub was never called (%v); output:\n%s", err, r.out)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			r.calls = append(r.calls, l)
		}
	}
	return r
}

func (r budgetRun) queries(rg string) int {
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, "rest ") && strings.Contains(c, "/resourceGroups/"+rg+"/providers/Microsoft.CostManagement/query") {
			n++
		}
	}
	return n
}

func (r budgetRun) stops() []string {
	var s []string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "aks ") {
			s = append(s, c)
		}
	}
	return s
}

func TestBudgetCheckScript(t *testing.T) {
	cases := []struct {
		name      string
		budget    string
		responses map[string][]string
		code      int
		stop      bool
		queries   map[string]int
		mention   []string
		absent    []string
	}{
		{
			name:      "two scopes summed, under budget",
			budget:    "1500",
			responses: map[string][]string{budgetRG: {"ok 600.25"}, budgetMC: {"ok 400.50"}},
			code:      0,
			queries:   map[string]int{budgetRG: 1, budgetMC: 1},
			mention:   []string{"spent 1001 of 1500 NOK this month (66%)"},
			absent:    []string{"::error", "::warning"},
		},
		{
			name:      "no rows yet this month counts as zero",
			budget:    "1500",
			responses: map[string][]string{budgetRG: {"ok"}, budgetMC: {"ok"}},
			code:      0,
			queries:   map[string]int{budgetRG: 1, budgetMC: 1},
			mention:   []string{"spent 0 of 1500 NOK this month (0%)"},
		},
		{
			name:      "over budget stops the cluster",
			budget:    "1500",
			responses: map[string][]string{budgetRG: {"ok 900"}, budgetMC: {"ok 700.10"}},
			code:      1,
			stop:      true,
			queries:   map[string]int{budgetRG: 1, budgetMC: 1},
			mention:   []string{"::error title=Monthly budget reached::", "spent 1600 of 1500"},
		},
		{
			name:      "429 then success",
			budget:    "1500",
			responses: map[string][]string{budgetRG: {"429", "ok 100"}, budgetMC: {"429", "429", "ok 50"}},
			code:      0,
			queries:   map[string]int{budgetRG: 2, budgetMC: 3},
			mention:   []string{"HTTP 429 (Too Many Requests), code 429", "retry 1 of 3", "spent 150 of 1500"},
			absent:    []string{"::error", "::warning"},
		},
		{
			name:      "429 throughout: warning, no verdict",
			budget:    "100",
			responses: map[string][]string{budgetRG: {"429"}, budgetMC: {"ok 5000"}},
			code:      0,
			queries:   map[string]int{budgetRG: 4, budgetMC: 1},
			mention:   []string{"::warning title=Budget not checked::", budgetRG},
			absent:    []string{"spent ", "::error"},
		},
		{
			name:      "403 fails the run with its status",
			budget:    "1500",
			responses: map[string][]string{budgetRG: {"403"}, budgetMC: {"ok 10"}},
			code:      1,
			queries:   map[string]int{budgetRG: 1, budgetMC: 1},
			mention:   []string{"HTTP 403 (Forbidden), code AuthorizationFailed", "::error title=Budget check failed::"},
			absent:    []string{"spent ", "retry "},
		},
		{
			name:      "a 403 is not hidden by a 429 on the other group",
			budget:    "1500",
			responses: map[string][]string{budgetRG: {"429"}, budgetMC: {"403"}},
			code:      1,
			queries:   map[string]int{budgetRG: 4, budgetMC: 1},
			mention:   []string{"HTTP 403", "::error title=Budget check failed::"},
			absent:    []string{"::warning"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runBudgetCheck(t, tc.budget, tc.responses)
			if r.code != tc.code {
				t.Errorf("exit %d, want %d\n%s", r.code, tc.code, r.out)
			}
			for rg, n := range tc.queries {
				if got := r.queries(rg); got != n {
					t.Errorf("%d queries at %s scope, want %d; calls: %v", got, rg, n, r.calls)
				}
			}
			for _, c := range r.calls {
				if strings.HasPrefix(c, "rest ") && !strings.Contains(c, "/resourceGroups/") {
					t.Errorf("a query outside resource-group scope: %s", c)
				}
			}
			stops := r.stops()
			if tc.stop {
				if len(stops) != 1 || stops[0] != "aks stop -g "+budgetRG+" -n vrsky-prod" {
					t.Errorf("want exactly one `aks stop -g %s -n vrsky-prod`, got %v", budgetRG, stops)
				}
			} else if len(stops) > 0 {
				t.Errorf("the cluster was stopped: %v\n%s", stops, r.out)
			}
			for _, m := range tc.mention {
				if !strings.Contains(r.out, m) {
					t.Errorf("output lacks %q:\n%s", m, r.out)
				}
			}
			for _, m := range tc.absent {
				if strings.Contains(r.out, m) {
					t.Errorf("output has %q:\n%s", m, r.out)
				}
			}
			if tc.code == 0 && !tc.stop && r.summary == "" {
				t.Errorf("nothing in the job summary; every run should show the spend or why there is none")
			}
		})
	}
}
