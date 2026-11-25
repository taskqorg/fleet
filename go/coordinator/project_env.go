package main

// execution.environment (docs/configuration.md#project-environment): the project's dev environment, a rootless Podman
// pod the executor runs outside the agent sandbox. Checked on save so a bad spec never reaches a host.

import (
	"bytes"
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"fleet/contract"
)

var devServiceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// checkDevEnvironment: the environment decodes strictly, names either an image or a build file inside the worktree,
// its services have distinct names, and execution.tools carries podman (the placement requirement).
func checkDevEnvironment(raw, tools json.RawMessage) error {
	bad := func(why string) error { return fail(400, "Invalid project setting execution.environment: "+why) }
	var env contract.ProjectDevEnvironment
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&env); err != nil {
		return bad(err.Error())
	}
	if err := env.Validate(); err != nil {
		return bad(err.Error())
	}
	image, build := strings.TrimSpace(deref(env.Image)), strings.TrimSpace(deref(env.Build))
	if (image == "") == (build == "") {
		return bad("set exactly one of image and build")
	}
	if build != "" && !filepath.IsLocal(build) {
		return bad("build must be a path inside the worktree")
	}
	if w := deref(env.Workdir); w != "" && (!path.IsAbs(w) || path.Clean(w) == "/") {
		return bad("workdir must be an absolute path below /")
	}
	seen := map[string]bool{"main": true}
	for _, s := range env.Services {
		if !devServiceName.MatchString(s.Name) || seen[s.Name] {
			return bad("service names are distinct, lowercase, and not main")
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Image) == "" {
			return bad("service " + s.Name + " needs an image")
		}
	}
	names := []string{}
	_ = json.Unmarshal(tools, &names)
	if !slices.Contains(names, "podman") {
		return bad("execution.tools must include podman")
	}
	return nil
}
