package main

import (
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fleet/harnessrelease"
	"fmt"
	"hash"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// prepareHarnesses installs verified payloads without disturbing the running release.
func (u *updater) prepareHarnesses(p *part, release string) error {
	assets, err := harnessrelease.Read(release, u.target)
	if err != nil {
		return fmt.Errorf("%w: managed CLI manifest: %v", refusal("harness-manifest"), err)
	}
	for _, name := range []string{"codex", "claude", "antigravity"} {
		a, ok := assets[name]
		if !ok {
			continue
		}
		if err := u.prepareHarness(p, name, a); err != nil {
			var invalid refusal
			if errors.As(err, &invalid) {
				return fmt.Errorf("%w: %v", refusal(fmt.Sprintf("%s: %s %s", invalid, name, a.Version)), err)
			}
			return fmt.Errorf("managed CLI %s %s: %w", name, a.Version, err)
		}
	}
	return nil
}

func (u *updater) prepareHarness(p *part, name string, a harnessrelease.Asset) error {
	if st, err := p.r.Stat(a.Executable(name)); err == nil && executable(st) {
		return nil
	}
	// Use a private trusted staging directory. Publish only the complete, verified payload.
	stage, err := os.MkdirTemp(p.stage, "cli-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	payload := filepath.Join(stage, "download")
	var digest hash.Hash = sha256.New()
	if a.SHA512 != "" {
		digest = sha512.New()
	}
	got, err := u.downloadDigest(a.URL, payload, digest)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, a.Digest()) {
		return fmt.Errorf("%w: got %s, expected %s", refusal("harness-checksum"), got, a.Digest())
	}
	files := filepath.Join(stage, "files")
	if a.Entry != "" {
		if err := unpack(payload, files); err != nil {
			return fmt.Errorf("%w: %v", refusal("harness-archive"), err)
		}
	} else {
		if err := os.Mkdir(files, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(payload, 0o755); err != nil {
			return err
		}
		if err := os.Rename(payload, filepath.Join(files, name)); err != nil {
			return err
		}
	}
	entry, err := filepath.Rel(a.Dir(name), a.Executable(name))
	if err != nil {
		return err
	}
	if st, err := os.Stat(filepath.Join(files, entry)); err != nil || !executable(st) {
		return fmt.Errorf("%w: executable %s is missing", refusal("harness-archive"), entry)
	}

	dir := a.Dir(name)
	tmp := dir + ".tmp"
	if err := p.r.RemoveAll(tmp); err != nil {
		return err
	}
	defer p.r.RemoveAll(tmp)
	if err := copyTree(files, p.r, tmp); err != nil {
		return err
	}
	if err := fs.WalkDir(p.r.FS(), tmp, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return p.r.Chmod(path, info.Mode().Perm()&^0o222)
	}); err != nil {
		return err
	}
	if err := p.r.RemoveAll(dir); err != nil {
		return err
	}
	return p.r.Rename(tmp, dir)
}

func executable(st fs.FileInfo) bool {
	return st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

func (u *updater) pruneHarnesses(p *part) {
	keep := map[string]bool{}
	for _, link := range []string{"current", "previous"} {
		assets, err := harnessrelease.Read(filepath.Join(p.root, link), u.target)
		if err != nil {
			return // Preserve rollback payloads when the manifest cannot be read.
		}
		for name, asset := range assets {
			keep[asset.Dir(name)] = true
		}
	}
	for _, name := range []string{"codex", "claude", "antigravity"} {
		parent := filepath.Join("harnesses", name)
		entries, _ := fs.ReadDir(p.r.FS(), parent)
		for _, entry := range entries {
			path := filepath.Join(parent, entry.Name())
			if !keep[path] {
				if err := p.r.RemoveAll(path); err != nil {
					log.Printf("prune managed CLI %s: %v", path, err)
				}
			}
		}
	}
}
