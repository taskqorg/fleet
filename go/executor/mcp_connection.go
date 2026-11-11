package main

// `executor mcp connection <connection> <tools.json>`: MCP stdio server with a granted connection's tools
// (docs/designs/fleet-connections.md §5). Calls go to fleet-gateway: POST <FLEET_GATEWAY_URL>/v1/call/<connection>/<tool>
// with the run's token (FLEET_RUN_TOKEN) as the bearer. The gateway holds the keys and records every call, so this
// client writes no record of its own. A remote host adds the Cloudflare Access service token from FLEET_CF_ACCESS_FILE
// (default ~/.config/fleet/cf-access-machine.env).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fleet/contract"
)

func accessHeaders() map[string]string {
	path := os.Getenv("FLEET_CF_ACCESS_FILE")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config/fleet/cf-access-machine.env")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	values := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(l, "export ")), "="); ok {
			values[k] = v
		}
	}
	out := map[string]string{}
	for n, h := range map[string]string{"CF_ACCESS_CLIENT_ID": "CF-Access-Client-Id", "CF_ACCESS_CLIENT_SECRET": "CF-Access-Client-Secret"} {
		if v, ok := values[n]; ok {
			out[h] = strings.Trim(v, `'"`)
		}
	}
	return out
}

// connectionCall is one gateway call (contract.GatewayCall) with this run's token.
func connectionCall(connection, tool string, arguments any) contract.CallLine {
	url, token := os.Getenv("FLEET_GATEWAY_URL"), os.Getenv("FLEET_RUN_TOKEN")
	// An owner connection token lives in a 0600 file (FLEET_TOKEN_FILE), never in the session config.
	if f := os.Getenv("FLEET_TOKEN_FILE"); token == "" && f != "" {
		b, _ := os.ReadFile(f)
		token = strings.TrimSpace(string(b))
	}
	var header map[string]string
	if strings.HasPrefix(url, "https://") {
		header = accessHeaders()
	}
	return contract.GatewayCall(context.Background(), url, token, connection, tool, []byte(enc(arguments)), header)
}

func connectionMCP(connection string, spec any, in io.Reader, w io.Writer) error {
	tools, _ := jAt(spec, "tools").([]any)
	if tools == nil {
		tools = []any{}
	}
	names := map[string]bool{}
	for _, t := range tools {
		names[jStr(jAt(t, "name"))] = true
	}
	out := bufio.NewWriter(w)
	sc := bufio.NewScanner(in)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		m, err := dec(sc.Text())
		if err != nil || !has(m, "id") {
			continue
		}
		params, _ := jAt(m, "params").(M)
		reply := M{"jsonrpc": "2.0", "id": jAt(m, "id")}
		switch jAt(m, "method") {
		case "initialize":
			pv := any("2025-06-18")
			if has(params, "protocolVersion") {
				pv = params["protocolVersion"]
			}
			instr := jAt(spec, "instructions")
			if !has(spec, "instructions") {
				instr = ""
			}
			reply["result"] = M{"protocolVersion": pv, "capabilities": M{"tools": M{}}, "serverInfo": M{"name": connection, "version": "1"}, "instructions": instr}
		case "ping":
			reply["result"] = M{}
		case "tools/list":
			reply["result"] = M{"tools": tools}
		case "tools/call":
			name, _ := str(params["name"])
			code := "tool-not-granted"
			line := contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: &code}
			if names[name] && name != "" {
				args := params["arguments"]
				if !truth(args) {
					args = M{}
				}
				line = connectionCall(connection, name, args)
			}
			ok := line.Outcome == contract.CallOutcomeOk
			text := string(line.Output)
			if text == "" {
				text = "null"
			}
			if !ok {
				text = fmt.Sprintf("Call %s: %s", line.Outcome, *line.ErrorCode)
				if o := string(line.Output); o != "" && o != "null" {
					text += " " + o
				}
			}
			reply["result"] = M{"content": []any{M{"type": "text", "text": text}}, "isError": !ok}
		default:
			reply["error"] = M{"code": -32601, "message": "method not found"}
		}
		fmt.Fprintln(out, enc(reply))
		out.Flush()
	}
	return sc.Err()
}

// loadRunFile reads KEY=value lines (FLEET_RUN_TOKEN, FLEET_GATEWAY_URL, FLEET_CF_ACCESS_FILE) from the executor's private
// run file into this process's environment. Only those keys are taken.
func loadRunFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && (k == "FLEET_RUN_TOKEN" || k == "FLEET_GATEWAY_URL" || k == "FLEET_CF_ACCESS_FILE") {
			os.Setenv(k, v)
		}
	}
	return nil
}
