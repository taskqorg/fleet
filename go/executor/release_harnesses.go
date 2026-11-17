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

// Probe the release's immutable paths as the executor user. These are observations, independent of account readiness.
var managedVersions = sync.OnceValue(func() []contract.HarnessVersion {
	release := releaseHarnesses()
	var versions []contract.HarnessVersion
	for _, name := range []string{"codex", "claude", "antigravity"} {
		a, ok := release.assets[name]
		if !ok {
			continue
		}
		version := contract.HarnessVersion{Harness: name, Expected: a.Version}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, filepath.Join(release.root, a.Executable(name)), "--version")
		cmd.Env = []string{}
		o, err := cmd.Output()
		cancel()
		if err == nil {
			if observed := cliSemver.FindString(string(o)); observed != "" {
				version.Observed = &observed
			}
		}
		versions = append(versions, version)
	}
	return versions
})
