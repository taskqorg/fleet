// Command gen writes Go types for every $defs entry of a contract JSON Schema (contracts/README.md "Supported subset").
// It fails on any keyword outside that subset, so a schema never yields a silently weaker type.
//
//	go run ./gen [-unions] <schema.json> <package> <out.go>
//
// Mapping: object -> struct (an open object names only the listed fields, as in TypeScript); an object without
// properties whose additionalProperties is a schema -> map[string]<that type>; string enum -> named string
// type with constants; {} -> json.RawMessage; integer -> int64; a property outside required, or with "null" in its type,
// is a pointer with omitempty (required nullable: pointer, always written); an object with unevaluatedProperties false
// gets Closed(), and the coordinator's strict decoder refuses keys it does not name; one with additionalProperties true
// keeps the other keys in Rest.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type schema map[string]any

var allowed = map[string]bool{"type": true, "properties": true, "required": true, "additionalProperties": true, "unevaluatedProperties": true, "items": true,
	"enum": true, "$ref": true, "minimum": true, "$comment": true, "const": true, "oneOf": true}

func check(s schema, where string) {
	for k := range s {
		if !allowed[k] {
			fail("%s: unsupported keyword %q", where, k)
		}
	}
}

var builtin = map[string]bool{"string": true, "int64": true, "float64": true, "bool": true, "json.RawMessage": true}

func fail(f string, a ...any) { fmt.Fprintf(os.Stderr, "gen: "+f+"\n", a...); os.Exit(1) }

func exported(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// types returns the JSON type and whether "null" is allowed.
func types(s schema, where string) (string, bool) {
	switch t := s["type"].(type) {
	case nil:
		return "", false
	case string:
		return t, false
	case []any:
		var one string
		null := false
		for _, x := range t {
			if x == "null" {
				null = true
			} else if one == "" {
				one, _ = x.(string)
			} else {
				fail("%s: more than one non-null type", where)
			}
		}
		return one, null
	}
	fail("%s: bad type", where)
	return "", false
}

func goType(s schema, where string) (string, bool) {
	check(s, where)
	if ref, ok := s["$ref"].(string); ok {
		// <file>.schema.json#/$defs/<Name>: a type of another contract file, in the same Go package.
		_, local, _ := strings.Cut(ref, "#")
		if !strings.HasPrefix(local, "/$defs/") || (!strings.HasPrefix(ref, "#") && !regexp.MustCompile(`^[a-z-]+\.schema\.json#`).MatchString(ref)) {
			fail("%s: $ref must be [<file>.schema.json]#/$defs/<Name>", where)
		}
		return exported(strings.TrimPrefix(local, "/$defs/")), false
	}
	if s["oneOf"] != nil {
		fail("%s: oneOf must be a $defs entry", where)
	}
	t, null := types(s, where)
	if _, ok := s["const"].(string); ok && (t == "" || t == "string") && !null {
		t = "string"
	} else if s["const"] != nil {
		fail("%s: const must be a string", where)
	}
	switch t {
	case "":
		return "json.RawMessage", false
	case "string":
		if s["enum"] != nil {
			fail("%s: an enum must be a $defs entry", where)
		}
		return "string", null
	case "integer":
		return "int64", null
	case "number":
		return "float64", null
	case "boolean":
		return "bool", null
	case "array":
		items, _ := s["items"].(map[string]any)
		if items == nil {
			fail("%s: array needs items", where)
		}
		it, _ := goType(items, where+".items")
		return "[]" + it, null
	case "object":
		if props, _ := s["properties"].(map[string]any); len(props) > 0 {
			fail("%s: an object with properties must be a $defs entry", where)
		}
		if values, ok := s["additionalProperties"].(map[string]any); ok {
			vt, null := goType(values, where+".*")
			if null {
				fail("%s: a map value cannot be null", where)
			}
			return "map[string]" + vt, null
		}
		return "map[string]json.RawMessage", null
	}
	fail("%s: unsupported type %q", where, t)
	return "", false
}

// toolSchema resolves canonical request references for self-contained MCP discovery.
func toolSchema(file string, value any) any {
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			source, name, _ := strings.Cut(ref, "#/$defs/")
			if source != "" {
				file = filepath.Join(filepath.Dir(file), source)
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				fail("%v", err)
			}
			var root struct {
				Defs map[string]any `json:"$defs"`
			}
			if err := json.Unmarshal(raw, &root); err != nil {
				fail("%v", err)
			}
			target, ok := root.Defs[name]
			if !ok {
				fail("missing schema %s", ref)
			}
			return toolSchema(file, target)
		}
		out := map[string]any{}
		for k, x := range v {
			out[k] = toolSchema(file, x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = toolSchema(file, x)
		}
		return out
	default:
		return value
	}
}

