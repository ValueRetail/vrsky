//go:build !windows

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ServiceName is the systemd unit name (without .service).
const ServiceName = "vrsky-agent"

const unitPath = "/etc/systemd/system/vrsky-agent.service"

// IsService is always false off Windows: systemd runs `run` like a console.
func IsService() bool { return false }

// RunAsService is only meaningful on Windows.
func RunAsService(fn func(ctx context.Context) error) error {
	return errors.New("not a Windows service")
}

// EventLogHandler is Windows-only; systemd's journal already captures stderr.
func EventLogHandler() slog.Handler { return nil }

func requireSystemd() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("installing as a service is supported on Windows and Linux (systemd); on %s, run `vrsky-agent run` under your own supervisor", runtime.GOOS)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found — this installer supports systemd only")
	}
	return nil
}

// InstallService writes a systemd unit that runs the agent and restarts it,
// and enables it at boot.
func InstallService(exe, configPath string) error {
	if err := requireSystemd(); err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); err == nil {
		return fmt.Errorf("%s already exists; run `uninstall` first to reinstall", unitPath)
	}
	unit := fmt.Sprintf(`[Unit]
Description=VRSky Agent — connects this machine's configured folders to VRSky
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run --config %s
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, exe, configPath)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write %s (run as root): %w", unitPath, err)
	}
	return systemctl("daemon-reload")
}

// UninstallService stops, disables and removes the unit.
func UninstallService() error {
	if err := requireSystemd(); err != nil {
		return err
	}
	_ = systemctl("disable", "--now", ServiceName)
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return systemctl("daemon-reload")
}

// StartService enables and starts the unit.
func StartService() error {
	if err := requireSystemd(); err != nil {
		return err
	}
	return systemctl("enable", "--now", ServiceName)
}

// StopService stops the unit.
func StopService() error {
	if err := requireSystemd(); err != nil {
		return err
	}
	return systemctl("stop", ServiceName)
}

// ServiceStatus reports systemd's view of the unit.
func ServiceStatus() (string, error) {
	if err := requireSystemd(); err != nil {
		return "", err
	}
	out, _ := exec.Command("systemctl", "is-active", ServiceName).Output()
	return strings.TrimSpace(string(out)), nil
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
