package mcpserve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"fleet/errtrack"
)

// Stdio: one MCP server on a newline JSON-RPC stream (the host's servers a native agent starts). Tools gives the
// tools/list entries; Call answers one tools/call with the whole result (content, isError) or an error, which becomes
// a JSON-RPC error -32602 with its message. With Concurrent, calls run side by side (a long call never holds a ping or
// another call) and the end of input waits for the calls in flight; otherwise requests are answered in order. Closed,
// when set, runs once input ends, before that wait (it lets waiting calls return early).
type Stdio struct {
	Name, Version, Instructions string
	Tools                       func() any
	Call                        func(name string, arguments json.RawMessage) (any, error)
	Concurrent                  bool
	Closed                      func()
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type stdioReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve answers requests from in on out until in ends. A line that is not JSON gets a parse error (id null); a
// request without an id is a notification and gets no answer; a method other than initialize, ping, tools/list and
// tools/call gets -32601.
func (s Stdio) Serve(in io.Reader, out io.Writer) error {
	w := bufio.NewWriter(out)
	var mu sync.Mutex
	write := func(r stdioReply) {
		r.JSONRPC = "2.0"
		if r.Result == nil && r.Error == nil {
			r.Result = map[string]any{}
		}
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		e.Encode(r)
		mu.Lock()
		defer mu.Unlock()
		w.Write(b.Bytes())
		w.Flush()
	}
	var calls sync.WaitGroup
	defer calls.Wait()
	if s.Closed != nil {
		defer s.Closed()
	}
	r := bufio.NewReader(in) // no line cap: a request may carry a large source
	for {
		line, rerr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				write(stdioReply{ID: json.RawMessage("null"), Error: &rpcError{-32700, "Parse error"}})
			} else if req.ID != nil {
				if s.Concurrent && req.Method == "tools/call" {
					calls.Add(1)
					errtrack.Go("mcp.call", func() {
						defer calls.Done()
						write(s.answer(req.ID, req.Method, req.Params))
					})
				} else {
					write(s.answer(req.ID, req.Method, req.Params))
				}
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

func (s Stdio) answer(id json.RawMessage, method string, raw json.RawMessage) stdioReply {
	var p struct {
		Name            string          `json:"name"`
		Arguments       json.RawMessage `json:"arguments"`
		ProtocolVersion string          `json:"protocolVersion"`
	}
	json.Unmarshal(raw, &p)
	switch method {
	case "initialize":
		return stdioReply{ID: id, Result: Initialize(p.ProtocolVersion, s.Name, s.Version, s.Instructions)}
	case "ping":
		return stdioReply{ID: id}
	case "tools/list":
		return stdioReply{ID: id, Result: map[string]any{"tools": s.Tools()}}
	case "tools/call":
		out, err := s.Call(p.Name, p.Arguments)
		if err != nil {
			return stdioReply{ID: id, Error: &rpcError{-32602, strings.TrimSpace(err.Error())}}
		}
		return stdioReply{ID: id, Result: out}
	}
	return stdioReply{ID: id, Error: &rpcError{-32601, "method not found"}}
}
