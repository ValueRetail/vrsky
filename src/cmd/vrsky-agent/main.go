// Command vrsky-agent connects a machine's folders to VRSky pipelines (#266).
//
// It dials out to VRSky over HTTPS — no inbound ports — and, for the folders
// named in its config file, uploads new files from read folders into pipelines
// and writes files VRSky sends into write folders.
//
//	vrsky-agent register --url https://vrsky.example --token vrsky_reg_… [--name NAME]
//	vrsky-agent run                  run in this console (Ctrl-C to stop)
//	vrsky-agent install              install as a service (Windows service / systemd)
//	vrsky-agent start | stop | status | uninstall
//	vrsky-agent check                validate the config and show what VRSky will see
//	vrsky-agent version
//
// Every command takes --config (default: %ProgramData%\VRSky\agent\config.json
// on Windows, /etc/vrsky-agent/config.json elsewhere).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agent"
	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

const usage = `vrsky-agent — connects this machine's folders to VRSky pipelines.

Usage:
  vrsky-agent register --url <VRSky URL> --token <registration token> [--name <name>]
  vrsky-agent run          Run in this console (Ctrl-C to stop)
  vrsky-agent install      Install as a service that starts at boot (needs admin)
  vrsky-agent start        Start the service
  vrsky-agent stop         Stop the service
  vrsky-agent status       Show whether the service is running
  vrsky-agent uninstall    Remove the service
  vrsky-agent check        Check the config file and show what VRSky will see
  vrsky-agent version

Every command accepts --config <path>. Default: %s
`

