package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// The same server-side grant applies to every harness, independent of its client permission syntax.
var serenaTools = []string{"initial_instructions", "check_onboarding_performed", "onboarding", "get_symbols_overview", "find_symbol", "find_referencing_symbols", "search_for_pattern"}

// serenaDependencies reads only the fixed named binding in the existing private native-account registry.
func serenaDependencies(registry, account string) (M, error) {
	if account == "" || registry == "" {
		return nil, errors.New("Serena requires a named host account and FLEET_NATIVE_ACCOUNTS binding")
	}
	if err := private(registry); err != nil {
		return nil, errors.New("Serena native-account registry must be a private absolute host-owned file")
	}
	info, err := os.Stat(registry)
	if err != nil || !info.Mode().IsRegular() || owner(info) != os.Geteuid() {
		return nil, errors.New("Serena native-account registry ownership invalid")
	}
	raw, err := os.ReadFile(registry)
	if err != nil {
		return nil, errors.New("Serena native-account registry unavailable")
	}
	accounts, err := dec(string(raw))
	if err != nil {
		return nil, errors.New("Serena native-account registry invalid")
	}
	binding := jObj(accounts, account, "serena")
	if len(binding) != 3 {
		return nil, errors.New("Serena named host binding requires executable, go and gopls paths")
	}
	out := M{}
	for _, entry := range [][2]string{{"serena", "executable"}, {"go", "go"}, {"gopls", "gopls"}} {
		path := jStr(binding, entry[1])
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("Serena binding %s must be an absolute host executable", entry[0])
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("Serena binding %s executable missing", entry[0])
		}
		st, err := os.Stat(resolved)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 || st.Mode().Perm()&0022 != 0 || (owner(st) != os.Geteuid() && owner(st) != 0) {
			return nil, fmt.Errorf("Serena binding %s needs a trusted regular executable without group/other write permission", entry[0])
		}
		out[entry[0]] = resolved
	}
	return out, nil
}

// Private aliases preserve the validated identities and required command names, even after symlink resolution.
func serenaPath(binding M) (string, func(), error) {
	dir, err := os.MkdirTemp("", "fleet-serena-bin-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	for _, name := range []string{"go", "gopls"} {
		if err := os.Symlink(jStr(binding, name), filepath.Join(dir, name)); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH"), cleanup, nil
}

func serenaArguments(root, harness string) ([]string, error) {
	context := map[string]string{"codex": "codex", "claude": "claude-code", "antigravity": "antigravity", "acp": "ide"}[harness]
	if context == "" || !filepath.IsAbs(root) {
		return nil, errors.New("Serena requires an absolute execution root and supported harness mapping")
	}
	return []string{"start-mcp-server", "--context", context, "--project", root, "--open-web-dashboard", "false", "--enable-web-dashboard", "false", "--enable-gui-log-window", "false"}, nil
}

func serenaAllowed(request M, root string) error {
	method := jStr(request, "method")
	if method == "initialize" || method == "notifications/initialized" || method == "ping" || method == "tools/list" {
		return nil
	}
	if method != "tools/call" || !slices.Contains(serenaTools, jStr(request, "params", "name")) {
		return errors.New("Tool is outside selected Serena capability")
	}
	if path := jStr(request, "params", "arguments", "relative_path"); path != "" {
		if filepath.IsAbs(path) {
			return errors.New("Serena path must be relative to execution root")
		}
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			return errors.New("Serena execution root unavailable")
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, path))
		if err != nil {
			return errors.New("Serena path unavailable in execution root")
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("Serena path outside execution root")
		}
	}
	return nil
}

// Reuse execution cleanup's incarnation-bound descendant handling; do not signal an unrelated process group.
func ownSerena(cmd *exec.Cmd) (func() error, error) {
	id, err := identity(cmd.Process.Pid)
	if err != nil {
		return nil, err
	}
	owned := &live{owned: map[int]string{cmd.Process.Pid: id}}
	var once sync.Once
	return func() error { once.Do(func() { err = owned.cleanup() }); return err }, nil
}

// serenaMCP is a narrow local stdio filter, not an arbitrary command or remote MCP proxy.
func serenaMCP(root, harness, registry, account string) (resultErr error) {
	dependencies, err := serenaDependencies(registry, account)
	if err != nil {
		return err
	}
	args, err := serenaArguments(root, harness)
	if err != nil {
		return err
	}
	cmd := exec.Command(jStr(dependencies, "serena"), args...)
	cmd.Dir = root
	for _, key := range []string{"HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "SYSTEMROOT"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	path, cleanup, err := serenaPath(dependencies)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd.Env = append(cmd.Env, "PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return errors.New("Serena executable could not start")
	}
	stop, err := ownSerena(cmd)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, stop())
		stdin.Close()
		cmd.Process.Kill()
		stdout.Close()
		cmd.Wait()
	}()
	return serveSerena(stop, os.Stdin, os.Stdout, stdin, stdout, root)
}

// Observe downstream closure even while a server response is stalled; never replay an uncertain request.
func serveSerena(stop func() error, input io.Reader, output io.Writer, upstream io.Writer, replies io.ReadCloser, root string) error {
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		_, err := io.Copy(writer, input)
		writer.CloseWithError(err)
		stop()
		replies.Close()
	}()
	return filterSerena(reader, output, upstream, replies, root)
}

func filterSerena(input io.Reader, output io.Writer, upstream io.Writer, replies io.Reader, root string) error {
	from := json.NewDecoder(input)
	to := json.NewEncoder(output)
	server := json.NewDecoder(replies)
	send := json.NewEncoder(upstream)
	for {
		var request M
		if err := from.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return errors.New("Invalid Serena MCP request")
		}
		id, hasID := request["id"]
		if err := serenaAllowed(request, root); err != nil {
			if hasID {
				if err := to.Encode(M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32602, "message": err.Error()}}); err != nil {
					return err
				}
			}
			continue
		}
		if err := send.Encode(request); err != nil {
			return errors.New("Serena request delivery failed; outcome unknown")
		}
		if !hasID {
			continue
		}
		for {
			var reply M
			if err := server.Decode(&reply); err != nil {
				return errors.New("Serena response unavailable; outcome unknown")
			}
			if !has(reply, "id") {
				continue
			} // Never forward server logs or unsolicited notifications into model/history.
			if enc(reply["id"]) != enc(id) {
				return errors.New("Serena response identity mismatch")
			}
			if jStr(request, "method") == "tools/list" {
				tools := []any{}
				for _, tool := range jArr(reply, "result", "tools") {
					if slices.Contains(serenaTools, jStr(tool, "name")) {
						tools = append(tools, tool)
					}
				}
				for _, required := range serenaTools {
					if !slices.ContainsFunc(tools, func(tool any) bool { return jStr(tool, "name") == required }) {
						reply = M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32001, "message": "Required selected Serena tool not advertised: " + required}}
						break
					}
				}
				if result := jObj(reply, "result"); result != nil {
					result["tools"] = tools
				}
			}
			if err := to.Encode(reply); err != nil {
				return err
			}
			break
		}
	}
}
