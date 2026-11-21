package main

import "encoding/json"

// Accessors of decoded JSON values for the tests' checks. A test builds and reads open JSON as doc; the coordinator's
// open values (json.RawMessage, rawObject) read through the same accessors decoded.

type doc = map[string]any

// decoded: v as its decoded JSON value when it is raw JSON (json.RawMessage, rawObject), else v.
func decoded(v any) any {
	switch x := v.(type) {
	case json.RawMessage:
		return rawAny(x)
	case rawObject:
		return norm(x)
	}
	return v
}

func astr(v any) string          { s, _ := decoded(v).(string); return s }
func mapOf(v any) map[string]any { m, _ := decoded(v).(map[string]any); return m }
func anys(v any) []any           { xs, _ := decoded(v).([]any); return xs }

// strp: m[k] when it is text.
func strp(m doc, k string) *string {
	if s, ok := m[k].(string); ok {
		return &s
	}
	return nil
}

// boundArguments: agentBoundArguments with the arguments and the result as maps.
func boundArguments(q *Request, name string, a doc, executionID string) (doc, error) {
	out, err := agentBoundArguments(q, name, recOf(a), executionID)
	if err != nil {
		return nil, err
	}
	return mapOf(norm(out)), nil
}

// docOf: v's JSON as a map.
func docOf(v any) doc {
	d := doc{}
	json.Unmarshal(jsonBytes(v), &d)
	return d
}

// openOf: d as the coordinator's open object.
func openOf(d doc) rawObject {
	var o rawObject
	json.Unmarshal(jsonBytes(d), &o)
	return o
}

// unconfirmed: the host has not confirmed the execution record's state.
func unconfirmed(e doc) bool { return e["observation"] == "unconfirmed" }
