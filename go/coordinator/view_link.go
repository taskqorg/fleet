package main

// view_link (docs/designs/fleet-view-urls-and-list-path.md §1): the canonical operator-app link for a view, or the view
// of a link. A Go port of parseView, formatView and viewProblem in packages/view-url/view-url.ts over the same words
// (contracts/vocabulary.json); the outside test view.test.ts compares both codecs on the same views.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"fleet/contract"
)

// The words of the view grammar come from contracts/vocabulary.json, the file packages/view-url reads.
var (
	viewNames     = contract.Vocabulary.Views
	deliveryStage = contract.Vocabulary.Lists["delivery"].States // delivery_status's stages, the Delivery list's state filter
	artifactState = contract.Vocabulary.Lists["results"].States
	// viewOrder: each list view's groups and sorts (task_page, sessions_list), each sort also descending, and defaults.
	viewOrder = map[string]listOrder{
		"tasks":         {slices.Sorted(maps.Keys(taskGroups)), bothWays(taskSorts), defaultTaskGroup, defaultTaskSort},
		"conversations": {slices.Sorted(maps.Keys(sessionGroups)), bothWays(sessionSorts), defaultSessionGroup, defaultSessionSort},
		// Delivery groups and sorts its rows in the browser (delivery_status is read whole).
		"delivery": {slices.Sorted(slices.Values(contract.Vocabulary.Lists["delivery"].Groups)), slices.Sorted(slices.Values(contract.Vocabulary.Lists["delivery"].Sorts)), contract.Vocabulary.Lists["delivery"].Group, contract.Vocabulary.Lists["delivery"].Sort},
	}
	viewKeys = func() map[string][]string {
		out := map[string][]string{}
		for name, l := range contract.Vocabulary.Lists {
			out[name] = slices.Sorted(slices.Values(l.Keys))
		}
		return out
	}()
	schemeURL = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
)

type listOrder struct {
	groups, sorts []string
	group, sort   string
}

func bothWays(sorts map[string]string) []string {
	out := []string{}
	for k := range sorts {
		out = append(out, k, "-"+k)
	}
	return slices.Sorted(slices.Values(out))
}

// view: one operator-app view state; Project holds nil for "no project".
type view struct {
	View     string            `json:"view"`
	Focus    string            `json:"focus,omitempty"`
	Group    string            `json:"group,omitempty"`
	Preset   string            `json:"preset,omitempty"`
	Project  []*string         `json:"project,omitempty"`
	Q        string            `json:"q,omitempty"`
	Sel      []string          `json:"sel,omitempty"`
	Sort     string            `json:"sort,omitempty"`
	State    []string          `json:"state,omitempty"`
	Archived bool              `json:"archived,omitempty"`
	Extra    map[string]string `json:"extra,omitempty"`
}

func viewStates(name string) []string {
	switch name {
	case "tasks":
		return listStates // task states, held included
	case "results":
		return artifactState
	case "delivery":
		return deliveryStage
	}
	return sessionStates
}

