package main

// ACP 1.5.3 session. Protocol shapes: AgentQ d0f6bfa acp.rs; T3 d0f1f72 AntigravityAcpSupport and
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
	profile               obj
	servers               obj
}

func newACPSession(root, resume string, profile, servers obj) *acpSession {
	s := &acpSession{root: root, resume: resume, profile: profile, servers: servers}
	s.mode, _ = str(get(s.config(), "settings", "mode"))
	return s
}

func (s *acpSession) config() any {
	if has(s.profile, "agentConfig") {
		return s.profile["agentConfig"]
	}
	return s.profile
}

func (s *acpSession) Initialize() map[string]any {
	return obj{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": obj{"protocolVersion": 1, "clientCapabilities": obj{}, "clientInfo": obj{"name": "fleet", "version": "0.1.0"}}}
}

func (s *acpSession) Steer(string, string) map[string]any { return nil }

func acpRequest(id int, method string, params obj) obj {
	return obj{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

func (s *acpSession) prompt() obj {
	s.stage = 5
	text := sv(s.profile["text"])
	if instructions := sv(get(s.config(), "instructions")); instructions != "" {
		text = fmt.Sprintf("Explicit Fleet instructions (user role):\n%s\n\nTask:\n%s", instructions, text)
	}
	return acpRequest(5, "session/prompt", obj{"sessionId": s.session, "prompt": []any{obj{"type": "text", "text": text}}})
}

func listHas(list any, key string, want any) bool {
	a, _ := list.([]any)
	return slices.ContainsFunc(a, func(m any) bool { return get(m, key) == want })
}

func (s *acpSession) Receive(v map[string]any) ([]map[string]any, map[string]any, bool, error) {
	if has(v, "id") && has(v, "method") {
		reply := obj{"jsonrpc": "2.0", "id": v["id"], "error": obj{"code": -32601, "message": "Fleet does not grant client file/terminal operations"}}
		if v["method"] == "session/request_permission" {
			reply = obj{"jsonrpc": "2.0", "id": v["id"], "result": obj{"outcome": obj{"outcome": "cancelled"}}}
		}
		return []obj{reply}, obj{"type": "permission.refused", "native": v}, false, nil
	}
	if has(v, "error") {
		return nil, nil, false, fmt.Errorf("ACP request failed (code %s); no replay", encode(get(v, "error", "code")))
	}
	if _, ok := v["result"].(obj); has(v, "id") && !ok {
		return nil, nil, false, errors.New("ACP response missing successful result")
	}
	r := v["result"]
	switch {
	case isID(v, 1) && s.stage == 0:
		if pv, _ := num(get(r, "protocolVersion")); pv != 1 || get(r, "agentInfo", "version") != "1.5.3" {
			return nil, nil, false, errors.New("unqualified ACP protocol/runtime version")
		}
		method := "session/new"
		if s.resume != "" {
			switch {
			case has(get(r, "agentCapabilities", "sessionCapabilities"), "resume"):
				method = "session/resume"
			case get(r, "agentCapabilities", "loadSession") == true:
				method = "session/load"
			default:
				return nil, nil, false, errors.New("native ACP resume unavailable")
			}
		}
		servers := []any{}
		for name, sv := range s.servers {
			servers = append(servers, obj{"name": name, "command": get(sv, "command"), "args": get(sv, "args"), "env": []any{}})
		}
		params := obj{"cwd": s.root, "mcpServers": servers}
		if s.resume != "" {
			params["sessionId"] = s.resume
		}
		s.stage = 1
		return []obj{acpRequest(2, method, params)}, obj{"type": "capabilities", "native": v}, false, nil
	case isID(v, 2) && s.stage == 1:
		id, ok := str(get(r, "sessionId"))
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
		if !listHas(get(r, "models", "availableModels"), "modelId", model) {
			return nil, nil, false, errors.New("requested ACP model unavailable; no substitute")
		}
		if s.mode != "" && !listHas(get(r, "modes", "availableModes"), "id", s.mode) {
			return nil, nil, false, errors.New("requested ACP mode unavailable")
		}
		s.session, s.stage = id, 2
		return []obj{acpRequest(3, "session/set_model", obj{"sessionId": id, "modelId": model})}, obj{"type": "session.started", "conversation_id": id, "native": v}, false, nil
	case isID(v, 3) && s.stage == 2:
		s.stage = 3
		next := acpRequest(4, "session/set_mode", obj{"sessionId": s.session, "modeId": s.mode})
		if s.mode == "" {
			next = s.prompt()
		}
		return []obj{next}, obj{"type": "configuration.applied", "model": s.profile["model"], "native": v}, false, nil
	case isID(v, 4) && s.stage == 3:
		return []obj{s.prompt()}, obj{"type": "configuration.applied", "native": v}, false, nil
	case isID(v, 5) && s.stage == 5:
		s.stage = 6
		stop, ok := str(get(r, "stopReason"))
		if !ok {
			return nil, nil, false, errors.New("ACP stop reason missing")
		}
		t := "turn.failed"
		if stop == "end_turn" {
			t = "turn.completed"
		}
		return nil, obj{"type": t, "usage": get(r, "usage"), "stopReason": stop, "native": v}, true, nil
	case v["method"] == "session/update" && s.session != "" && get(v, "params", "sessionId") == s.session:
		return nil, obj{"type": "acp.update", "update": get(v, "params", "update"), "native": v}, false, nil
	}
	return nil, nil, false, nil
}
