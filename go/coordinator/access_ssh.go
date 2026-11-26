// The provisioner's SSH side (docs/designs/fleet-project-access.md): its key and the grants' credentials under
// <FLEET_SECRET_DIR>/access, connections to access servers (through a jump server when the server names one) with
// pinned host keys, recipe runs of fleet-provision and the login probes. Only values' fingerprints leave this file.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

//go:embed fleet-provision.sh
var fleetProvisionScript []byte

// accessJumpUser: the user on a jump server that only forwards to the servers behind it (recipe jump-allow).
const accessJumpUser = "fleet-jump"

// accessHop: one server on the way: its address and pinned host key ("" while learning it).
type accessHop struct {
	ID, Address, HostKey string
}

func (h accessHop) hostPort() string {
	if _, _, err := net.SplitHostPort(h.Address); err == nil {
		return h.Address
	}
	return net.JoinHostPort(h.Address, "22")
}

func accessSecretRoot() (string, error) {
	if secretDir() == "" {
		return "", fail(409, "FLEET_SECRET_DIR is not set on the coordinator")
	}
	return filepath.Join(secretDir(), "access"), nil
}

// accessKeyLoad loads the ed25519 key at <access>/<name>/id_ed25519 and never creates it (os.ErrNotExist when
// missing): reads use it, so two reads at once never race to write two keys.
func accessKeyLoad(name string) (ssh.Signer, error) {
	root, err := accessSecretRoot()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(root, name, "id_ed25519"))
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}

// accessKey loads the ed25519 key at <access>/<name>/id_ed25519, creating it (0600 in a 0700 folder) when missing.
func accessKey(name string) (ssh.Signer, error) {
	s, err := accessKeyLoad(name)
	if s != nil || !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	root, err := accessSecretRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, name)
	path := filepath.Join(dir, "id_ed25519")
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "fleet "+name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(block)); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// accessSecret loads the password at <access>/<name>, creating 32 random letters and digits when missing.
func accessSecret(name string) (string, error) {
	root, err := accessSecretRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, name)
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	const letters = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i, b := range raw {
		raw[i] = letters[int(b)%len(letters)]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return string(raw), writeFileAtomic(path, raw)
}

// accessForget removes a credential file or folder under <access> (a revoked grant's).
func accessForget(name string) error {
	root, err := accessSecretRoot()
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(root, name))
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// accessPublic: the public key's base64 body (fleet-provision takes the ed25519 body alone) and its fingerprint.
func accessPublic(s ssh.Signer) (body, fingerprint string) {
	fields := strings.Fields(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
	return fields[1], ssh.FingerprintSHA256(s.PublicKey())
}

func accessSecretFingerprint(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "sha256:" + hex.EncodeToString(sum[:6])
}

// errHostKeyLearned stops a handshake once the host key is captured.
var errHostKeyLearned = errors.New("host key learned")

// accessHostKeyCallback: a pinned key must match; with learn set an empty pin captures the presented key and stops.
func accessHostKeyCallback(h accessHop, learned *string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		if h.HostKey == "" {
			if learned != nil {
				*learned = got
				return errHostKeyLearned
			}
			return fmt.Errorf("server %s has no pinned host key", h.ID)
		}
		if got != h.HostKey {
			return fmt.Errorf("server %s presented host key %s, pinned %s", h.ID, ssh.FingerprintSHA256(key), accessKeyFingerprint(h.HostKey))
		}
		return nil
	}
}

func accessKeyFingerprint(line string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "unreadable"
	}
	return ssh.FingerprintSHA256(k)
}

// accessConn: a login on the last hop; via hops are passed as accessJumpUser with the same key.
type accessConn struct {
	client *ssh.Client
	closer []io.Closer
}

func (c *accessConn) Close() {
	for i := len(c.closer) - 1; i >= 0; i-- {
		_ = c.closer[i].Close()
	}
}

// accessDial logs in as user on the last of hops with signer. learned, when set, captures the last hop's key instead
// of checking it (access_server_add), and the reply is then (nil, errHostKeyLearned) on success.
func accessDial(ctx context.Context, hops []accessHop, user string, signer ssh.Signer, learned *string) (*accessConn, error) {
	c := &accessConn{}
	var through *ssh.Client
	for i, h := range hops {
		last := i == len(hops)-1
		var raw net.Conn
		var err error
		if through == nil {
			raw, err = (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, "tcp", h.hostPort())
		} else {
			raw, err = through.DialContext(ctx, "tcp", h.hostPort())
		}
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("reach %s (%s): %w", h.ID, h.hostPort(), err)
		}
		c.closer = append(c.closer, raw)
		if dl, ok := ctx.Deadline(); ok { // bounds the whole login and every recipe on it
			_ = raw.SetDeadline(dl)
		}
		u := accessJumpUser
		var capture *string
		if last {
			u, capture = user, learned
		}
		cfg := &ssh.ClientConfig{User: u, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: accessHostKeyCallback(h, capture), Timeout: 20 * time.Second}
		conn, chans, reqs, err := ssh.NewClientConn(raw, h.hostPort(), cfg)
		if err != nil {
			c.Close()
			if capture != nil && *capture != "" {
				return nil, errHostKeyLearned
			}
			return nil, fmt.Errorf("log in to %s as %s: %w", h.ID, u, err)
		}
		through = ssh.NewClient(conn, chans, reqs)
		c.closer = append(c.closer, through)
	}
	c.client = through
	return c, nil
}

