package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
)

// cmdID returns a command identity field: non-empty ASCII alphanumerics, '_' and '-'.
func cmdID(v any, k string) (string, error) {
	s, ok := jAt(v, k).(string)
	if !ok {
		return "", errors.New("missing identity")
	}
	return checkedID(s)
}

// idOf returns a command's identity field (id, executionId or sessionId) as cmdID checks it; a command that does not fit
// its kind's type is read from its JSON.
func idOf(c contract.Command, k string) (string, error) {
	if c.Value() != nil {
		var s *string
		switch k {
		case "id":
			s = new(c.Id())
		case "executionId":
			s = new(c.ExecutionId())
		case "sessionId":
			s = c.SessionId()
		}
		if s != nil && *s != "" {
			return checkedID(*s)
		}
	}
	raw := c.Raw
	if raw == nil {
		raw, _ = json.Marshal(c)
	}
	s, ok := valStr(typedRaw[map[string]jsonValue](raw)[k])
	if !ok {
		return "", errors.New("missing identity")
	}
	return checkedID(s)
}

func checkedID(s string) (string, error) {
	if s == "" || !onlyBytes(s, func(b byte) bool { return alnum(b) || b == '_' || b == '-' }) {
		return "", errors.New("invalid identity")
	}
	return s, nil
}

// typedCommand: a decoded command as its contract type (commandOf of its journal body).
func typedCommand(c M) contract.Command { return commandOf(enc(c)) }

// commandOf: a journal body as its contract type; one that does not fit its kind's type keeps only Raw (no member set),
// so its identities are still read from the JSON.
func commandOf(body string) contract.Command {
	var cmd contract.Command
	if json.Unmarshal([]byte(body), &cmd) != nil {
		return contract.Command{Raw: json.RawMessage(body)}
	}
	return cmd
}

// commandKind: the kind of a command's JSON, "" when it names none.
func commandKind(body string) contract.CommandKind {
	var k struct {
		Kind contract.CommandKind `json:"kind"`
	}
	json.Unmarshal([]byte(body), &k)
	return k.Kind
}

// payloadOf decodes a command payload into its per-kind contract type (an absent or null payload is the zero value).
func payloadOf[T any](c contract.Command) (T, error) {
	var v T
	raw := c.Raw
	if raw == nil {
		raw, _ = json.Marshal(c)
	}
	var x struct {
		Payload json.RawMessage `json:"payload"`
	}
	json.Unmarshal(raw, &x)
	if len(x.Payload) > 0 && string(x.Payload) != "null" {
		err := json.Unmarshal(x.Payload, &v)
		return v, err
	}
	return v, nil
}

