package main

// The coordinator's JSON helpers: ids, times, encoding and reading values of open JSON without panicking.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
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
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return jsonNulFree(b) }

// jsonNulFree replaces each JSON escape \u0000 with \ufffd. PostgreSQL jsonb refuses U+0000 ("unsupported Unicode
// escape sequence", 22P05), and a host event can carry one from a tool's output: the whole batch then failed with 500
// and its host retried it every few minutes (2026-11-24). An escaped backslash before "u0000" is text, not an escape,
// and stays. jsonBytes applies it, so every jsonb value the store writes (events, receipts' errors, tool results)
// carries U+FFFD in its place.
func jsonNulFree(b []byte) []byte {
	const nul, repl = `\u0000`, `\ufffd`
	if !bytes.Contains(b, []byte(nul)) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			out = append(out, b[i])
			continue
		}
		if bytes.HasPrefix(b[i:], []byte(nul)) {
			out = append(out, repl...)
			i += len(nul) - 1
			continue
		}
		// Any other escape is copied whole, so the backslash it starts never begins a match.
		out = append(out, b[i])
		if i+1 < len(b) {
			i++
			out = append(out, b[i])
		}
	}
	return out
}

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

func decode(raw json.RawMessage, v any) error {
	if json.Unmarshal(raw, v) != nil {
		return fail(400, "Invalid body")
	}
	return nil
}

type HTTPError struct {
	Code  int
	Msg   string
	Extra any // a struct whose JSON fields join the error body (a 409's DefinitionConflict or reason, an upload's received)
	// Refused marks a refusal by design (a removed host): its span outcome is refused, not error.
	Refused bool
}

// numOK: a JSON number or a SQL integer column (pgx scans integer as int32, smallint as int16).
func numOK(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case int16:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

// num: a finite number of any JSON or SQL integer or float type, or a numeric text (a ->> read); 0 otherwise. numOK
// refuses text. A SQL numeric column needs ::float8 (pgx scans it as pgtype.Numeric).
func num(v any) float64 {
	f, ok := numOK(v)
	if s, isText := v.(string); isText {
		f, ok = parseNum(s)
	}
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

func parseNum(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// msTime: a millisecond time (JSON or SQL number) as a timestamp, or nil.
func msTime(v any) any {
	f, ok := numOK(v)
	if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil
	}
	return time.UnixMilli(int64(f)).UTC()
}

func intOK(v any) (int64, bool) {
	f, ok := numOK(v)
	if !ok || f != float64(int64(f)) || f > 1<<53 || f < -(1<<53) {
		return 0, false
	}
	return int64(f), true
}

func keysOnly(b rawObject, allowed ...string) bool {
	for k := range b {
		ok := false
		for _, a := range allowed {
			if k == a {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