// viewEnc is encodeURIComponent with ':' and '@' put back.
func viewEnc(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'():@", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// viewDec is decodeURIComponent ('+' is a space in a query value); an invalid escape leaves the value as it is.
func viewDec(s string, query bool) string {
	if query {
		s = strings.ReplaceAll(s, "+", " ")
	}
	if d, err := url.PathUnescape(s); err == nil && utf8.ValidString(d) { // decodeURIComponent throws on both
		return d
	}
	return s
}

// ponytail: byte order; JS sorts by UTF-16 code units, so the two differ only for ids mixing astral and high-BMP characters.
func sortedSet(xs []string) []string {
	out := slices.Clone(xs)
	sort.Strings(out)
	return slices.Compact(out)
}

func sortProjects(ps []*string) []*string {
	ids, none := []string{}, false
	for _, p := range ps {
		if p == nil {
			none = true
		} else {
			ids = append(ids, *p)
		}
	}
	out := []*string{}
	if none {
		out = append(out, nil)
	}
	for _, id := range sortedSet(ids) {
		out = append(out, &id)
	}
	return out
}

// jsonKind: the first byte of a JSON value ('{', '[', '"', 't', 'f', 'n' or a digit), 0 for none.
func jsonKind(raw json.RawMessage) byte {
	if t := bytes.TrimSpace(raw); len(t) > 0 {
		return t[0]
	}
	return 0
}

// jsonString: raw as a JSON string; ok is false for any other JSON type.
func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if jsonKind(raw) != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// viewProblem checks a view object from an agent: unknown keys and bad values are errors, never dropped.
func viewProblem(v any) string {
	raw := json.RawMessage(jsonBytes(v))
	var o map[string]json.RawMessage
	if jsonKind(raw) != '{' || json.Unmarshal(raw, &o) != nil {
		return "view must be an object"
	}
	name, _ := jsonString(o["view"])
	if !slices.Contains(viewNames, name) {
		return "view.view must be one of " + strings.Join(viewNames, ", ")
	}
	// strs: a list of non-empty strings.
	strs := func(x json.RawMessage) ([]string, bool) {
		var xs []json.RawMessage
		if jsonKind(x) != '[' || json.Unmarshal(x, &xs) != nil {
			return nil, false
		}
		out := []string{}
		for _, i := range xs {
			if s, isStr := jsonString(i); !isStr || s == "" {
				return nil, false
			} else {
				out = append(out, s)
			}
		}
		return out, true
	}
	keys := slices.Sorted(maps.Keys(o))
	for _, k := range keys {
		x := o[k]
		switch {
		case k == "view":
		case k == "focus":
			if s, _ := jsonString(x); s == "" {
				return "focus must be a non-empty string"
			}
		case k == "extra":
			var m map[string]json.RawMessage
			ok := jsonKind(x) == '{' && json.Unmarshal(x, &m) == nil
			for _, i := range m {
				if _, isStr := jsonString(i); !isStr {
					ok = false
				}
			}
			if !ok {
				return "extra must map keys to strings"
			}
		case !slices.Contains(viewKeys[name], k):
			return k + " is not a key of the " + name + " view"
		case k == "state":
			states := viewStates(name)
			xs, ok := strs(x)
			for _, i := range xs {
				ok = ok && slices.Contains(states, i)
			}
			if !ok {
				return "state must be a list of: " + strings.Join(states, ", ")
			}
		case k == "group":
			if s, _ := jsonString(x); !slices.Contains(viewOrder[name].groups, s) {
				return "group must be one of " + strings.Join(viewOrder[name].groups, ", ")
			}
		case k == "sort":
			if s, _ := jsonString(x); !slices.Contains(viewOrder[name].sorts, s) {
				return "sort must be one of " + strings.Join(viewOrder[name].sorts, ", ")
			}
		case k == "project":
			var xs []json.RawMessage
			ok := jsonKind(x) == '[' && json.Unmarshal(x, &xs) == nil
			for _, i := range xs {
				if s, isStr := jsonString(i); jsonKind(i) != 'n' && (!isStr || s == "") {
					ok = false
				}
			}
			if !ok {
				return "project must be a list of project ids or null"
			}
		case k == "sel":
			if _, ok := strs(x); !ok {
				return "sel must be a list of ids"
			}
		case k == "q" || k == "preset":
			if _, ok := jsonString(x); !ok {
				return k + " must be a string"
			}
		case k == "archived":
			if t := string(bytes.TrimSpace(x)); t != "true" && t != "false" {
				return "archived must be true or false"
			}
		}
	}
	return ""
}

// viewOf converts a view object that passed viewProblem (its keys are the view's fields; a null project is "no project").
func viewOf(raw json.RawMessage) view {
	var v view
	json.Unmarshal(raw, &v)
	return v
}

// parseView reads a path with query or a full URL; ok is false when it is not a view or a full URL is on another
// origin than origin (any origin when origin is "").
func parseView(input, origin string) (view, bool) {
	s := input
	if schemeURL.MatchString(s) {
		u, err := url.Parse(s)
		if err != nil {
			return view{}, false
		}
		if origin != "" && originOf(u) != originOf(must(url.Parse(origin))) {
			return view{}, false
		}
		s = u.EscapedPath()
		if u.RawQuery != "" {
			s += "?" + u.RawQuery
		}
	}
	s, _, _ = strings.Cut(s, "#")
	path, query, _ := strings.Cut(s, "?")
	seg := strings.Split(path, "/")
	if seg[0] != "" || len(seg) > 3 || len(seg) == 3 && seg[2] == "" || len(seg) < 2 {
		return view{}, false
	}
	v := view{View: viewDec(seg[1], false)}
	if !slices.Contains(viewNames, v.View) {
		return view{}, false
	}
	if len(seg) == 3 {
		v.Focus = viewDec(seg[2], false)
	}
	items := func(raw string) []string {
		out := []string{}
		for _, x := range strings.Split(raw, ",") {
			if d := viewDec(x, true); d != "" {
				out = append(out, d)
			}
		}
		return out
	}
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		k, raw, _ := strings.Cut(part, "=")
		k = viewDec(k, true)
		if !slices.Contains(viewKeys[v.View], k) {
			if v.Extra == nil {
				v.Extra = map[string]string{}
			}
			v.Extra[k] = viewDec(raw, true)
			continue
		}
		switch k {
		case "group":
			if g := viewDec(raw, true); slices.Contains(viewOrder[v.View].groups, g) {
				v.Group = g
			}
		case "sort":
			if x := viewDec(raw, true); slices.Contains(viewOrder[v.View].sorts, x) {
				v.Sort = x
			}
		case "preset":
			if x := viewDec(raw, true); x != "" {
				v.Preset = x
			}
		case "q":
			if x := viewDec(raw, true); x != "" {
				v.Q = x
			}
		case "archived":
			if raw == "1" {
				v.Archived = true
			}
		case "sel":
			if xs := items(raw); len(xs) > 0 {
				v.Sel = sortedSet(append(v.Sel, xs...))
			}
		case "state":
			xs := []string{}
			for _, x := range items(raw) {
				if slices.Contains(viewStates(v.View), x) {
					xs = append(xs, x)
				}
			}
			if len(xs) > 0 {
				v.State = sortedSet(append(v.State, xs...))
			}
		case "project":
			// A bare '-' is "no project"; a project whose id is '-' is written %2D.
			xs := []*string{}
			for _, x := range strings.Split(raw, ",") {
				if x == "-" {
					xs = append(xs, nil)
				} else if d := viewDec(x, true); d != "" {
					xs = append(xs, &d)
				}
			}
			if len(xs) > 0 {
				v.Project = sortProjects(append(v.Project, xs...))
			}
		}
	}
	return v, true
}

