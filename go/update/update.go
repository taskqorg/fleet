package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// verRe is the strict release version N.yymmdd.M (section 1); the tag is "v" + version.
var verRe = regexp.MustCompile(`^[1-9][0-9]*\.[0-9]{6}\.(0|[1-9][0-9]*)$`)

// newer reports whether version a is newer than b, comparing numeric dot parts; b "" or "dev" is older than any.
func newer(a, b string) bool {
	if !verRe.MatchString(a) {
		return false
	}
	if !verRe.MatchString(b) {
		return true
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range pa {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x > y
		}
	}
	return false
}

// Result is `last` of the update state, sent by the executor in its poll and shown by hosts_list.
type Result struct {
	Version string `json:"version"`
	State   string `json:"state"` // ok, pending, held, error, refused, failed
	Error   string `json:"error,omitempty"`
	At      string `json:"at"`
}

// State is update-state.json; pds18-state.json holds one per part under "coordinator" and "executor".
type State struct {
	Refused map[string]string `json:"refused,omitempty"`
	Last    *Result           `json:"last,omitempty"`
}

// signalFile is <data dir>/update/update.json, written by the executor from its poll answers.
type signalFile struct {
	HostID     string `json:"hostId"`
	Target     string `json:"target"`
	AutoUpdate string `json:"autoUpdate"`
	Running    string `json:"running"`
	PollOk     bool   `json:"pollOk"`
	PollError  string `json:"pollError"`
}

type probe int

const (
	down    probe = iota // counts as an unhealthy start once the window ends
	waiting              // executor polls fail with network or 5xx: not the new release's fault
	up
)

// part is one installation on this host: its releases root, the binaries it runs and how to restart and probe them.
// Every file operation under the releases root goes through r: on PDS18 root works in /home/fleet/releases, a directory
// the fleet user owns, and os.Root refuses any symlink out of it. Links are relative (current -> <ver>).
type part struct {
	r                 *os.Root
	name, root, stage string
	bins              []string
	dump              bool
	restart           func() error
	health            func(ver string) probe
	st                *State
}

type updater struct {
	base         string // releases URL: <base>/download/v<ver>/<file>, <base>/latest/download/latest.json
	key          ed25519.PublicKey
	target       string // <os>-<arch>
	client       *http.Client
	now          func() time.Time
	window, tick time.Duration
	coordURL     string // coordinator health on PDS18
	gatewayURL   string
	smokeURL     string // POST with the PDS18 executor's host secret: host auth and the database end to end
	smokeSecret  string // that secret's file; "" or missing skips the smoke (first install)
	dumpCmd      func(ver, file string) error
}

func newUpdater(key ed25519.PublicKey) *updater {
	return &updater{base: releasesURL, key: key, target: runtime.GOOS + "-" + runtime.GOARCH,
		client: &http.Client{Timeout: 10 * time.Minute}, now: time.Now, window: 30 * time.Second, tick: time.Second,
		coordURL: "http://127.0.0.1:9470/api/health", gatewayURL: "http://127.0.0.1:8742/healthz",
		smokeURL: "http://127.0.0.1:9470/api/hosts/whoami",
		dumpCmd: pgDump}
}

func (u *updater) result(ver, state, msg string) *Result {
	log.Printf("%s %s: %s", ver, state, msg)
	return &Result{Version: ver, State: state, Error: msg, At: u.now().UTC().Format(time.RFC3339)}
}

// held: autoUpdate "off" or an RFC 3339 UTC time still in the future.
func (u *updater) held(autoUpdate string) bool {
	if autoUpdate == "off" {
		return true
	}
	t, err := time.Parse(time.RFC3339, autoUpdate)
	return err == nil && strings.HasSuffix(autoUpdate, "Z") && t.After(u.now())
}

