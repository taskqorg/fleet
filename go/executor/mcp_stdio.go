package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func trustedMCPExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("MCP executable must be an absolute host-owned path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("MCP executable missing")
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 || st.Mode().Perm()&0022 != 0 || (owner(st) != os.Geteuid() && owner(st) != 0) {
		return "", errors.New("MCP executable must be trusted, regular and not group/other writable")
	}
	return resolved, nil
}

// mcpBinding is one named MCP server of a host account in the native-account registry.
// A list field is nil when absent or not an array, and empty for [].
type mcpBinding struct {
	Transport         string               `json:"transport"`
	Command           string               `json:"command"`
	Args              []jsonValue          `json:"args"`
	Dependencies      map[string]jsonValue `json:"dependencies"` // nil when absent or not an object
	PathPolicy        string               `json:"pathPolicy"`
	OwnedDependencies []jsonValue          `json:"ownedDependencies"`
}

func namedMCPBinding(registry, account, name, root string) (M, error) {
	if err := private(registry); err != nil {
		return nil, errors.New("MCP requires private absolute host native-account registry")
	}
	st, err := os.Stat(registry)
	if err != nil || !st.Mode().IsRegular() || owner(st) != os.Geteuid() {
		return nil, errors.New("MCP registry ownership invalid")
	}
	raw, err := os.ReadFile(registry)
	if err != nil {
		return nil, errors.New("MCP registry unavailable")
	}
	v, err := dec(string(raw))
	if err != nil {
		return nil, errors.New("MCP registry invalid")
	}
	keys, _ := typed[map[string]struct {
		MCPServers map[string]jsonValue `json:"mcpServers"`
	}](v)[account].MCPServers[name].V.(M)
	if len(keys) == 0 {
		return nil, fmt.Errorf("named MCP binding %s missing for host account", name)
	}
	for key := range keys {
		if !slices.Contains([]string{"transport", "command", "args", "dependencies", "pathPolicy", "ownedDependencies"}, key) {
			return nil, errors.New("unsupported MCP binding field")
		}
	}
	binding := typed[mcpBinding](keys)
	if binding.Transport != "stdio" {
		return nil, errors.New("MCP binding transport unsupported; only local stdio is qualified by this adapter")
	}
	command, err := trustedMCPExecutable(binding.Command)
	if err != nil {
		return nil, fmt.Errorf("MCP %s: %w", name, err)
	}
	args := []any{}
	for _, item := range binding.Args {
		text, ok := valStr(item)
		if !ok {
			return nil, errors.New("MCP binding args must be strings")
		}
		if strings.Contains(strings.ReplaceAll(text, "${executionRoot}", ""), "${") {
			return nil, errors.New("MCP binding has unsupported argument substitution")
		}
		if root != "" {
			text = strings.ReplaceAll(text, "${executionRoot}", root)
		}
		args = append(args, text)
	}
	if binding.Args == nil {
		return nil, errors.New("MCP binding args required")
	}
	deps := M{}
	dependencies := binding.Dependencies
	for key, raw := range dependencies {
		if key == "" || !onlyBytes(key, func(b byte) bool { return alnum(b) || b == '_' || b == '-' }) {
			return nil, errors.New("MCP dependency name invalid")
		}
		path, _ := valStr(raw)
		resolved, err := trustedMCPExecutable(path)
		if err != nil {
			return nil, fmt.Errorf("MCP dependency %s: %w", key, err)
		}
		deps[key] = resolved
	}
	if _, in := keys["dependencies"]; in && dependencies == nil {
		return nil, errors.New("MCP dependencies must be named paths")
	}
	policy := binding.PathPolicy
	if policy != "" && policy != "project-relative" {
		return nil, errors.New("MCP path policy unsupported")
	}
	var owned []any
	if binding.OwnedDependencies != nil {
		owned = []any{}
	}
	seen := map[string]bool{}
	for _, item := range binding.OwnedDependencies {
		name, ok := valStr(item)
		if !ok || !has(deps, name) || seen[name] {
			return nil, errors.New("owned MCP dependency must name a unique existing executable")
		}
		seen[name] = true
		owned = append(owned, name)
	}
	if _, in := keys["ownedDependencies"]; in && owned == nil {
		return nil, errors.New("owned MCP dependencies must be a list")
	}
	return M{"command": command, "args": args, "dependencies": deps, "pathPolicy": policy, "ownedDependencies": owned}, nil
}
func mcpChildEnv(path string) []string {
	env := []string{"PATH=" + path}
	for _, name := range []string{"HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "SYSTEMROOT"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}
func mcpPrivatePath(deps M, parent string, owned []any) (string, error) {
	if len(deps) == 0 {
		return os.Getenv("PATH"), nil
	}
	paths := typed[map[string]string](deps)
	dir, err := os.MkdirTemp(parent, "bin-")
	if err != nil {
		return "", err
	}
	for name, path := range paths {
		target := path
		if slices.Contains(owned, any(name)) {
			target = exe()
		}
		if err = os.Symlink(target, filepath.Join(dir, name)); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	if err = os.Chmod(dir, 0555); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH"), nil
}

// mcpLaunch is the part of a wrapped server spec (wrapMCPServers, then the MCP guardian) that starts the server.
type mcpLaunch struct {
	Command      string      `json:"command"`
	TargetSha256 string      `json:"targetSha256"`
	Args         []jsonValue `json:"args"`
	ChildPath    string      `json:"childPath"`
	ChildHome    string      `json:"childHome"`
	Generation   string      `json:"generation"`
	OwnerSocket  string      `json:"ownerSocket"`
}

func mcpBoundCommand(v any) (*exec.Cmd, func(), error) {
	spec := typed[mcpLaunch](v)
	command, err := trustedMCPExecutable(spec.Command)
	if err != nil {
		return nil, nil, err
	}
	if expected := spec.TargetSha256; expected != "" {
		actual, err := mcpFileHash(command)
		if err != nil || actual != expected {
			return nil, nil, errors.New("MCP dependency target changed before execution")
		}
	}
	args := []string{}
	for _, item := range spec.Args {
		s, ok := valStr(item)
		if !ok {
			return nil, nil, errors.New("MCP argument must be a string")
		}
		args = append(args, s)
	}
	cmd := exec.Command(command, args...)
	cmd.Env = mcpChildEnv(spec.ChildPath)
	if home := spec.ChildHome; home != "" {
		st, err := os.Lstat(home)
		if err != nil || !filepath.IsAbs(home) || !st.IsDir() || st.Mode().Perm() != 0700 || owner(st) != os.Geteuid() {
			return nil, nil, errors.New("MCP child HOME must be a private execution-owned directory")
		}
		setEnv(cmd, "HOME", home)
	}

	if generation := spec.Generation; generation != "" {
		cmd.Env = append(cmd.Env, "FLEET_MCP_GENERATION="+generation, "FLEET_MCP_OWNER="+spec.OwnerSocket)
	}
	cmd.Dir, _ = os.Getwd()
	return cmd, func() {}, nil
}
func wrapMCPServers(servers M, socket, root string, receiptDir ...string) (M, error) {
	wrapped := M{}
	for name, raw := range servers {
		// open JSON: the server spec passes on whole to the MCP guardian; only its command and tools are read here.
		server, _ := jClone(raw).(M)
		launch := typed[struct {
			Command string    `json:"command"`
			Tools   jsonValue `json:"tools"`
		}](server)
		if server == nil || launch.Command == "" {
			return nil, errors.New("MCP launch command missing")
		}
		if len(receiptDir) > 0 {
			if err := os.Mkdir(receiptDir[0], 0700); err != nil && !os.IsExist(err) {
				return nil, errors.New("MCP receipt directory unavailable")
			}
			server["receiptPath"] = filepath.Join(receiptDir[0], "mcp-lifecycle-"+sha([]byte(name)))
		}
		// These are already authoritative runtime specs, not commands supplied by an agent profile.
		wrapped[name] = M{"command": exe(), "args": []any{"mcp", "stdio", socket, enc(server), root}, "tools": launch.Tools.V, "env": []any{"HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "PATH"}}
	}
	return wrapped, nil
}

// Binding files are read once during preparation; apply only the execution root to that same snapshot.
func mcpAtRoot(selected M, root string) M {
	// open JSON: each server spec passes on whole; only its args are rewritten.
	servers, _ := jClone(selected).(M) // a copy
	if servers == nil {
		return M{}
	}
	for name, raw := range servers {
		server, _ := raw.(M)
		if name == "design-rules" || server == nil {
			continue
		}
		args := []any{} // an argument that is no string is ""
		for _, arg := range typed[struct {
			Args []string `json:"args"`
		}](server).Args {
			args = append(args, strings.ReplaceAll(arg, "${executionRoot}", root))
		}
		server["args"] = args
	}
	return servers
}
