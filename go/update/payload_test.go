package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// TestUnpackPayload: a vendor payload keeps its hard and relative symbolic links (pnpm, node's bin/npm); a link or path
// that leaves the payload is refused; a zip (gh on macOS) unpacks.
func TestUnpackPayload(t *testing.T) {
	tgz := func(entries ...tar.Header) string {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, h := range entries {
			body := ""
			if h.Typeflag == tar.TypeReg {
				body = "#!/bin/sh\n"
				h.Size, h.Mode = int64(len(body)), 0o755
			}
			tw.WriteHeader(&h)
			tw.Write([]byte(body))
		}
		tw.Close()
		gz.Close()
		path := filepath.Join(t.TempDir(), "p.tgz")
		os.WriteFile(path, buf.Bytes(), 0o600)
		return path
	}
	dir := filepath.Join(t.TempDir(), "files")
	err := unpackPayload(tgz(
		tar.Header{Name: "node/lib/npm-cli.js", Typeflag: tar.TypeReg},
		tar.Header{Name: "node/bin/npm", Typeflag: tar.TypeSymlink, Linkname: "../lib/npm-cli.js"},
		tar.Header{Name: "pnpm", Typeflag: tar.TypeReg},
		tar.Header{Name: "dist/pnpm", Typeflag: tar.TypeLink, Linkname: "pnpm"},
	), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "node/bin/npm")); err != nil || target != "../lib/npm-cli.js" {
		t.Fatalf("symlink %q %v", target, err)
	}
	if st, err := os.Stat(filepath.Join(dir, "dist/pnpm")); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("hard link: %v", err)
	}
	for _, h := range []tar.Header{
		{Name: "bin/x", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
		{Name: "bin/x", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "../x", Typeflag: tar.TypeReg},
		{Name: "x", Typeflag: tar.TypeLink, Linkname: "../y"},
	} {
		if err := unpackPayload(tgz(h), filepath.Join(t.TempDir(), "files"), false); err == nil {
			t.Errorf("%s -> %s unpacked", h.Name, h.Linkname)
		}
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "gh_1/bin/gh", Method: zip.Deflate})
	w.Write([]byte("#!/bin/sh\n"))
	zw.Close()
	archive := filepath.Join(t.TempDir(), "p.zip")
	os.WriteFile(archive, buf.Bytes(), 0o600)
	if err := unpackPayload(archive, filepath.Join(t.TempDir(), "files"), true); err != nil {
		t.Fatal(err)
	}
}