// cycle is one run of one part (section 3.2). It returns the result to record, or nil when nothing changed.
func (u *updater) cycle(p *part, autoUpdate string, candidate func() string) *Result {
	running := rootVersion(p.r, "current")
	// A release without a healthy marker is still being checked (pending, an interrupted run, a first install).
	var resumed *Result
	if _, err := p.r.Stat("current/healthy"); running != "" && err != nil {
		if resumed = u.settle(p, running, false); resumed.State != "ok" {
			return resumed
		}
	}
	if u.held(autoUpdate) {
		if p.st.Last != nil && p.st.Last.State == "held" {
			return resumed
		}
		return u.result(running, "held", "autoUpdate "+autoUpdate)
	}
	cand := candidate()
	if !newer(cand, running) || p.st.Refused[cand] != "" {
		return resumed
	}
	log.Printf("%s: %s -> %s", p.name, running, cand)
	dir, err := u.fetch(p, cand)
	var ref refusal
	if errors.As(err, &ref) {
		return u.refuse(p, cand, string(ref), err.Error())
	}
	if err != nil {
		return u.result(cand, "error", err.Error())
	}
	if err := u.install(p, cand, dir); err != nil {
		return u.result(cand, "error", err.Error())
	}
	return u.settle(p, cand, true)
}

func (u *updater) refuse(p *part, ver, reason, msg string) *Result {
	if p.st.Refused == nil {
		p.st.Refused = map[string]string{}
	}
	p.st.Refused[ver] = reason
	r := u.result(ver, "refused", msg)
	r.Error = reason
	return r
}

type refusal string

func (r refusal) Error() string { return string(r) }

// fetch downloads, verifies and selftests release ver into <stage>/<ver>.tmp and returns that directory.
func (u *updater) fetch(p *part, ver string) (string, error) {
	dir := filepath.Join(p.stage, ver+".tmp")
	if releaseVersion(dir) == ver && p.stage != p.root {
		return dir, nil // staged and verified earlier in this run (PDS18 coordinator side)
	}
	os.RemoveAll(dir)
	file := "fleet-" + ver + "-" + u.target + ".tgz"
	at := u.base + "/download/v" + ver + "/"
	sums, err := u.get(at + "SHA256SUMS")
	if err != nil {
		return "", err
	}
	sig, err := u.get(at + "SHA256SUMS.sig")
	if err != nil {
		return "", err
	}
	if !ed25519.Verify(u.key, sums, sig) {
		return "", fmt.Errorf("%w: SHA256SUMS signature does not verify", refusal("provenance"))
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("%w: %s not in SHA256SUMS", refusal("checksum"), file)
	}
	tgz := dir + ".tgz"
	defer os.Remove(tgz)
	for try := 0; ; try++ {
		got, err := u.download(at+file, tgz)
		if err != nil {
			return "", err
		}
		if got == want {
			break
		}
		if try == 1 { // one fresh download first: a truncated transfer is transient
			return "", fmt.Errorf("%w: %s digest %s, SHA256SUMS %s", refusal("checksum"), file, got, want)
		}
	}
	if err := unpack(tgz, dir); err != nil {
		return "", err
	}
	if v := releaseVersion(dir); v != ver {
		return "", fmt.Errorf("%w: release.json version %q", refusal("checksum"), v)
	}
	for _, b := range p.bins {
		if out, err := exec.Command(filepath.Join(dir, b), "selftest").CombinedOutput(); err != nil {
			return "", fmt.Errorf("%w: %s: %v %s", refusal("selftest"), b, err, bytes.TrimSpace(out))
		}
	}
	return dir, nil
}

func (u *updater) get(url string) ([]byte, error) {
	resp, err := u.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// download streams url to file and returns its hex SHA-256.
func (u *updater) download(url, file string) (string, error) {
	resp, err := u.client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), f.Close()
}

// unpack extracts a release tarball: regular files and directories only, no path outside dir.
func unpack(tgz, dir string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return refusal("checksum")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if name == "." {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%w: tarball path %q", refusal("checksum"), h.Name)
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			err = writeFrom(path, tr, fs.FileMode(h.Mode)&0o755|0o644)
		default:
			err = fmt.Errorf("%w: tarball entry %q of type %c", refusal("checksum"), h.Name, h.Typeflag)
		}
		if err != nil {
			return err
		}
	}
}