// accessRun runs one fleet-provision recipe (its forced command reads SSH_ORIGINAL_COMMAND) with stdin; the reply is its
// output, and a failure carries the output's last line.
func accessRun(c *accessConn, args []string, stdin string) (string, error) {
	s, err := c.client.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	var stdout, stderr bytes.Buffer // two buffers: the session copies each stream in its own goroutine
	s.Stdout, s.Stderr = &stdout, &stderr
	s.Stdin = strings.NewReader(stdin)
	err = s.Run(strings.Join(args, " ")) // the connection's deadline (accessDial) bounds it
	text := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if err != nil {
		lines := strings.Split(text, "\n")
		return text, fmt.Errorf("%s: %s", args[0], strings.TrimSpace(lines[len(lines)-1]))
	}
	return text, nil
}

// accessProbeSFTP: the user's login opens the sftp subsystem and answers its version.
func accessProbeSFTP(c *accessConn) error {
	s, err := c.client.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	in, err := s.StdinPipe()
	if err != nil {
		return err
	}
	out, err := s.StdoutPipe()
	if err != nil {
		return err
	}
	if err := s.RequestSubsystem("sftp"); err != nil {
		return fmt.Errorf("sftp subsystem: %w", err)
	}
	// SSH_FXP_INIT version 3: length 5, type 1, version.
	if _, err := in.Write([]byte{0, 0, 0, 5, 1, 0, 0, 0, 3}); err != nil {
		return err
	}
	head := make([]byte, 5)
	if _, err := io.ReadFull(out, head); err != nil {
		return fmt.Errorf("sftp version: %w", err)
	}
	if head[4] != 2 {
		return fmt.Errorf("sftp answered packet type %d, not version", head[4])
	}
	return nil
}

// accessProbeForward: the user's login forwards to addr on the server and the far side answers. A MySQL server speaks
// first: a protocol-10 greeting, or an error packet (0xff) that still proves the path.
func accessProbeForward(c *accessConn, addr string) (string, error) {
	conn, err := c.client.Dial("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("forward to %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	head := make([]byte, 5)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", fmt.Errorf("greeting from %s: %w", addr, err)
	}
	n := int(head[0]) | int(head[1])<<8 | int(head[2])<<16
	if head[4] != 10 && head[4] != 0xff || n <= 1 || n > 1<<16 {
		return "", fmt.Errorf("%s answered no MySQL greeting", addr)
	}
	rest := make([]byte, n-1)
	_, _ = io.ReadFull(conn, rest)
	if head[4] == 0xff {
		return "", fmt.Errorf("%s refused: %s", addr, strings.TrimSpace(string(rest[min(len(rest), 8):])))
	}
	version, _, _ := bytes.Cut(rest, []byte{0})
	return string(version), nil
}

// accessProbePostgres: a forwarded connection to addr answers a PostgreSQL SSL request ('S' or 'N'); PostgreSQL
// sends no greeting of its own.
func accessProbePostgres(c *accessConn, addr string) error {
	conn, err := c.client.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("forward to %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}); err != nil { // length 8, code 80877103
		return fmt.Errorf("SSL request to %s: %w", addr, err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(conn, b); err != nil {
		return fmt.Errorf("answer from %s: %w", addr, err)
	}
	if b[0] != 'S' && b[0] != 'N' {
		return fmt.Errorf("%s answered no PostgreSQL SSL reply", addr)
	}
	return nil
}

// accessEnrollCommand: what the owner runs as root on a server once: it writes fleet-provision and lets the
// provisioner's key run only that command.
func accessEnrollCommand(provisionerBody string) string {
	b64 := base64.StdEncoding.EncodeToString(fleetProvisionScript)
	return "sudo sh -c 'set -e; umask 022; echo " + b64 + " | base64 -d > /usr/local/sbin/fleet-provision; chmod 0755 /usr/local/sbin/fleet-provision; /usr/local/sbin/fleet-provision install " + provisionerBody + "'"
}

// fleetProvisionVersion: the version of the embedded script (its FLEET_PROVISION_VERSION line).
func fleetProvisionVersion() string {
	m := regexp.MustCompile(`(?m)^FLEET_PROVISION_VERSION=([0-9]+)$`).FindSubmatch(fleetProvisionScript)
	if m == nil {
		return "0"
	}
	return string(m[1])
}
