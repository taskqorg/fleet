package main

import "encoding/json"

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

// docOf: v's JSON as a map.
func docOf(v any) doc {
	d := doc{}
	json.Unmarshal(jsonBytes(v), &d)
	return d
}

// unconfirmed: the host has not confirmed the execution record's state.
func unconfirmed(e doc) bool { return e["observation"] == "unconfirmed" }
