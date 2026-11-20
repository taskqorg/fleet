// Package mcpserve is the one MCP tools server over Fleet operations (docs/designs/fleet-standard.md S8): the JSON-RPC
// request, initialize, tools/list described from contract.Operations, and tools/call handed to the caller's dispatcher.
package mcpserve

import (
	"encoding/json"
	"errors"
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
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
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
		return Reply{"2.0", r.ID, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": s.Name, "version": "1"}}}, nil
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
