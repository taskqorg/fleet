// Local agents (docs/designs/fleet-connections.md#13-local-agents): `fleet-executor local-agent <harness> <token>`
// connects a person's own Claude Code, Codex or Antigravity on this machine to the connections their token names. It
// saves a new token from standard input, reads the token's specs from the gateway, registers one
// `fleet-executor mcp connection <name>` server per connection in the harness's own settings (its CLI, or Antigravity's
// settings file), removes the Fleet servers of connections the token no longer names, and makes one read call per
// connection so the report shows it working. Settings that are not Fleet servers stay as they are.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"fleet/contract"
)

const tokenSpecsPath = "/v1/call/-/connections"

var localAgentToken = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// fetchSpecs: the bearer's connection specs, from the gateway.
func fetchSpecs(ctx context.Context, t contract.GatewayTarget) ([]any, error) {
	if t.URL == "" || t.Token == "" {
		return nil, errors.New("FLEET_GATEWAY_URL and a token (FLEET_TOKEN_FILE or FLEET_RUN_TOKEN) are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(t.URL, "/")+tokenSpecsPath, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.Header {
		req.Header.Set(k, v)
	}
	resp, err := httpClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gateway answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Connections []any `json:"connections"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Connections, nil
}

// specOf: connection name's spec among the bearer's.
func specOf(ctx context.Context, name string) (any, error) {
	specs, err := fetchSpecs(ctx, gatewayTarget())
	if err != nil {
		return nil, err
	}
	for _, s := range specs {
		if typed[connectionSpec](s).Name == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("the token does not name connection %s", name)
}

func localAgentCLI(args []string) error {
	if len(args) != 2 || !slices.Contains([]string{"claude", "codex", "antigravity"}, args[0]) || !localAgentToken.MatchString(args[1]) {
		return errors.New("usage: local-agent <claude|codex|antigravity> <token name> [< token]")
	}
	return localAgent(context.Background(), args[0], args[1], os.Stdin, os.Stdout)
}

func localAgent(ctx context.Context, harness, name string, stdin io.Reader, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "fleet", "tokens")
	file := filepath.Join(dir, name)
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 || stdin != os.Stdin {
		value, _ := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
		if value = strings.TrimSpace(value); value != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(file, []byte(value+"\n"), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(out, "token saved: %s\n", file)
		}
	}
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("%s is missing: copy the command from System › Access › Local agents again", file)
	}
	gateway := os.Getenv("FLEET_GATEWAY_URL")
	if gateway == "" {
		gateway = "https://fleet.ufobe.com"
	}
	os.Setenv("FLEET_GATEWAY_URL", gateway)
	os.Setenv("FLEET_TOKEN_FILE", file)
	specs, err := fetchSpecs(ctx, gatewayTarget())
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// The release link, not the versioned file it points at, so a later release serves the same settings.
	if link := filepath.Join(home, "releases", "current", "fleet-executor"); fileExists(link) {
		exe = link
	}
	env := map[string]string{"FLEET_GATEWAY_URL": gateway, "FLEET_TOKEN_FILE": file}
	names := []string{}
	for _, s := range specs {
		names = append(names, typed[connectionSpec](s).Name)
	}
	sort.Strings(names)
	if err := registerServers(harness, home, exe, env, names, out); err != nil {
		return err
	}
	if err := codexApprovals(harness, home, specs); err != nil {
		return err
	}
	// One read per connection: a tool without required arguments that only reads.
	for _, s := range specs {
		spec := typed[connectionSpec](s)
		tool := probeTool(s)
		if tool == nil {
			fmt.Fprintf(out, "%s: no read without arguments; its first real call shows in the report\n", spec.Name)
			continue
		}
		line := connectionCall(spec.Name, tool, map[string]any{})
		fmt.Fprintf(out, "%s %s: %s\n", spec.Name, typed[connectionTool](tool).Name, line.Outcome)
	}
	fmt.Fprintf(out, "%s on this machine now has %d Fleet connections: %s. A new session lists them.\n", harness, len(names), strings.Join(names, ", "))
	return nil
}

// probeTool: a connection's idempotent tool whose input schema requires nothing, preferring one named like a listing.
func probeTool(spec any) any {
	var best any
	for _, t := range typed[struct {
		Tools []jsonValue `json:"tools"`
	}](spec).Tools {
		ts := typed[struct {
			Name        string `json:"name"`
			Idempotent  bool   `json:"idempotent"`
			File        any    `json:"file"`
			InputSchema struct {
				Required []string `json:"required"`
			} `json:"inputSchema"`
		}](t.V)
		if !ts.Idempotent || ts.File != nil || len(ts.InputSchema.Required) > 0 {
			continue
		}
		if best == nil || ts.Name == "projects" || ts.Name == "sql_schema" || ts.Name == "list_models" {
			best = t.V
		}
	}
	return best
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func registerServers(harness, home, exe string, env map[string]string, names []string, out io.Writer) error {
	switch harness {
	case "antigravity":
		return antigravityServers(filepath.Join(home, ".gemini", "antigravity", "mcp_config.json"), exe, env, names, out)
	case "claude":
		current := claudeFleetServers(home)
		for _, n := range names {
			cfg, _ := json.Marshal(map[string]any{"type": "stdio", "command": exe, "args": []string{"mcp", "connection", n}, "env": env})
			_ = exec.Command("claude", "mcp", "remove", "-s", "user", n).Run() // a former entry of the same name (an old spec copy)
			if b, err := exec.Command("claude", "mcp", "add-json", "-s", "user", n, string(cfg)).CombinedOutput(); err != nil {
				return fmt.Errorf("claude mcp add-json %s: %v: %s", n, err, bytes.TrimSpace(b))
			}
			fmt.Fprintf(out, "%s: registered in Claude Code\n", n)
		}
		for _, n := range current {
			if !slices.Contains(names, n) {
				_ = exec.Command("claude", "mcp", "remove", "-s", "user", n).Run()
				fmt.Fprintf(out, "%s: removed from Claude Code (the token no longer names it)\n", n)
			}
		}
	case "codex":
		current := codexFleetServers()
		for _, n := range names {
			_ = exec.Command("codex", "mcp", "remove", n).Run()
			args := []string{"mcp", "add", n}
			for _, k := range []string{"FLEET_GATEWAY_URL", "FLEET_TOKEN_FILE"} {
				args = append(args, "--env", k+"="+env[k])
			}
			args = append(args, "--", exe, "mcp", "connection", n)
			if b, err := exec.Command("codex", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("codex mcp add %s: %v: %s", n, err, bytes.TrimSpace(b))
			}
			fmt.Fprintf(out, "%s: registered in Codex\n", n)
		}
		for _, n := range current {
			if !slices.Contains(names, n) {
				_ = exec.Command("codex", "mcp", "remove", n).Run()
				fmt.Fprintf(out, "%s: removed from Codex (the token no longer names it)\n", n)
			}
		}
	}
	return nil
}

// codexApprovals: Codex asks before every MCP tool call unless the tool is approved; a Fleet tool is approved as Fleet
// runs approve it (harness_codex.go), by tables appended to config.toml after `codex mcp add` wrote the server.
func codexApprovals(harness, home string, specs []any) error {
	if harness != "codex" {
		return nil
	}
	path := filepath.Join(home, ".codex", "config.toml")
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var add strings.Builder
	for _, s := range specs {
		spec := typed[connectionSpec](s)
		for _, t := range spec.Tools {
			tn := typed[connectionTool](t).Name
			head := fmt.Sprintf("[mcp_servers.%s.tools.%s]", spec.Name, tn)
			if !bytes.Contains(old, []byte(head)) {
				fmt.Fprintf(&add, "\n%s\napproval_mode = \"approve\"\n", head)
			}
		}
	}
	if add.Len() == 0 {
		return nil
	}
	return os.WriteFile(path, append(old, add.String()...), 0o600)
}

// claudeFleetServers: the user-scope servers of ~/.claude.json that run fleet-executor mcp connection.
func claudeFleetServers(home string) []string {
	var cfg struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	_ = json.Unmarshal(b, &cfg)
	out := []string{}
	for n, s := range cfg.McpServers {
		if strings.HasSuffix(s.Command, "fleet-executor") && len(s.Args) >= 3 && s.Args[0] == "mcp" && s.Args[1] == "connection" {
			out = append(out, n)
		}
	}
	return out
}

// codexFleetServers: Codex's servers that run fleet-executor mcp connection (codex mcp list --json).
func codexFleetServers() []string {
	b, err := exec.Command("codex", "mcp", "list", "--json").Output()
	if err != nil {
		return nil
	}
	var list []struct {
		Name      string `json:"name"`
		Transport struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"transport"`
	}
	_ = json.Unmarshal(b, &list)
	out := []string{}
	for _, s := range list {
		if strings.HasSuffix(s.Transport.Command, "fleet-executor") && len(s.Transport.Args) >= 3 && s.Transport.Args[0] == "mcp" && s.Transport.Args[1] == "connection" {
			out = append(out, s.Name)
		}
	}
	return out
}

// antigravityServers rewrites the Fleet entries of Antigravity's MCP settings file, keeping every other entry and key.
func antigravityServers(path, exe string, env map[string]string, names []string, out io.Writer) error {
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s is not JSON: %w", path, err)
		}
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	for n, v := range servers {
		s, _ := v.(map[string]any)
		cmd, _ := s["command"].(string)
		raw, _ := json.Marshal(s["args"])
		var args []string
		_ = json.Unmarshal(raw, &args)
		if strings.HasSuffix(cmd, "fleet-executor") && len(args) >= 3 && args[0] == "mcp" && args[1] == "connection" && !slices.Contains(names, n) {
			delete(servers, n)
			fmt.Fprintf(out, "%s: removed from Antigravity (the token no longer names it)\n", n)
		}
	}
	for _, n := range names {
		servers[n] = map[string]any{"command": exe, "args": []string{"mcp", "connection", n}, "env": env}
		fmt.Fprintf(out, "%s: registered in Antigravity\n", n)
	}
	doc["mcpServers"] = servers
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".before-fleet", old, 0o600) // the file as it was, once per run
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
