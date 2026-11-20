package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
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
	// A second Decode, not More: More is false before a stray ']' or '}'.
	if d.Decode(new(json.RawMessage)) != io.EOF {
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

// optStr: an optional contract string field; "" is absent.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// decRaw: a JSON-RPC field as a journal value (numbers kept as written); nil when absent or not JSON.
func decRaw(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	v, _ := dec(string(raw))
	return v
}

// exactKeys refuses a JSON text in which one object names a key twice, or names two keys that differ only in case: a
// typed read (encoding/json matches field names case-insensitively, last duplicate wins) and another reader of the same
// bytes could then see different values.
func exactKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	type frame struct {
		object bool
		key    bool // the next token in an object is a key
		seen   map[string]bool
	}
	var stack []*frame
	for {
		t, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.key {
			if k, ok := t.(string); ok {
				f := strings.ToLower(k)
				if top.seen[f] {
					return errors.New("duplicate or case-variant key: " + k)
				}
				top.seen[f] = true
				top.key = false
				continue
			}
		}
		if top != nil && top.object && !top.key {
			top.key = true // this token is the key's value (or opens it)
		}
		switch t {
		case json.Delim('{'):
			stack = append(stack, &frame{object: true, key: true, seen: map[string]bool{}})
		case json.Delim('['):
			stack = append(stack, &frame{})
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		}
	}
}

// The one way the executor reads received JSON into records: typed (a decoded value) or typedRaw (bytes). Record fields
// are plain Go types (string, *string, bool, *bool, numbers, []T, map[string]T, nested structs) and jsonValue for an
// open or varying value. Keys match a field's json name exactly (encoding/json alone also takes a key differing in case,
// and the last of two duplicates), and a value of another JSON type than the field's reads as absent, so a decode never
// fails on a record of an unexpected shape.

// typed decodes a received JSON value (a decoded M, or a jsonValue field of another record) into T. encoding/json
// decodes the typed fields from fit's reduction of v: only keys that name a field exactly, and only values of the
// field's JSON type, so a field of another type reads as absent ("" for a string, nil for a pointer), as the map
// accessors read it. Open values (any, jsonValue) are then set from v itself, so they keep their decoded form.
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

// typedRaw decodes JSON text into T as typed does (numbers kept as json.Number); text that is not one JSON value is T's
// zero value.
func typedRaw[T any](raw []byte) T {
	v, _ := dec(string(raw))
	return typed[T](v)
}

// jsonValue: an open field of a record, as decoded: Set when the key is present (even as null), V its value.
type jsonValue struct {
	Set bool
	V   any
}

// MarshalJSON writes the value as decoded (a json.Number as written); an absent value is null.
func (j jsonValue) MarshalJSON() ([]byte, error) {
	if !j.Set {
		return []byte("null"), nil
	}
	return []byte(enc(j.V)), nil
}

// valStr: an open field's string, and whether it is one (jIsStr).
func valStr(j jsonValue) (string, bool) {
	s, ok := j.V.(string)
	return s, ok
}

var jsonValueType = reflect.TypeFor[jsonValue]()

func fieldName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" || !f.IsExported() {
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
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8,
		reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		switch v.(type) {
		case json.Number, float64, float32, int, int64, int32, uint, uint64, uint32:
			// a number the field holds exactly (an integer field refuses 1.5, -1 for an unsigned one, or an overflow)
			if json.Unmarshal([]byte(enc(v)), reflect.New(t).Interface()) == nil {
				return v
			}
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
		if !ok || rv.IsNil() {
			return
		}
		switch rv.Type().Elem() {
		case jsonValueType:
			for k, x := range m {
				rv.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(jsonValue{true, x}))
			}
		default:
			for k, x := range m {
				if rv.Type().Elem().Kind() == reflect.Interface {
					if x != nil {
						rv.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(x))
					}
				} else if e := rv.MapIndex(reflect.ValueOf(k)); e.IsValid() { // a map element is not addressable
					c := reflect.New(e.Type()).Elem()
					c.Set(e)
					fill(c, x)
					rv.SetMapIndex(reflect.ValueOf(k), c)
				}
			}
		}
	}
}
