package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fleet/harnessrelease"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

var managedHarnesses = []string{"codex", "claude", "antigravity"}

// prepareHarnesses installs verified payloads without disturbing the running release.
func (u *updater) prepareHarnesses(p *part, release string) error {
	assets, err := harnessrelease.Read(release, u.target)
	if err != nil {
		return fmt.Errorf("%w: managed CLI manifest: %v", refusal("harness-manifest"), err)
	}
	for _, name := range managedHarnesses {
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
	dir := a.Dir(name)
	if _, err := p.r.Stat(dir); err == nil {
		cached, err := p.r.ReadFile(filepath.Join(dir, ".fleet-checksums"))
		if err == nil {
			actual, err := harnessChecksums(p.r.FS(), dir)
			if err == nil && bytes.Equal(actual, cached) {
				if st, err := p.r.Stat(a.Executable(name)); err == nil && executable(st) {
					return nil
				}
			}
		}
		kept, err := u.retainedHarnesses(p)
		if err != nil {
			return err
		}
		if kept[dir] {
			return fmt.Errorf("%w: cached payload %s is damaged and belongs to the current or rollback release", refusal("harness-cache"), dir)
		}
		if err := p.r.RemoveAll(dir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Use a private trusted staging directory. Publish only the complete, verified payload.
	stage, err := os.MkdirTemp(p.stage, "cli-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	payload := filepath.Join(stage, "download")
	newHash := sha256.New
	if a.SHA512 != "" {
		newHash = sha512.New
	}
	if err := u.downloadHarness(a, payload, newHash); err != nil {
		return err
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

	tmp := dir + ".tmp"
	if err := p.r.RemoveAll(tmp); err != nil {
		return err
	}
	defer p.r.RemoveAll(tmp)
	if err := copyTree(files, p.r, tmp); err != nil {
		return err
	}
	if err := chmodReadOnly(p.r, tmp); err != nil {
		return err
	}
	checksums, err := harnessChecksums(p.r.FS(), tmp)
	if err != nil {
		return err
	}
	if err := p.r.WriteFile(filepath.Join(tmp, ".fleet-checksums"), checksums, 0o444); err != nil {
		return err
	}
	return p.r.Rename(tmp, dir)
}

func (u *updater) downloadHarness(a harnessrelease.Asset, path string, newHash func() hash.Hash) error {
	for attempt := 0; ; attempt++ {
		got, err := u.downloadDigest(a.URL, path, newHash())
		if err != nil {
			return err
		}
		if strings.EqualFold(got, a.Digest()) {
			return nil
		}
		if attempt == 1 {
			return fmt.Errorf("%w: got %s, expected %s", refusal("harness-checksum"), got, a.Digest())
		}
	}
}

func executable(st fs.FileInfo) bool {
	return st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

func (u *updater) retainedHarnesses(p *part) (map[string]bool, error) {
	keep := map[string]bool{}
	for _, link := range []string{"current", "previous"} {
		assets, err := harnessrelease.Read(filepath.Join(p.root, link), u.target)
		if err != nil {
			return nil, err
		}
		for name, asset := range assets {
			keep[asset.Dir(name)] = true
		}
	}
	return keep, nil
}

func (u *updater) pruneHarnesses(p *part) {
	keep, err := u.retainedHarnesses(p)
	if err != nil {
		log.Print("prune managed CLIs: ", err)
		return
	}
	for _, name := range managedHarnesses {
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

func harnessChecksums(tree fs.FS, dir string) ([]byte, error) {
	var out bytes.Buffer
	err := fs.WalkDir(tree, dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(path, dir+"/")
		if rel == ".fleet-checksums" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		f, err := tree.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		fmt.Fprintf(&out, "%x %o %q\n", h.Sum(nil), info.Mode().Perm(), rel)
		return nil
	})
	return out.Bytes(), err
}