// str0 is the value of an optional string field, "" when absent.
func str0(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func okStatus(ok bool) string {
	if ok {
		return "completed"
	}
	return "failed"
}

// startPayload reads the already-decoded start/input payload from c when available, falling back to payloadOf for
// raw commands without typed sub-payloads.
func startPayload(c contract.Command) (contract.StartPayload, error) {
	switch {
	case c.Start != nil:
		if c.Start.Payload != nil {
			return *c.Start.Payload, nil
		}
		return contract.StartPayload{}, nil
	case c.Input != nil:
		if c.Input.Payload != nil {
			return *c.Input.Payload, nil
		}
		return contract.StartPayload{}, nil
	}
	return payloadOf[contract.StartPayload](c)
}

// harnessName: payload harness, else agentConfig.harness; a non-string agentConfig harness is "invalid"; default codex.
// The decoded payload cannot tell a null harness from none: callers with the command check harnessNull.
func harnessName(p contract.StartPayload) string {
	if p.Harness != nil {
		return *p.Harness
	}
	raw, ok := p.AgentConfig["harness"]
	if !ok {
		return "codex"
	}
	if s := typedRaw[*string](raw); s != nil {
		return *s
	}
	return "invalid"
}

// harnessNull: the command's payload names "harness": null, which names no harness ("invalid": the start fails as an
// unknown harness, never falls back to codex).
func harnessNull(c contract.Command) bool {
	raw := c.Raw
	if raw == nil {
		return false // built here, not received: a nil harness was omitted
	}
	var x struct {
		Payload struct {
			Harness json.RawMessage `json:"harness"`
		} `json:"payload"`
	}
	return json.Unmarshal(raw, &x) == nil && string(x.Payload.Harness) == "null"
}

// commandHarness: the harness of a start or input command; other kinds name none, so codex.
func commandHarness(c contract.Command) string {
	if harnessNull(c) {
		return "invalid"
	}
	switch {
	case c.Start != nil && c.Start.Payload != nil:
		return harnessName(*c.Start.Payload)
	case c.Input != nil && c.Input.Payload != nil:
		return harnessName(*c.Input.Payload)
	}
	return harnessName(contract.StartPayload{})
}

// workspaceKey: the command payload's workspaceKey ("" when absent or not a string).
func workspaceKey(c contract.Command) string {
	raw := c.Raw
	if raw == nil {
		raw, _ = json.Marshal(c)
	}
	return typedRaw[struct {
		Payload struct {
			WorkspaceKey string `json:"workspaceKey"`
		} `json:"payload"`
	}](raw).Payload.WorkspaceKey
}

// safeNativeID: native ids reach argv; ASCII alphanumerics and '-', at most 64, never a leading '-'.
func safeNativeID(n string) bool {
	return n != "" && len(n) <= 64 && n[0] != '-' && onlyBytes(n, func(b byte) bool { return alnum(b) || b == '-' })
}

// hostSecret reads the host secret file: not a symlink, no group or other access, at least 32 characters.
func hostSecret(path string) (string, error) {
	m, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if m.Mode()&os.ModeSymlink != 0 || m.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s: host secret file must be a private file (0600)", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 32 {
		return "", errors.New("host secret too short")
	}
	return s, nil
}

// platform for the coordinator: a WSL host whose Claude placement is windows-wsl counts as win32.
func platform() string {
	if p, err := claudePlacementHere(); err == nil && p == "windows-wsl" {
		return "win32"
	}
	return runtime.GOOS
}

func capabilities() []string {
	c := []string{}
	for _, x := range contract.CapabilityValues {
		c = append(c, string(x))
	}
	if v := ffmpegVersion(); v != "" {
		c = append(c, contract.FfmpegCapability(v))
	}
	for _, tool := range []string{"node", "pnpm"} {
		if _, err := exec.LookPath(tool); err == nil {
			c = append(c, contract.ToolCapability(tool))
		}
	}
	if store := os.Getenv("FLEET_FORGE_CONFIG_DIR"); filepath.IsAbs(store) && isFile(filepath.Join(store, "hosts.yml")) {
		for _, repo := range strings.Split(os.Getenv("FLEET_FORGE_REPOSITORIES"), ",") {
			if forgeRepo("https://github.com/"+repo) == repo && repo != "" {
				c = append(c, contract.ForgeCapability(repo))
			}
		}
	}
	if isFile(os.Getenv("FLEET_PLAYWRIGHT_MODULE")) && isFile(os.Getenv("FLEET_BROWSER_EXECUTABLE")) && private(os.Getenv("FLEET_BROWSER_BINDINGS")) == nil {
		c = append(c, contract.ToolCapability("browser"))
	}
	return c
}

func (e *executor) stores() []contract.StoreReport {
	if e.storeCfg == nil {
		return []contract.StoreReport{}
	}
	if e.storeAt.IsZero() || time.Since(e.storeAt) >= 15*time.Second {
		e.storeAt, e.storeLast = time.Now(), storeReport(e.storeCfg)
	}
	return e.storeLast
}

// sessionDir: existing sessions keep their directory; new ones go under FLEET_SESSIONS_ROOT.
func (e *executor) sessionDir(session string) string {
	legacy := filepath.Join(e.root, "sessions", session)
	if exists(legacy) {
		return legacy
	}
	return filepath.Join(e.sessions, session)
}

func workspaceSession(session, key string) string {
	if key != "" {
		sum := sha256.Sum256([]byte(key))
		return session + "-" + fmt.Sprintf("%x", sum[:])
	}
	return session
}