func writeFrom(path string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// install puts the staged tree at <root>/<ver> (read-only files) and switches current and previous.
func (u *updater) install(p *part, ver, staged string) error {
	if p.dump {
		file := "/backup/fleet/pre-deploy/fleet-pre-" + ver + ".dump"
		if err := u.dumpCmd(ver, file); err != nil {
			return fmt.Errorf("pg_dump: %w", err)
		}
	}
	tmp := ver + ".tmp"
	if staged != filepath.Join(p.root, tmp) {
		p.r.RemoveAll(tmp)
		if err := copyTree(staged, p.r, tmp); err != nil {
			return err
		}
	}
	if err := fs.WalkDir(p.r.FS(), tmp, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return p.r.Chmod(path, info.Mode().Perm()&^0o222)
	}); err != nil {
		return err
	}
	p.r.RemoveAll(ver)
	if err := p.r.Rename(tmp, ver); err != nil {
		return err
	}
	if old, err := p.r.Readlink("current"); err == nil && filepath.Base(old) != ver {
		if err := relink(p.r, "previous", filepath.Base(old)); err != nil {
			return err
		}
	}
	return relink(p.r, "current", ver)
}

// relink points <root>/<name> at the relative target atomically.
func relink(r *os.Root, name, target string) error {
	tmp := name + ".tmp-" + strconv.Itoa(os.Getpid())
	r.Remove(tmp)
	if err := r.Symlink(target, tmp); err != nil {
		return err
	}
	return r.Rename(tmp, name)
}

