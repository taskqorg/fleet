package main

// `executor mcp connection <connection> <tools.json>`: MCP stdio server with a granted connection's tools
// (docs/designs/fleet-connections.md §5). Calls go to fleet-gateway: POST <FLEET_GATEWAY_URL>/v1/call/<connection>/<tool>
// with the run's token (FLEET_RUN_TOKEN) as the bearer. The gateway holds the keys and records every call, so this
// client writes no record of its own. A remote host adds the Cloudflare Access service token from FLEET_CF_ACCESS_FILE
// (default ~/.config/fleet/cf-access-machine.env).

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Gateway reasons that leave the provider charge unknown; every other reason is a plain error.
var unknownReasons = map[string]bool{"provider-body": true, "provider-timeout": true, "client-timeout": true, "invalid-response": true}

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

var gatewayClient = &http.Client{Timeout: 60 * time.Second}

// connectionCall is one gateway call: {tool, callId, outcome: ok|error|unknown, error_code, usage, output}.
func connectionCall(connection, tool string, arguments any) M {
	url, token := strings.TrimRight(os.Getenv("FLEET_GATEWAY_URL"), "/"), os.Getenv("FLEET_RUN_TOKEN")
	// An owner connection token lives in a 0600 file (FLEET_TOKEN_FILE), never in the session config.
	if f := os.Getenv("FLEET_TOKEN_FILE"); token == "" && f != "" {
		b, _ := os.ReadFile(f)
		token = strings.TrimSpace(string(b))
	}
	line := M{"tool": tool, "callId": nil, "outcome": "error", "error_code": nil, "usage": nil, "output": nil}
	with := func(kv ...any) M {
		r := M{}
		for k, v := range line {
			r[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			r[kv[i].(string)] = kv[i+1]
		}
		return r
	}
	if url == "" || token == "" {
		return with("error_code", "connection-unavailable")
	}
	req, err := http.NewRequest("POST", url+"/v1/call/"+connection+"/"+tool, bytes.NewReader([]byte(enc(arguments))))
	if err != nil {
		return with("error_code", "unreachable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fleet-connection/1")
	if strings.HasPrefix(url, "https://") {
		for k, v := range accessHeaders() {
			req.Header.Set(k, v)
		}
	}
	r, err := gatewayClient.Do(req)
	if err != nil {
		var ne net.Error
		var dns *net.DNSError
		switch {
		case errors.As(err, &ne) && ne.Timeout():
			return with("outcome", "unknown", "error_code", "client-timeout")
		case errors.Is(err, syscall.ECONNREFUSED) || errors.As(err, &dns):
			// Not connected at all: nothing was sent.
			return with("error_code", "unreachable")
		}
		return with("outcome", "unknown", "error_code", "unreachable")
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return with("outcome", "unknown", "error_code", "connection-lost")
	}
	if id := r.Header.Get("x-fleet-call-id"); id != "" {
		line["callId"] = id
	}
	ok := r.StatusCode >= 200 && r.StatusCode < 300
	output, err := dec(string(body))
	if err != nil {
		output = M{"text": strings.ToValidUTF8(string(body[:min(len(body), 2000)]), "�")}
		if ok {
			return with("outcome", "unknown", "error_code", "invalid-response", "output", output)
		}
	}
	if ok {
		return with("outcome", "ok", "output", output, "usage", jAt(output, "usage"))
	}
	// The gateway names its own refusals in x-fleet-error; a provider reply passed through is http-<status>.
	reason := r.Header.Get("x-fleet-error")
	code := reason
	if reason == "" || regexp.MustCompile(`^provider-\d+$`).MatchString(reason) {
		code = fmt.Sprintf("http-%d", r.StatusCode)
	}
	outcome := "error"
	if r.Header.Get("x-fleet-outcome") == "unknown" || unknownReasons[code] {
		outcome = "unknown"
	}
	return with("outcome", outcome, "error_code", code, "output", output)
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
			line := M{"outcome": "error", "error_code": "tool-not-granted", "output": nil}
			if names[name] && name != "" {
				args := params["arguments"]
				if !truth(args) {
					args = M{}
				}
				line = connectionCall(connection, name, args)
			}
			ok := line["outcome"] == "ok"
			text := enc(line["output"])
			if !ok {
				text = fmt.Sprintf("Call %v: %v", line["outcome"], line["error_code"])
				if truth(line["output"]) {
					text += " " + enc(line["output"])
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