// commandDir keeps transitioned workspaces isolated; legacy commands retain their original directory.
func (e *executor) commandDir(session, key string) string {
	dir := e.sessionDir(session)
	if key != "" {
		// Hash the untrusted key so it cannot escape the owned session directory.
		sum := sha256.Sum256([]byte(key))
		dir = filepath.Join(dir, "workspaces", fmt.Sprintf("%x", sum[:]))
	}
	return dir
}

// insideDirs: dir is a real directory under the executor or session root.
func (e *executor) insideDirs(dir string) bool {
	m, err := os.Lstat(dir)
	if err != nil || m.Mode()&os.ModeSymlink != 0 || !m.IsDir() {
		return false
	}
	d, err := canon(dir)
	if err != nil {
		return false
	}
	for _, r := range []string{e.root, e.sessions} {
		if c, err := canon(r); err == nil && under(d, c) {
			return true
		}
	}
	return false
}

func (e *executor) forbidden() []string { return []string{e.root, e.sessions} }

func (e *executor) receipt(c contract.Command, status string, native string) error {
	declared := e.declared
	e.declared = nil
	cid, err := idOf(c, "id")
	if err != nil {
		return err
	}
	ex, err := idOf(c, "executionId")
	if err != nil {
		return err
	}
	if declared == nil {
		declared = []string{}
	}
	r := contract.Receipt{HostId: e.host, CommandId: cid, ExecutionId: ex, Status: contract.ReceiptStatus(status), Artifacts: declared}
	if native != "" {
		r.NativeSessionId = &native
	}
	return queueTo(e.ex, contract.RouteReceipts, r)
}

// event queues one Fleet-originated execution event; nativeEvent one harness record under its own open type.
func (e *executor) event(c contract.Command, seq uint64, kind contract.FleetEventType, data any) error {
	return e.nativeEvent(c, seq, string(kind), data)
}

func (e *executor) nativeEvent(c contract.Command, seq uint64, kind string, data any) error {
	cid, err := idOf(c, "id")
	if err != nil {
		return err
	}
	ex, err := idOf(c, "executionId")
	if err != nil {
		return err
	}
	ev := contract.HostEvents{HostId: e.host, ExecutionId: ex, Events: []contract.HostEvent{{Id: fmt.Sprintf("%s-%d", cid, seq), Seq: int64(seq), Type: kind, Timestamp: nowMs(), Data: json.RawMessage(enc(data))}}}
	return queueTo(e.ex, contract.RouteEvents, ev)
}

func (e *executor) finish(c contract.Command, status, native string) error {
	id, _ := idOf(c, "id")
	execution, _ := idOf(c, "executionId")
	if l := e.live[execution]; l != nil && l.mcp != nil && l.commandID() == id {
		if err := l.mcp.close(); err != nil {
			return err
		}
	}
	return e.inTx(func() error {
		if err := e.receipt(c, status, native); err != nil {
			return err
		}
		cid, err := idOf(c, "id")
		if err != nil {
			return err
		}
		if _, err = e.ex.Exec("UPDATE launches SET mcp_fenced=1 WHERE execution=? AND command=?", execution, cid); err != nil {
			return err
		}
		_, err = e.ex.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
		return err
	})
}

// stderrTail: the end of a run's stderr file when it exited non-zero; nil otherwise. Harness stderr carries no
// credentials (they reach the harness through its own login files, never argv or stderr echo).
func stderrTail(dir, execution string, code *int64) *string {
	if code == nil || *code == 0 {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, execution+".stderr"))
	if err != nil || len(b) == 0 {
		return nil
	}
	t := tail(b[max(0, len(b)-2000):])
	return &t
}

func buildExecutionResourceSummary(l *live) *contract.ExecutionResourceSummary {
	out := &contract.ExecutionResourceSummary{}
	if l.metrics != nil {
		l.metrics.applyTo(commandHarness(l.command), out)
	} else if !l.started.IsZero() {
		active := max(int64(0), time.Since(l.started).Milliseconds())
		out.ActiveMs = &active
	}
	missingReason := func() *contract.ExecutionResourceMissingReason {
		r := contract.ExecutionResourceMissingReasonNoSample
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			r = contract.ExecutionResourceMissingReasonOsUnsupported
		} else if l.stopped || l.timedOut {
			r = contract.ExecutionResourceMissingReasonInterrupted
		}
		return &r
	}
	if len(l.cpu) > 0 {
		var cpuSec float64
		for _, sec := range l.cpu {
			cpuSec += sec
		}
		cpuMs := int64(math.Round(cpuSec * 1000))
		out.CpuMs = &cpuMs
	} else {
		out.CpuMissingReason = missingReason()
	}
	if l.peakRSS > 0 {
		peak := int64(l.peakRSS)
		out.PeakMemoryBytes = &peak
	} else {
		out.MemoryMissingReason = missingReason()
	}
	return out
}

