package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// unpackPayload extracts a component payload (fleet-host-updates.md): a vendor's tar.gz or zip, pinned by digest.
// Unlike a release tarball (unpack) it may hold hard links (pnpm) and relative symbolic links (node's bin/npm), each
// resolving inside dir; nothing else but directories and regular files.
func unpackPayload(archive, dir string, isZip bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if isZip {
		return unpackZip(archive, dir)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := payloadPath(h.Name)
		if err != nil || name == "." {
			if err != nil {
				return err
			}
			continue
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			err = writeFrom(path, tr, fs.FileMode(h.Mode)&0o755|0o644)
		case tar.TypeLink:
			var from string
			if from, err = payloadPath(h.Linkname); err == nil {
				err = copyLinked(filepath.Join(dir, from), path)
			}
		case tar.TypeSymlink:
			err = payloadLink(dir, name, h.Linkname)
		default:
			err = fmt.Errorf("payload entry %q of type %c", h.Name, h.Typeflag)
		}
		if err != nil {
			return err
		}
	}
}

func unpackZip(archive, dir string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		name, err := payloadPath(f.Name)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, name)
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = os.MkdirAll(path, 0o755)
		case mode.IsRegular():
			var r io.ReadCloser
			if r, err = f.Open(); err == nil {
				err = writeFrom(path, r, mode.Perm()&0o755|0o644)
				r.Close()
			}
		default:
			err = fmt.Errorf("payload entry %q of mode %s", f.Name, mode)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// payloadPath: an archive member name as a local path, or an error for one that leaves the payload.
func payloadPath(name string) (string, error) {
	clean := filepath.Clean(strings.TrimPrefix(name, "./"))
	if !filepath.IsLocal(clean) && clean != "." {
		return "", fmt.Errorf("payload path %q leaves the payload", name)
	}
	return clean, nil
}

// payloadLink makes a relative symbolic link that resolves inside dir.
func payloadLink(dir, name, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("payload link %q is absolute", name)
	}
	if _, err := payloadPath(filepath.Join(filepath.Dir(name), target)); err != nil {
		return fmt.Errorf("payload link %q leaves the payload", name)
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, path)
}

func copyLinked(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("payload hard link to %s: not a regular file", from)
	}
	return writeFrom(to, in, st.Mode().Perm())
}
