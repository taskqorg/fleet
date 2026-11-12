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
	accounts, err := dec(string(raw))
	if err != nil {
		return nil, errors.New("MCP registry invalid")
	}
	binding := jObj(accounts, account, "mcpServers", name)
	if len(binding) == 0 {
		return nil, fmt.Errorf("named MCP binding %s missing for host account", name)
	}
	for key := range binding {
		if !slices.Contains([]string{"transport", "command", "args", "dependencies", "pathPolicy", "ownedDependencies"}, key) {
			return nil, errors.New("unsupported MCP binding field")
		}
	}
	if jStr(binding, "transport") != "stdio" {
		return nil, errors.New("MCP binding transport unsupported; only local stdio is qualified by this adapter")
	}
	command, err := trustedMCPExecutable(jStr(binding, "command"))
	if err != nil {
		return nil, fmt.Errorf("MCP %s: %w", name, err)
	}
	args := []any{}
	for _, arg := range jArr(binding, "args") {
		text, ok := arg.(string)
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
	if _, ok := binding["args"].([]any); !ok {
		return nil, errors.New("MCP binding args required")
	}
	deps := M{}
	for key, path := range jObj(binding, "dependencies") {
		if key == "" || !onlyBytes(key, func(b byte) bool { return alnum(b) || b == '_' || b == '-' }) {
			return nil, errors.New("MCP dependency name invalid")
		}
		resolved, err := trustedMCPExecutable(jStr(path))
		if err != nil {
			return nil, fmt.Errorf("MCP dependency %s: %w", key, err)
		}
		deps[key] = resolved
	}
	if has(binding, "dependencies") && jObj(binding, "dependencies") == nil {
		return nil, errors.New("MCP dependencies must be named paths")
	}
	policy := jStr(binding, "pathPolicy")
	if policy != "" && policy != "project-relative" {
		return nil, errors.New("MCP path policy unsupported")
	}
	owned := jArr(binding, "ownedDependencies")
	seen := map[string]bool{}
	for _, value := range owned {
		name, ok := value.(string)
		if !ok || !has(deps, name) || seen[name] {
			return nil, errors.New("owned MCP dependency must name a unique existing executable")
		}
		seen[name] = true
	}
	if has(binding, "ownedDependencies") && owned == nil {
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
	dir, err := os.MkdirTemp(parent, "bin-")
	if err != nil {
		return "", err
	}
	for name, path := range deps {
		target := jStr(path)
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
func mcpBoundCommand(spec any) (*exec.Cmd, func(), error) {
	command, err := trustedMCPExecutable(jStr(spec, "command"))
	if err != nil {
		return nil, nil, err
	}
	if expected := jStr(spec, "targetSha256"); expected != "" {
		actual, err := mcpFileHash(command)
		if err != nil || actual != expected {
			return nil, nil, errors.New("MCP dependency target changed before execution")
		}
	}
	args := []string{}
	for _, v := range jArr(spec, "args") {
		s, ok := v.(string)
		if !ok {
			return nil, nil, errors.New("MCP argument must be a string")
		}
		args = append(args, s)
	}
	cmd := exec.Command(command, args...)
	cmd.Env = mcpChildEnv(jStr(spec, "childPath"))
	if home := jStr(spec, "childHome"); home != "" {
		st, err := os.Lstat(home)
		if err != nil || !filepath.IsAbs(home) || !st.IsDir() || st.Mode().Perm() != 0700 || owner(st) != os.Geteuid() {
			return nil, nil, errors.New("MCP child HOME must be a private execution-owned directory")
		}
		setEnv(cmd, "HOME", home)
	}

	if generation := jStr(spec, "generation"); generation != "" {
		cmd.Env = append(cmd.Env, "FLEET_MCP_GENERATION="+generation, "FLEET_MCP_OWNER="+jStr(spec, "ownerSocket"))
	}
	cmd.Dir, _ = os.Getwd()
	return cmd, func() {}, nil
}
func wrapMCPServers(servers M, socket, root string) (M, error) {
	wrapped := M{}
	for name, raw := range servers {
		server := jObj(jClone(raw))
		if jStr(server, "command") == "" {
			return nil, errors.New("MCP launch command missing")
		}
		// These are already authoritative runtime specs, not commands supplied by an agent profile.
		wrapped[name] = M{"command": exe(), "args": []any{"mcp", "stdio", socket, enc(server), root}, "tools": jAt(server, "tools"), "env": []any{"HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "PATH"}}
	}
	return wrapped, nil
}

// Binding files are read once during preparation; apply only the execution root to that same snapshot.
func mcpAtRoot(selected M, root string) M {
	out := jObj(jClone(selected))
	if out == nil {
		return M{}
	}
	for name, value := range out {
		if name == "design-rules" {
			continue
		}
		server := jObj(value)
		args := []any{}
		for _, arg := range jArr(server, "args") {
			args = append(args, strings.ReplaceAll(jStr(arg), "${executionRoot}", root))
		}
		server["args"] = args
	}
	return out
}
