// Package harnessrelease defines the release-managed components shared by fleet-update and fleet-executor
// (docs/designs/fleet-host-updates.md): agent CLIs in config/harness-pins.json and host tools in config/tool-pins.json.
// Each component is a pinned payload per target, cached by digest under its kind's directory of the releases root.
package harnessrelease

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Asset struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256,omitempty"`
	SHA512  string `json:"sha512,omitempty"`
	Entry   string `json:"entry,omitempty"` // empty for a standalone binary
	kind    string // the cache directory of the component's kind, set by Kind.Read
}

// Kind is one kind of release-managed component: its pin file, cache directory and component names.
type Kind struct {
	Name    string // "cli" or "tool", the prefix of a component name (cli/codex)
	File    string // under the release's config/
	Dir     string // under the releases root
	Names   []string
	Origins map[string]string // per component, the vendor's official download prefix; every pin's URL starts with it
}

var (
	CLIs = Kind{Name: "cli", File: "harness-pins.json", Dir: "harnesses", Names: []string{"codex", "claude", "antigravity"},
		Origins: map[string]string{
			"codex":       "https://github.com/openai/codex/releases/download/",
			"claude":      "https://downloads.claude.ai/claude-code-releases/",
			"antigravity": "https://storage.googleapis.com/antigravity-public/antigravity-cli/",
		}}
	Tools = Kind{Name: "tool", File: "tool-pins.json", Dir: "tools", Names: []string{"node", "pnpm", "gh", "glab"},
		Origins: map[string]string{
			"node": "https://nodejs.org/dist/",
			"pnpm": "https://github.com/pnpm/pnpm/releases/download/",
			"gh":   "https://github.com/cli/cli/releases/download/",
			"glab": "https://gitlab.com/gitlab-org/cli/-/releases/",
		}}
	Kinds = []Kind{CLIs, Tools}
)

// KindNamed: the kind called name ("cli", "tool").
func KindNamed(name string) (Kind, bool) {
	for _, k := range Kinds {
		if k.Name == name {
			return k, true
		}
	}
	return Kind{}, false
}

// Candidate validates one proposed asset of component name (a pin under qualification, fleet-host-updates.md step
// 4): the same checks as a shipped pin, and its URL must come from the component's vendor.
func (k Kind) Candidate(name string, a Asset) (Asset, error) {
	if err := k.checked(name, a); err != nil {
		return Asset{}, err
	}
	a.kind = k.Dir
	return a, nil
}

func (k Kind) checked(name string, a Asset) error {
	origin, ok := k.Origins[name]
	if !ok {
		return fmt.Errorf("unknown %s %s", k.Name, name)
	}
	if err := a.validate(); err != nil {
		return err
	}
	if !strings.HasPrefix(a.URL, origin) {
		return fmt.Errorf("%s/%s URL is not from %s", k.Name, name, origin)
	}
	return nil
}

func (a Asset) Digest() string {
	if a.SHA256 != "" {
		return a.SHA256
	}
	return a.SHA512
}

func (a Asset) Dir(name string) string {
	return filepath.Join(cmp.Or(a.kind, CLIs.Dir), name, a.Digest())
}

func (a Asset) Executable(name string) string {
	entry := a.Entry
	if entry == "" {
		entry = name
	}
	return filepath.Join(a.Dir(name), entry)
}

// Read returns the managed agent CLIs for target, or nil when this release leaves them to the host.
func Read(release, target string) (map[string]Asset, error) { return CLIs.Read(release, target) }

// Read returns the kind's assets for target, or nil when this release leaves the kind to the host (no file, or
// enabled false). Every component of the kind must be pinned for the target.
func (k Kind) Read(release, target string) (map[string]Asset, error) {
	data, err := os.ReadFile(filepath.Join(release, "config", k.File))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Enabled bool                        `json:"enabled"`
		Targets map[string]map[string]Asset `json:"targets"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if !manifest.Enabled {
		return nil, nil
	}
	assets := manifest.Targets[target]
	for _, name := range k.Names {
		if err := k.checked(name, assets[name]); err != nil {
			return nil, fmt.Errorf("%s %s: %w", target, name, err)
		}
	}
	if len(assets) != len(k.Names) {
		return nil, fmt.Errorf("%s: unexpected managed %s", target, k.Name)
	}
	for name, a := range assets {
		a.kind = k.Dir
		assets[name] = a
	}
	return assets, nil
}

func (a Asset) validate() error {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("invalid download URL")
	}
	digest, err := hex.DecodeString(a.Digest())
	if err != nil || (a.SHA256 != "" && (len(digest) != 32 || a.SHA512 != "")) || (a.SHA256 == "" && len(digest) != 64) {
		return errors.New("invalid payload digest")
	}
	if _, stable := versionParts(a.Version); !stable || (a.Entry != "" && (!filepath.IsLocal(a.Entry) || filepath.Clean(a.Entry) != a.Entry || a.Entry == ".")) {
		return errors.New("invalid version or executable path")
	}
	return nil
}

func versionParts(v string) ([3]int, bool) {
	var out [3]int
	fields := strings.Split(v, ".")
	if len(fields) != len(out) {
		return out, false
	}
	for i, field := range fields {
		var err error
		out[i], err = strconv.Atoi(field)
		if err != nil || out[i] < 0 {
			return out, false
		}
	}
	return out, true
}

// ModelError uses only known incompatibilities. Unknown combinations still reach the provider.
func ModelError(name, version, model string) error {
	if name != "codex" || model != "gpt-6.2-sol" {
		return nil
	}
	got, known := versionParts(version)
	bad := [3]int{0, 162, 0}
	if !known {
		return nil
	}
	for i, limit := range bad {
		switch cmp.Compare(got[i], limit) {
		case 1:
			return nil
		case -1:
			return fmt.Errorf("Codex %s cannot run %s", version, model)
		}
	}
	return fmt.Errorf("Codex %s cannot run %s", version, model)
}