// originOf is a URL's origin as WHATWG writes it: lower-case scheme and host, default port dropped; "" for nil.
func originOf(u *url.URL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	scheme := strings.ToLower(u.Scheme)
	host = strings.TrimSuffix(host, map[string]string{"https": ":443", "http": ":80"}[scheme])
	return scheme + "://" + host
}

func must(u *url.URL, err error) *url.URL {
	if err != nil {
		return nil
	}
	return u
}

// formatView: the canonical path and query, keys sorted, defaults omitted.
func formatView(v view) string {
	ps := map[string]string{}
	keys := viewKeys[v.View]
	has := func(k string) bool { return slices.Contains(keys, k) }
	join := func(xs []string) string {
		out := []string{}
		for _, x := range sortedSet(xs) {
			out = append(out, viewEnc(x))
		}
		return strings.Join(out, ",")
	}
	if has("group") && v.Group != "" && v.Group != viewOrder[v.View].group {
		ps["group"] = v.Group
	}
	if has("sort") && v.Sort != "" && v.Sort != viewOrder[v.View].sort {
		ps["sort"] = viewEnc(v.Sort)
	}
	if has("preset") && v.Preset != "" {
		ps["preset"] = viewEnc(v.Preset)
	}
	if has("q") && v.Q != "" {
		ps["q"] = viewEnc(v.Q)
	}
	if has("archived") && v.Archived {
		ps["archived"] = "1"
	}
	if has("sel") && len(v.Sel) > 0 {
		ps["sel"] = join(v.Sel)
	}
	if has("state") && len(v.State) > 0 {
		ps["state"] = join(v.State)
	}
	if has("project") && len(v.Project) > 0 {
		out := []string{}
		for _, p := range sortProjects(v.Project) {
			switch {
			case p == nil:
				out = append(out, "-")
			case *p == "-":
				out = append(out, "%2D")
			default:
				out = append(out, viewEnc(*p))
			}
		}
		ps["project"] = strings.Join(out, ",")
	}
	for k, x := range v.Extra {
		if !has(k) {
			ps[viewEnc(k)] = viewEnc(x)
		}
	}
	q := []string{}
	for _, k := range slices.Sorted(maps.Keys(ps)) {
		q = append(q, k+"="+ps[k])
	}
	out := "/" + v.View
	switch v.Focus {
	case "":
	case ".", "..": // a dot segment would be resolved away by the browser
		out += "/" + strings.ReplaceAll(v.Focus, ".", "%2E")
	default:
		out += "/" + viewEnc(v.Focus)
	}
	if len(q) > 0 {
		out += "?" + strings.Join(q, "&")
	}
	return out
}

func viewLink(r *contract.ViewLinkRequest) (contract.ViewLinkReply, error) {
	hasView, hasURL := len(r.View) > 0, len(r.Url) > 0
	if hasView == hasURL {
		return contract.ViewLinkReply{}, fail(400, "Expected exactly one of view or url")
	}
	if hasURL {
		s, ok := jsonString(r.Url)
		if !ok {
			return contract.ViewLinkReply{}, fail(400, "Expected url")
		}
		parsed, ok := parseView(s, browserOrigin)
		if !ok {
			return contract.ViewLinkReply{}, fail(400, "Not a Fleet view URL")
		}
		return contract.ViewLinkReply{View: jsonBytes(parsed)}, nil
	}
	if p := viewProblem(r.View); p != "" {
		return contract.ViewLinkReply{}, fail(400, "Invalid view: "+p)
	}
	path := formatView(viewOf(r.View))
	out := contract.ViewLinkReply{Path: &path}
	if o := originOf(must(url.Parse(browserOrigin))); o != "" {
		full := o + path
		out.Url = &full
	}
	return out, nil
}

func init() {
	op("view_link", func(_ *Request, r *contract.ViewLinkRequest) (contract.ViewLinkReply, error) { return viewLink(r) })
}
