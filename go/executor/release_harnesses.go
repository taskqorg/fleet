package main

import (
	"context"
	"fleet/contract"
	"fleet/harnessrelease"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"
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
	go probeCLIVersion(path, sample)
	return nil
}

func probeCLIVersion(path string, sample *cliObservation) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{}
	cmd.WaitDelay = time.Second
	o, err := cmd.Output()
	cliObservationsMu.Lock()
	defer cliObservationsMu.Unlock()
	if cliObservations[path] != sample {
		return
	}
	sample.at = time.Now()
	if err == nil {
		if observed := cliSemver.FindString(string(o)); observed != "" {
			sample.version = &observed
		}
	}
}
