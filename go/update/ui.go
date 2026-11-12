package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// validateUI binds a UI tree to the exact backend qualified by its release job.
func validateUI(dir, version, backend string) error {
	var manifest struct{ Kind, Version, BackendVersion, Commit string }
	b, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return err
	}
	sha, err := hex.DecodeString(manifest.Commit)
	if manifest.Kind != "ui" || manifest.Version != version || manifest.BackendVersion != backend || err != nil || len(sha) != 20 {
		return fmt.Errorf("%w: incompatible UI manifest", refusal("compatibility"))
	}
	for _, name := range []string{"index.html", "screens.json", "sw.js"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("%w: missing UI entry %s", refusal("checksum"), name)
		}
	}
	return nil
}

// latestUI discovers the newest UI release for backend with git protocol v2 ls-refs, which returns only the
// ui-<backend>-v* tags (about 100 bytes), unauthenticated and outside the api.github.com limit of 60 requests per
// hour that counts 304 answers too. The release workflow creates the tag when it publishes the release with its
// assets, so a tag is a published release. A deleted release must delete its tag too (gh release delete
// --cleanup-tag): otherwise it stays the candidate and every run reports its 404, retried like any HTTP error
// (fleet-self-update.md, section 3: GitHub down is transient, runs retry). Native latest.json is untouched.
func (u *updater) latestUI(backend string) (string, error) {
	prefix := "refs/tags/ui-" + backend + "-v"
	body := pkt("command=ls-refs\n") + "0001" + pkt("ref-prefix "+prefix+"\n") + "0000"
	req, err := http.NewRequest("POST", u.repoURL+"/git-upload-pack", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := okStatus("POST", req.URL.String(), resp); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // the prefix keeps it to a few lines; never a whole advertisement
	if err != nil {
		return "", err
	}
	lines, err := pktLines(b)
	if err != nil {
		return "", fmt.Errorf("ls-refs: %w", err)
	}
	latest := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "ERR ") {
			return "", fmt.Errorf("ls-refs: %s", line)
		}
		f := strings.Fields(line) // <object id> <ref> [attributes]; only the ref is used
		if len(f) < 2 {
			return "", fmt.Errorf("ls-refs: unexpected line %q", line)
		}
		if !strings.HasPrefix(f[1], prefix) {
			continue // ref-prefix is a hint; a server may answer more broadly (HEAD, other refs)
		}
		if v := strings.TrimPrefix(f[1], prefix); newer(v, latest) {
			latest = v
		}
	}
	return latest, nil
}

// pktLines reads git pkt-lines up to the flush packet: 4 hex digits of total length, then the payload, whose
// trailing LF is optional and dropped.
func pktLines(b []byte) ([]string, error) {
	var lines []string
	for len(b) >= 4 {
		n, err := strconv.ParseUint(string(b[:4]), 16, 16)
		if err != nil || n == 1 || n == 2 || n == 3 || int(n) > len(b) {
			return nil, fmt.Errorf("not a pkt-line stream: %.80q", b)
		}
		if n == 0 {
			return lines, nil
		}
		lines = append(lines, strings.TrimSuffix(string(b[4:n]), "\n"))
		b = b[n:]
	}
	return nil, fmt.Errorf("pkt-line stream without a flush: %.80q", b)
}

// pkt is one git pkt-line: 4 hex digits of the total length, then the payload.
func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// cycleUI runs under the PDS18 update lock and never restarts a native service.
func (u *updater) cycleUI(backend, autoUpdate, root, stage, stateFile string) error {
	if !verRe.MatchString(backend) {
		return fmt.Errorf("UI update: backend version unavailable")
	}
	r, err := openReleases(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.Chmod(root, 0755); err != nil {
		return err
	}
	st := &State{}
	readJSON(stateFile, st)
	p := &part{r: r, name: "ui", root: root, stage: stage, uiBackend: backend, st: st, restart: func() error { return nil }}
	p.health = func(ver string) probe {
		resp, err := u.client.Get(strings.TrimSuffix(u.coordURL, "/api/health") + "/ui-release.json")
		if err != nil {
			return down
		}
		defer resp.Body.Close()
		var m struct{ Version, BackendVersion string }
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&m) != nil || m.Version != ver || m.BackendVersion != backend {
			return down
		}
		return up
	}
	running := rootVersion(r, "current")
	if _, err := r.Stat("current/healthy"); running != "" && err != nil {
		st.Last = u.settle(p, running, false)
		if st.Last.State == "failed" {
			if err := r.Remove("current"); err != nil && !os.IsNotExist(err) {
				return err
			}
			st.Last = u.refuse(p, st.Last.Version, "unhealthy", "restored bundled UI")
		}
		if err := writeJSON(stateFile, st, 0600); err != nil {
			return err
		}
		if st.Last.State != "ok" {
			return nil
		}
	}
	if u.held(autoUpdate) {
		return nil
	}
	candidate, err := u.latestUI(backend)
	if err != nil {
		return fmt.Errorf("UI discovery: %w", err)
	}
	if !newer(candidate, rootVersion(r, "current")) || st.Refused[candidate] != "" {
		return nil
	}
	dir, err := u.fetch(p, candidate)
	var ref refusal
	if errors.As(err, &ref) { // recorded, so the next run does not download and refuse it again
		st.Last = u.refuse(p, candidate, string(ref), err.Error())
		return writeJSON(stateFile, st, 0600)
	}
	if err != nil {
		return err
	}
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0444)
		if entry.IsDir() {
			mode = 0755
		}
		return os.Chmod(path, mode)
	}); err != nil {
		return err
	}
	assets := filepath.Join(dir, "assets")
	entries, err := os.ReadDir(assets)
	if err != nil {
		return err
	}
	if err := r.MkdirAll("assets", 0755); err != nil {
		return err
	}
	if err := r.Chmod("assets", 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return fmt.Errorf("UI asset is not regular: %s", entry.Name())
		}
		b, err := os.ReadFile(filepath.Join(assets, entry.Name()))
		if err != nil {
			return err
		}
		name := filepath.Join("assets", entry.Name())
		old, err := r.ReadFile(name)
		if err == nil {
			if !bytes.Equal(old, b) {
				return fmt.Errorf("UI asset collision: %s", entry.Name())
			}
			if err := r.Chmod(name, 0444); err != nil {
				return err
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := r.WriteFile(name, b, 0444); err != nil {
			return err
		}
		if err := r.Chmod(name, 0444); err != nil {
			return err
		}
	}
	if err := u.install(p, candidate, dir); err != nil {
		return err
	}
	st.Last = u.settle(p, candidate, true)
	if st.Last.State == "failed" {
		if err := r.Remove("current"); err != nil && !os.IsNotExist(err) {
			return err
		}
		st.Last = u.refuse(p, st.Last.Version, "unhealthy", "restored bundled UI")
	}
	return writeJSON(stateFile, st, 0600)
}