// authRule and agentRule: the values an operation's auth and agent take (contracts/README.md, Operations).
var (
	authRule  = regexp.MustCompile(`^(person|architect|owner|system|project:[A-Za-z][A-Za-z0-9.]*|holder:[a-z]+)$`)
	agentRule = regexp.MustCompile(`^(no|tool|bound:[a-z]+)$`)
)

func main() {
	// -unions: a discriminated oneOf becomes a Go struct (union); without it every oneOf stays json.RawMessage, as the
	// operation schemas' callers still read them.
	unions := len(os.Args) == 5 && os.Args[1] == "-unions"
	if unions {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	if len(os.Args) != 4 {
		fail("usage: gen [-unions] <schema.json> <package> <out.go>")
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail("%v", err)
	}
	// A data file (contracts/vocabulary.json), not a schema: its JSON as one constant, decoded by hand-written Go.
	if !strings.HasSuffix(os.Args[1], ".schema.json") {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			fail("%v", err)
		}
		compact, _ := json.Marshal(v)
		base := strings.TrimSuffix(filepath.Base(os.Args[1]), ".json")
		src := fmt.Sprintf("// Code generated by go/gen from %s. DO NOT EDIT.\n\npackage %s\n\n// %sJSON: %s as it is.\nconst %sJSON = %q\n", strings.TrimLeft(os.Args[1], "./"), os.Args[2], base, filepath.Base(os.Args[1]), base, compact)
		if err := os.WriteFile(os.Args[3], []byte(src), 0o644); err != nil {
			fail("%v", err)
		}
		return
	}
	var root struct {
		Defs       map[string]schema `json:"$defs"`
		Operations map[string]struct {
			Read, Long               bool
			Errors                   []int
			Auth, Agent, Description string
			Changes                  []string
		} `json:"operations"`
		Planned map[string]any             `json:"planned"`
		Routes  map[string]json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		fail("%v", err)
	}
	names := make([]string, 0, len(root.Defs))
	for n := range root.Defs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by go/gen from %s. DO NOT EDIT.\n\npackage %s\n\nimport \"encoding/json\"\n\nvar _ json.RawMessage\n\n", strings.TrimLeft(os.Args[1], "./"), os.Args[2])
	for _, n := range names {
		s, name := root.Defs[n], exported(n)
		check(s, n)
		if c, ok := s["$comment"].(string); ok {
			fmt.Fprintf(&b, "// %s: %s\n", name, c)
		}
		t, _ := types(s, n)
		props, _ := s["properties"].(map[string]any)
		switch {
		case t == "string" && s["enum"] != nil:
			fmt.Fprintf(&b, "type %s string\n\nconst (\n", name)
			for _, v := range s["enum"].([]any) {
				fmt.Fprintf(&b, "\t%s%s %s = %q\n", name, exported(v.(string)), name, v)
			}
			b.WriteString(")\n\n")
			fmt.Fprintf(&b, "// %sValues lists every %s in schema order.\nvar %sValues = []%s{", name, name, name, name)
			for _, v := range s["enum"].([]any) {
				fmt.Fprintf(&b, "%s%s, ", name, exported(v.(string)))
			}
			b.WriteString("}\n\n")
			fmt.Fprintf(&b, "// Valid: v is one of the schema's values.\nfunc (v %s) Valid() bool {\n\tfor _, x := range %sValues {\n\t\tif v == x {\n\t\t\treturn true\n\t\t}\n\t}\n\treturn false\n}\n\n", name, name)
		case t == "object" && len(props) > 0:
			req := map[string]bool{}
			rs, _ := s["required"].([]any)
			for _, r := range rs {
				req[r.(string)] = true
			}
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintf(&b, "type %s struct {\n", name)
			// Validate: a required list or object is not null, and every enum and nested struct present is valid (Check).
			// Key presence is the decoder's (the coordinator's exactKeys): a zero value cannot tell a missing field.
			var v bytes.Buffer
			for _, k := range keys {
				ft, null := goType(props[k].(map[string]any), n+"."+k)
				tag := k
				if !req[k] {
					tag += ",omitempty"
				}
				plain := !strings.HasPrefix(ft, "[]") && !strings.HasPrefix(ft, "map[") && ft != "json.RawMessage"
				if (null || !req[k]) && plain {
					ft = "*" + ft
				}
				f, named := exported(k), plain && !builtin[strings.TrimPrefix(ft, "*")]
				fmt.Fprintf(&b, "\t%s %s `json:%q`\n", f, ft, tag)
				switch {
				case req[k] && !null && !plain:
					fmt.Fprintf(&v, "\tif v.%s == nil {\n\t\treturn Missing(%q)\n\t}\n", f, k)
				}
				switch {
				case named && strings.HasPrefix(ft, "*"):
					fmt.Fprintf(&v, "\tif v.%s != nil {\n\t\tif err := Check(v.%s, %q); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", f, f, k)
				case named:
					fmt.Fprintf(&v, "\tif err := Check(&v.%s, %q); err != nil {\n\t\treturn err\n\t}\n", f, k)
				case strings.HasPrefix(ft, "[]") && !builtin[ft[2:]] && !strings.HasPrefix(ft[2:], "[]") && !strings.HasPrefix(ft[2:], "map["):
					fmt.Fprintf(&v, "\tfor i := range v.%s {\n\t\tif err := Check(&v.%s[i], %q); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n", f, f, k)
				}
			}
			open := s["additionalProperties"] == true
			if open {
				b.WriteString("\t// Rest: the keys the schema does not name (additionalProperties: true), kept through decode and encode.\n\tRest map[string]json.RawMessage `json:\"-\"`\n")
			}
			b.WriteString("}\n\n")
			if open {
				fmt.Fprintf(&b, "func (v %s) MarshalJSON() ([]byte, error) {\n\ttype plain %s\n\treturn marshalRest(plain(v), v.Rest)\n}\n\n", name, name)
				fmt.Fprintf(&b, "func (v *%s) UnmarshalJSON(b []byte) error {\n\ttype plain %s\n\tvar rest map[string]json.RawMessage\n\tif err := unmarshalRest(b, (*plain)(v), &rest); err != nil {\n\t\treturn err\n\t}\n\tv.Rest = rest\n\treturn nil\n}\n\n", name, name)
			}
			fmt.Fprintf(&b, "// Validate: the schema's required fields and enums (see Check).\nfunc (v *%s) Validate() error {\n%s\treturn nil\n}\n\n", name, v.String())
			if u, ok := s["unevaluatedProperties"]; ok && u != false {
				fail("%s: unevaluatedProperties must be false", n)
			}
			// Closed: the strict decoder refuses a request key the schema does not name (unevaluatedProperties false).
			// additionalProperties false alone only types: requests have always been sent with keys it does not list.
			if s["unevaluatedProperties"] == false {
				fmt.Fprintf(&b, "// Closed: unevaluatedProperties is false; a request key outside the schema is refused.\nfunc (*%s) Closed() bool { return true }\n\n", name)
			}
		case s["oneOf"] != nil:
			// A union of $refs: each member is its own struct. With a discriminator (one property every member fixes by
			// const) the union is a struct with one pointer per member (union); else it stays JSON (Go has no sum type).
			var members []string
			for i, x := range s["oneOf"].([]any) {
				m, _ := x.(map[string]any)
				if m == nil || m["$ref"] == nil || len(m) != 1 {
					fail("%s.oneOf[%d]: only $ref", n, i)
				}
				ref, _ := m["$ref"].(string)
				members = append(members, strings.TrimPrefix(ref, "#/$defs/"))
			}
			if !unions || !union(&b, n, members, root.Defs) {
				fmt.Fprintf(&b, "type %s = json.RawMessage\n\n", name)
			}
		default:
			if r, ok := s["required"].([]any); ok && len(r) > 0 {
				fail("%s: required without properties", n)
			}
			ft, _ := goType(s, n)
			fmt.Fprintf(&b, "type %s = %s\n\n", name, ft)
		}
	}
	for n := range root.Planned {
		if _, ok := root.Operations[n]; ok {
			fail("operation %s is both planned and built: remove it from planned", n)
		}
		for _, d := range []string{"Request", "Reply"} {
			if root.Defs[exported(n)+d] == nil {
				fail("planned operation %s has no $defs %s%s", n, exported(n), d)
			}
		}
	}
	if len(root.Operations) > 0 {
		ops := make([]string, 0, len(root.Operations))
		for n := range root.Operations {
			for _, d := range []string{"Request", "Reply"} {
				if root.Defs[exported(n)+d] == nil {
					fail("operation %s has no $defs %s%s", n, exported(n), d)
				}
			}
			if o := root.Operations[n]; !authRule.MatchString(o.Auth) || !agentRule.MatchString(o.Agent) || strings.TrimSpace(o.Description) == "" {
				fail("operation %s: auth (person, project:<path>, holder:<kind>, architect, owner, system), agent (no, tool, bound:<binder>) and description are required", n)
			}
			ops = append(ops, n)
		}
		sort.Strings(ops)
		// Each domain file adds its operations to the one Operations map (go/contract/contract.go); a name in two files fails at start.
		b.WriteString("func init() {\n\tfor n, o := range map[string]Operation{\n")
		for _, n := range ops {
			o := root.Operations[n]
			raw, err := json.Marshal(toolSchema(os.Args[1], map[string]any(root.Defs[exported(n)+"Request"])))
			if err != nil {
				fail("%v", err)
			}
			changes := ""
			if len(o.Changes) > 0 {
				changes = fmt.Sprintf(", Changes: %#v", o.Changes)
			}
			fmt.Fprintf(&b, "\t\t%q: {Read: %v, Long: %v, Errors: %#v, Auth: %q, Agent: %q, Description: %q%s, InputSchema: json.RawMessage(%q)},\n", n, o.Read, o.Long, o.Errors, o.Auth, o.Agent, o.Description, changes, string(raw))
		}
		b.WriteString("\t} {\n\t\taddOperation(n, o)\n\t}\n}\n\n")
	}
	if len(root.Routes) > 0 {
		routes(&b, root.Routes, root.Defs)
	}
	out, err := format.Source(b.Bytes())
	if err != nil {
		fail("format: %v\n%s", err, b.String())
	}
	if err := os.WriteFile(os.Args[3], out, 0o644); err != nil {
		fail("%v", err)
	}
}

