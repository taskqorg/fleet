package contract

import (
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
)

type ContextSourceFile struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
}
type ContextSource struct {
	Repo     string              `json:"repo"`
	Revision string              `json:"revision"`
	Core     []ContextSourceFile `json:"core"`
	Linked   []ContextSourceFile `json:"linked,omitempty"`
}

var sourceRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sourceDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseContextSources validates immutable references, never commands, credentials or caller-selected host paths.
func ParseContextSources(raw []byte) ([]ContextSource, error) {
	var sources []ContextSource
	if err := strict(raw, &sources); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, errors.New("contextSources must be an array")
	}
	seen := map[string]bool{}
	for _, s := range sources {
		if !grantID.MatchString(s.Repo) || seen[s.Repo] || !sourceRevision.MatchString(s.Revision) || len(s.Core) == 0 {
			return nil, errors.New("contextSources requires unique repo, full commit and core files")
		}
		seen[s.Repo] = true
		paths := map[string]bool{}
		for _, f := range append(append([]ContextSourceFile{}, s.Core...), s.Linked...) {
			if f.Path == "" || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || f.Path == "." || f.Path == ".." || strings.HasPrefix(f.Path, "../") || strings.ContainsAny(f.Path, "\\\x00\r\n\t") || paths[f.Path] || !sourceDigest.MatchString(f.Sha256) {
				return nil, errors.New("contextSources requires unique relative files and SHA256 digests")
			}
			paths[f.Path] = true
		}
	}
	return sources, nil
}

func ContextSources(value any) ([]ContextSource, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return ParseContextSources(raw)
}
