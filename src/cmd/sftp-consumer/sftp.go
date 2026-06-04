package main

import (
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// remoteFile is a non-directory entry in the watched directory.
type remoteFile struct {
	Name string
	Size int64
}

// sftpConn is the minimal SFTP surface the consumer needs. The real
// implementation wraps pkg/sftp + crypto/ssh; tests inject a fake so the
// consumer logic runs without a live server (no Docker).
type sftpConn interface {
	List(dir string) ([]remoteFile, error)
	Read(filePath string) ([]byte, error)
	Remove(filePath string) error
	Rename(oldPath, newPath string) error
	MkdirAll(dir string) error
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

func (r *realSFTP) List(dir string) ([]remoteFile, error) {
	infos, err := r.sftp.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]remoteFile, 0, len(infos))
	for _, fi := range infos {
		if fi.IsDir() {
			continue
		}
		out = append(out, remoteFile{Name: fi.Name(), Size: fi.Size()})
	}
	return out, nil
}

func (r *realSFTP) Read(filePath string) ([]byte, error) {
	f, err := r.sftp.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (r *realSFTP) Remove(filePath string) error { return r.sftp.Remove(filePath) }

func (r *realSFTP) Rename(oldPath, newPath string) error {
	// PosixRename atomically overwrites an existing target; fall back to plain
	// Rename if the server lacks the posix-rename@openssh.com extension.
	if err := r.sftp.PosixRename(oldPath, newPath); err != nil {
		return r.sftp.Rename(oldPath, newPath)
	}
	return nil
}

func (r *realSFTP) MkdirAll(dir string) error { return r.sftp.MkdirAll(dir) }

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

	hostKeyCallback, err := hostKeyCallback(cfg.HostKey)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
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
		// No pinned key — accept any. Acceptable for trusted networks / dev;
		// production should set host_key. The dial caller logs a warning.
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // opt-in host-key pinning via config.host_key
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil {
		return nil, fmt.Errorf("parse host_key: %w", err)
	}
	return ssh.FixedHostKey(pub), nil
}

// joinRemote joins remote (slash-separated) path elements.
func joinRemote(elems ...string) string { return path.Join(elems...) }
