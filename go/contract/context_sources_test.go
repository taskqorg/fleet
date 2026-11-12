package contract

import (
	"strings"
	"testing"
)

// R1, R3; risk: source configuration becomes an arbitrary path/command or silently accepts malformed hashes.
func TestContextSourcesStrict(t *testing.T) {
	good := `[{"repo":"specq","revision":"` + strings.Repeat("a", 40) + `","core":[{"path":"engineering/principles.md","sha256":"` + strings.Repeat("b", 64) + `"}]}]`
	if _, err := ParseContextSources([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"null", strings.Replace(good, "principles.md", "../private", 1), strings.Replace(good, `"core":`, `"command":"echo","core":`, 1), strings.Replace(good, strings.Repeat("a", 40), "main", 1), strings.Replace(good, strings.Repeat("b", 64), "wrong", 1)} {
		if _, err := ParseContextSources([]byte(bad)); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
