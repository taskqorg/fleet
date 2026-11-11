package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// M is a JSON object; values decode with UseNumber so integers keep their text.
type M = map[string]any

// enc writes compact JSON with sorted keys and no HTML escaping (serde_json's to_string).
func enc(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return "null"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func dec(s string) (any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

func decM(s string) M {
	v, _ := dec(s)
	m, _ := v.(M)
	if m == nil {
		m = M{}
	}
	return m
}

// at walks object keys (serde v["a"]["b"]); missing is nil.
func jAt(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(M)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func jStr(v any, keys ...string) string  { s, _ := jAt(v, keys...).(string); return s }
func jIsStr(v any, keys ...string) bool  { _, ok := jAt(v, keys...).(string); return ok }
func jObj(v any, keys ...string) M       { m, _ := jAt(v, keys...).(M); return m }
func jArr(v any, keys ...string) []any   { a, _ := jAt(v, keys...).([]any); return a }
func jIsTrue(v any, keys ...string) bool { b, _ := jAt(v, keys...).(bool); return b }

// boolOr returns the boolean at keys, or def when it is not a boolean.
func jBoolOr(def bool, v any, keys ...string) bool {
	if b, ok := jAt(v, keys...).(bool); ok {
		return b
	}
	return def
}

// u64 returns a non-negative integer at keys.
func jU64(v any, keys ...string) (uint64, bool) {
	switch n := jAt(v, keys...).(type) {
	case json.Number:
		var x uint64
		if err := json.Unmarshal([]byte(n), &x); err == nil {
			return x, true
		}
	case float64:
		if n >= 0 && n == float64(uint64(n)) {
			return uint64(n), true
		}
	case int:
		if n >= 0 {
			return uint64(n), true
		}
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	case uint64:
		return n, true
	}
	return 0, false
}

// eq compares two JSON values by their encoding (serde Value equality).
func jEq(a, b any) bool { return enc(a) == enc(b) }

// clone deep-copies a JSON value.
func jClone(v any) any { c, _ := dec(enc(v)); return c }

func sortedKeys(m M) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func has(v any, k string) bool { m, ok := v.(M); _, in := m[k]; return ok && in }

func str(v any) (string, bool) { s, ok := v.(string); return s, ok }

// truth is Python truthiness for JSON values.
func truth(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case M:
		return len(x) > 0
	}
	return true
}
