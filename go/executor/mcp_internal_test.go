package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// R2/R3; risk: internal MCP filtering omits receipt recovery or widens an explicit adapter grant.
func TestInternalMCPActualOuterSelection(t *testing.T) {
	o, _ := testMCPOwner(t)
	var seq int
	var name, journal string
	if err := o.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &journal); err != nil {
		t.Fatal(err)
	}
	ctx := M{"commandId": "command", "executionId": "execution", "projectId": "fixture", "workdir": t.TempDir()}
	key := "command:agent:receipt"
	if _, err := o.db.Exec("INSERT INTO delegation_requests(id,body,response) VALUES(?,?,?)", key, enc(M{"commandId": "command", "executionId": "execution", "request": M{}}), enc(M{"result": M{"content": []any{M{"type": "text", "text": "original-ack"}}}})); err != nil {
		t.Fatal(err)
	}
	fleet := delegationServer(ctx, journal)
	if enc(jArr(fleet, "tools")) != enc(mcpToolNames(mcpTools(ctx))) {
		t.Fatal("discovery/selection drift")
	}
	rules, err := profileServers(M{"mcpServers": []any{M{"id": "design-rules", "data": M{"heading": "explicit-rules"}}}}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var gatewayHits atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayHits.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-run-token" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(`{"answer":"explicit-gateway"}`))
	}))
	defer gateway.Close()
	run := filepath.Join(t.TempDir(), "run.env")
	os.WriteFile(run, []byte("FLEET_RUN_TOKEN=fixture-run-token\nFLEET_GATEWAY_URL="+gateway.URL+"\n"), 0600)
	grant := M{"tools": []any{M{"name": "read", "inputSchema": M{"type": "object", "additionalProperties": false}}}}
	connection := M{"command": exe(), "args": []any{"mcp", "connection", "web", enc(grant), run}, "tools": mcpToolNames(grant)}
	cases := []struct {
		name   string
		server M
		tool   string
		args   M
		want   string
	}{{"fleet", fleet, "agent_tool_result", M{"requestKey": key}, "original-ack"}, {"design-rules", jObj(rules["design-rules"]), "get_design_rules", M{}, "explicit-rules"}, {"gateway", connection, "read", M{}, "explicit-gateway"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(exe(), "mcp", "stdio", o.socket, enc(tc.server), t.TempDir())
			cmd.Env = mcpChildEnv(os.Getenv("PATH"))
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			in, _ := cmd.StdinPipe()
			out, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			decoder, encoder := json.NewDecoder(out), json.NewEncoder(in)
			call := func(id int, method string, params M) M {
				t.Helper()
				if err := encoder.Encode(M{"id": id, "method": method, "params": params}); err != nil {
					t.Fatal(err)
				}
				var reply M
				if err := decoder.Decode(&reply); err != nil {
					t.Fatal(err, stderr.String())
				}
				return reply
			}
			initialized := call(1, "initialize", M{"protocolVersion": "2025-06-18"})
			if has(initialized, "error") {
				t.Fatal(initialized)
			}
			listed := call(2, "tools/list", M{})
			if has(listed, "error") || enc(mcpToolNames(jAt(listed, "result"))) != enc(jArr(tc.server, "tools")) {
				t.Fatal(listed)
			}
			allowed := call(3, "tools/call", M{"name": tc.tool, "arguments": tc.args})
			if has(allowed, "error") || !strings.Contains(enc(allowed), tc.want) {
				t.Fatal(allowed)
			}
			denied := call(4, "tools/call", M{"name": "never_granted", "arguments": M{}})
			if jStr(denied, "error", "message") != "Tool is outside selected MCP capability" {
				t.Fatal(denied)
			}
			if tc.name == "fleet" {
				foreign := call(5, "tools/call", M{"name": "agent_tool_result", "arguments": M{"requestKey": "foreign:agent:receipt"}})
				if !strings.Contains(enc(foreign), "Request belongs to another command") {
					t.Fatal(foreign)
				}
			}
			in.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatal(err, stderr.String())
			}
		})
	}
	if gatewayHits.Load() != 1 {
		t.Fatal("denied call escaped filtering", gatewayHits.Load())
	}
	empty, err := profileServers(M{"mcpServers": []any{}}, "", "", "")
	if err != nil || len(empty) != 0 {
		t.Fatal("no-grant path expanded", empty, err)
	}
}
