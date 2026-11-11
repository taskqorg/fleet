package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

func mcpAllowed(request M, root string, tools []any, policy string) error {
	method := jStr(request, "method")
	if method == "initialize" || method == "notifications/initialized" || method == "ping" || method == "tools/list" {
		return nil
	}
	if method != "tools/call" || !slices.Contains(tools, any(jStr(request, "params", "name"))) {
		return errors.New("Tool is outside selected MCP capability")
	}
	if path := jStr(request, "params", "arguments", "relative_path"); path != "" && policy == "project-relative" {
		if filepath.IsAbs(path) {
			return errors.New("MCP path must be relative to execution root")
		}
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			return errors.New("Serena execution root unavailable")
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, path))
		if err != nil {
			return errors.New("MCP path unavailable in execution root")
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("MCP path outside execution root")
		}
	}
	return nil
}

func filterMCP(input io.Reader, output io.Writer, upstream io.Writer, replies io.Reader, root string, tools []any, policy string) error {
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
			return errors.New("Invalid MCP request")
		}
		id, hasID := request["id"]
		if err := mcpAllowed(request, root, tools, policy); err != nil {
			if hasID {
				if err := to.Encode(M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32602, "message": err.Error()}}); err != nil {
					return err
				}
			}
			continue
		}
		if err := send.Encode(request); err != nil {
			return errors.New("MCP request delivery failed; outcome unknown")
		}
		if !hasID {
			continue
		}
		for {
			var reply M
			if err := server.Decode(&reply); err != nil {
				return errors.New("MCP response unavailable; outcome unknown")
			}
			if !has(reply, "id") {
				continue
			} // Never forward server logs or unsolicited notifications into model/history.
			if enc(reply["id"]) != enc(id) {
				return errors.New("MCP response identity mismatch")
			}
			if jStr(request, "method") == "tools/list" {
				advertised := []any{}
				for _, tool := range jArr(reply, "result", "tools") {
					if slices.Contains(tools, any(jStr(tool, "name"))) {
						advertised = append(advertised, tool)
					}
				}
				for _, required := range tools {
					if !slices.ContainsFunc(advertised, func(tool any) bool { return jStr(tool, "name") == jStr(required) }) {
						reply = M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32001, "message": "Required selected MCP tool not advertised: " + jStr(required)}}
						break
					}
				}
				if result := jObj(reply, "result"); result != nil {
					result["tools"] = advertised
				}
			}
			if err := to.Encode(reply); err != nil {
				return err
			}
			break
		}
	}
}
