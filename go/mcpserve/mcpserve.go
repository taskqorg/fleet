// Package mcpserve is the one MCP tools server (docs/designs/fleet-standard.md S8): the JSON-RPC request, initialize,
// tools/list and tools/call handed to the caller's dispatcher. Server answers one request over Fleet operations
// (tools described from contract.Operations); Stdio serves a newline JSON-RPC stream with the caller's own tools.
package mcpserve

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"fleet/contract"
)

// ProtocolVersion: the MCP protocol version initialize answers.
const ProtocolVersion = "2025-06-18"

// ErrUnknownMethod: a JSON-RPC method other than initialize, tools/list and tools/call.
var ErrUnknownMethod = errors.New("mcpserve: unknown method")

// Server: one MCP server. Tools names the operations tools/list publishes (each described by its contract entry);
// Call runs one tools/call and decides itself which names it accepts. Its error is the server's reply.
type Server struct {
	Name  string
	Tools []string
	Call  func(name string, arguments json.RawMessage) (any, error)
}

// Request: a JSON-RPC request; Params names the tool and its arguments for tools/call.
type Request struct {
	ID     any    `json:"id"`
	Method string `json:"method"`
	Params struct {
		Name            string          `json:"name"`
		Arguments       json.RawMessage `json:"arguments"`
		ProtocolVersion string          `json:"protocolVersion"`
	} `json:"params"`
}

// Parse reads a JSON-RPC request; a body that is not one reads as a request without a method.
func Parse(body []byte) Request {
	var r Request
	json.Unmarshal(body, &r)
	return r
}

// Tool: a tools/list entry.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Reply: a JSON-RPC result.
type Reply struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Handle answers one JSON-RPC request.
func (s Server) Handle(body []byte) (any, error) {
	r := Parse(body)
	switch r.Method {
	case "initialize":
		return Reply{"2.0", r.ID, Initialize(r.Params.ProtocolVersion, s.Name, "1", "")}, nil
	case "tools/list":
		return Reply{"2.0", r.ID, map[string]any{"tools": List(s.Tools)}}, nil
	case "tools/call":
		out, err := s.Call(r.Params.Name, r.Params.Arguments)
		if err != nil {
			return nil, err
		}
		return Result(r.ID, out), nil
	}
	return nil, ErrUnknownMethod
}

// Versions: the MCP protocol versions these servers answer; they publish tools only, the same in each.
var Versions = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

// Initialize: the initialize result. The protocol version is the client's requested one when it is in Versions, else
// ProtocolVersion. instructions is left out when empty.
func Initialize(requested, name, version, instructions string) map[string]any {
	if !slices.Contains(Versions, requested) {
		requested = ProtocolVersion
	}
	out := map[string]any{"protocolVersion": requested, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": name, "version": version}}
	if instructions != "" {
		out["instructions"] = instructions
	}
	return out
}

// Result: a tools/call result: the operation's reply as one JSON text content.
func Result(id any, out any) Reply {
	text, _ := json.Marshal(out)
	return Reply{"2.0", id, map[string]any{"content": []content{{"text", string(text)}}}}
}

// List: the tools of the named operations, sorted by name, each with its contract description and input schema.
func List(names []string) []Tool {
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		o := contract.Operations[n]
		out = append(out, Tool{Name: n, Description: o.Description, InputSchema: o.InputSchema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
