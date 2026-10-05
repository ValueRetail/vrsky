package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ValueRetail/vrsky/pkg/notify"
)

// fakeAPI stands in for the management API and records what reached it.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string // "METHOD path" in order
	auth  []string // "Authorization|X-Tenant-ID" per call
	srv   *httptest.Server
	fail  map[string]int // "METHOD path" → status to answer with
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{fail: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		key := r.Method + " " + r.URL.RequestURI()
		f.calls = append(f.calls, key)
		f.auth = append(f.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Tenant-ID"))
		status := f.fail[key]
		f.mu.Unlock()
		if status != 0 {
			http.Error(w, `{"error":"nope"}`, status)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/connections":
			// The management API's ListResponse envelope.
			_, _ = io.WriteString(w, `{"data":[{"id":"c1","name":"Catalogue","status":"error","last_error":"401 from Business Central","nodes":[{"config":{"client_secret":"hunter2"}}]}],"total":1,"limit":50,"offset":0}`)
		case strings.HasSuffix(r.URL.Path, "/c1"):
			_, _ = io.WriteString(w, `{"id":"c1","status":"error","nodes":[{"config":{"business_central":{"client_secret":"hunter2","client_secret_secret_id":"sec-1","api_key":"k","company_id":"C1"}}}]}`)
		case strings.HasSuffix(r.URL.Path, "/dlq/7"):
			_, _ = io.WriteString(w, `{"data":{"seq":7,"reason":"upstream 503","payload":"`+strings.Repeat("x", 10000)+`"}}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeAPI) client() *apiClient {
	return &apiClient{baseURL: f.srv.URL, http: f.srv.Client(), keys: map[string]string{"tenant-a": "vrsky_a_key"}}
}

func callTool(t *testing.T, tools []Tool, name, input string) (string, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl.Call(context.Background(), json.RawMessage(input))
		}
	}
	t.Fatalf("no tool %q in the set (have %v)", name, toolNames(tools))
	return "", nil
}

func toolNames(tools []Tool) []string {
	var n []string
	for _, tl := range tools {
		n = append(n, tl.Name)
	}
	return n
}

// The action tools do not exist in observe mode — the model cannot call what
// it is not given — and exist, marked, in act mode.
func TestTools_ActToolsOnlyInActMode(t *testing.T) {
	r := &run{tenantID: "tenant-a", api: newFakeAPI(t).client(), guard: newGuard(20), maxCalls: 25}
	for _, tl := range r.tools(false) {
		if tl.Act || strings.Contains(tl.Name, "redeploy") || strings.Contains(tl.Name, "retry") || strings.Contains(tl.Name, "resend") {
			t.Errorf("observe mode exposes the action tool %q", tl.Name)
		}
	}
	var acts []string
	for _, tl := range r.tools(true) {
		if tl.Act {
			acts = append(acts, tl.Name)
		}
	}
	if strings.Join(acts, ",") != "redeploy_pipeline,retry_dlq_message,resend_everything" {
		t.Errorf("act tools = %v", acts)
	}
}

// Every call goes out as the workspace, with its own key; what comes back has
// credentials blanked (secret ids kept) before the model sees it.
func TestTools_CallAsTheWorkspaceAndRedactCredentials(t *testing.T) {
	f := newFakeAPI(t)
	r := &run{tenantID: "tenant-a", api: f.client(), guard: newGuard(20), maxCalls: 25}
	out, err := callTool(t, r.tools(false), "get_pipeline", `{"connection_id":"c1"}`)
	if err != nil {
		t.Fatalf("get_pipeline: %v", err)
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, `"api_key":"k"`) {
		t.Errorf("credentials reached the model: %s", out)
	}
	if !strings.Contains(out, "sec-1") || !strings.Contains(out, "C1") {
		t.Errorf("non-secret fields were lost: %s", out)
	}
	list, err := callTool(t, r.tools(false), "list_pipelines", `{}`)
	if err != nil || strings.Contains(list, "hunter2") || !strings.Contains(list, "401 from Business Central") {
		t.Errorf("list_pipelines = %q, %v", list, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.auth {
		if a != "Bearer vrsky_a_key|tenant-a" {
			t.Errorf("request authenticated as %q, want the workspace's key and id", a)
		}
	}
	// A workspace without a key cannot be reached at all.
	other := &run{tenantID: "tenant-b", api: f.client(), guard: newGuard(20), maxCalls: 25}
	if _, err := callTool(t, other.tools(false), "list_pipelines", `{}`); err == nil {
		t.Error("a workspace without a configured key was served")
	}
}

// An action runs at most once per pipeline per hour whichever run asks,
// needs a reason, and a refusal or an API error is an error the model sees.
func TestTools_ActionsAreRateLimitedAndNeedAReason(t *testing.T) {
	f := newFakeAPI(t)
	g := newGuard(20)
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	r := &run{tenantID: "tenant-a", api: f.client(), guard: g, maxCalls: 25}
	tools := r.tools(true)

	if _, err := callTool(t, tools, "redeploy_pipeline", `{"connection_id":"c1"}`); err == nil {
		t.Error("an action without a reason was accepted")
	}
	if _, err := callTool(t, tools, "redeploy_pipeline", `{"connection_id":"c1","reason":"upstream 503s stopped"}`); err != nil {
		t.Fatalf("first redeploy: %v", err)
	}
	if got := strings.Join(f.called(), " ; "); got != "POST /api/v1/connections/c1/stop ; POST /api/v1/connections/c1/start" {
		t.Errorf("redeploy sent %q, want stop then start", got)
	}
	// A second run (another alert) on the same pipeline within the hour.
	r2 := &run{tenantID: "tenant-a", api: f.client(), guard: g, maxCalls: 25}
	if _, err := callTool(t, r2.tools(true), "redeploy_pipeline", `{"connection_id":"c1","reason":"again"}`); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("second redeploy within the hour = %v, want a refusal", err)
	}
	if n := len(f.called()); n != 2 {
		t.Errorf("a refused action still reached the API (%d calls)", n)
	}
	now = now.Add(61 * time.Minute)
	if _, err := callTool(t, r2.tools(true), "redeploy_pipeline", `{"connection_id":"c1","reason":"an hour later"}`); err != nil {
		t.Errorf("redeploy after the cooldown: %v", err)
	}
	if len(r.actions) != 1 || len(r2.actions) != 1 {
		t.Errorf("recorded actions = %v / %v, want one each", r.actions, r2.actions)
	}

	// DLQ retries: 20 per pipeline per hour, then refused; an API failure is surfaced.
	for i := 1; i <= maxDLQRetriesPerHour; i++ {
		if _, err := callTool(t, tools, "retry_dlq_message", `{"connection_id":"c1","seq":`+itoa(i)+`,"reason":"transient"}`); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	if _, err := callTool(t, tools, "retry_dlq_message", `{"connection_id":"c1","seq":99,"reason":"transient"}`); err == nil {
		t.Error("the 21st DLQ retry in an hour was accepted")
	}
	f.mu.Lock()
	f.fail["POST /api/v1/connections/c2/resend"] = http.StatusConflict
	f.mu.Unlock()
	if _, err := callTool(t, tools, "resend_everything", `{"connection_id":"c2","reason":"till lost data"}`); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("a 409 from the API = %v, want an error naming it", err)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// The per-run budget stops a run that keeps calling tools.
func TestTools_CallBudget(t *testing.T) {
	r := &run{tenantID: "tenant-a", api: newFakeAPI(t).client(), guard: newGuard(20), maxCalls: 2}
	tools := r.tools(false)
	for i := 0; i < 2; i++ {
		if _, err := callTool(t, tools, "list_agents", `{}`); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := callTool(t, tools, "list_agents", `{}`); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("third call = %v, want the budget refusal", err)
	}
}

func TestGuard_RunCooldownAndHourlyCap(t *testing.T) {
	g := newGuard(2)
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	a := &notify.Alert{Name: "ConnectionInError", Labels: map[string]string{"tenant_id": "t", "severity": "critical"}}
	b := &notify.Alert{Name: "ConnectionInError", Labels: map[string]string{"severity": "critical", "tenant_id": "t"}}
	if fingerprint(a) != fingerprint(b) {
		t.Fatal("the fingerprint depends on label order")
	}
	if ok, _ := g.admitRun(fingerprint(a)); !ok {
		t.Fatal("first run refused")
	}
	// Alertmanager repeats the same alert: no second run inside the hour.
	if ok, _ := g.admitRun(fingerprint(a)); ok {
		t.Error("the same alert got a second run within the hour")
	}
	if ok, _ := g.admitRun("other-1"); !ok {
		t.Error("a different alert was refused below the cap")
	}
	if ok, why := g.admitRun("other-2"); ok || !strings.Contains(why, "cap") {
		t.Errorf("third run in an hour with cap 2: ok=%v why=%q", ok, why)
	}
	now = now.Add(61 * time.Minute)
	if ok, _ := g.admitRun(fingerprint(a)); !ok {
		t.Error("the alert was still refused after the cooldown")
	}
}

// scriptedModel plays the part of Claude: it calls the named tools in order
// and returns a fixed report.
type scriptedModel struct {
	calls  [][2]string // tool name, input
	report string
	err    error
	gotSys string
	gotMsg string
	tools  []string
	runs   int
}

func (m *scriptedModel) Run(ctx context.Context, system, user string, tools []Tool) (RunResult, error) {
	m.runs++
	m.gotSys, m.gotMsg, m.tools = system, user, toolNames(tools)
	if m.err != nil {
		return RunResult{}, m.err
	}
	for _, c := range m.calls {
		for _, tl := range tools {
			if tl.Name == c[0] {
				_, _ = tl.Call(ctx, json.RawMessage(c[1]))
			}
		}
	}
	return RunResult{Text: m.report, InputTokens: 20000, OutputTokens: 1500, Iterations: 3}, nil
}

type capturedReport struct {
	about                 *notify.Alert
	status, summary, body string
}

type captureReporter struct {
	mu      sync.Mutex
	reports []capturedReport
}

func (c *captureReporter) post(_ context.Context, about *notify.Alert, status, summary, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, capturedReport{about, status, summary, body})
	return nil
}

func testResponder(t *testing.T, mode string, m Model) (*responder, *captureReporter, *fakeAPI) {
	t.Helper()
	f := newFakeAPI(t)
	rep := &captureReporter{}
	return &responder{
		mode: mode, model: m, api: f.client(), guard: newGuard(20), reporter: rep, maxCalls: 25,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), sem: make(chan struct{}, 1),
	}, rep, f
}

func firing(name, tenant string) *notify.Alert {
	labels := map[string]string{"alertname": name, "severity": "critical"}
	if tenant != "" {
		labels["tenant_id"] = tenant
	}
	return &notify.Alert{Name: name, Status: "firing", Severity: "critical", Summary: "1 pipeline(s) in error", TenantID: tenant, Labels: labels}
}

// The whole point: an alert becomes exactly one report about that alert, in
// the workspace it came from, with the model's summary line as the headline.
func TestHandle_AlertBecomesOneReport(t *testing.T) {
	m := &scriptedModel{
		calls:  [][2]string{{"list_pipelines", `{}`}, {"get_pipeline", `{"connection_id":"c1"}`}},
		report: "SUMMARY: Catalogue is failing on a rejected Business Central secret; needs a new secret.\n\nCause — 401 from Business Central.\nAction — none.\nNext — renew the client secret.",
	}
	s, rep, f := testResponder(t, modeObserve, m)
	a := firing("ConnectionInError", "tenant-a")
	s.handle(context.Background(), a)

	if len(rep.reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(rep.reports))
	}
	r := rep.reports[0]
	if r.about.TenantID != "tenant-a" || r.about.Severity != "critical" || r.status != "firing" {
		t.Errorf("report routed as %+v / %s", r.about, r.status)
	}
	if r.summary != "ConnectionInError: Catalogue is failing on a rejected Business Central secret; needs a new secret." {
		t.Errorf("summary = %q", r.summary)
	}
	if !strings.Contains(r.body, "renew the client secret") || !strings.Contains(r.body, "observe mode · 2 tool calls") || strings.Contains(r.body, "SUMMARY:") {
		t.Errorf("body = %q", r.body)
	}
	if !strings.Contains(m.gotMsg, "observe mode") || !strings.Contains(m.gotMsg, "tenant-a") || !strings.Contains(m.gotMsg, "ConnectionInError") {
		t.Errorf("the model was told: %q", m.gotMsg)
	}
	if got := strings.Join(f.called(), " ; "); got != "GET /api/v1/connections ; GET /api/v1/connections/c1" {
		t.Errorf("API calls = %q", got)
	}

	// Alertmanager repeats the alert: no second run, no second report.
	s.handle(context.Background(), firing("ConnectionInError", "tenant-a"))
	if m.runs != 1 || len(rep.reports) != 1 {
		t.Errorf("a repeated alert got another run (%d runs, %d reports)", m.runs, len(rep.reports))
	}
}

// What must never start a run: the responder's own report (a loop), the
// heartbeat and test alerts, info-level noise, and a workspace it has no key for.
func TestHandle_IgnoredAlerts(t *testing.T) {
	m := &scriptedModel{report: "SUMMARY: x"}
	s, rep, _ := testResponder(t, modeAct, m)
	for _, a := range []*notify.Alert{
		firing(notify.ResponderReportName, "tenant-a"),
		firing("Watchdog", ""),
		firing("TestAlert", "tenant-a"),
		{Name: "CPUThrottlingHigh", Status: "firing", Severity: "info", TenantID: "tenant-a"},
		firing("ConnectionInError", "tenant-without-a-key"),
	} {
		s.handle(context.Background(), a)
	}
	if m.runs != 0 || len(rep.reports) != 0 {
		t.Fatalf("ignored alerts caused %d runs and %d reports", m.runs, len(rep.reports))
	}
}

// Act mode: the action reaches the API, the report says so, and when the
// alert resolves a card follows — but only because the responder acted.
func TestHandle_ActModeActsAndReportsTheResolution(t *testing.T) {
	m := &scriptedModel{
		calls:  [][2]string{{"redeploy_pipeline", `{"connection_id":"c1","reason":"upstream 503s have stopped"}`}},
		report: "SUMMARY: Catalogue redeployed after upstream errors; recovering.\n\nAction — redeployed.",
	}
	s, rep, f := testResponder(t, modeAct, m)
	if !strings.Contains(strings.Join(func() []string {
		s.handle(context.Background(), firing("ConnectionInError", "tenant-a"))
		return m.tools
	}(), ","), "redeploy_pipeline") {
		t.Fatal("act mode did not offer the action tools")
	}
	if got := strings.Join(f.called(), " ; "); got != "POST /api/v1/connections/c1/stop ; POST /api/v1/connections/c1/start" {
		t.Fatalf("API calls = %q, want the redeploy", got)
	}
	resolved := firing("ConnectionInError", "tenant-a")
	resolved.Status = "resolved"
	s.handle(context.Background(), resolved)
	if len(rep.reports) != 2 || rep.reports[1].status != "resolved" {
		t.Fatalf("reports = %+v, want the diagnosis and a resolved card", rep.reports)
	}
	// A second resolve (or one for an alert it never acted on) says nothing.
	s.handle(context.Background(), resolved)
	other := firing("PipelineDown", "tenant-a")
	other.Status = "resolved"
	s.handle(context.Background(), other)
	if len(rep.reports) != 2 {
		t.Errorf("resolved cards without a prior action: %d reports", len(rep.reports))
	}
}

// Observe mode with a model that tries to act anyway: nothing reaches the API.
func TestHandle_ObserveModeCannotAct(t *testing.T) {
	m := &scriptedModel{
		calls:  [][2]string{{"redeploy_pipeline", `{"connection_id":"c1","reason":"x"}`}, {"resend_everything", `{"connection_id":"c1","reason":"x"}`}},
		report: "SUMMARY: would redeploy.",
	}
	s, rep, f := testResponder(t, modeObserve, m)
	s.handle(context.Background(), firing("ConnectionInError", "tenant-a"))
	if n := len(f.called()); n != 0 {
		t.Errorf("observe mode reached the API %d times: %v", n, f.called())
	}
	if len(rep.reports) != 1 {
		t.Fatalf("reports = %d", len(rep.reports))
	}
	resolved := firing("ConnectionInError", "tenant-a")
	resolved.Status = "resolved"
	s.handle(context.Background(), resolved)
	if len(rep.reports) != 1 {
		t.Error("a resolved card was posted although nothing was done")
	}
}

// A platform alert has no workspace: no tools, and the report is routed
// without a tenant so it reaches the platform targets.
func TestHandle_PlatformAlertHasNoTools(t *testing.T) {
	m := &scriptedModel{report: "SUMMARY: vrsky-sitoo-consumer has no running replica.\n\nNext — check the pod."}
	s, rep, _ := testResponder(t, modeAct, m)
	s.handle(context.Background(), firing("ConnectorUnavailable", ""))
	if len(m.tools) != 0 {
		t.Errorf("a platform alert got tools: %v", m.tools)
	}
	if len(rep.reports) != 1 || rep.reports[0].about.TenantID != "" {
		t.Fatalf("reports = %+v", rep.reports)
	}
}

// A failed run posts nothing: the plain alert card is already in the channel.
func TestHandle_ModelFailurePostsNothing(t *testing.T) {
	m := &scriptedModel{err: context.DeadlineExceeded}
	s, rep, _ := testResponder(t, modeAct, m)
	s.handle(context.Background(), firing("ConnectionInError", "tenant-a"))
	if len(rep.reports) != 0 {
		t.Errorf("a failed run posted %d reports", len(rep.reports))
	}
}

// The report goes back through the alerts webhook as a ResponderReport with
// the original workspace and severity — that is what puts it in the same
// channel — authenticated with the webhook token.
func TestWebhookReporter_PostsAResponderReport(t *testing.T) {
	var got struct {
		Status string `json:"status"`
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"alerts"`
	}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	w := &webhookReporter{url: srv.URL, token: "tok", http: srv.Client()}
	if err := w.post(context.Background(), firing("ConnectionInError", "tenant-a"), "firing", "sum", "body"); err != nil {
		t.Fatalf("post: %v", err)
	}
	if auth != "Bearer tok" || len(got.Alerts) != 1 {
		t.Fatalf("auth %q, alerts %d", auth, len(got.Alerts))
	}
	a := got.Alerts[0]
	if a.Labels["alertname"] != notify.ResponderReportName || a.Labels["tenant_id"] != "tenant-a" || a.Labels["severity"] != "critical" || a.Labels["about"] != "ConnectionInError" {
		t.Errorf("labels = %v", a.Labels)
	}
	if a.Annotations["summary"] != "sum" || a.Annotations["description"] != "body" {
		t.Errorf("annotations = %v", a.Annotations)
	}
}

func TestSplitReport_WithoutASummaryLineStillReports(t *testing.T) {
	sum, body := splitReport(firing("DLQGrowing", "t"), "The DLQ has three messages.")
	if sum != "diagnosis of DLQGrowing" || body != "The DLQ has three messages." {
		t.Errorf("summary %q body %q", sum, body)
	}
}

func TestParseTenantKeys(t *testing.T) {
	keys, err := parseTenantKeys("# prod\n t1 = vrsky_a_k1 \nt2=vrsky_b_k2,t3=k3\n")
	if err != nil || len(keys) != 3 || keys["t1"] != "vrsky_a_k1" || keys["t3"] != "k3" {
		t.Errorf("keys = %v, err %v", keys, err)
	}
	if _, err := parseTenantKeys("just-a-key"); err == nil {
		t.Error("an entry without a tenant id was accepted")
	}
}

// A dead-lettered payload is customer data: the model gets a sample, not the lot.
func TestTools_PayloadSamplesAreClipped(t *testing.T) {
	r := &run{tenantID: "tenant-a", api: newFakeAPI(t).client(), guard: newGuard(20), maxCalls: 25}
	out, err := callTool(t, r.tools(false), "get_dlq_message", `{"connection_id":"c1","seq":7}`)
	if err != nil {
		t.Fatalf("get_dlq_message: %v", err)
	}
	if !strings.Contains(out, "upstream 503") || !strings.Contains(out, "truncated") || len(out) > maxStringValue+500 {
		t.Errorf("result is %d bytes, want the reason plus a clipped payload sample", len(out))
	}
}

// TestClaudeModel_Live runs one real diagnosis against the Claude API to
// check the SDK wiring (tool runner, fallbacks beta, effort). It needs
// ANTHROPIC_API_KEY and costs a few cents, so it is skipped without one.
func TestClaudeModel_Live(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	f := newFakeAPI(t)
	r := &run{tenantID: "tenant-a", api: f.client(), guard: newGuard(20), maxCalls: 10}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s := &responder{mode: modeObserve}
	res, err := newClaudeModel("", 12).Run(ctx, systemPrompt, s.userMessage(firing("ConnectionInError", "tenant-a")), r.tools(false))
	if err != nil {
		t.Fatalf("live run: %v", err)
	}
	if !strings.HasPrefix(res.Text, "SUMMARY:") {
		t.Errorf("report does not start with SUMMARY: %q", res.Text)
	}
	if len(f.called()) == 0 {
		t.Error("the model reported without looking at anything")
	}
	t.Logf("live run: %d iterations, %d in / %d out tokens, ~$%.3f\n%s", res.Iterations, res.InputTokens, res.OutputTokens, costUSD(res), res.Text)
}