// settle restarts and probes ver up to three unhealthy starts, then rolls back (section 3.4). restart false first
// probes the running release without a restart (resume of an earlier run).
func (u *updater) settle(p *part, ver string, restart bool) *Result {
	startsFile, healthyFile := ver+"/starts", ver+"/healthy"
	for {
		if restart {
			if err := p.restart(); err != nil {
				log.Printf("%s: restart: %v", p.name, err)
			}
		}
		restart = true
		switch u.probe(p, ver) {
		case up:
			p.r.Remove(startsFile)
			if err := p.r.WriteFile(healthyFile, []byte(u.now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
				return u.result(ver, "error", err.Error())
			}
			return u.result(ver, "ok", "")
		case waiting:
			return u.result(ver, "pending", "coordinator not reachable from the executor")
		}
		b, _ := p.r.ReadFile(startsFile)
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		n++
		p.r.WriteFile(startsFile, []byte(strconv.Itoa(n)+"\n"), 0o644)
		log.Printf("%s %s: unhealthy start %d", p.name, ver, n)
		if n < 3 {
			continue
		}
		prev, err := p.r.Readlink("previous")
		if err != nil || filepath.Base(prev) == ver {
			r := u.result(ver, "failed", "unhealthy, no previous release")
			r.Error = "unhealthy"
			return r
		}
		if err := relink(p.r, "current", filepath.Base(prev)); err != nil {
			return u.result(ver, "failed", err.Error())
		}
		if err := p.restart(); err != nil {
			log.Printf("%s: restart: %v", p.name, err)
		}
		return u.refuse(p, ver, "unhealthy", "rolled back to "+filepath.Base(prev))
	}
}

func (u *updater) probe(p *part, ver string) probe {
	last := down
	for end := u.now().Add(u.window); ; {
		if last = p.health(ver); last == up || !u.now().Before(end) {
			return last
		}
		time.Sleep(u.tick)
	}
}

func (u *updater) coordinatorSelf() (ver, autoUpdate string) {
	var h struct{ Version, AutoUpdate, Database string }
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(u.coordURL)
	if err != nil {
		return "", "on" // not answering: an update may repair it
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(&h)
	if h.AutoUpdate == "" {
		h.AutoUpdate = "on"
	}
	return h.Version, h.AutoUpdate
}

func (u *updater) coordinatorHealth(ver string) probe {
	c := &http.Client{Timeout: 3 * time.Second}
	var h struct{ Version, Database string }
	resp, err := c.Get(u.coordURL)
	if err != nil {
		return down
	}
	json.NewDecoder(resp.Body).Decode(&h)
	resp.Body.Close()
	if resp.StatusCode != 200 || h.Database != "ok" || h.Version != ver {
		return down
	}
	g, err := c.Get(u.gatewayURL)
	if err != nil {
		return down
	}
	g.Body.Close()
	if g.StatusCode != 200 {
		return down
	}
	return u.smoke(c)
}

// smoke: the new coordinator authenticates the PDS18 executor's host secret and names its host (delivery pipeline §7).
func (u *updater) smoke(c *http.Client) probe {
	secret, err := os.ReadFile(u.smokeSecret)
	if u.smokeSecret == "" || errors.Is(err, fs.ErrNotExist) {
		return up
	}
	if err != nil {
		log.Print("smoke: ", err)
		return down
	}
	req, _ := http.NewRequest("POST", u.smokeURL, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return down
	}
	defer resp.Body.Close()
	var w struct{ HostID string }
	json.NewDecoder(resp.Body).Decode(&w)
	if resp.StatusCode != 200 || w.HostID == "" {
		log.Printf("smoke: %s %s", u.smokeURL, resp.Status)
		return down
	}
	return up
}

func (u *updater) executorHealth(signal string) func(string) probe {
	return func(ver string) probe {
		var s signalFile
		if !readJSON(signal, &s) || s.Running != ver {
			return down
		}
		if s.PollOk {
			return up
		}
		if s.PollError == "network" || strings.HasPrefix(s.PollError, "http-5") {
			return waiting
		}
		return down
	}
}

func (u *updater) latest() string {
	b, err := u.get(u.base + "/latest/download/latest.json")
	if err != nil {
		log.Print("latest.json: ", err)
		return ""
	}
	var l struct{ Version string }
	json.Unmarshal(b, &l)
	return l.Version
}

// pgDump runs pg_dump as postgres; root writes the file (postgres cannot write /backup/fleet/pre-deploy).
func pgDump(_, file string) error {
	f, err := os.OpenFile(file+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	c := exec.Command("runuser", "-u", "postgres", "--", "pg_dump", "-Fc", "-d", "fleet")
	var stderr bytes.Buffer
	c.Stdout, c.Stderr = f, &stderr
	err = c.Run()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(file + ".tmp")
		return fmt.Errorf("%v %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return os.Rename(file+".tmp", file)
}

func command(argv ...string) func() error {
	return func() error {
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v %s", strings.Join(argv, " "), err, bytes.TrimSpace(out))
		}
		return nil
	}
}

// rootVersion reads version from <name>/release.json under r; "" when absent.
func rootVersion(r *os.Root, name string) string {
	var v struct{ Version string }
	if b, err := r.ReadFile(name + "/release.json"); err == nil {
		json.Unmarshal(b, &v)
	}
	return v.Version
}

// openReleases opens the releases root as an os.Root, refusing a symlink in its place (checked again after opening).
func openReleases(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	if after, err := r.Stat("."); err != nil || !os.SameFile(before, after) {
		r.Close()
		return nil, fmt.Errorf("%s changed while opening", dir)
	}
	return r, nil
}

// releaseVersion reads version from <dir>/release.json; "" when absent.
func releaseVersion(dir string) string {
	var r struct{ Version string }
	readJSON(filepath.Join(dir, "release.json"), &r)
	return r.Version
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

// writeJSON writes durably: temp file, fsync, rename, fsync of the directory.
func writeJSON(path string, v any, mode fs.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func lock(path string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		log.Print("another fleet-update run holds the lock")
		return nil, false, nil
	}
	return func() { f.Close() }, true, nil
}

// pruneReleases keeps current and previous: removes every other <ver>/ and leftover .tmp entries.
func pruneReleases(r *os.Root) {
	keep := map[string]bool{}
	for _, l := range []string{"current", "previous"} {
		if t, err := r.Readlink(l); err == nil {
			keep[filepath.Base(t)] = true
		}
	}
	entries, _ := fs.ReadDir(r.FS(), ".")
	for _, e := range entries {
		n := e.Name()
		if strings.Contains(n, ".tmp") || verRe.MatchString(n) && !keep[n] {
			r.RemoveAll(n)
		}
	}
}

// pruneDumps keeps the pre-deploy dumps of current and previous of r and of refused versions (a rolled-back
// release ran its migrations; its dump is the state before them).
func pruneDumps(dir string, r *os.Root, st *State) {
	keep := map[string]bool{}
	for _, l := range []string{"current", "previous"} {
		if t, err := r.Readlink(l); err == nil {
			keep["fleet-pre-"+filepath.Base(t)+".dump"] = true
		}
	}
	for v := range st.Refused { // ponytail: kept until removed by hand; a cap if refused releases pile up

		keep["fleet-pre-"+v+".dump"] = true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "fleet-pre-") && !keep[n] {
			os.Remove(filepath.Join(dir, n))
		}
	}
}

// copyTree copies the trusted stage tree src into dst under r.
func copyTree(src string, r *os.Root, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return r.MkdirAll(to, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := r.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFrom(dst, bufio.NewReader(in), mode)
}
