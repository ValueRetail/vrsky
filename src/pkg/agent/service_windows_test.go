//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestService_InstallStartStopUninstall drives the real Windows service path
// end to end: build the agent, install it as a service, start it, see it reach
// a gateway, stop it, uninstall it. It runs as LocalSystem — the account it
// will run as on a customer machine — so it also proves that account can read
// the credential file locked down to SYSTEM and Administrators.
//
// Changes machine-wide state (a service, an Event Log source), so it only runs
// when asked: CI sets VRSKY_AGENT_SERVICE_TEST=1 on a disposable Windows runner.
func TestService_InstallStartStopUninstall(t *testing.T) {
	if os.Getenv("VRSKY_AGENT_SERVICE_TEST") != "1" {
		t.Skip("set VRSKY_AGENT_SERVICE_TEST=1 on a disposable Windows machine (needs admin)")
	}
	if s, err := ServiceStatus(); err == nil {
		t.Fatalf("a %s service already exists (%s); refusing to touch it", ServiceName, s)
	}

	exe := filepath.Join(t.TempDir(), "vrsky-agent.exe")
	build := exec.Command("go", "build", "-o", exe, "../../cmd/vrsky-agent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}

	g := newFakeGateway(t)
	c := testConfig(t)
	cfgPath := filepath.Join(filepath.Dir(c.DataDir), "config.json")
	cfgJSON := `{"poll_interval_seconds": 1, "data_dir": ` + jsonString(c.DataDir) + `,
		"directories": {"inbox": {"path": ` + jsonString(c.Directories["inbox"].Path) + `, "mode": "read"},
		                "outbox": {"path": ` + jsonString(c.Directories["outbox"].Path) + `, "mode": "write"}}}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredential(c.DataDir, &Credential{AgentID: "svc-test", Name: "svc-test",
		ServerURL: g.srv.URL, Credential: g.credential}); err != nil {
		t.Fatal(err)
	}

	if err := InstallService(exe, cfgPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = UninstallService() })
	if err := InstallService(exe, cfgPath); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Errorf("second install: err = %v, want already installed", err)
	}

	if err := StartService(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitStatus(t, "running")

	// The service process — LocalSystem, reading the locked-down credential —
	// reaches the gateway and announces itself.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, _, n := g.snapshot(); n > 0 {
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(c.DataDir, "logs", "agent.log"))
			t.Fatalf("the service never reached the gateway; agent log:\n%s", log)
		}
		time.Sleep(200 * time.Millisecond)
	}

	if err := StopService(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitStatus(t, "stopped")

	if err := UninstallService(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := ServiceStatus(); err == nil {
		t.Fatal("the service still exists after uninstall")
	}
}

func waitStatus(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := ServiceStatus()
		if err == nil && got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("service status %q (err %v), want %q", got, err, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func jsonString(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }
