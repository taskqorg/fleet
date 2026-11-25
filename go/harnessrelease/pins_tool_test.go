package harnessrelease

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestShippedPins: both pin files of this repository parse for both targets once enabled (fleet-host-updates.md).
func TestShippedPins(t *testing.T) {
	for _, k := range Kinds {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", k.File))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "config"), 0o755)
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		m["enabled"] = true
		enabled, _ := json.Marshal(m)
		os.WriteFile(filepath.Join(dir, "config", k.File), enabled, 0o644)
		for _, target := range []string{"linux-amd64", "darwin-arm64"} {
			assets, err := k.Read(dir, target)
			if err != nil || len(assets) != len(k.Names) {
				t.Fatalf("%s %s: %v %v", k.File, target, err, assets)
			}
			for name, a := range assets {
				if got := filepath.Dir(a.Dir(name)); got != filepath.Join(k.Dir, name) {
					t.Errorf("%s dir %s", name, got)
				}
			}
		}
	}
}
