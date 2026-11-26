package main

import (
	"bytes"
	"context"
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
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// kindLabel: how errors and logs name a kind ("managed CLI", "managed tool"); the coordinator asks on that prefix.
func kindLabel(k harnessrelease.Kind) string {
	return "managed " + map[string]string{"cli": "CLI", "tool": "tool"}[k.Name]
}

// prepareHarnesses installs the verified payloads of every managed kind (agent CLIs, host tools) without disturbing
// the running release (docs/designs/fleet-host-updates.md, step 8).
func (u *updater) prepareHarnesses(p *part, release string) error {
	for _, k := range harnessrelease.Kinds {
		assets, err := k.Read(release, u.target)
		if err != nil {
			return fmt.Errorf("%w: %s manifest: %v", refusal("harness-manifest"), kindLabel(k), err)
		}
		// A tool's --version may need an earlier tool of its kind (playwright runs on node): each prepared tool's
		// directory goes first on the PATH of the later version runs, as the executor puts it on every run's PATH.
		var path []string
		for _, name := range k.Names {
			a, ok := assets[name]
			if !ok {
				continue
			}
			if err := u.prepareHarness(p, name, a, path); err != nil {
				var invalid refusal
				if errors.As(err, &invalid) {
					return fmt.Errorf("%w: %v", refusal(fmt.Sprintf("%s: %s/%s %s", invalid, k.Name, name, a.Version)), err)
				}
				return fmt.Errorf("%s %s %s: %w", kindLabel(k), name, a.Version, err)
			}
			if k.Name == harnessrelease.Tools.Name {
				path = append(path, filepath.Join(p.root, filepath.Dir(a.Executable(name))))
			}
		}
	}
	return nil
}

func (u *updater) prepareHarness(p *part, name string, a harnessrelease.Asset, path []string) error {
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
		if err := unpackPayload(payload, files, strings.HasSuffix(a.URL, ".zip")); err != nil {
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
	// Run the new component here before any switch (fleet-host-updates.md, step 8): a payload this host cannot run (its
	// libc, its macOS) or that reports another version refuses the release, and the host stays on its current one.
	// Root (PDS18) does not run vendor code; its target, linux-amd64, is qualified offline before the merge.
	if os.Geteuid() != 0 {
		if err := harnessVersionRuns(filepath.Join(files, entry), stage, a.Version, path); err != nil {
			return err
		}
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

// harnessVersionRuns: `<exe> --version` with a private home and no account names version. A wrong answer or a failed
// start refuses; a timeout is transient (a busy host), so the next run tries again. path goes first on its PATH.
func harnessVersionRuns(exe, stage, version string, path []string) error {
	home, err := os.MkdirTemp(stage, "home-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(context.Background(), harnessVersionWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.Env = []string{"PATH=" + strings.Join(append(path, os.Getenv("PATH")), string(os.PathListSeparator)), "HOME=" + home, "TMPDIR=" + home, "DISABLE_AUTOUPDATER=1"}
	cmd.Dir = home
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("%s --version did not answer within %s", filepath.Base(exe), harnessVersionWait)
	}
	if err != nil || !strings.Contains(string(out), version) {
		answer := strings.TrimSpace(string(out))
		if len(answer) > 200 {
			answer = answer[:200]
		}
		return fmt.Errorf("%w: %s --version: %v %q", refusal("harness-version"), filepath.Base(exe), err, answer)
	}
	return nil
}

var harnessVersionWait = 30 * time.Second

func executable(st fs.FileInfo) bool {
	return st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

func (u *updater) retainedHarnesses(p *part) (map[string]bool, error) {
	keep := map[string]bool{}
	for _, link := range []string{"current", "previous"} {
		for _, k := range harnessrelease.Kinds {
			assets, err := k.Read(filepath.Join(p.root, link), u.target)
			if err != nil {
				return nil, err
			}
			for name, asset := range assets {
				keep[asset.Dir(name)] = true
			}
		}
	}
	return keep, nil
}

func (u *updater) pruneHarnesses(p *part) {
	keep, err := u.retainedHarnesses(p)
	if err != nil {
		log.Print("prune managed components: ", err)
		return
	}
	for _, k := range harnessrelease.Kinds {
		for _, name := range k.Names {
			parent := filepath.Join(k.Dir, name)
			entries, _ := fs.ReadDir(p.r.FS(), parent)
			for _, entry := range entries {
				path := filepath.Join(parent, entry.Name())
				if !keep[path] {
					if err := p.r.RemoveAll(path); err != nil {
						log.Printf("prune %s %s: %v", kindLabel(k), path, err)
					}
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
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := fs.ReadLink(tree, path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&out, "link %q %q\n", target, rel)
			return nil
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
