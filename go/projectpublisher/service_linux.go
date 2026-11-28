package projectpublisher

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/executor/supervise"

	"golang.org/x/sys/unix"
)

const gitRemote = "https://github.com/" + Repo + ".git"

var pushUnconfirmed = errors.New("project-push-unconfirmed; inspect remote branch before retry")

type peerKey struct{}

type publisher struct {
	installation          *installation
	coordinator, executor uint32
	mu                    sync.Mutex
	tickets               map[string]pushTicket
}

type pushTicket struct {
	op      Operation
	expires time.Time
}

// Serve runs only as the dedicated system service. Worker and coordinator UIDs have distinct IPC authority.
func Serve() error {
	l, err := activatedPublisherListener(Socket, 0)
	if err != nil {
		return err
	}
	defer l.Close()

	raw, err := readCredential()
	if err != nil {
		return err
	}
	key, err := appKey(raw)
	clear(raw)
	if err != nil {
		return err
	}
	app, _ := strconv.ParseInt(os.Getenv("FLEET_PUBLISHER_APP_ID"), 10, 64)
	id, _ := strconv.ParseInt(os.Getenv("FLEET_PUBLISHER_INSTALLATION_ID"), 10, 64)
	if app <= 0 || id <= 0 {
		return errors.New("publisher App and installation IDs required")
	}
	coordinator, err := accountUID("fleet")
	if err != nil {
		return err
	}
	executor, err := accountUID("fleet-agent")
	if err != nil || executor == coordinator {
		return errors.New("publisher requires a separate executor account")
	}
	if euid := uint32(os.Geteuid()); euid == 0 || euid == coordinator || euid == executor {
		return errors.New("publisher requires its dedicated service account")
	}

	p := publisher{installation: &installation{app: app, id: id, key: key, do: githubClient().Do},
		coordinator: coordinator, executor: executor, tickets: make(map[string]pushTicket)}
	s := http.Server{Handler: &p, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute,
		IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10, ErrorLog: log.New(io.Discard, "", 0), ConnContext: withPeer}
	return s.Serve(l)
}

func readCredential() ([]byte, error) {
	const directory = "/run/credentials/fleet-project-publisher.service"
	if os.Getenv("CREDENTIALS_DIRECTORY") != directory {
		return nil, errors.New("publisher systemd credential directory invalid")
	}
	dir, err := unix.Open(directory, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("publisher systemd credential directory unavailable")
	}
	defer unix.Close(dir)
	var d unix.Stat_t
	var fs unix.Statfs_t
	serviceUID := uint32(os.Geteuid())
	if unix.Fstat(dir, &d) != nil || (d.Uid != 0 && d.Uid != serviceUID) || d.Mode&0022 != 0 ||
		unix.Fstatfs(dir, &fs) != nil || fs.Flags&unix.ST_RDONLY == 0 {
		return nil, errors.New("publisher requires a read-only systemd credential namespace")
	}
	fd, err := unix.Openat(dir, "github-app.pem", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("publisher systemd credential unavailable")
	}
	file := os.NewFile(uintptr(fd), "github-app.pem")
	defer file.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !validCredentialMode(st, serviceUID) {
		return nil, errors.New("publisher systemd credential mode rejected")
	}
	const pemLimit = 32 << 10
	b, err := io.ReadAll(io.LimitReader(file, pemLimit+1))
	if err != nil || len(b) > pemLimit {
		return nil, errors.New("publisher systemd credential unreadable")
	}
	return b, nil
}

// Systemd can give a dynamic service a root:root 0440 credential in its read-only namespace.
// Other group-readable PEM files are outside this boundary.
func validCredentialMode(st unix.Stat_t, uid uint32) bool {
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return false
	}
	mode := st.Mode & 07777
	return (st.Uid == 0 && st.Gid == 0 && mode == 0440) ||
		(st.Uid == uid && mode == 0400)
}

func accountUID(name string) (uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, errors.New("publisher IPC account unavailable")
	}
	id, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil || id == 0 {
		return 0, errors.New("publisher IPC UID invalid")
	}
	return uint32(id), nil
}