// route: one entry of a schema's routes map (contracts/README.md, Routes).
type route struct {
	Request, Reply, Decode string
	Long                   bool
}

// routes writes one contract.Route<Name> descriptor per host route, named from its path below /api/hosts/, and HostRoutes.
func routes(b *bytes.Buffer, raw map[string]json.RawMessage, defs map[string]schema) {
	paths, rs := []string{}, map[string]route{}
	for p, x := range raw {
		if p == "$comment" {
			continue
		}
		var r route
		d := json.NewDecoder(bytes.NewReader(x))
		var keys map[string]any
		if json.Unmarshal(x, &keys) != nil || d.Decode(&r) != nil {
			fail("route %s: an object", p)
		}
		for k := range keys {
			if k != "$comment" && k != "request" && k != "reply" && k != "long" && k != "decode" {
				fail("route %s: unsupported key %q", p, k)
			}
		}
		paths, rs[p] = append(paths, p), r
	}
	sort.Strings(paths)
	def := func(p, ref string) string {
		_, local, cross := strings.Cut(ref, "#/$defs/")
		if !cross {
			local = ref
			if defs[local] == nil {
				fail("route %s: no $defs %s", p, ref)
			}
		}
		return exported(local)
	}
	b.WriteString("// Host routes (the routes map): request, reply (any: not described), long, and plain (decoded only, no required-key check).\nvar (\n")
	for _, p := range paths {
		r := rs[p]
		if !strings.HasPrefix(p, "/api/hosts/") || r.Request == "" || (r.Decode != "plain" && r.Decode != "checked") {
			fail("route %s: a /api/hosts/ path with request and decode (plain or checked)", p)
		}
		rep := "any"
		if r.Reply != "" {
			rep = def(p, r.Reply)
		}
		fmt.Fprintf(b, "\tRoute%s = HostRoute[%s, %s]{Path: %q, Long: %v, Plain: %v}\n", exported(strings.ReplaceAll(strings.TrimPrefix(p, "/api/hosts/"), "/", "-")), def(p, r.Request), rep, p, r.Long, r.Decode == "plain")
	}
	b.WriteString(")\n\n// HostRoutes: every host route path.\nvar HostRoutes = []string{\n")
	for _, p := range paths {
		fmt.Fprintf(b, "\t%q,\n", p)
	}
	b.WriteString("}\n\n")
}

