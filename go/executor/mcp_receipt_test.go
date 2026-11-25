package main

import (
	"os"
	"path/filepath"
	"testing"
)

// R3; risk: diagnostics overwrite data or follow symlinks/traversal/untrusted ancestor directories.
func TestMCPReceiptPathBoundaryAndUniqueInvocation(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(parent, 0700)
	prefix := filepath.Join(parent, "receipt")
	first, err := openMCPReceipt(prefix)
	if err != nil {
		t.Fatal(err)
	}
	first.Write([]byte("first"))
	first.Close()
	second, err := openMCPReceipt(prefix)
	if err != nil {
		t.Fatal(err)
	}
	second.Write([]byte("second"))
	second.Close()
	files, _ := os.ReadDir(parent)
	if len(files) != 2 {
		t.Fatal("invocations reused a path", files)
	}
	seen := map[string]bool{}
	for _, file := range files {
		b, _ := os.ReadFile(filepath.Join(parent, file.Name()))
		seen[string(b)] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatal("overwritten receipt")
	}
	target := filepath.Join(parent, "target")
	os.Mkdir(target, 0700)
	link := filepath.Join(parent, "link")
	os.Symlink(target, link)
	unsafe := filepath.Join(parent, "untrusted")
	os.Mkdir(unsafe, 0777)
	os.Chmod(unsafe, 0777)
	for _, path := range []string{"relative", parent + "/target/../receipt", filepath.Join(link, "receipt"), filepath.Join(unsafe, "receipt")} {
		if f, err := openMCPReceipt(path); err == nil {
			f.Close()
			t.Fatal("unsafe receipt accepted", path)
		}
	}
	os.WriteFile(prefix, []byte("preserve"), 0600)
	if f, err := openMCPReceipt(prefix); err == nil {
		f.Close()
		t.Fatal("existing prefix overwritten")
	}
}
