package main

// Typed reads of what native harnesses write: the JSON-RPC envelope shared by codex app-server and ACP, the event union
// the projection reads, and the decoder both use. Per-harness shapes live in harness_claude_records.go,
// harness_codex_records.go and harness_acp_records.go. Only the fields the executor reads are typed; every record is
// still forwarded to the coordinator as received (nativeEvent Data, "native").

import (
	"encoding/json"
	"reflect"
	"strings"
)

// typed decodes a received JSON value (a decoded M, or a jsonValue field of another record) into T. encoding/json
// decodes the typed fields from fit's reduction of v: only keys that name a field exactly (encoding/json alone would
// also take a key differing in case), and only values of the field's JSON type, so a field of another type reads as
// absent ("" for a string, nil for a pointer), as the map accessors read it, and the decode never fails on a native
// record. Open values (any, jsonValue) are then set from v itself, so they keep their decoded form.
func typed[T any](v any) T {
	var t T
	if j, ok := v.(jsonValue); ok {
		v = j.V
	}
	rt := reflect.TypeOf(&t).Elem()
	d := json.NewDecoder(strings.NewReader(enc(fit(v, rt))))
	d.UseNumber()
	_ = d.Decode(&t)
	fill(reflect.ValueOf(&t).Elem(), v)
	return t
}

// jsonValue: an open field of a record, as decoded: Set when the key is present (even as null), V its value.
type jsonValue struct {
	Set bool
	V   any
}

var jsonValueType = reflect.TypeFor[jsonValue]()

func fieldName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}

// fit is v reduced to the typed fields t can hold: struct keys by exact json name, values of the target kind; any
// other value, and every open value (fill sets those), is dropped (a list element becomes null).
func fit(v any, t reflect.Type) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(M)
		if !ok || t == jsonValueType {
			return nil
		}
		out := M{}
		for i := range t.NumField() {
			f := t.Field(i)
			if x, ok := m[fieldName(f)]; ok && fieldName(f) != "" {
				if x = fit(x, f.Type); x != nil {
					out[fieldName(f)] = x
				}
			}
		}
		return out
	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return nil
		}
		out := make([]any, len(a))
		for i, x := range a {
			out[i] = fit(x, t.Elem())
		}
		return out
	case reflect.Map:
		m, ok := v.(M)
		if !ok || t.Key().Kind() != reflect.String {
			return nil
		}
		out := make(M, len(m))
		for k, x := range m {
			out[k] = fit(x, t.Elem())
		}
		return out
	case reflect.String:
		if _, ok := v.(string); ok {
			return v
		}
	case reflect.Bool:
		if _, ok := v.(bool); ok {
			return v
		}
	}
	return nil
}

// fill sets the open values of rv (decoded from fit(src)) from src.
func fill(rv reflect.Value, src any) {
	switch {
	case rv.Type() == jsonValueType:
		rv.Set(reflect.ValueOf(jsonValue{true, src}))
	case rv.Kind() == reflect.Interface:
		if src != nil {
			rv.Set(reflect.ValueOf(src))
		}
	case rv.Kind() == reflect.Pointer:
		if !rv.IsNil() {
			fill(rv.Elem(), src)
		}
	case rv.Kind() == reflect.Struct:
		m, _ := src.(M)
		for i := range rv.NumField() {
			if x, ok := m[fieldName(rv.Type().Field(i))]; ok && fieldName(rv.Type().Field(i)) != "" {
				fill(rv.Field(i), x)
			}
		}
	case rv.Kind() == reflect.Slice:
		if a, ok := src.([]any); ok && len(a) == rv.Len() {
			for i, x := range a {
				fill(rv.Index(i), x)
			}
		}
	case rv.Kind() == reflect.Map:
		m, ok := src.(M)
		if !ok || rv.IsNil() || rv.Type().Elem().Kind() != reflect.Interface {
			return
		}
		for k, x := range m {
			if x != nil {
				rv.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(x))
			}
		}
	}
}

// valStr: an open field's string, and whether it is one (jIsStr).
func valStr(j jsonValue) (string, bool) {
	s, ok := j.V.(string)
	return s, ok
}

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