// union writes a discriminated oneOf: a struct with one pointer per member (field: the member name without the union's
// name as suffix) and Raw, the JSON it was decoded from, written back unchanged so a decoded value passes on byte for
// byte; a discriminator value no member has keeps only Raw, so a newer sender's kind still decodes. A property every
// member names with one Go type gets a getter. It reports false when the members share no const property.
func union(b *bytes.Buffer, n string, members []string, defs map[string]schema) bool {
	name := exported(n)
	// The discriminator: the first property (in name order) that every member fixes by a const string.
	first, _ := defs[members[0]]["properties"].(map[string]any)
	names := make([]string, 0, len(first))
	for k := range first {
		names = append(names, k)
	}
	sort.Strings(names)
	tag, values := "", map[string]string{}
	for _, k := range names {
		vs, all := map[string]string{}, true
		for _, m := range members {
			props, _ := defs[m]["properties"].(map[string]any)
			p, _ := props[k].(map[string]any)
			c, ok := p["const"].(string)
			if !ok {
				all = false
				break
			}
			if _, dup := vs[c]; dup {
				fail("%s: two members with %s %q", n, k, c)
			}
			vs[c] = m
		}
		if all {
			tag, values = k, vs
			break
		}
	}
	if tag == "" {
		return false
	}
	field := func(m string) string {
		if f := strings.TrimSuffix(exported(m), name); f != "" {
			return f
		}
		return exported(m)
	}
	fmt.Fprintf(b, "type %s struct {\n", name)
	for _, m := range members {
		fmt.Fprintf(b, "\t%s *%s\n", field(m), exported(m))
	}
	fmt.Fprintf(b, "\t// Raw: the JSON this value was decoded from, written back unchanged (clear it after changing a member); alone when its %s is no member's.\n\tRaw json.RawMessage\n}\n\n", tag)
	fmt.Fprintf(b, "func (v %s) MarshalJSON() ([]byte, error) {\n\tswitch {\n\tcase v.Raw != nil:\n\t\treturn v.Raw, nil\n", name)
	for _, m := range members {
		fmt.Fprintf(b, "\tcase v.%s != nil:\n\t\treturn json.Marshal(v.%s)\n", field(m), field(m))
	}
	b.WriteString("\t}\n\treturn []byte(\"null\"), nil\n}\n\n")
	fmt.Fprintf(b, "func (v *%s) UnmarshalJSON(b []byte) error {\n\tvar d struct {\n\t\tTag string `json:%q`\n\t}\n\tif err := json.Unmarshal(b, &d); err != nil {\n\t\treturn err\n\t}\n\t*v = %s{Raw: append(json.RawMessage(nil), b...)}\n\tswitch d.Tag {\n", name, tag, name)
	keys := make([]string, 0, len(values))
	for c := range values {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	for _, c := range keys {
		fmt.Fprintf(b, "\tcase %q:\n\t\tv.%s = new(%s)\n\t\treturn json.Unmarshal(b, v.%s)\n", c, field(values[c]), exported(values[c]), field(values[c]))
	}
	b.WriteString("\t}\n\treturn nil\n}\n\n")
	fmt.Fprintf(b, "// Value: the member that is set, nil for a %s no member has.\nfunc (v *%s) Value() interface{ Validate() error } {\n\tswitch {\n", tag, name)
	for _, m := range members {
		fmt.Fprintf(b, "\tcase v.%s != nil:\n\t\treturn v.%s\n", field(m), field(m))
	}
	b.WriteString("\t}\n\treturn nil\n}\n\n")
	fmt.Fprintf(b, "// Validate: the set member's Validate; a %s no member has passes (its reader refuses it), an empty value does not.\nfunc (v *%s) Validate() error {\n\tif x := v.Value(); x != nil {\n\t\treturn x.Validate()\n\t}\n\tif v.Raw != nil {\n\t\treturn nil\n\t}\n\treturn Missing(%q)\n}\n\n", tag, name, tag)
	// Getters: a property every member names with the same Go type (the envelope), from the member that is set.
	req := func(m, k string) bool {
		rs, _ := defs[m]["required"].([]any)
		for _, r := range rs {
			if r == k {
				return true
			}
		}
		return false
	}
	gtype := func(m, k string) string {
		props := defs[m]["properties"].(map[string]any)
		p, ok := props[k].(map[string]any)
		if !ok {
			return ""
		}
		t, null := goType(p, m+"."+k)
		if (null || !req(m, k)) && !strings.HasPrefix(t, "[]") && !strings.HasPrefix(t, "map[") && t != "json.RawMessage" {
			t = "*" + t
		}
		return t
	}
	ks := make([]string, 0, len(first))
	for k := range first {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		t := gtype(members[0], k)
		common := true
		for _, m := range members[1:] {
			if gtype(m, k) != t {
				common = false
			}
		}
		if !common {
			continue
		}
		for _, m := range members {
			if field(m) == exported(k) {
				fail("%s: getter %s collides with member %s", n, exported(k), m)
			}
		}
		fmt.Fprintf(b, "// %s: the set member's %s (zero when no member is set).\nfunc (v %s) %s() (out %s) {\n\tswitch {\n", exported(k), k, name, exported(k), t)
		for _, m := range members {
			fmt.Fprintf(b, "\tcase v.%s != nil:\n\t\treturn v.%s.%s\n", field(m), field(m), exported(k))
		}
		b.WriteString("\t}\n\treturn out\n}\n\n")
	}
	return true
}
