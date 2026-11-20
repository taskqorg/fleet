package main

// Typed reads of what native harnesses write: the JSON-RPC envelope shared by codex app-server and ACP, the event union
// the projection reads; the decoder (typed) is in json.go. Per-harness shapes live in harness_claude_records.go,
// harness_codex_records.go and harness_acp_records.go. Only the fields the executor reads are typed; every record is
// still forwarded to the coordinator as received (nativeEvent Data, "native").

// firstRaw: the value of the first present field (Rust's a.or(b) on present keys); nil when none is.
func firstRaw(js ...jsonValue) any {
	for _, j := range js {
		if j.Set {
			return j.V
		}
	}
	return nil
}

func isStr(p *string, s string) bool { return p != nil && *p == s }

// rpcFrame: the JSON-RPC envelope of a frame a protocol harness writes (codex app-server, ACP); each harness decodes
// params and result for the method or request id.
type rpcFrame struct {
	ID     jsonValue `json:"id"`
	Method jsonValue `json:"method"`
	Params jsonValue `json:"params"`
	Result jsonValue `json:"result"`
	Error  jsonValue `json:"error"`
}

// request: a native request to Fleet (it carries both id and method).
func (f rpcFrame) request() bool { return f.ID.Set && f.Method.Set }

func (f rpcFrame) method() string { s, _ := valStr(f.Method); return s }

func (f rpcFrame) isID(n float64) bool { x, ok := num(f.ID.V); return ok && x == n }

// resultObject: the frame carries a result that is a JSON object.
func (f rpcFrame) resultObject() bool { _, ok := f.Result.V.(M); return ok }

// errorCode: the JSON of error.code ("null" when absent).
func (f rpcFrame) errorCode() string {
	return enc(typed[struct {
		Code any `json:"code"`
	}](f.Error).Code)
}

// isID: whether the frame's id is the number n.
func isID(v any, n float64) bool { return typed[rpcFrame](v).isID(n) }

// harnessEvent: one event of an execution as tickOne projects it, after Normalize: a native stream record (claude
// stream-json, codex exec --json, the Antigravity step format) or an event a protocol session emitted (acp.update,
// item.completed, turn.*). Nested shapes are decoded where read: Message as claudeMessage, Item as codexItem, Update as
// acpUpdate, StepUpdate as acpStepUpdate, Result as eventResult, Usage as usageCounts.
type harnessEvent struct {
	Type                jsonValue        `json:"type"`
	Event               jsonValue        `json:"event"`
	Error               jsonValue        `json:"error"`
	Result              jsonValue        `json:"result"`
	Usage               jsonValue        `json:"usage"`
	ThreadID            any              `json:"thread_id"`  // codex exec
	SessionID           any              `json:"session_id"` // claude; the ACP step format
	ConversationID      jsonValue        `json:"conversation_id"`
	ConversationIDCamel jsonValue        `json:"conversationId"`
	IsError             *bool            `json:"is_error"` // claude result
	NumTurns            any              `json:"num_turns"`
	DurationMsSnake     any              `json:"duration_ms"`
	DurationMs          any              `json:"durationMs"` // a session's turn.completed / turn.failed
	Message             jsonValue        `json:"message"`
	Item                jsonValue        `json:"item"`
	Update              jsonValue        `json:"update"`
	StepUpdate          jsonValue        `json:"step_update"`
	ToolCalls           []acpPlannerCall `json:"tool_calls"`   // PLANNER_RESPONSE
	ToolCallID          any              `json:"tool_call_id"` // TOOL_RESPONSE
	Status              jsonValue        `json:"status"`
	Content             jsonValue        `json:"content"`
	Native              jsonValue        `json:"native"` // the frame a session event came from
}

// steerResult: the steer.result event a protocol session emits for a steer frame's answer.
type steerResult struct {
	CommandID *string `json:"commandId"`
	Ok        *bool   `json:"ok"`
}

// kind: the event's type when it is a string, else "".
func (e harnessEvent) kind() string { s, _ := valStr(e.Type); return s }

func (e harnessEvent) event() string { s, _ := valStr(e.Event); return s }

func (e harnessEvent) result() eventResult { return typed[eventResult](e.Result) }

// eventResult: the fields of an event's result object the projection reads.
type eventResult struct {
	Error          jsonValue `json:"error"`
	Usage          jsonValue `json:"usage"`
	ConversationID jsonValue `json:"conversation_id"`
	Status         string    `json:"status"`
	Response       *string   `json:"response"`
}

// usageCounts: a native usage report's token counts (any of the harnesses' spellings); codex nests them in total.
type usageCounts struct {
	Total                 jsonValue `json:"total"`
	InputTokens           any       `json:"inputTokens"`
	OutputTokens          any       `json:"outputTokens"`
	CachedInputTokens     any       `json:"cachedInputTokens"`
	CacheWriteInputTokens any       `json:"cacheWriteInputTokens"`
	ReasoningOutputTokens any       `json:"reasoningOutputTokens"`
	TotalTokens           any       `json:"totalTokens"`
	CachedReadTokens      any       `json:"cachedReadTokens"`
	CachedWriteTokens     any       `json:"cachedWriteTokens"`
	ThoughtTokens         any       `json:"thoughtTokens"`
	InputTokensSnake      any       `json:"input_tokens"`
	OutputTokensSnake     any       `json:"output_tokens"`
	CachedInputSnake      any       `json:"cached_input_tokens"`
}

// fields: each count with its normalized name, in the order a later spelling overrides an earlier one.
func (u usageCounts) fields() []struct {
	n    any
	name string
} {
	return []struct {
		n    any
		name string
	}{{u.InputTokens, "input_tokens"}, {u.OutputTokens, "output_tokens"}, {u.CachedInputTokens, "cached_input_tokens"},
		{u.CacheWriteInputTokens, "cache_write_input_tokens"}, {u.ReasoningOutputTokens, "reasoning_output_tokens"}, {u.TotalTokens, "total_tokens"},
		{u.CachedReadTokens, "cache_read_tokens"}, {u.CachedWriteTokens, "cache_write_tokens"}, {u.ThoughtTokens, "thought_tokens"},
		{u.InputTokensSnake, "input_tokens"}, {u.OutputTokensSnake, "output_tokens"}, {u.CachedInputSnake, "cached_input_tokens"}}
}
