package main

// `executor mcp connection <connection> <spec> [run file]`: MCP stdio server with a granted connection's tools
// (docs/designs/fleet-connections.md §5, §9). Calls go to fleet-gateway: POST <FLEET_GATEWAY_URL>/v1/call/<connection>/<tool>
// with the run's token (FLEET_RUN_TOKEN, or an owner token from FLEET_TOKEN_FILE) as the bearer. The gateway holds the
// keys and records every call, so this client writes no record of its own. A remote host adds the Cloudflare Access
// service token from FLEET_CF_ACCESS_FILE (default ~/.config/fleet/cf-access-machine.env).
//
// A spec tool with file: upload takes {<path fields>, path}: the local file passes the path guard and the client
// tripwire (file_guard.go), then its raw bytes go to the gateway. A tool with file: download writes the attachment to
// FLEET_INPUT_DIR/attachments in a Fleet run, else FLEET_FILES_OUT (default ~/.local/share/fleet/files), and answers its
// path. file and idempotent stay out of tools/list.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// gatewayTarget is this process's gateway, bearer and Access headers.
func gatewayTarget() contract.GatewayTarget {
	t := contract.GatewayTarget{URL: os.Getenv("FLEET_GATEWAY_URL"), Token: os.Getenv("FLEET_RUN_TOKEN")}
	// An owner connection token lives in a 0600 file (FLEET_TOKEN_FILE), never in the session config.
	if f := os.Getenv("FLEET_TOKEN_FILE"); t.Token == "" && f != "" {
		b, _ := os.ReadFile(f)
		t.Token = strings.TrimSpace(string(b))
	}
	if strings.HasPrefix(t.URL, "https://") {
		t.Header = accessHeaders()
	}
	return t
}

func refusedLine(connection, tool, code, reason string) contract.CallLine {
	out, _ := json.Marshal(map[string]string{"reason": reason})
	return contract.CallLine{Connection: connection, Tool: tool, Outcome: contract.CallOutcomeError, ErrorCode: &code, Output: out}
}

// connectionLimits are each connection spec's gateway waits (stallMs, answerMs), set by the server that loaded it.
var connectionLimits = map[string]any{}

// connectionCall is one call of a spec tool ({name, file?, idempotent}) with this process's token: a JSON call, an
// upload or a download by the tool's file mode. Its output is what the agent reads.
func connectionCall(connection string, tool any, arguments any) contract.CallLine {
	name := jStr(tool, "name")
	t := gatewayTarget()
	t.Idempotent = jAt(tool, "idempotent") == true
	// The client outwaits the gateway: its stall wait at least the connection's, its answer wait a minute beyond.
	ms := func(k string) time.Duration {
		n, _ := num(jAt(connectionLimits[connection], k))
		return time.Duration(n) * time.Millisecond
	}
	if s := ms("stallMs"); s > 0 {
		t.Stall = s
	}
	if a := ms("answerMs"); a > 0 {
		t.Answer = a + time.Minute
	}
	switch jStr(tool, "file") {
	case "upload":
		return connectionUpload(t, connection, name, jObj(tool, "inputSchema", "properties"), arguments)
	case "download":
		return connectionDownload(t, connection, tool, arguments)
	}
	return contract.GatewayCall(context.Background(), t, connection, name, []byte(enc(arguments)))
}

