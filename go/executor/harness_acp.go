package main

// ACP protocol version 1 session (qualified with Antigravity 1.5.3 and 1.5.4). Protocol shapes: AgentQ d0f6bfa acp.rs; T3 d0f1f72 AntigravityAcpSupport and
// AcpSessionRuntime. Behavioural reuse only; no source copied. Fleet owns admission, logs and process cleanup.

import (
	"errors"
	"fmt"
	"slices"
)

type acpSession struct {
	stage                   int
	session, resume, root   string
	mode, primaryModeID     string
	modelSelect, modeSelect string
	legacyModeAvailable     bool
	legacyModelMode         any
	profile                 M
	servers                 M
	promptStartedAtMs       int64
}

func newACPSession(root, resume string, profile, servers M) *acpSession {
	s := &acpSession{root: root, resume: resume, profile: profile, servers: servers}
	s.mode, _ = str(jAt(s.config(), "settings", "mode"))
	if jIsTrue(profile, "readOnly") {
		s.mode = "plan"
	}
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

// ACP v1 peers may expose the model and mode through select options instead of the legacy fields.
func acpSelect(result any, category, value, pinnedID string) any {
	for _, opt := range jArr(result, "configOptions") {
		id, kind := jStr(opt, "id"), jStr(opt, "category")
		if (kind != category && (kind != "" || id != category)) || jStr(opt, "type") != "select" ||
			id == "" || pinnedID != "" && id != pinnedID {
			continue
		}
		if value == "" || jStr(opt, "currentValue") == value || acpSelectHas(opt, value) {
			return opt
		}
		// Only the primary mode can establish the session's access policy.
		if category == "mode" {
			break
		}
	}
	return nil
}

func acpSelectHas(opt any, value string) bool {
	options := jAt(opt, "options")
	if listHas(options, "value", value) {
		return true
	}
	for _, group := range jArr(opt, "options") {
		if listHas(jAt(group, "options"), "value", value) {
			return true
		}
	}
	return false
}

func acpSelected(result any, id, value string) bool {
	for _, opt := range jArr(result, "configOptions") {
		if jStr(opt, "id") == id && jStr(opt, "type") == "select" && jStr(opt, "currentValue") == value {
			return true
		}
	}
	return false
}

func (s *acpSession) setOption(id int, option, value string) M {
	return acpRequest(id, "session/set_config_option", M{"sessionId": s.session, "configId": option, "value": value})
}

// Keep the primary selector across configuration replies. A secondary mode
// cannot establish read-only access even if the peer changes the option order.
func (s *acpSession) selectMode(result any) any {
	if s.mode == "" {
		return nil
	}
	if s.primaryModeID == "" {
		s.primaryModeID = jStr(acpSelect(result, "mode", "", ""), "id")
	}
	return acpSelect(result, "mode", s.mode, s.primaryModeID)
}

func (s *acpSession) applyMode(result any) (M, error) {
	if s.mode == "" {
		return s.prompt(), nil
	}
	if s.modelSelect == "" && s.legacyModeAvailable {
		s.stage = 3
		return acpRequest(4, "session/set_mode", M{"sessionId": s.session, "modeId": s.mode}), nil
	}
	option := s.selectMode(result)
	if option != nil && jStr(option, "currentValue") == s.mode {
		return s.prompt(), nil
	}
	if option == nil && !has(result, "configOptions") {
		// A legacy model acknowledgement may omit configOptions. Its
		// initial selector is still an address, but its prior value is stale.
		option = s.legacyModelMode
	}
	if option != nil {
		s.stage, s.modeSelect = 3, jStr(option, "id")
		return s.setOption(4, s.modeSelect, s.mode), nil
	}
	if !s.legacyModeAvailable {
		return nil, errors.New("requested ACP mode unavailable")
	}
	s.stage = 3
	return acpRequest(4, "session/set_mode", M{"sessionId": s.session, "modeId": s.mode}), nil
}

// selectModel returns nil when the peer has not offered the exact model yet.
func (s *acpSession) selectModel(result any, model string) (M, error) {
	s.legacyModeAvailable = listHas(jAt(result, "modes", "availableModes"), "id", s.mode)
	if option := acpSelect(result, "model", model, ""); option != nil {
		s.modelSelect = jStr(option, "id")
		if jStr(option, "currentValue") == model {
			return s.applyMode(result)
		}
		s.stage = 2
		return s.setOption(3, s.modelSelect, model), nil
	}
	// Antigravity's legacy IDs can select an exact model and effort together.
	if !listHas(jAt(result, "models", "availableModels"), "modelId", model) {
		return nil, nil
	}
	s.legacyModelMode = s.selectMode(result)
	if s.mode != "" && s.legacyModelMode == nil && !s.legacyModeAvailable {
		return nil, errors.New("requested ACP mode unavailable")
	}
	s.stage = 2
	return acpRequest(3, "session/set_model", M{"sessionId": s.session, "modelId": model}), nil
}

// Antigravity requests ACP permission even for tools supplied by Fleet. This
// permits a single call to the injected Code Mode server during its owning turn.
func (s *acpSession) codePermission(params any) string {
	mode := jStr(s.config(), "codeMode")
	if pharness(s.profile) != "antigravity" || (mode != "add" && mode != "only") || !has(s.servers, "code") ||
		s.stage != 5 || s.session == "" || jStr(params, "sessionId") != s.session {
		return ""
	}
	call := jAt(params, "toolCall")
	name := jStr(call, "name")
	if (name != "mcp_code_code" && name != "mcp_code_poll") || jStr(call, "kind") != "other" ||
		jStr(call, "status") != "pending" || jStr(call, "toolCallId") == "" {
		return ""
	}
	for _, opt := range jArr(params, "options") {
		if id := jStr(opt, "optionId"); id != "" && jStr(opt, "kind") == "allow_once" {
			return id
		}
	}
	return ""
}

func (s *acpSession) Receive(v map[string]any) ([]map[string]any, map[string]any, bool, error) {
	if has(v, "id") && has(v, "method") {
		reply := M{"jsonrpc": "2.0", "id": v["id"], "error": M{"code": -32601, "message": "Fleet does not grant client file/terminal operations"}}
		if v["method"] == "session/request_permission" {
			if option := s.codePermission(v["params"]); option != "" {
				reply = M{"jsonrpc": "2.0", "id": v["id"], "result": M{"outcome": M{"outcome": "selected", "optionId": option}}}
				return []M{reply}, M{"type": "permission.granted", "native": v}, false, nil
			}
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
		s.session = id
		s.selectMode(r)
		next, err := s.selectModel(r, model)
		if err != nil {
			return nil, nil, false, err
		}
		if next == nil {
			// An ACP peer can expose different models after a mode change.
			mode := s.selectMode(r)
			if s.mode == "" || mode == nil || jStr(mode, "currentValue") == s.mode {
				return nil, nil, false, errors.New("requested ACP model unavailable; no substitute")
			}
			s.stage, s.modeSelect = 7, jStr(mode, "id")
			next = s.setOption(6, s.modeSelect, s.mode)
		}
		return []M{next}, M{"type": "session.started", "conversation_id": id, "native": v}, false, nil
	case isID(v, 6) && s.stage == 7:
		if !acpSelected(r, s.modeSelect, s.mode) {
			return nil, nil, false, errors.New("ACP did not confirm the requested mode")
		}
		next, err := s.selectModel(r, jStr(s.profile, "model"))
		if err != nil {
			return nil, nil, false, err
		}
		if next == nil {
			return nil, nil, false, errors.New("requested ACP model unavailable; no substitute")
		}
		return []M{next}, M{"type": "configuration.applied", "native": v}, false, nil
	case isID(v, 3) && s.stage == 2:
		if s.modelSelect != "" && !acpSelected(r, s.modelSelect, jStr(s.profile, "model")) {
			return nil, nil, false, errors.New("ACP did not apply the requested model")
		}
		next, err := s.applyMode(r)
		if err != nil {
			return nil, nil, false, err
		}
		return []M{next}, M{"type": "configuration.applied", "model": s.profile["model"], "native": v}, false, nil
	case isID(v, 4) && s.stage == 3:
		if s.modeSelect != "" && !acpSelected(r, s.modeSelect, s.mode) {
			return nil, nil, false, errors.New("ACP did not confirm the requested mode")
		}
		// A legacy setter may acknowledge with an empty result. Check any
		// returned state, using the earlier exact selection for omitted fields.
		model := jStr(s.profile, "model")
		if observed := jStr(r, "models", "currentModelId"); observed != "" && observed != model {
			return nil, nil, false, errors.New("ACP mode change did not retain the requested model")
		}
		if s.modelSelect != "" && (s.modeSelect != "" || has(r, "configOptions")) && !acpSelected(r, s.modelSelect, model) {
			return nil, nil, false, errors.New("ACP mode change did not retain the requested model")
		}
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
