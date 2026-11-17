// Package harnessrelease defines the managed CLI files shared by fleet-update and fleet-executor.
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
}

func (a Asset) Digest() string {
	if a.SHA256 != "" {
		return a.SHA256
	}
	return a.SHA512
}

func (a Asset) Dir(name string) string {
	return filepath.Join("harnesses", name, a.Digest())
}

func (a Asset) Executable(name string) string {
	entry := a.Entry
	if entry == "" {
		entry = name
	}
	return filepath.Join(a.Dir(name), entry)
}

// Read returns the managed assets for target, or nil when this release leaves CLI provisioning to the host.
func Read(release, target string) (map[string]Asset, error) {
	data, err := os.ReadFile(filepath.Join(release, "config", "harness-pins.json"))
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
	for _, name := range []string{"codex", "claude", "antigravity"} {
		if err := assets[name].validate(); err != nil {
			return nil, fmt.Errorf("%s %s: %w", target, name, err)
		}
	}
	if len(assets) != 3 {
		return nil, fmt.Errorf("%s: unexpected managed harness", target)
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
	if a.Version == "" || (a.Entry != "" && (!filepath.IsLocal(a.Entry) || filepath.Clean(a.Entry) != a.Entry || a.Entry == ".")) {
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