func connectionUpload(t contract.GatewayTarget, connection, tool string, properties M, arguments any) contract.CallLine {
	args := map[string]any{}
	for k, v := range jObj(arguments) {
		if _, known := properties[k]; properties != nil && !known {
			// The gateway takes the path fields only; name the extra argument before any file is read.
			return refusedLine(connection, tool, "bad-request", "unknown argument "+k)
		}
		args[k] = v
	}
	path, _ := str(args["path"])
	delete(args, "path")
	if t.URL == "" || t.Token == "" {
		return refusedLine(connection, tool, "connection-unavailable", "no gateway URL or token")
	}
	base := os.Getenv("FLEET_WORK_DIR")
	if base == "" {
		base, _ = os.Getwd()
	}
	f, info, err := openForUpload(path, base)
	if err != nil {
		return refusedLine(connection, tool, "path-refused", err.Error())
	}
	defer f.Close()
	// The tripwire: this caller's own bearer and Access secret never leave in a file.
	patterns := [][]byte{[]byte(t.Token)}
	secret := t.Header["CF-Access-Client-Secret"]
	if t.Header == nil {
		secret = accessHeaders()["CF-Access-Client-Secret"]
	}
	if secret != "" {
		patterns = append(patterns, []byte(secret))
	}
	sum, found, err := scanFile(io.NewSectionReader(f, 0, info.Size()), patterns)
	if err != nil {
		return refusedLine(connection, tool, "path-refused", "the file cannot be read")
	}
	if found {
		return refusedLine(connection, tool, "secret-in-file", "the file holds this client's Fleet token or Access secret; nothing was sent")
	}
	head := make([]byte, 512)
	n, _ := f.ReadAt(head, 0)
	// The opened file's name, in the gateway's file-name rules (no quote, backslash, control or bidi character).
	fileName := strings.ReplaceAll(contract.SafeName(filepath.Base(f.Name())), `"`, "_") // the file opened, not a link to it
	if strings.TrimSpace(fileName) == "" {
		fileName = "file" // the gateway refuses a blank name
	}
	return contract.GatewayUpload(context.Background(), t, connection, tool, args, contract.Upload{File: f, Size: info.Size(),
		Name: fileName, MediaType: mediaTypeOf(fileName, head[:n]), Sha256: sum})
}

// connectionDownload: the file lands in <root>/<required arguments but the last...>/<last>-<name> (for
// attachment_get {issue, attachment}: <root>/TGT-12/85-1234-screen.png).
func connectionDownload(t contract.GatewayTarget, connection string, tool any, arguments any) contract.CallLine {
	name := jStr(tool, "name")
	// The trusted root is the run's input folder (the executor made it) or the host's output folder; the attachments
	// folder below the input folder is the agent's to change, so it is opened without following links like the rest.
	root, folder := os.Getenv("FLEET_FILES_OUT"), []string{}
	if in := os.Getenv("FLEET_INPUT_DIR"); in != "" {
		root, folder = in, []string{"attachments"}
	} else if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".local/share/fleet/files")
	}
	var parts []string
	for _, field := range jArr(tool, "inputSchema", "required") {
		raw := jAt(arguments, jStr(field))
		v, ok := str(raw)
		if n, isNumber := raw.(json.Number); isNumber {
			v, ok = n.String(), true
		}
		if !ok {
			return refusedLine(connection, name, "bad-request", fmt.Sprintf("%s must be a string", jStr(field)))
		}
		parts = append(parts, v)
	}
	d := contract.Download{Root: root, Folder: folder}
	if len(parts) > 0 {
		d.Folder, d.Prefix = append(folder, parts[:len(parts)-1]...), parts[len(parts)-1]
	}
	return contract.GatewayDownload(context.Background(), t, connection, name, []byte(enc(arguments)), d)
}

// modelTool is a spec tool as the model sees it: without the client's file and idempotent flags.
func modelTool(tool any) any {
	m := M{}
	for k, v := range jObj(tool) {
		if k != "file" && k != "idempotent" {
			m[k] = v
		}
	}
	return m
}

// callErrorOutput includes the bytes that may have reached the provider so the caller can verify an uncertain upload.
func callErrorOutput(line contract.CallLine) json.RawMessage {
	if line.Outcome == contract.CallOutcomeUnknown && line.Transfer != nil && line.Transfer.Direction == contract.CallTransferDirectionUpload {
		output, _ := json.Marshal(M{"transfer": line.Transfer, "callId": line.CallId, "reply": line.Output})
		return output
	}
	return line.Output
}

// callText is a call line as the MCP text result.
func callText(line contract.CallLine) (string, bool) {
	ok := line.Outcome == contract.CallOutcomeOk
	text := string(line.Output)
	if text == "" {
		text = "null"
	}
	if !ok {
		text = fmt.Sprintf("Call %s: %s", line.Outcome, *line.ErrorCode)
		if o := string(callErrorOutput(line)); o != "" && o != "null" {
			text += " " + o
		}
	}
	return text, ok
}

