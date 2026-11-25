package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
	"fleet/errtrack"
	"fleet/harnessrelease"
)

type harnessRelease struct {
	root   string
	assets map[string]harnessrelease.Asset
	err    error
}

var releaseHarnesses = sync.OnceValue(func() harnessRelease {
	exe, err := os.Executable()
	if err != nil {
		return harnessRelease{err: err}
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return harnessRelease{err: err}
	}
	dir := filepath.Dir(exe)
	assets, err := harnessrelease.Read(dir, runtime.GOOS+"-"+runtime.GOARCH)
	return harnessRelease{root: filepath.Dir(dir), assets: assets, err: err}
})

func managedExecutable(name, fallback, model string) (path string, managed bool, err error) {
	release := releaseHarnesses()
	if release.err != nil {
		return "", false, fmt.Errorf("managed CLI manifest: %w", release.err)
	}
	a, ok := release.assets[name]
	if !ok {
		return fallback, false, nil
	}
	if err := harnessrelease.ModelError(name, a.Version, model); err != nil {
		return "", true, err
	}
	exe := filepath.Join(release.root, a.Executable(name))
	if !isFile(exe) {
		return "", true, fmt.Errorf("release-managed %s %s executable missing", name, a.Version)
	}
	return exe, true, nil
}

// useManagedTools puts the release's pinned host tools (config/tool-pins.json, docs/designs/fleet-host-updates.md)
// first on this process's PATH, once at start. Every child inherits it: native runs, readiness, the tool:<name>
// capability (exec.LookPath) and forge calls all find the same binary. A release without tool pins keeps the host's.
func useManagedTools() error {
	release := releaseHarnesses()
	if release.err != nil {
		return nil // the CLI manifest error is reported where a CLI is used (managedExecutable)
	}
	dirs, err := managedToolDirs(release.root)
	if err != nil || len(dirs) == 0 {
		return err
	}
	return os.Setenv("PATH", strings.Join(append(dirs, os.Getenv("PATH")), string(os.PathListSeparator)))
}

// managedToolDirs: the directory of each pinned tool's executable under the releases root, each checked present. A
// missing one stops the executor, so the release fails its health check and rolls back.
func managedToolDirs(root string) ([]string, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return nil, err
	}
	assets, err := harnessrelease.Tools.Read(filepath.Dir(exe), runtime.GOOS+"-"+runtime.GOARCH)
	if err != nil {
		return nil, fmt.Errorf("managed tool manifest: %w", err)
	}
	var dirs []string
	for _, name := range harnessrelease.Tools.Names {
		a, ok := assets[name]
		if !ok {
			continue
		}
		path := filepath.Join(root, a.Executable(name))
		if !isFile(path) {
			return nil, fmt.Errorf("release-managed tool %s %s executable missing", name, a.Version)
		}
		if d := filepath.Dir(path); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// runExecutable: the CLI a run launches. A host qualification run (docs/designs/fleet-host-updates.md, step 4) names a
// proposed pin in its profile (harnessCandidate: {name, asset}); fleet-update of this release stages it into the
// candidate cache with the same checks as a shipped pin, vendor origin included, and the run uses it. Every other run
// uses its release's pin.
func runExecutable(name, fallback string, p any) (string, bool, error) {
	model := launchOf(p).Model
	cand := jAt(p, "harnessCandidate")
	if cand == nil {
		cand = jAt(p, "agentConfig", "harnessCandidate")
	}
	if cand == nil {
		return managedExecutable(name, fallback, model)
	}
	c := typed[harnessCandidate](cand)
	if c.Name != name {
		return "", true, fmt.Errorf("harness candidate is for %s, not %s", c.Name, name)
	}
	if err := harnessrelease.ModelError(name, c.Asset.Version, model); err != nil {
		return "", true, err
	}
	release := releaseHarnesses()
	if release.err != nil {
		return "", true, release.err
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(filepath.Dir(exe), "fleet-update"), "stage-candidate", "cli", name,
		filepath.Join(envOr("FLEET_DATA_DIR", defaultDataDir()), "candidates"), enc(c.Asset))
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", true, fmt.Errorf("harness candidate %s %s: %s", name, c.Asset.Version, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", true, err
	}
	return strings.TrimSpace(string(out)), true, nil
}