// artifacts collects the output folder dir/parts...: the folder and every entry below it are opened relative to their
// parent descriptor with O_NOFOLLOW, so no link at any level (including one swapped in after listing) leads outside.
// Each regular file with one link is copied into the spool, so later edits and worktree cleanup cannot change what is
// uploaded, and queued with its relative path as name. Links and hard-linked files are skipped, the same policy as
// artifact_save. A missing folder collects nothing.
func (e *executor) artifacts(dir string, parts []string, execution string, declared *[]string) error {
	root, err := openInside(dir, parts)
	if err != nil {
		if _, serr := os.Lstat(filepath.Join(append([]string{dir}, parts...)...)); os.IsNotExist(serr) {
			return nil
		}
		return errors.New("artifact folder is a link or unreadable")
	}
	return e.collect(root, "", execution, declared)
}

func (e *executor) collect(d *os.File, rel, execution string, declared *[]string) error {
	defer d.Close()
	if st, err := d.Stat(); err != nil || !st.IsDir() {
		return errors.New("artifact folder is not a directory")
	}
	entries, err := d.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, en := range entries {
		name := en.Name()
		if rel != "" {
			name = rel + "/" + en.Name()
		}
		fd, err := unix.Openat(int(d.Fd()), en.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ELOOP) {
			continue // a link
		}
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), name)
		st, err := f.Stat()
		switch {
		case err != nil:
			f.Close()
			return err
		case st.IsDir():
			if err := e.collect(f, name, execution, declared); err != nil {
				return err
			}
			continue
		case !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Nlink != 1:
			f.Close()
			continue
		}
		id := newUUID()
		spool := filepath.Join(e.root, "spool", id)
		out, err := os.Create(spool)
		if err == nil {
			_, err = io.Copy(out, f)
			out.Close()
		}
		f.Close()
		if err != nil {
			return err
		}
		if err := enqueue(e.db, id, "upload", M{"path": spool, "spool": true, "previews": true, "name": name, "mediaType": mediaType(name), "executionId": execution, "required": false}); err != nil {
			return err
		}
		*declared = append(*declared, name)
	}
	return nil
}

// mediaType from the file extension; unknown types are stored as octet-stream.
func mediaType(name string) string {
	ext := strings.ToLower(name[strings.LastIndexByte(name, '.')+1:])
	for types, exts := range map[string][]string{
		"text/html": {"html", "htm"}, "text/css": {"css"}, "text/javascript": {"js", "mjs"}, "application/json": {"json"},
		"text/plain": {"txt", "md", "log"}, "text/csv": {"csv"}, "application/xml": {"xml"}, "application/pdf": {"pdf"},
		"image/svg+xml": {"svg"}, "image/png": {"png"}, "image/jpeg": {"jpg", "jpeg"}, "image/webp": {"webp"}, "image/gif": {"gif"}, "image/x-icon": {"ico"},
		"video/mp4": {"mp4"}, "video/quicktime": {"mov"}, "video/webm": {"webm"}, "video/x-matroska": {"mkv"},
		"model/gltf-binary": {"glb"}, "model/gltf+json": {"gltf"}, "model/obj": {"obj"}, "model/stl": {"stl"}, "model/vnd.usdz+zip": {"usdz"},
		"font/woff2": {"woff2"}, "application/zip": {"zip"}, "application/x-ndjson": {"jsonl", "ndjson"},
	} {
		if contains(exts, ext) {
			return types
		}
	}
	return "application/octet-stream"
}

func newUUID() string {
	var b [16]byte
	if f, err := os.Open("/dev/urandom"); err == nil {
		io.ReadFull(f, b[:])
		f.Close()
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := fmt.Sprintf("%x", b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
