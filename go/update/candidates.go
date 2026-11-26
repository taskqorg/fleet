package main

import (
	"encoding/json"
	"errors"
	"fleet/harnessrelease"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"
)

// stageCandidate installs one proposed component (a pin under host qualification, docs/designs/fleet-host-updates.md
// step 4) into dir, the executor's candidate cache, exactly as a release's pin is installed: vendor origin, digest,
// unpack, executable and --version. It keeps only this candidate of the component and answers its executable's
// absolute path. The running release and its caches are not touched.
func (u *updater) stageCandidate(kindName, name, dir string, raw []byte) (string, error) {
	kind, ok := harnessrelease.KindNamed(kindName)
	if !ok {
		return "", fmt.Errorf("unknown component kind %q", kindName)
	}
	var a harnessrelease.Asset
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("candidate %s/%s: %w", kindName, name, err)
	}
	a, err := kind.Candidate(name, a)
	if err != nil {
		return "", fmt.Errorf("%w: candidate %v", refusal("harness-manifest"), err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".stage"), 0o700); err != nil {
		return "", err
	}
	root, err := openReleases(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// Trials on one host start together and stage the same candidate: wait for the one staging it, then reuse its cache.
	var unlock func()
	for deadline := time.Now().Add(10 * time.Minute); ; {
		var ok bool
		if unlock, ok, err = lockReleases(root); err != nil {
			return "", err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return "", errors.New("another candidate is being staged")
		}
		time.Sleep(time.Second)
	}
	defer unlock()
	p := &part{r: root, root: dir, stage: filepath.Join(dir, ".stage")}
	if err := u.prepareHarness(p, name, a, nil); err != nil { // the executor's PATH already leads with its managed tools
		return "", err
	}
	// One candidate per component: the others of this component go.
	parent := filepath.Dir(a.Dir(name))
	entries, _ := fs.ReadDir(root.FS(), parent)
	for _, e := range entries {
		if path := filepath.Join(parent, e.Name()); path != a.Dir(name) {
			if err := removeCache(root, path); err != nil {
				log.Printf("prune candidate %s: %v", path, err)
			}
		}
	}
	return filepath.Join(dir, a.Executable(name)), nil
}
