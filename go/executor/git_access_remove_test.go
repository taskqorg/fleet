package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: an archived project's deploy key leaves the host (connection-remove).
// Risk: a crafted connection name deletes outside FLEET_CONNECTIONS_DIR.
func TestRemoveConnection(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "conns")
	os.MkdirAll(filepath.Join(dir, "k1"), 0o700)
	os.WriteFile(filepath.Join(dir, "k1", "id_ed25519"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "outside"), []byte("x"), 0o600)
	for _, bad := range []string{"", ".", "..", "../outside", "a/b"} {
		if removeConnection(dir, bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
	if !removeConnection(dir, "k1") || isFile(filepath.Join(dir, "k1", "id_ed25519")) || !isFile(filepath.Join(root, "outside")) {
		t.Fatal("k1 not removed or outside touched")
	}
	if !removeConnection(dir, "gone") {
		t.Fatal("absent directory is removed")
	}
}
