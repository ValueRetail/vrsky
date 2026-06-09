//go:build integration

// Integration test for the SFTP consumer against a live SFTP server
// (atmoz/sftp). It uploads a file with a raw client (the connector's sftpConn is
// read-only — it is a consumer), then exercises the connector's real connection
// code (realDial) + List/Read/Rename/Remove. Run:
//
//	docker compose up -d sftp-test
//	SFTP_TEST_HOST=localhost SFTP_TEST_PORT=2222 \
//	  go test -tags=integration -run SFTP ./cmd/sftp-consumer/...
//
// Skipped unless SFTP_TEST_HOST is set.
package main

import (
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func sftpEnvOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestSFTP_RoundTrip_Integration(t *testing.T) {
	host := os.Getenv("SFTP_TEST_HOST")
	if host == "" {
		t.Skip("SFTP_TEST_HOST not set; skipping SFTP integration test")
	}
	port := 22
	if p := os.Getenv("SFTP_TEST_PORT"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("invalid SFTP_TEST_PORT %q: %v", p, err)
		}
		port = parsed
	}
	user := sftpEnvOr("SFTP_TEST_USER", "vrsky")
	pass := sftpEnvOr("SFTP_TEST_PASS", "vrsky")
	// atmoz/sftp ("vrsky:vrsky:::upload") makes only the listed dir writable.
	dir := sftpEnvOr("SFTP_TEST_DIR", "upload")

	cfg := &SFTPConfig{Host: host, Port: port, Username: user, Password: pass, RemoteDir: dir}

	// --- setup: upload a file with a raw client (connector only reads) ---
	rawSSH, raw := rawSFTPClient(t, cfg)
	defer rawSSH.Close()
	defer raw.Close()

	key := dir + "/it-order.json"
	payload := []byte(`{"id":1,"name":"Acme"}`)
	wf, err := raw.Create(key)
	if err != nil {
		t.Fatalf("raw create %q: %v", key, err)
	}
	if _, err := wf.Write(payload); err != nil {
		t.Fatalf("raw write: %v", err)
	}
	if err := wf.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	// --- exercise the connector's real connection code ---
	conn, err := realDial(cfg)
	if err != nil {
		t.Fatalf("realDial: %v", err)
	}
	defer conn.Close()

	files, err := conn.List(dir)
	if err != nil {
		t.Fatalf("List(%q): %v", dir, err)
	}
	found := false
	for _, f := range files {
		if f.Name == "it-order.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("List(%q) missing it-order.json: %+v", dir, files)
	}

	body, err := conn.Read(key)
	if err != nil {
		t.Fatalf("Read(%q): %v", key, err)
	}
	if string(body) != string(payload) {
		t.Errorf("Read body = %q, want %q", body, payload)
	}

	// Rename (after_action=move uses this) then Remove (after_action=delete).
	moved := dir + "/it-order.processed.json"
	if err := conn.Rename(key, moved); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := conn.Remove(moved); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// rawSFTPClient opens a plain SSH+SFTP client for test setup (writing files the
// read-only connector will then ingest).
func rawSFTPClient(t *testing.T, cfg *SFTPConfig) (*ssh.Client, *sftp.Client) {
	t.Helper()
	sshCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // test-only
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	// atmoz/sftp may still be starting; retry briefly.
	var sshClient *ssh.Client
	var err error
	deadline := time.Now().Add(30 * time.Second)
	for {
		sshClient, err = ssh.Dial("tcp", addr, sshCfg)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ssh dial %s: %v", addr, err)
		}
		time.Sleep(time.Second)
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		t.Fatalf("sftp client: %v", err)
	}
	return sshClient, client
}
