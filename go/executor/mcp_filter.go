package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// mcpRequest is what the filter reads of a JSON-RPC request; the request itself goes upstream as it came.
type mcpRequest struct {
	ID     json.RawMessage `json:"id"` // nil when absent: a notification
	Method looseStr        `json:"method"`
	Params loose[struct {
		Name      looseStr `json:"name"`
		Arguments loose[struct {
			RelativePath looseStr `json:"relative_path"`
		}] `json:"arguments"`
	}] `json:"params"`
}

func mcpAllowed(request mcpRequest, root string, tools []any, policy string) error {
	method := request.Method.S
	if method == "initialize" || method == "notifications/initialized" || method == "ping" || method == "tools/list" {
		return nil
	}
	if method != "tools/call" || !slices.Contains(tools, any(request.Params.V.Name.S)) {
		return errors.New("Tool is outside selected MCP capability")
	}
	if path := request.Params.V.Arguments.V.RelativePath.S; path != "" && policy == "project-relative" {
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

// jsonValue is raw as a plain JSON value (numbers as float64), as the filter compared and echoed ids.
func jsonValue(raw json.RawMessage) any {
	var v any
	json.Unmarshal(raw, &v)
	return v
}

func filterMCP(input io.Reader, output io.Writer, upstream io.Writer, replies io.Reader, root string, tools []any, policy string) error {
	from := json.NewDecoder(input)
	to := json.NewEncoder(output)
	server := json.NewDecoder(replies)
	send := json.NewEncoder(upstream)
	for {
		var raw json.RawMessage
		var request mcpRequest
		if err := from.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return errors.New("Invalid MCP request")
		}
		if json.Unmarshal(raw, &request) != nil {
			return errors.New("Invalid MCP request")
		}
		hasID := request.ID != nil
		id := jsonValue(request.ID)
		if err := mcpAllowed(request, root, tools, policy); err != nil {
			if hasID {
				if err := to.Encode(M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32602, "message": err.Error()}}); err != nil {
					return err
				}
			}
			continue
		}
		if err := send.Encode(raw); err != nil {
			return errors.New("MCP request delivery failed; outcome unknown")
		}
		if !hasID {
			continue
		}
		for {
			// open JSON: the server's reply goes on as it came, but for the initialize capabilities and the listed tools.
			var reply map[string]json.RawMessage
			if err := server.Decode(&reply); err != nil {
				return errors.New("MCP response unavailable; outcome unknown")
			}
			replyID, in := reply["id"]
			if !in {
				continue
			} // Never forward server logs or unsolicited notifications into model/history.
			if enc(jsonValue(replyID)) != enc(id) {
				return errors.New("MCP response identity mismatch")
			}
			var out any = reply
			if request.Method.S == "initialize" {
				if result := jsonObject(reply["result"]); result != nil {
					result["capabilities"] = json.RawMessage(`{"tools":{}}`)
					reply["result"] = json.RawMessage(enc(result))
				}
			}
			if request.Method.S == "tools/list" {
				var listed struct {
					Tools looseList `json:"tools"`
				}
				json.Unmarshal(reply["result"], &listed)
				advertised := []json.RawMessage{}
				var names []string
				for _, tool := range listed.Tools.Items {
					var t loose[struct {
						Name looseStr `json:"name"`
					}]
					json.Unmarshal(tool, &t)
					if slices.Contains(tools, any(t.V.Name.S)) {
						advertised = append(advertised, tool)
						names = append(names, t.V.Name.S)
					}
				}
				missing := false
				for _, required := range tools {
					name, _ := str(required)
					if !slices.Contains(names, name) {
						out, missing = M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32001, "message": "Required selected MCP tool not advertised: " + name}}, true
						break
					}
				}
				if result := jsonObject(reply["result"]); result != nil && !missing {
					result["tools"] = json.RawMessage(enc(advertised))
					reply["result"] = json.RawMessage(enc(result))
				}
			}
			if err := to.Encode(out); err != nil {
				return err
			}
			break
		}
	}
}
