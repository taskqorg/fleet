package main

import (
	"context"
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

func observeCLIVersion(path string) *string {
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
	errtrack.Go("harness.version", func() { probeCLIVersion(path, sample) })
	return nil
}

func probeCLIVersion(path string, sample *cliObservation) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{}
	cmd.WaitDelay = time.Second
	o, err := cmd.Output()
	var observed *string
	if err == nil {
		if v := cliSemver.FindString(string(o)); v != "" {
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