func withPeer(ctx context.Context, c net.Conn) context.Context {
	conn, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *unix.Ucred
	if err := raw.Control(func(fd uintptr) {
		cred, _ = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || cred == nil {
		return ctx
	}
	return context.WithValue(ctx, peerKey{}, cred.Uid)
}

func (p *publisher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	uid, ok := r.Context().Value(peerKey{}).(uint32)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/readiness" && (uid == p.executor || uid == p.coordinator || uid == 0):
		v := Identity()
		v.AppID, v.InstallationID = p.installation.app, p.installation.id
		t, err := p.installation.access(r.Context())
		if err != nil {
			v.Reason = err.Error()
		} else {
			v.Ready, v.VerifiedAt, v.ExpiresAt = true, &t.verified, &t.expires
		}
		_ = json.NewEncoder(w).Encode(v)
	case r.Method == http.MethodPost && r.URL.Path == "/operation" && uid == p.coordinator:
		var op Operation
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if dec.Decode(&op) != nil || dec.Decode(new(any)) != io.EOF || !validOperation(op) {
			writeReply(w, Reply{}, errors.New("publisher-operation-invalid"))
			return
		}
		if op.Action == "publish_draft" {
			if _, err := p.installation.access(r.Context()); err != nil {
				writeReply(w, Reply{}, err)
				return
			}
			ticket := p.issue(op)
			writeReply(w, Reply{Ticket: ticket}, nil)
			return
		}
		p.run(w, r, op, nil)
	case r.Method == http.MethodPost && r.URL.Path == "/push" && uid == p.executor:
		op, ok := p.consume(r.Header.Get("X-Fleet-Publish-Ticket"))
		if !ok {
			writeReply(w, Reply{}, errors.New("publisher-push-ticket-invalid"))
			return
		}
		p.run(w, r, op, r.Body)
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}

func (p *publisher) issue(op Operation) string {
	ticket := rand.Text()
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for id, t := range p.tickets {
		if now.After(t.expires) {
			delete(p.tickets, id)
		}
	}
	p.tickets[ticket] = pushTicket{op: op, expires: now.Add(time.Minute)}
	return ticket
}

func (p *publisher) consume(ticket string) (Operation, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tickets[ticket]
	delete(p.tickets, ticket)
	return t.op, ok && time.Now().Before(t.expires)
}

func validOperation(op Operation) bool {
	if op.Base != "main" || op.Number < 0 || (op.Head != "" && !validSHA(op.Head)) ||
		strings.ContainsAny(op.Branch, " ~^:?*[\\\x00\t\n\r") || strings.Contains(op.Branch, "..") {
		return false
	}
	if op.Action == "cancel_merge_request" {
		return op.Branch != "" || op.Number > 0
	}
	if !strings.HasPrefix(op.Branch, "fleet/") || len(op.Branch) <= len("fleet/") {
		return false
	}
	switch op.Action {
	case "status":
		return true
	case "publish_draft":
		return validSHA(op.Head) && (op.Lease == "" || validSHA(op.Lease)) && strings.TrimSpace(op.Title) != ""
	case "mark_ready":
		return true
	case "record_code_review":
		return validSHA(op.Head) && len(op.Description) <= 140 && strings.TrimSpace(op.Description) != "" &&
			!strings.ContainsAny(op.Description, "\r\n\x00")
	case "request_merge":
		return op.MergeMethod == "squash" || op.MergeMethod == "merge" || op.MergeMethod == "rebase"
	default:
		return false
	}
}

func writeReply(w http.ResponseWriter, v Reply, err error) {
	if err != nil {
		v = Reply{Error: err.Error()}
	}
	_ = json.NewEncoder(w).Encode(v)
}

func (p *publisher) run(w http.ResponseWriter, r *http.Request, op Operation, pack io.Reader) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	t, err := p.installation.access(ctx)
	if err != nil {
		writeReply(w, Reply{}, err)
		return
	}
	if pack != nil {
		if err := push(ctx, op, pack, t); err != nil {
			if errors.Is(err, pushUnconfirmed) {
				p.installation.invalidate(t.value)
			}
			writeReply(w, Reply{}, err)
			return
		}
	}
	g := github{installation: p.installation, token: t}
	result, err := g.publication(ctx, op)
	if err != nil {
		writeReply(w, Reply{}, err)
		return
	}
	result["appId"], result["installationId"] = p.installation.app, p.installation.id
	body, err := json.Marshal(result)
	writeReply(w, Reply{Result: body}, err)
}

func push(ctx context.Context, op Operation, pack io.Reader, t token) error {
	dir, err := os.MkdirTemp("", "fleet-publisher-")
	if err != nil {
		return errors.New("publisher-object-directory-unavailable")
	}
	defer os.RemoveAll(dir)
	if err := importObjects(ctx, dir, pack, op.Head); err != nil {
		return err
	}
	if _, err := git(ctx, dir, nil, "", "check-ref-format", "refs/heads/"+op.Branch); err != nil {
		return errors.New("publisher-branch-invalid")
	}
	ref := "refs/heads/" + op.Branch
	args := []string{"push", "--no-follow-tags", "--recurse-submodules=no"}
	if op.Lease != "" {
		args = append(args, "--force-with-lease="+ref+":"+op.Lease)
	}
	args = append(args, "--", gitRemote, op.Head+":"+ref)
	if _, err := git(ctx, dir, nil, t.value, args...); err != nil {
		return pushUnconfirmed
	}
	return nil
}

func importObjects(ctx context.Context, dir string, pack io.Reader, head string) error {
	if _, err := git(ctx, dir, nil, "", "init", "--bare", "--template=", "--object-format=sha1", "."); err != nil {
		return errors.New("publisher-object-directory-unavailable")
	}
	// Bound an untrusted stream before Git parses it. This ceiling protects the dedicated service's temporary storage.
	reader := &io.LimitedReader{R: pack, N: 512 << 20}
	if _, err := git(ctx, dir, reader, "", "index-pack", "--strict", "--stdin"); err != nil || reader.N == 0 {
		return errors.New("publisher-object-pack-rejected")
	}
	out, err := git(ctx, dir, nil, "", "rev-parse", "--verify", head+"^{commit}")
	if err != nil || strings.TrimSpace(string(out)) != head {
		return errors.New("publisher-object-head-mismatch")
	}
	return nil
}

func git(ctx context.Context, dir string, stdin io.Reader, secret string, args ...string) ([]byte, error) {
	hardening := []string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=",
		"-c", "http.followRedirects=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always"}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command("/usr/bin/git", append(hardening, args...)...)
	cmd.Dir, cmd.Stdin = dir, stdin
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1"}
	if secret != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + secret))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http."+gitRemote+".extraHeader",
			fmt.Sprintf("GIT_CONFIG_VALUE_0=Authorization: Basic %s", basic))
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd, KillLeftovers: true})
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		proc.Kill()
	case <-proc.Done():
	}
	err = proc.Wait()
	return out.Bytes(), err
}
