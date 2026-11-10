package main

// JSON helpers for the MCP subcommands and harness sessions, plus Python json.loads error messages: the stdio servers
// report a malformed line exactly as the former Python adapter did (captured in tests/executor/fixtures/mcp.json).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type obj = map[string]any

// get walks object keys; a missing key or non-object yields nil.
func get(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(obj)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func has(v any, k string) bool { m, ok := v.(obj); _, in := m[k]; return ok && in }

func str(v any) (string, bool) { s, ok := v.(string); return s, ok }

func sv(v any) string { s, _ := v.(string); return s }

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
	case obj:
		return len(x) > 0
	}
	return true
}

func decode(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("trailing data")
	}
	return v, nil
}

func encode(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return "null"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func clone(v any) any { c, _ := decode([]byte(encode(v))); return c }

// pyJSONError returns Python's json.loads message for raw, or "" when raw parses.
func pyJSONError(raw string) string {
	s := []rune(raw)
	p := &pyParser{s: s}
	pos, msg := func() (pos int, msg string) {
		defer func() {
			if e := recover(); e != nil {
				f := e.(pyFail)
				pos, msg = f.pos, f.msg
			}
		}()
		end := p.value(p.ws(0))
		if end = p.ws(end); end != len(s) {
			return end, "Extra data"
		}
		return -1, ""
	}()
	if pos < 0 {
		return ""
	}
	line := strings.Count(string(s[:pos]), "\n") + 1
	col := pos + 1
	if i := strings.LastIndex(string(s[:pos]), "\n"); i >= 0 {
		col = pos - len([]rune(string(s[:i])))
	}
	return fmt.Sprintf("%s: line %d column %d (char %d)", msg, line, col, pos)
}

type pyFail struct {
	pos int
	msg string
}
type pyParser struct{ s []rune }

func (p *pyParser) fail(pos int, msg string) { panic(pyFail{pos, msg}) }
func (p *pyParser) at(i int) rune {
	if i < len(p.s) {
		return p.s[i]
	}
	return 0
}
func (p *pyParser) ws(i int) int {
	for i < len(p.s) && strings.ContainsRune(" \t\n\r", p.s[i]) {
		i++
	}
	return i
}
func (p *pyParser) lit(i int, w string) bool { return strings.HasPrefix(string(p.s[min(i, len(p.s)):]), w) }

func (p *pyParser) value(i int) int {
	switch c := p.at(i); {
	case i >= len(p.s):
		p.fail(i, "Expecting value")
	case c == '"':
		return p.str(i + 1)
	case c == '{':
		return p.object(i + 1)
	case c == '[':
		return p.array(i + 1)
	}
	for _, w := range []string{"null", "true", "false", "NaN", "Infinity", "-Infinity"} {
		if p.lit(i, w) {
			return i + len(w)
		}
	}
	return p.number(i)
}

func digits(p *pyParser, i int) int {
	for p.at(i) >= '0' && p.at(i) <= '9' {
		i++
	}
	return i
}

func (p *pyParser) number(i int) int {
	j := i
	if p.at(j) == '-' {
		j++
	}
	switch c := p.at(j); {
	case c == '0':
		j++
	case c >= '1' && c <= '9':
		j = digits(p, j)
	default:
		p.fail(i, "Expecting value")
	}
	if p.at(j) == '.' && p.at(j+1) >= '0' && p.at(j+1) <= '9' {
		j = digits(p, j+1)
	}
	if c := p.at(j); c == 'e' || c == 'E' {
		k := j + 1
		if p.at(k) == '+' || p.at(k) == '-' {
			k++
		}
		if p.at(k) >= '0' && p.at(k) <= '9' {
			j = digits(p, k)
		}
	}
	return j
}

func (p *pyParser) str(i int) int {
	begin := i - 1
	for {
		if i >= len(p.s) {
			p.fail(begin, "Unterminated string starting at")
		}
		c := p.s[i]
		switch {
		case c == '"':
			return i + 1
		case c < 0x20:
			p.fail(i, "Invalid control character at")
		case c == '\\':
			if i+1 >= len(p.s) {
				p.fail(begin, "Unterminated string starting at")
			}
			e := p.s[i+1]
			if e == 'u' {
				for k := i + 2; k < i+6; k++ {
					if !strings.ContainsRune("0123456789abcdefABCDEF", p.at(k)) {
						p.fail(i, "Invalid \\uXXXX escape")
					}
				}
				i += 6
				continue
			}
			if !strings.ContainsRune(`"\/bfnrt`, e) {
				p.fail(i, "Invalid \\escape")
			}
			i += 2
		default:
			i++
		}
	}
}

func (p *pyParser) object(i int) int {
	i = p.ws(i)
	if p.at(i) == '}' && i < len(p.s) {
		return i + 1
	}
	for {
		if p.at(i) != '"' || i >= len(p.s) {
			p.fail(i, "Expecting property name enclosed in double quotes")
		}
		i = p.ws(p.str(i + 1))
		if p.at(i) != ':' || i >= len(p.s) {
			p.fail(i, "Expecting ':' delimiter")
		}
		i = p.ws(p.value(p.ws(i + 1)))
		switch {
		case i < len(p.s) && p.s[i] == '}':
			return i + 1
		case i < len(p.s) && p.s[i] == ',':
			i = p.ws(i + 1)
		default:
			p.fail(i, "Expecting ',' delimiter")
		}
	}
}

func (p *pyParser) array(i int) int {
	i = p.ws(i)
	if p.at(i) == ']' && i < len(p.s) {
		return i + 1
	}
	for {
		i = p.ws(p.value(i))
		switch {
		case i < len(p.s) && p.s[i] == ']':
			return i + 1
		case i < len(p.s) && p.s[i] == ',':
			i = p.ws(i + 1)
		default:
			p.fail(i, "Expecting ',' delimiter")
		}
	}
}
