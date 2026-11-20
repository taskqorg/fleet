package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// mcpRequest is what the filter reads of a JSON-RPC request, by exact key (typed); the request goes upstream as the
// value it was checked on (re-encoded), never as the bytes it came in.
type mcpRequest struct {
	ID     jsonValue `json:"id"` // unset: a notification
	Method *string   `json:"method"`
	Params struct {
		Name      *string `json:"name"`
		Arguments struct {
			RelativePath *string `json:"relative_path"`
		} `json:"arguments"`
	} `json:"params"`
}

// mcpAllowed checks a decoded request (a JSON value as json.Unmarshal reads it into any).
func mcpAllowed(v any, root string, tools []any, policy string) error {
	request := typed[mcpRequest](v)
	method := str0(request.Method)
	if method == "initialize" || method == "notifications/initialized" || method == "ping" || method == "tools/list" {
		return nil
	}
	if method != "tools/call" || !slices.Contains(tools, any(str0(request.Params.Name))) {
		return errors.New("Tool is outside selected MCP capability")
	}
	if path := str0(request.Params.Arguments.RelativePath); path != "" && policy == "project-relative" {
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

// plainJSON is raw as a plain JSON value (numbers as float64), as the filter compared and echoed ids.
func plainJSON(raw json.RawMessage) any {
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
		if err := from.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return errors.New("Invalid MCP request")
		}
		// A key repeated in one object, or spelled in another case, could be read one way here and another way upstream.
		var value any
		if exactKeys(raw, "id", "method", "params", "name", "arguments", "relative_path") != nil || json.Unmarshal(raw, &value) != nil {
			return errors.New("Invalid MCP request")
		}
		request := typed[mcpRequest](value)
		hasID := request.ID.Set
		id := request.ID.V
		if err := mcpAllowed(value, root, tools, policy); err != nil {
			if hasID {
				if err := to.Encode(M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32602, "message": err.Error()}}); err != nil {
					return err
				}
			}
			continue
		}
		if err := send.Encode(value); err != nil {
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
			if enc(plainJSON(replyID)) != enc(id) {
				return errors.New("MCP response identity mismatch")
			}
			var out any = reply
			if str0(request.Method) == "initialize" {
				if result := typedRaw[M](reply["result"]); result != nil {
					result["capabilities"] = M{"tools": M{}}
					reply["result"] = json.RawMessage(enc(result))
				}
			}
			if str0(request.Method) == "tools/list" {
				listed := typedRaw[struct {
					Tools []jsonValue `json:"tools"`
				}](reply["result"])
				advertised := []jsonValue{}
				var names []string
				for _, tool := range listed.Tools {
					name := typed[struct {
						Name string `json:"name"`
					}](tool).Name
					if slices.Contains(tools, any(name)) {
						advertised = append(advertised, tool)
						names = append(names, name)
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
				if result := typedRaw[M](reply["result"]); result != nil && !missing {
					result["tools"] = advertised
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