// harnessCandidate: a proposed CLI pin a qualification run uses.
type harnessCandidate struct {
	Name  string               `json:"name"`
	Asset harnessrelease.Asset `json:"asset"`
}

var cliSemver = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?\b`)

type cliObservation struct {
	info    os.FileInfo
	version *string
	at      time.Time
}

var (
	cliObservationsMu sync.Mutex
	cliObservations   = map[string]*cliObservation{}
)

// Probe the release paths as the executor user when a file changes. A failed probe is retried on later polls.
func managedVersions() []contract.HarnessVersion {
	release := releaseHarnesses()
	if len(release.assets) == 0 {
		return nil
	}
	cliObservationsMu.Lock()
	defer cliObservationsMu.Unlock()
	var versions []contract.HarnessVersion
	for _, name := range []string{"codex", "claude", "antigravity"} {
		a := release.assets[name]
		path := filepath.Join(release.root, a.Executable(name))
		versions = append(versions, contract.HarnessVersion{Harness: name, Expected: a.Version, Observed: observeCLIVersion(path)})
	}
	return versions
}

// toolSemver: a tool's version in its --version output ("v26.13.1", "gh version 2.105.0 (2026-11-01)").
var toolSemver = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// releaseTools: this release's host tool pins (config/tool-pins.json), nil while they are off.
var releaseTools = sync.OnceValue(func() map[string]harnessrelease.Asset {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return nil
	}
	assets, _ := harnessrelease.Tools.Read(filepath.Dir(exe), runtime.GOOS+"-"+runtime.GOARCH)
	return assets
})

// toolVersions: each host tool's version (docs/designs/fleet-host-updates.md, Host tools). With tool pins on, the pin and
// the release-managed executable's version; off, the version of the tool on the executor's PATH, with no expected one.
// Probed like the CLIs: once per changed file, in the background, as the executor user.
func toolVersions() []contract.ToolVersion {
	assets := releaseTools()
	root := releaseHarnesses().root
	cliObservationsMu.Lock()
	defer cliObservationsMu.Unlock()
	out := []contract.ToolVersion{}
	for _, name := range harnessrelease.Tools.Names {
		v := contract.ToolVersion{Tool: name}
		if a, ok := assets[name]; ok && root != "" {
			v.Expected = &a.Version
			v.Observed = observeVersion(filepath.Join(root, a.Executable(name)), toolSemver, os.Getenv("PATH"))
		} else if path, err := exec.LookPath(name); err == nil {
			v.Observed = observeVersion(path, toolSemver, os.Getenv("PATH"))
		}
		out = append(out, v)
	}
	return out
}

func observeCLIVersion(path string) *string { return observeVersion(path, cliSemver, "") }

// observeVersion: the cached version of the executable at path, probed again when the file changes. pathEnv is the
// probe's PATH (a tool such as pnpm may need node beside it); a CLI runs with an empty environment.
func observeVersion(path string, re *regexp.Regexp, pathEnv string) *string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || unix.Access(path, unix.X_OK) != nil {
		delete(cliObservations, path)
		return nil
	}
	old, ok := cliObservations[path]
	if ok && os.SameFile(info, old.info) && info.Size() == old.info.Size() && info.Mode() == old.info.Mode() &&
		info.ModTime().Equal(old.info.ModTime()) && (old.version != nil || time.Since(old.at) < 30*time.Second) {
		return old.version
	}
	sample := &cliObservation{info: info, at: time.Now()}
	cliObservations[path] = sample
	errtrack.Go("harness.version", func() { probeVersion(path, sample, re, pathEnv) })
	return nil
}

func probeVersion(path string, sample *cliObservation, re *regexp.Regexp, pathEnv string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{}
	if pathEnv != "" {
		cmd.Env = []string{"PATH=" + pathEnv}
	}
	cmd.WaitDelay = time.Second
	o, err := cmd.Output()
	var observed *string
	if err == nil {
		if v := re.FindString(string(o)); v != "" {
			observed = &v
		}
	}
	cliObservationsMu.Lock()
	defer cliObservationsMu.Unlock()
	if cliObservations[path] == sample {
		sample.at = time.Now()
		sample.version = observed
	}
}
