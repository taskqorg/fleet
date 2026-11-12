package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// latestUI discovers the newest UI release for backend from the repository's tag list (git smart HTTP advertisement):
// api.github.com counts every unauthenticated request, 304 included, against 60 per hour, which the 1 minute timer
// alone would exhaust. The release workflow creates the ui-<backend>-v<ver> tag when it publishes the release with
// its assets, so a tag is a published release. Native latest.json is untouched.
func (u *updater) latestUI(backend string) (string, error) {
	prefix := "refs/tags/ui-" + backend + "-v"
	c, err := u.getCached(u.refsURL)
	if err != nil {
		return "", err
	}
	latest := ""
	for _, line := range strings.Split(string(c.Body), "\n") {
		f := strings.Fields(line) // <pkt-len><sha> refs/tags/<tag>[^{}]
		if len(f) == 2 && strings.HasPrefix(f[1], prefix) {
			if v := strings.TrimSuffix(strings.TrimPrefix(f[1], prefix), "^{}"); newer(v, latest) {
				latest = v
			}
		}
	}
	return latest, nil
}

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
	candidate, err := u.latestUI(backend)
	if err != nil {
		return fmt.Errorf("UI discovery: %w", err)
	}
	if u.held(autoUpdate) || !newer(candidate, rootVersion(r, "current")) || st.Refused[candidate] != "" {
		return nil
	}
	dir, err := u.fetch(p, candidate)
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
