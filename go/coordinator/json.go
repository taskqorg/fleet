package main

// The coordinator's JSON helpers: ids, times, encoding and reading values of open JSON without panicking.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

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

func js(v any) string        { b, _ := json.Marshal(v); return string(b) }
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }

// clone deep-copies a JSON value (maps and slices), so a stored record never aliases another.
func clone(v any) any { var out any; json.Unmarshal(jsonBytes(v), &out); return out }

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
	xs, _ := v.([]any)
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// openObject: an open JSON object read from storage or a file, each value as openJSON writes it, so what it is sent
// or hashed as does not follow the stored spelling. Absent or null is nil; a value that is not an object is an error.
func openObject(raw []byte) (rawObject, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var o rawObject
	return o, json.Unmarshal(jsonBytes(v), &o)
}

// openJSON: v as a value of an open object: the encoding of its decoded value (keys sorted at every level, numbers as
// float64), the same whatever struct or spelling it came from.
func openJSON(v any) json.RawMessage { return jsonBytes(norm(v)) }

// jsonRoundTrip decodes the JSON encoding of v into out (a typed view of an open value).
func jsonRoundTrip(v any, out any) { b, _ := json.Marshal(v); json.Unmarshal(b, out) }

type HTTPError struct {
	Code  int
	Msg   string
	Extra any // a struct whose JSON fields join the error body (a 409's DefinitionConflict or reason, an upload's received)
	// Refused marks a refusal by design (a removed host): its span outcome is refused, not error.
	Refused bool
}
