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
	legacyModelMode         *acpConfigOption
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

// ACP v1 peers may expose the model and mode through select options instead of the legacy fields.
func acpSelect(result acpResult, category, value, pinnedID string) *acpConfigOption {
	for _, opt := range result.configOptions() {
		id, kind := opt.ID, opt.Category
		if (kind != category && (kind != "" || id != category)) || opt.Type != "select" ||
			id == "" || pinnedID != "" && id != pinnedID {
			continue
		}
		if value == "" || opt.CurrentValue == value || acpSelectHas(opt, value) {
			return &opt
		}
		// Only the primary mode can establish the session's access policy.
		if category == "mode" {
			break
		}
	}
	return nil
}

func acpValuesHave(values []acpOptionValue, value string) bool {
	return slices.ContainsFunc(values, func(o acpOptionValue) bool { return isStr(o.Value, value) })
}

func acpSelectHas(opt acpConfigOption, value string) bool {
	if acpValuesHave(opt.Options, value) {
		return true
	}
	for _, group := range opt.Options {
		if acpValuesHave(group.Options, value) {
			return true
		}
	}
	return false
}

func acpSelected(result acpResult, id, value string) bool {
	for _, opt := range result.configOptions() {
		if opt.ID == id && opt.Type == "select" && opt.CurrentValue == value {
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
func (s *acpSession) selectMode(result acpResult) *acpConfigOption {
	if s.mode == "" {
		return nil
	}
	if s.primaryModeID == "" {
		if o := acpSelect(result, "mode", "", ""); o != nil {
			s.primaryModeID = o.ID
		}
	}
	return acpSelect(result, "mode", s.mode, s.primaryModeID)
}

func (s *acpSession) applyMode(result acpResult) (M, error) {
	if s.mode == "" {
		return s.prompt(), nil
	}
	if s.modelSelect == "" && s.legacyModeAvailable {
		s.stage = 3
		return acpRequest(4, "session/set_mode", M{"sessionId": s.session, "modeId": s.mode}), nil
	}
	option := s.selectMode(result)
	if option != nil && option.CurrentValue == s.mode {
		return s.prompt(), nil
	}
	if option == nil && !result.ConfigOptions.Set {
		// A legacy model acknowledgement may omit configOptions. Its
		// initial selector is still an address, but its prior value is stale.
		option = s.legacyModelMode
	}
	if option != nil {
		s.stage, s.modeSelect = 3, option.ID
		return s.setOption(4, s.modeSelect, s.mode), nil
	}
	if !s.legacyModeAvailable {
		return nil, errors.New("requested ACP mode unavailable")
	}
	s.stage = 3
	return acpRequest(4, "session/set_mode", M{"sessionId": s.session, "modeId": s.mode}), nil
}

// selectModel returns nil when the peer has not offered the exact model yet.
func (s *acpSession) selectModel(result acpResult, model string) (M, error) {
	s.legacyModeAvailable = slices.ContainsFunc(result.Modes.AvailableModes, func(m struct {
		ID *string `json:"id"`
	}) bool {
		return isStr(m.ID, s.mode)
	})
	if option := acpSelect(result, "model", model, ""); option != nil {
		s.modelSelect = option.ID
		if option.CurrentValue == model {
			return s.applyMode(result)
		}
		s.stage = 2
		return s.setOption(3, s.modelSelect, model), nil
	}
	// Antigravity's legacy IDs can select an exact model and effort together.
	if !slices.ContainsFunc(result.Models.AvailableModels, func(m struct {
		ModelID *string `json:"modelId"`
	}) bool {
		return isStr(m.ModelID, model)
	}) {
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
func (s *acpSession) codePermission(params acpParams) string {
	mode := jStr(s.config(), "codeMode")
	if pharness(s.profile) != "antigravity" || (mode != "add" && mode != "only") || !has(s.servers, "code") ||
		s.stage != 5 || s.session == "" || params.SessionID != s.session {
		return ""
	}
	call := params.ToolCall
	if (call.Name != "mcp_code_code" && call.Name != "mcp_code_poll") || call.Kind != "other" ||
		call.Status != "pending" || call.ToolCallID == "" {
		return ""
	}
	for _, opt := range params.Options {
		if opt.OptionID != "" && opt.Kind == "allow_once" {
			return opt.OptionID
		}
	}
	return ""
}

func (s *acpSession) Receive(v map[string]any) ([]map[string]any, map[string]any, bool, error) {
	f := typed[rpcFrame](v)
	if f.request() {
		id := f.ID.V
		reply := M{"jsonrpc": "2.0", "id": id, "error": M{"code": -32601, "message": "Fleet does not grant client file/terminal operations"}}
		if f.method() == "session/request_permission" {
			if option := s.codePermission(typed[acpParams](f.Params)); option != "" {
				reply = M{"jsonrpc": "2.0", "id": id, "result": M{"outcome": M{"outcome": "selected", "optionId": option}}}
				return []M{reply}, M{"type": "permission.granted", "native": v}, false, nil
			}
			reply = M{"jsonrpc": "2.0", "id": id, "result": M{"outcome": M{"outcome": "cancelled"}}}
		}
		return []M{reply}, M{"type": "permission.refused", "native": v}, false, nil
	}
	if f.Error.Set {
		return nil, nil, false, fmt.Errorf("ACP request failed (code %s); no replay", f.errorCode())
	}
	if f.ID.Set && !f.resultObject() {
		return nil, nil, false, errors.New("ACP response missing successful result")
	}
	r := typed[acpResult](f.Result)
	switch {
	case f.isID(1) && s.stage == 0:
		// Qualification is the ACP protocol version, not the runtime release: a pin on agentInfo.version refused every
		// vendor patch release (1.5.4) although the protocol was unchanged. The runtime version is retained in the
		// capabilities event below; capability gaps (resume, model, mode) are refused where they are used.
		if pv, _ := num(r.ProtocolVersion); pv != 1 {
			return nil, nil, false, errors.New("unqualified ACP protocol version")
		}
		method := "session/new"
		if s.resume != "" {
			switch {
			case r.AgentCapabilities.SessionCapabilities.Resume.Set:
				method = "session/resume"
			case r.AgentCapabilities.LoadSession:
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
	case f.isID(2) && s.stage == 1:
		id := s.resume
		if r.SessionID != nil {
			id = *r.SessionID
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
			if s.mode == "" || mode == nil || mode.CurrentValue == s.mode {
				return nil, nil, false, errors.New("requested ACP model unavailable; no substitute")
			}
			s.stage, s.modeSelect = 7, mode.ID
			next = s.setOption(6, s.modeSelect, s.mode)
		}
		return []M{next}, M{"type": "session.started", "conversation_id": id, "native": v}, false, nil
	case f.isID(6) && s.stage == 7:
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
	case f.isID(3) && s.stage == 2:
		if s.modelSelect != "" && !acpSelected(r, s.modelSelect, jStr(s.profile, "model")) {
			return nil, nil, false, errors.New("ACP did not apply the requested model")
		}
		next, err := s.applyMode(r)
		if err != nil {
			return nil, nil, false, err
		}
		return []M{next}, M{"type": "configuration.applied", "model": s.profile["model"], "native": v}, false, nil
	case f.isID(4) && s.stage == 3:
		if s.modeSelect != "" && !acpSelected(r, s.modeSelect, s.mode) {
			return nil, nil, false, errors.New("ACP did not confirm the requested mode")
		}
		// A legacy setter may acknowledge with an empty result. Check any
		// returned state, using the earlier exact selection for omitted fields.
		model := jStr(s.profile, "model")
		if observed := r.Models.CurrentModelID; observed != "" && observed != model {
			return nil, nil, false, errors.New("ACP mode change did not retain the requested model")
		}
		if s.modelSelect != "" && (s.modeSelect != "" || r.ConfigOptions.Set) && !acpSelected(r, s.modelSelect, model) {
			return nil, nil, false, errors.New("ACP mode change did not retain the requested model")
		}
		return []M{s.prompt()}, M{"type": "configuration.applied", "native": v}, false, nil
	case f.isID(5) && s.stage == 5:
		s.stage = 6
		if r.StopReason == nil {
			return nil, nil, false, errors.New("ACP stop reason missing")
		}
		stop := *r.StopReason
		t := "turn.failed"
		if stop == "end_turn" {
			t = "turn.completed"
		}
		ev := M{"type": t, "usage": r.Usage, "stopReason": stop, "native": v}
		if s.promptStartedAtMs > 0 {
			ev["durationMs"] = max(nowMs()-s.promptStartedAtMs, int64(0))
			s.promptStartedAtMs = 0
		}
		return nil, ev, true, nil
	case f.method() == "session/update" && s.session != "":
		if p := typed[acpParams](f.Params); p.SessionID == s.session {
			return nil, M{"type": "acp.update", "update": p.Update.V, "native": v}, false, nil
		}
	}
	return nil, nil, false, nil
}
