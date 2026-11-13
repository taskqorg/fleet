package main

// ACP protocol version 1 session (qualified with Antigravity 1.5.3 and 1.5.4). Protocol shapes: AgentQ d0f6bfa acp.rs; T3 d0f1f72 AntigravityAcpSupport and
// AcpSessionRuntime. Behavioural reuse only; no source copied. Fleet owns admission, logs and process cleanup.

import (
	"errors"
	"fmt"
	"slices"
)

type acpSession struct {
	stage                 int
	session, resume, root string
	mode                  string
	profile               M
	servers               M
	promptStartedAtMs     int64
}

func newACPSession(root, resume string, profile, servers M) *acpSession {
	s := &acpSession{root: root, resume: resume, profile: profile, servers: servers}
	s.mode, _ = str(jAt(s.config(), "settings", "mode"))
	return s
}

func (s *acpSession) config() any {
	if has(s.profile, "agentConfig") {
		return s.profile["agentConfig"]
	}
	return s.profile
}

func (s *acpSession) Initialize() map[string]any {
	return M{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": M{"protocolVersion": 1, "clientCapabilities": M{}, "clientInfo": M{"name": "fleet", "version": "0.1.0"}}}
}

func (s *acpSession) Steer(string, string) map[string]any { return nil }

func acpRequest(id int, method string, params M) M {
	return M{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

func (s *acpSession) prompt() M {
	s.stage = 5
	s.promptStartedAtMs = nowMs()
	text := jStr(s.profile["text"])
	if instructions := jStr(jAt(s.config(), "instructions")); instructions != "" {
		text = fmt.Sprintf("Explicit Fleet instructions (user role):\n%s\n\nTask:\n%s", instructions, text)
	}
	return acpRequest(5, "session/prompt", M{"sessionId": s.session, "prompt": []any{M{"type": "text", "text": text}}})
}

func listHas(list any, key string, want any) bool {
	a, _ := list.([]any)
	return slices.ContainsFunc(a, func(m any) bool { return jAt(m, key) == want })
}

func (s *acpSession) Receive(v map[string]any) ([]map[string]any, map[string]any, bool, error) {
	if has(v, "id") && has(v, "method") {
		reply := M{"jsonrpc": "2.0", "id": v["id"], "error": M{"code": -32601, "message": "Fleet does not grant client file/terminal operations"}}
		if v["method"] == "session/request_permission" {
			reply = M{"jsonrpc": "2.0", "id": v["id"], "result": M{"outcome": M{"outcome": "cancelled"}}}
		}
		return []M{reply}, M{"type": "permission.refused", "native": v}, false, nil
	}
	if has(v, "error") {
		return nil, nil, false, fmt.Errorf("ACP request failed (code %s); no replay", enc(jAt(v, "error", "code")))
	}
	if _, ok := v["result"].(M); has(v, "id") && !ok {
		return nil, nil, false, errors.New("ACP response missing successful result")
	}
	r := v["result"]
	switch {
	case isID(v, 1) && s.stage == 0:
		// Qualification is the ACP protocol version, not the runtime release: a pin on agentInfo.version refused every
		// vendor patch release (1.5.4) although the protocol was unchanged. The runtime version is retained in the
		// capabilities event below; capability gaps (resume, model, mode) are refused where they are used.
		if pv, _ := num(jAt(r, "protocolVersion")); pv != 1 {
			return nil, nil, false, errors.New("unqualified ACP protocol version")
		}
		method := "session/new"
		if s.resume != "" {
			switch {
			case has(jAt(r, "agentCapabilities", "sessionCapabilities"), "resume"):
				method = "session/resume"
			case jAt(r, "agentCapabilities", "loadSession") == true:
				method = "session/load"
			default:
				return nil, nil, false, errors.New("native ACP resume unavailable")
			}
		}
		servers := []any{}
		for name, sv := range s.servers {
			servers = append(servers, M{"name": name, "command": jAt(sv, "command"), "args": jAt(sv, "args"), "env": []any{}})
		}
		params := M{"cwd": s.root, "mcpServers": servers}
		if s.resume != "" {
			params["sessionId"] = s.resume
		}
		s.stage = 1
		return []M{acpRequest(2, method, params)}, M{"type": "capabilities", "native": v}, false, nil
	case isID(v, 2) && s.stage == 1:
		id, ok := str(jAt(r, "sessionId"))
		if !ok {
			id = s.resume
		}
		if id == "" {
			return nil, nil, false, errors.New("ACP session ID missing")
		}
		if s.resume != "" && s.resume != id {
			return nil, nil, false, errors.New("ACP resumed identity mismatch")
		}
		model, ok := str(s.profile["model"])
		if !ok {
			return nil, nil, false, errors.New("model missing")
		}
		if !listHas(jAt(r, "models", "availableModels"), "modelId", model) {
			return nil, nil, false, errors.New("requested ACP model unavailable; no substitute")
		}
		if s.mode != "" && !listHas(jAt(r, "modes", "availableModes"), "id", s.mode) {
			return nil, nil, false, errors.New("requested ACP mode unavailable")
		}
		s.session, s.stage = id, 2
		return []M{acpRequest(3, "session/set_model", M{"sessionId": id, "modelId": model})}, M{"type": "session.started", "conversation_id": id, "native": v}, false, nil
	case isID(v, 3) && s.stage == 2:
		s.stage = 3
		next := acpRequest(4, "session/set_mode", M{"sessionId": s.session, "modeId": s.mode})
		if s.mode == "" {
			next = s.prompt()
		}
		return []M{next}, M{"type": "configuration.applied", "model": s.profile["model"], "native": v}, false, nil
	case isID(v, 4) && s.stage == 3:
		return []M{s.prompt()}, M{"type": "configuration.applied", "native": v}, false, nil
	case isID(v, 5) && s.stage == 5:
		s.stage = 6
		stop, ok := str(jAt(r, "stopReason"))
		if !ok {
			return nil, nil, false, errors.New("ACP stop reason missing")
		}
		t := "turn.failed"
		if stop == "end_turn" {
			t = "turn.completed"
		}
		ev := M{"type": t, "usage": jAt(r, "usage"), "stopReason": stop, "native": v}
		if s.promptStartedAtMs > 0 {
			ev["durationMs"] = max(nowMs()-s.promptStartedAtMs, int64(0))
			s.promptStartedAtMs = 0
		}
		return nil, ev, true, nil
	case v["method"] == "session/update" && s.session != "" && jAt(v, "params", "sessionId") == s.session:
		return nil, M{"type": "acp.update", "update": jAt(v, "params", "update"), "native": v}, false, nil
	}
	return nil, nil, false, nil
}
