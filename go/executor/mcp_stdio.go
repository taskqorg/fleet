package main

import (
	"bytes"
	"encoding/json"
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

// looseStr is a JSON string field read as jStr did: "" when absent or not a string; Set when it was a string.
type looseStr struct {
	S   string
	Set bool
}

func (l *looseStr) UnmarshalJSON(b []byte) error {
	*l = looseStr{}
	if len(b) > 0 && b[0] == '"' && json.Unmarshal(b, &l.S) == nil {
		l.Set = true
	}
	return nil
}

// looseBool is true only for JSON true, as jAt(...) == true read it.
type looseBool bool

func (l *looseBool) UnmarshalJSON(b []byte) error { *l = string(b) == "true"; return nil }

// looseList is a JSON array field read as jArr did: nil Items when absent or not an array; IsArray when it was one.
type looseList struct {
	Items   []json.RawMessage
	IsArray bool
}

func (l *looseList) UnmarshalJSON(b []byte) error {
	*l = looseList{}
	if len(b) > 0 && b[0] == '[' && json.Unmarshal(b, &l.Items) == nil {
		l.IsArray = true
		if l.Items == nil {
			l.Items = []json.RawMessage{}
		}
	}
	return nil
}

// loose is an object field read as the map walk did: T's zero value when absent or not an object of T's shape.
type loose[T any] struct{ V T }

func (l *loose[T]) UnmarshalJSON(b []byte) error {
	var v T
	if json.Unmarshal(b, &v) == nil {
		l.V = v
	}
	return nil
}

// jsonObject reads raw as a JSON object of raw values; nil when it is absent, null or not an object.
func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if b := bytes.TrimSpace(raw); len(b) == 0 || b[0] != '{' || json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// retype decodes an in-memory JSON value into a typed record; false when the value does not fit it.
func retype(v any, out any) bool { return json.Unmarshal([]byte(enc(v)), out) == nil }

// mcpBinding is one named MCP server of a host account in the native-account registry.
type mcpBinding struct {
	Transport         looseStr  `json:"transport"`
	Command           looseStr  `json:"command"`
	Args              looseList `json:"args"`
	PathPolicy        looseStr  `json:"pathPolicy"`
	OwnedDependencies looseList `json:"ownedDependencies"`
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
	if _, err := dec(string(raw)); err != nil {
		return nil, errors.New("MCP registry invalid")
	}
	keys := jsonObject(jsonObject(jsonObject(jsonObject(raw)[account])["mcpServers"])[name])
	if len(keys) == 0 {
		return nil, fmt.Errorf("named MCP binding %s missing for host account", name)
	}
	for key := range keys {
		if !slices.Contains([]string{"transport", "command", "args", "dependencies", "pathPolicy", "ownedDependencies"}, key) {
			return nil, errors.New("unsupported MCP binding field")
		}
	}
	var binding mcpBinding
	b, _ := json.Marshal(keys)
	json.Unmarshal(b, &binding)
	if binding.Transport.S != "stdio" {
		return nil, errors.New("MCP binding transport unsupported; only local stdio is qualified by this adapter")
	}
	command, err := trustedMCPExecutable(binding.Command.S)
	if err != nil {
		return nil, fmt.Errorf("MCP %s: %w", name, err)
	}
	args := []any{}
	for _, item := range binding.Args.Items {
		var arg looseStr
		json.Unmarshal(item, &arg)
		if !arg.Set {
			return nil, errors.New("MCP binding args must be strings")
		}
		text := arg.S
		if strings.Contains(strings.ReplaceAll(text, "${executionRoot}", ""), "${") {
			return nil, errors.New("MCP binding has unsupported argument substitution")
		}
		if root != "" {
			text = strings.ReplaceAll(text, "${executionRoot}", root)
		}
		args = append(args, text)
	}
	if !binding.Args.IsArray {
		return nil, errors.New("MCP binding args required")
	}
	deps := M{}
	dependencies := jsonObject(keys["dependencies"])
	for key, raw := range dependencies {
		if key == "" || !onlyBytes(key, func(b byte) bool { return alnum(b) || b == '_' || b == '-' }) {
			return nil, errors.New("MCP dependency name invalid")
		}
		var path looseStr
		json.Unmarshal(raw, &path)
		resolved, err := trustedMCPExecutable(path.S)
		if err != nil {
			return nil, fmt.Errorf("MCP dependency %s: %w", key, err)
		}
		deps[key] = resolved
	}
	if _, in := keys["dependencies"]; in && dependencies == nil {
		return nil, errors.New("MCP dependencies must be named paths")
	}
	policy := binding.PathPolicy.S
	if policy != "" && policy != "project-relative" {
		return nil, errors.New("MCP path policy unsupported")
	}
	var owned []any
	if binding.OwnedDependencies.IsArray {
		owned = []any{}
	}
	seen := map[string]bool{}
	for _, item := range binding.OwnedDependencies.Items {
		var name looseStr
		json.Unmarshal(item, &name)
		if !name.Set || !has(deps, name.S) || seen[name.S] {
			return nil, errors.New("owned MCP dependency must name a unique existing executable")
		}
		seen[name.S] = true
		owned = append(owned, name.S)
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
	var paths map[string]looseStr
	retype(deps, &paths)
	dir, err := os.MkdirTemp(parent, "bin-")
	if err != nil {
		return "", err
	}
	for name, path := range paths {
		target := path.S
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
	Command      looseStr  `json:"command"`
	TargetSha256 looseStr  `json:"targetSha256"`
	Args         looseList `json:"args"`
	ChildPath    looseStr  `json:"childPath"`
	ChildHome    looseStr  `json:"childHome"`
	Generation   looseStr  `json:"generation"`
	OwnerSocket  looseStr  `json:"ownerSocket"`
}

func mcpBoundCommand(v any) (*exec.Cmd, func(), error) {
	var spec mcpLaunch
	retype(v, &spec)
	command, err := trustedMCPExecutable(spec.Command.S)
	if err != nil {
		return nil, nil, err
	}
	if expected := spec.TargetSha256.S; expected != "" {
		actual, err := mcpFileHash(command)
		if err != nil || actual != expected {
			return nil, nil, errors.New("MCP dependency target changed before execution")
		}
	}
	args := []string{}
	for _, item := range spec.Args.Items {
		var s looseStr
		json.Unmarshal(item, &s)
		if !s.Set {
			return nil, nil, errors.New("MCP argument must be a string")
		}
		args = append(args, s.S)
	}
	cmd := exec.Command(command, args...)
	cmd.Env = mcpChildEnv(spec.ChildPath.S)
	if home := spec.ChildHome.S; home != "" {
		st, err := os.Lstat(home)
		if err != nil || !filepath.IsAbs(home) || !st.IsDir() || st.Mode().Perm() != 0700 || owner(st) != os.Geteuid() {
			return nil, nil, errors.New("MCP child HOME must be a private execution-owned directory")
		}
		setEnv(cmd, "HOME", home)
	}

	if generation := spec.Generation.S; generation != "" {
		cmd.Env = append(cmd.Env, "FLEET_MCP_GENERATION="+generation, "FLEET_MCP_OWNER="+spec.OwnerSocket.S)
	}
	cmd.Dir, _ = os.Getwd()
	return cmd, func() {}, nil
}
func wrapMCPServers(servers M, socket, root string, receiptDir ...string) (M, error) {
	wrapped := M{}
	for name, raw := range servers {
		// open JSON: the server spec passes on whole to the MCP guardian; only its command and tools are read here.
		var server map[string]json.RawMessage
		var launch struct {
			Command looseStr        `json:"command"`
			Tools   json.RawMessage `json:"tools"`
		}
		b := []byte(enc(raw))
		if json.Unmarshal(b, &server) != nil || server == nil || json.Unmarshal(b, &launch) != nil || launch.Command.S == "" {
			return nil, errors.New("MCP launch command missing")
		}
		if len(receiptDir) > 0 {
			if err := os.Mkdir(receiptDir[0], 0700); err != nil && !os.IsExist(err) {
				return nil, errors.New("MCP receipt directory unavailable")
			}
			server["receiptPath"] = json.RawMessage(enc(filepath.Join(receiptDir[0], "mcp-lifecycle-"+sha([]byte(name)))))
		}
		// These are already authoritative runtime specs, not commands supplied by an agent profile.
		wrapped[name] = M{"command": exe(), "args": []any{"mcp", "stdio", socket, enc(server), root}, "tools": decRaw(launch.Tools), "env": []any{"HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "PATH"}}
	}
	return wrapped, nil
}

// Binding files are read once during preparation; apply only the execution root to that same snapshot.
func mcpAtRoot(selected M, root string) M {
	// open JSON: each server spec passes on whole; only its args are rewritten.
	var servers map[string]json.RawMessage
	if json.Unmarshal([]byte(enc(selected)), &servers) != nil || servers == nil {
		return M{}
	}
	for name, raw := range servers {
		if name == "design-rules" {
			continue
		}
		server := jsonObject(raw)
		if server == nil {
			continue
		}
		var spec struct {
			Args looseList `json:"args"`
		}
		json.Unmarshal(raw, &spec)
		args := []any{}
		for _, item := range spec.Args.Items {
			var arg looseStr
			json.Unmarshal(item, &arg)
			args = append(args, strings.ReplaceAll(arg.S, "${executionRoot}", root))
		}
		server["args"] = json.RawMessage(enc(args))
		servers[name] = json.RawMessage(enc(server))
	}
	return decM(enc(servers))
}
