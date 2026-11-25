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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fleet/contract"
	"fleet/errtrack"
	"fleet/mcpserve"
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

// connectionTool: a tool of a connection spec ({name, file?, idempotent, inputSchema, ...}) as the client reads it; the
// model sees its other keys as written (modelTool).
type connectionTool struct {
	Name        string `json:"name"`
	File        string `json:"file"`
	Idempotent  bool   `json:"idempotent"`
	InputSchema struct {
		Properties map[string]jsonValue `json:"properties"` // nil when it is no object
		Required   []string             `json:"required"`
	} `json:"inputSchema"`
}

// connectionSpec: a connection spec as a server or the call command loads it.
type connectionSpec struct {
	Name         string      `json:"name"`
	Tools        []jsonValue `json:"tools"`
	Limits       jsonValue   `json:"limits"`
	Instructions string      `json:"instructions"`
}

// connectionCall is one call of a spec tool ({name, file?, idempotent}) with this process's token: a JSON call, an
// upload or a download by the tool's file mode. Its output is what the agent reads.
func connectionCall(connection string, spec any, arguments any) contract.CallLine {
	tool := typed[connectionTool](spec)
	name := tool.Name
	t := gatewayTarget()
	t.Idempotent = tool.Idempotent
	if t.Header == nil {
		t.Header = map[string]string{}
	}
	t.Header["X-Fleet-File-Client"] = "1"
	// The client outwaits the gateway: its stall wait at least the connection's, its answer wait a minute beyond.
	limits := typed[contract.GatewayLimits](connectionLimits[connection])
	ms := func(n *int64) time.Duration {
		if n == nil {
			return 0
		}
		return time.Duration(*n) * time.Millisecond
	}
	if s := ms(limits.StallMs); s > 0 {
		t.Stall = s
	}
	if a := ms(limits.AnswerMs); a > 0 {
		t.Answer = a + time.Minute
	}
	switch tool.File {
	case "upload":
		return connectionUpload(t, connection, name, tool.InputSchema.Properties, arguments)
	case "download":
		return connectionDownload(t, connection, tool, arguments)
	}
	return contract.GatewayCall(context.Background(), t, connection, name, []byte(enc(arguments)))
}

func connectionUpload(t contract.GatewayTarget, connection, tool string, properties map[string]jsonValue, arguments any) contract.CallLine {
	given, _ := jClone(arguments).(M) // the agent's arguments, passed to the gateway as they came
	args := map[string]any{}
	for k, v := range given {
		if _, known := properties[k]; properties != nil && !known {
			// The gateway takes the path fields only; name the extra argument before any file is read.
			return refusedLine(connection, tool, "bad-request", "unknown argument "+k)
		}
		args[k] = v
	}
	path, _ := given["path"].(string)
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
	if secret == "" {
		secret = accessHeaders()["CF-Access-Client-Secret"]
	}
	if secret != "" {
		patterns = append(patterns, []byte(secret))
	}
	// The opened file's name, in the gateway's file-name rules (no quote, backslash, control or bidi character).
	fileName := strings.ReplaceAll(contract.SafeName(filepath.Base(f.Name())), `"`, "_") // the file opened, not a link to it
	if strings.TrimSpace(fileName) == "" {
		fileName = "file" // the gateway refuses a blank name
	}
	for _, pattern := range patterns {
		if bytes.Contains([]byte(fileName), pattern) {
			return refusedLine(connection, tool, "secret-in-file", "the upload name holds this client's Fleet token or Access secret; nothing was sent")
		}
	}
	staged, err := os.CreateTemp("", "fleet-upload-*")
	if err != nil {
		return refusedLine(connection, tool, "staging-failed", "cannot create a private upload copy")
	}
	defer staged.Close()
	if err := os.Remove(staged.Name()); err != nil {
		return refusedLine(connection, tool, "staging-failed", "cannot keep the upload copy private")
	}
	sum, found, err := scanFile(io.TeeReader(io.NewSectionReader(f, 0, info.Size()), staged), patterns)
	if err != nil {
		return refusedLine(connection, tool, "path-refused", "the file cannot be read")
	}
	if found {
		return refusedLine(connection, tool, "secret-in-file", "the file holds this client's Fleet token or Access secret; nothing was sent")
	}
	head := make([]byte, 512)
	size, err := staged.Seek(0, io.SeekCurrent)
	if err != nil || size != info.Size() {
		return refusedLine(connection, tool, "staging-failed", "the file size changed while preparing the upload")
	}
	n, _ := staged.ReadAt(head, 0)
	return contract.GatewayUpload(context.Background(), t, connection, tool, args, contract.Upload{File: staged, Size: size,
		Name: fileName, MediaType: mediaTypeOf(fileName, head[:n]), Sha256: sum})
}

