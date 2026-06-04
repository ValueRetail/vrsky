package main

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpConn is the minimal SFTP surface the producer needs (upload). The real
// implementation wraps pkg/sftp + crypto/ssh; tests inject a fake so the
// producer logic runs without a live server (no Docker).
type sftpConn interface {
	MkdirAll(dir string) error
	Write(filePath string, data []byte) error
	Close() error
}

// dialFunc opens an SFTP connection for a config. Defaulted to realDial in
// Configure; tests inject a fake.
type dialFunc func(cfg *SFTPConfig) (sftpConn, error)

// realSFTP is the production sftpConn backed by an SSH transport.
type realSFTP struct {
	ssh  *ssh.Client
	sftp *sftp.Client
}

func (r *realSFTP) MkdirAll(dir string) error { return r.sftp.MkdirAll(dir) }

func (r *realSFTP) Write(filePath string, data []byte) error {
	f, err := r.sftp.Create(filePath)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (r *realSFTP) Close() error {
	var err error
	if r.sftp != nil {
		err = r.sftp.Close()
	}
	if r.ssh != nil {
		if cerr := r.ssh.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

// realDial opens a real SSH+SFTP connection. Auth is private-key (preferred) or
// password. Host-key verification: when cfg.HostKey is set it is pinned;
// otherwise it is skipped (callers log a warning — pin host_key in production).
func realDial(cfg *SFTPConfig) (sftpConn, error) {
	var auths []ssh.AuthMethod
	if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auths = append(auths, ssh.Password(cfg.Password))
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("no SFTP auth configured (set a password or a private key)")
	}

	hkCallback, err := hostKeyCallback(cfg.HostKey)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            auths,
		HostKeyCallback: hkCallback,
		Timeout:         15 * time.Second,
	}

	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	sshClient, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}
	return &realSFTP{ssh: sshClient, sftp: sftpClient}, nil
}

// hostKeyCallback pins the given authorized-keys-format host key, or returns an
// insecure (skip-verify) callback when none is configured.
func hostKeyCallback(hostKey string) (ssh.HostKeyCallback, error) {
	if hostKey == "" {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // opt-in host-key pinning via config.host_key
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil {
		return nil, fmt.Errorf("parse host_key: %w", err)
	}
	return ssh.FixedHostKey(pub), nil
}
