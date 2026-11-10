package main

// The coordinator's JSON helpers: a doc is an open JSON object; these read values from it without panicking.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

type doc = map[string]any

// newID: a random UUID (version 4).
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// nowMs: the current time in Unix milliseconds.
func nowMs() int64 { return time.Now().UnixMilli() }

func astr(v any) string          { s, _ := v.(string); return s }
func mapOf(v any) map[string]any { m, _ := v.(map[string]any); return m }
func anys(v any) []any           { xs, _ := v.([]any); return xs }
func js(v any) string            { b, _ := json.Marshal(v); return string(b) }
func jsonBytes(v any) []byte     { b, _ := json.Marshal(v); return b }

// clone deep-copies a JSON value (maps and slices), so a stored record never aliases another.
func clone(v any) any { var out any; json.Unmarshal([]byte(js(v)), &out); return out }

// firstString: the first non-empty string among vs, else "".
func firstString(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// strList: the strings of a JSON array; other elements are skipped.
func strList(v any) []string {
	out := []string{}
	for _, x := range anys(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// jsonRoundTrip decodes the JSON encoding of v into out (a typed view of an open value).
func jsonRoundTrip(v any, out any) { b, _ := json.Marshal(v); json.Unmarshal(b, out) }