func connectionMCP(connection string, spec any, in io.Reader, w io.Writer) error {
	tools := jArr(spec, "tools")
	connectionLimits[connection] = jAt(spec, "limits")
	byName, listed := map[string]any{}, []any{}
	for _, t := range tools {
		byName[jStr(t, "name")] = t
		listed = append(listed, modelTool(t))
	}
	out := bufio.NewWriter(w)
	var wmu sync.Mutex
	write := func(reply M) {
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprintln(out, enc(reply))
		out.Flush()
	}
	// Calls run concurrently: a long file transfer never holds a ping or another call. At end of input the server
	// waits for the calls in flight.
	var calls sync.WaitGroup
	defer calls.Wait()
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
			reply["result"] = M{"tools": listed}
		case "tools/call":
			calls.Add(1)
			go func() {
				defer calls.Done()
				name, _ := str(params["name"])
				code := "tool-not-granted"
				line := contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: &code}
				if tool, ok := byName[name]; ok && name != "" {
					args := params["arguments"]
					if !truth(args) {
						args = M{}
					}
					line = connectionCall(connection, tool, args)
				}
				text, ok := callText(line)
				reply["result"] = M{"content": []any{M{"type": "text", "text": text}}, "isError": !ok}
				write(reply)
			}()
			continue
		default:
			reply["error"] = M{"code": -32601, "message": "method not found"}
		}
		write(reply)
	}
	return sc.Err()
}

// runFileKeys are the keys a run file may set: the token, gateway and Access file, the run's work and input folders
// (the base of relative upload paths and the download folder), and the upload denylist's anchors (file_guard.go).
var runFileKeys = map[string]bool{"FLEET_RUN_TOKEN": true, "FLEET_GATEWAY_URL": true, "FLEET_CF_ACCESS_FILE": true, "FLEET_WORK_DIR": true, "FLEET_INPUT_DIR": true,
	"FLEET_USER_HOME": true, "FLEET_RUN_DENY": true, "FLEET_FILES_ROOTS": true, "FLEET_STALL_MS": true, "FLEET_ANSWER_MS": true}

// loadRunFile reads KEY=value lines from the executor's private run file into this process's environment. Only
// runFileKeys are taken. The file is denylisted for uploads.
func loadRunFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	runFilePath = path
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && runFileKeys[k] {
			os.Setenv(k, v)
		}
	}
	return nil
}

// connectionCLI: `fleet-executor connection call <connection> <tool> [spec file]` reads the arguments as JSON on stdin
// and writes one CallLine as JSON on stdout, with the same environment as the MCP server. With a spec file (a
// connection spec, a list of them or a connections_token_create reply) a file tool uses its file mode; without one the
// tool is called as JSON.
func connectionCLI(args []string) error {
	ok, err := connectionCommand(args, os.Stdin, os.Stdout)
	if err == nil && !ok {
		os.Exit(1) // the call line is printed; the exit status tells a script the call did not succeed
	}
	return err
}

func connectionCommand(args []string, stdin io.Reader, stdout io.Writer) (bool, error) {
	if len(args) < 3 || args[0] != "call" {
		return false, fmt.Errorf("usage: connection call <connection> <tool> [spec file] < args.json")
	}
	connection, name := args[1], args[2]
	tool := any(M{"name": name})
	if len(args) > 3 {
		b, err := os.ReadFile(args[3])
		if err != nil {
			return false, err
		}
		spec, err := dec(string(b))
		if err != nil {
			return false, fmt.Errorf("spec file: %w", err)
		}
		specs := jArr(spec)
		if specs == nil {
			specs = jArr(spec, "connections")
		}
		if specs == nil {
			specs = []any{spec}
		}
		found := false
		for _, s := range specs {
			if n := jStr(s, "name"); n != connection && (n != "" || len(specs) > 1) {
				continue
			}
			for _, t := range jArr(s, "tools") {
				if jStr(t, "name") == name {
					tool, found = t, true
					connectionLimits[connection] = jAt(s, "limits")
				}
			}
		}
		if !found {
			return false, fmt.Errorf("tool %s of %s is not in the spec file", name, connection)
		}
	}
	in, err := io.ReadAll(stdin)
	if err != nil {
		return false, err
	}
	var arguments any = M{}
	if len(bytes.TrimSpace(in)) > 0 {
		if arguments, err = dec(string(in)); err != nil {
			return false, fmt.Errorf("arguments: %w", err)
		}
	}
	line := connectionCall(connection, tool, arguments)
	fmt.Fprintln(stdout, enc(line))
	return line.Outcome == contract.CallOutcomeOk, nil
}
