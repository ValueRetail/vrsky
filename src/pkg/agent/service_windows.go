//go:build windows

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// ServiceName is the Windows service (and Event Log source) name.
const ServiceName = "VRSkyAgent"

// IsService reports whether this process was started by the Service Control
// Manager.
func IsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// RunAsService runs fn under the Service Control Manager until Windows asks
// the service to stop. A revoked agent stops cleanly (exit 0) so the
// restart-on-failure recovery does not relaunch it pointlessly; any other
// error exits non-zero, which recovery does restart — a network share not yet
// mounted at boot is the typical transient cause.
func RunAsService(fn func(ctx context.Context) error) error {
	return svc.Run(ServiceName, &serviceHandler{fn: fn})
}

type serviceHandler struct {
	fn func(ctx context.Context) error
}

func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.fn(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, ErrRevoked) {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		}
	}
}

// InstallService registers the agent as an automatic-start service running
// `<exe> run --config <configPath>` as LocalSystem, restarts it after a
// failure, and registers its Event Log source. Needs an elevated prompt.
func InstallService(exe, configPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return friendlyAccessError(err)
	}
	defer func() { _ = m.Disconnect() }()
	if s, err := m.OpenService(ServiceName); err == nil {
		s.Close()
		return fmt.Errorf("the %s service is already installed; run `uninstall` first to reinstall", ServiceName)
	}
	s, err := m.CreateService(ServiceName, exe, mgr.Config{
		DisplayName: "VRSky Agent",
		Description: "Connects this machine's configured folders to VRSky pipelines. Outbound HTTPS only.",
		StartType:   mgr.StartAutomatic,
		// After boot-critical services, so the network is up first.
		DelayedAutoStart: true,
	}, "run", "--config", configPath)
	if err != nil {
		return friendlyAccessError(err)
	}
	defer s.Close()
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
	}, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("set restart-on-failure: %w", err)
	}
	if err := eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "exists") {
		return fmt.Errorf("register the Event Log source: %w", err)
	}
	return nil
}

// UninstallService stops and removes the service and its Event Log source.
func UninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return friendlyAccessError(err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("the %s service is not installed", ServiceName)
	}
	defer s.Close()
	_ = stopAndWait(s)
	if err := s.Delete(); err != nil {
		return friendlyAccessError(err)
	}
	_ = eventlog.Remove(ServiceName)
	return nil
}

// StartService starts the installed service.
func StartService() error {
	return withService(func(s *mgr.Service) error { return s.Start() })
}

// StopService stops the installed service and waits for it.
func StopService() error {
	return withService(stopAndWait)
}

// ServiceStatus describes the installed service's state.
func ServiceStatus() (string, error) {
	var out string
	err := withService(func(s *mgr.Service) error {
		q, err := s.Query()
		if err != nil {
			return err
		}
		out = map[svc.State]string{
			svc.Stopped: "stopped", svc.StartPending: "starting", svc.StopPending: "stopping",
			svc.Running: "running", svc.ContinuePending: "resuming", svc.PausePending: "pausing",
			svc.Paused: "paused",
		}[q.State]
		return nil
	})
	return out, err
}

func withService(fn func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return friendlyAccessError(err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("the %s service is not installed — run `vrsky-agent install` first", ServiceName)
	}
	defer s.Close()
	return friendlyAccessError(fn(s))
}

func stopAndWait(s *mgr.Service) error {
	q, err := s.Control(svc.Stop)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for q.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("the service did not stop within 30 seconds")
		}
		time.Sleep(300 * time.Millisecond)
		if q, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

func friendlyAccessError(err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return errors.New("access denied — run this from a Command Prompt or PowerShell opened with \"Run as administrator\"")
	}
	return err
}

// EventLogHandler mirrors warnings and errors to the Windows Event Log
// (Application log, source VRSkyAgent), where administrators look first when
// a service misbehaves. Returns nil if the source is not registered.
func EventLogHandler() slog.Handler {
	el, err := eventlog.Open(ServiceName)
	if err != nil {
		return nil
	}
	return &eventLogHandler{el: el}
}

type eventLogHandler struct {
	el    *eventlog.Log
	attrs []slog.Attr
}

func (h *eventLogHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }

func (h *eventLogHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool { fmt.Fprintf(&b, "\n%s: %v", a.Key, a.Value); return true }
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	if r.Level >= slog.LevelError {
		return h.el.Error(1, b.String())
	}
	return h.el.Warning(2, b.String())
}

func (h *eventLogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &eventLogHandler{el: h.el, attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
}

func (h *eventLogHandler) WithGroup(string) slog.Handler { return h }