func main() {
	// Started by the Service Control Manager: the arguments are the ones
	// `install` registered (run --config …), so handle them the usual way.
	if agent.IsService() {
		if err := runCommand(os.Args[1:], true); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, agent.DefaultConfigPath())
		os.Exit(2)
	}
	if err := runCommand(os.Args[1:], false); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runCommand(args []string, asService bool) error {
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configPath := fs.String("config", agent.DefaultConfigPath(), "path to the config file")
	switch cmd {
	case "register":
		serverURL := fs.String("url", "", "VRSky address, e.g. https://vrsky.valueretail.no")
		token := fs.String("token", "", "one-time registration token from Settings → Remote agents")
		name := fs.String("name", "", "name for this agent in VRSky (default: from config, else the hostname)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return register(*configPath, *serverURL, *token, *name)
	case "run":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return run(*configPath, asService)
	case "install":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return install(*configPath)
	case "uninstall":
		if err := agent.UninstallService(); err != nil {
			return err
		}
		fmt.Println("Service removed. The config, credential and logs are left in place.")
		return nil
	case "start":
		if err := agent.StartService(); err != nil {
			return err
		}
		fmt.Println("Service started.")
		return nil
	case "stop":
		if err := agent.StopService(); err != nil {
			return err
		}
		fmt.Println("Service stopped.")
		return nil
	case "status":
		s, err := agent.ServiceStatus()
		if err != nil {
			return err
		}
		fmt.Println("Service:", s)
		return nil
	case "check":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return check(*configPath)
	case "version", "--version", "-v":
		fmt.Printf("vrsky-agent %s (%s/%s)\n", agent.Version, runtime.GOOS, runtime.GOARCH)
		return nil
	case "help", "--help", "-h":
		fmt.Printf(usage, agent.DefaultConfigPath())
		return nil
	default:
		return fmt.Errorf("unknown command %q — run `vrsky-agent help`", cmd)
	}
}

func register(configPath, serverURL, token, name string) error {
	cfg, err := agent.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if serverURL == "" {
		serverURL = cfg.ServerURL
	}
	if serverURL == "" {
		return errors.New("--url is required (or set server_url in the config)")
	}
	if err := agent.CheckServerURL(serverURL, cfg.AllowInsecureHTTP); err != nil {
		return err
	}
	if token == "" {
		return errors.New("--token is required: generate one in VRSky under Settings → Remote agents")
	}
	if existing, err := agent.LoadCredential(cfg.DataDir); err == nil && !existing.Revoked {
		return fmt.Errorf("already registered as %q with %s — to register again, first delete %s",
			existing.Name, existing.ServerURL, filepath.Join(cfg.DataDir, "credential.json"))
	}
	if name == "" {
		name = cfg.AgentName
	}
	host, _ := os.Hostname()

	client, err := agent.NewClient(serverURL, "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := client.Register(ctx, agentproto.RegisterRequest{
		RegistrationToken: token, Name: name, Hostname: host,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Version: agent.Version,
		Directories: cfg.Announced(),
	})
	if err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	if err := agent.SaveCredential(cfg.DataDir, &agent.Credential{
		AgentID: resp.AgentID, Name: resp.Name, ServerURL: serverURL,
		Credential: resp.Credential, RegisteredAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("registered, but could not save the credential: %w — revoke %q in VRSky and register again", err, resp.Name)
	}
	fmt.Printf("Registered as %q. It now appears under Settings → Remote agents.\n", resp.Name)
	fmt.Println("Next: `vrsky-agent run` to try it in this window, then `vrsky-agent install` to run it as a service.")
	return nil
}

func run(configPath string, asService bool) error {
	cfg, err := agent.LoadConfig(configPath)
	if err != nil {
		return err
	}
	var extra = agent.EventLogHandler()
	if !asService {
		extra = nil // a console user reads the console
	}
	log, closer, err := agent.NewLogger(cfg, !asService, extra)
	if err != nil {
		return err
	}
	defer closer.Close()

	cred, err := agent.LoadCredential(cfg.DataDir)
	if err != nil {
		log.Error("Cannot start", "error", err)
		return err
	}
	runner, err := agent.NewRunner(cfg, cred, log)
	if err != nil {
		log.Error("Cannot start", "error", err)
		return err
	}

	if asService {
		return agent.RunAsService(runner.Run)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = runner.Run(ctx)
	if err != nil {
		log.Error("Agent stopped", "error", err)
	}
	return err
}

func install(configPath string) error {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	cfg, err := agent.LoadConfig(abs)
	if err != nil {
		return err
	}
	if _, err := agent.LoadCredential(cfg.DataDir); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if err := agent.InstallService(exe, abs); err != nil {
		return err
	}
	fmt.Println("Installed. It starts automatically at boot. Start it now with: vrsky-agent start")
	return nil
}

func check(configPath string) error {
	cfg, err := agent.LoadConfig(configPath)
	if err != nil {
		return err
	}
	out := io.Writer(os.Stdout)
	fmt.Fprintf(out, "Config OK: %s\n", configPath)
	fmt.Fprintf(out, "Data folder: %s\nLog file: %s\n\n", cfg.DataDir, cfg.Log.File)
	fmt.Fprintln(out, "Folders (VRSky sees only the name and direction, never the path):")
	for _, d := range cfg.Announced() {
		dir := cfg.Directories[d.Name]
		verb := "uploads new files from"
		if d.Mode == agentproto.ModeWrite {
			verb = "writes delivered files into"
		}
		fmt.Fprintf(out, "  %-20s %-5s  %s %s\n", d.Name, d.Mode, verb, dir.Path)
	}
	cred, err := agent.LoadCredential(cfg.DataDir)
	switch {
	case errors.Is(err, agent.ErrNotRegistered):
		fmt.Fprintln(out, "\nNot registered yet.")
	case err != nil:
		return err
	case cred.Revoked:
		fmt.Fprintf(out, "\nRegistered as %q with %s — REVOKED in VRSky; register again.\n", cred.Name, cred.ServerURL)
	default:
		fmt.Fprintf(out, "\nRegistered as %q with %s.\n", cred.Name, cred.ServerURL)
	}
	return nil
}