// connectionDownload: the file lands in <root>/<required arguments but the last...>/<last>-<name> (for
// attachment_get {issue, attachment}: <root>/TGT-12/85-1234-screen.png).
func connectionDownload(t contract.GatewayTarget, connection string, tool connectionTool, arguments any) contract.CallLine {
	name := tool.Name
	// The trusted root is the run's input folder (the executor made it) or the host's output folder; the attachments
	// folder below the input folder is the agent's to change, so it is opened without following links like the rest.
	root, folder := os.Getenv("FLEET_FILES_OUT"), []string{}
	if in := os.Getenv("FLEET_INPUT_DIR"); in != "" {
		root, folder = in, []string{"attachments"}
	} else if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".local/share/fleet/files")
	}
	given, _ := jClone(arguments).(M)
	var parts []string
	for _, field := range tool.InputSchema.Required {
		var v string
		switch x := given[field].(type) {
		case string:
			v = x
		case json.Number:
			if _, err := x.Float64(); err != nil {
				return refusedLine(connection, name, "bad-request", fmt.Sprintf("%s must be a string", field))
			}
			v = x.String() // a number as written
		default:
			return refusedLine(connection, name, "bad-request", fmt.Sprintf("%s must be a string", field))
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
	keys, _ := jClone(tool).(M)
	m := M{}
	for k, v := range keys {
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

func connectionMCP(connection string, specValue any, in io.Reader, w io.Writer) error {
	exitWithOwner()
	spec := typed[connectionSpec](specValue)
	connectionLimits[connection] = spec.Limits.V
	byName, listed := map[string]any{}, []any{}
	for _, raw := range spec.Tools {
		t := raw.V
		byName[typed[connectionTool](t).Name] = t
		listed = append(listed, modelTool(t))
	}
	// Calls run concurrently: a long file transfer never holds a ping or another call.
	return mcpserve.Stdio{Name: connection, Version: "1", Instructions: spec.Instructions, Concurrent: true,
		Tools: func() any { return listed },
		Call: func(name string, arguments json.RawMessage) (any, error) {
			code := "tool-not-granted"
			line := contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: &code}
			if tool, ok := byName[name]; ok && name != "" {
				args := decRaw(arguments)
				if !truth(args) {
					args = M{}
				}
				line = connectionCall(connection, tool, args)
			}
			text, ok := callText(line)
			return M{"content": []any{M{"type": "text", "text": text}}, "isError": !ok}, nil
		}}.Serve(in, w)
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
		file, err := dec(string(b)) // a list of specs, a reply with connections, or one spec
		if err != nil {
			return false, fmt.Errorf("spec file: %w", err)
		}
		specs, isList := file.([]any)
		if !isList {
			reply := typed[struct {
				Connections jsonValue `json:"connections"`
			}](file)
			if specs, isList = reply.Connections.V.([]any); !isList {
				specs = []any{file}
			}
		}
		found := false
		for _, raw := range specs {
			s := typed[connectionSpec](raw)
			if n := s.Name; n != connection && (n != "" || len(specs) > 1) {
				continue
			}
			for _, t := range s.Tools {
				if typed[connectionTool](t).Name == name {
					tool, found = t.V, true
					connectionLimits[connection] = s.Limits.V
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

// exitWithOwner: a connection server outlives nothing it serves. Once a minute it exits when its parent changed (the
// harness that started it is gone and it was reparented) or its executable was removed (its release was replaced)
// (docs/designs/fleet-host-cleanup.md#4-mcp-connection-lifetime).
func exitWithOwner() {
	parent, path := os.Getppid(), exe()
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // through releases/current: the release this server runs from
	}
	errtrack.Go("connection.owner", func() {
		for {
			time.Sleep(time.Minute)
			if _, err := os.Stat(path); os.Getppid() != parent || err != nil {
				os.Exit(0)
			}
		}
	})
}
