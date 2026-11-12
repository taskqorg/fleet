package main

// view_link (docs/designs/fleet-view-urls-and-list-path.md §1): the canonical operator-app link for a view, or the view
// of a link. A Go port of parseView, formatView and viewProblem in packages/view-url/view-url.ts; the outside test
// view.test.ts compares both on the same views, so the two codecs cannot drift apart unnoticed.

import (
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

var (
	viewNames     = []string{"tasks", "conversations", "hosts", "overview", "results"}
	artifactState = []string{"stored", "pending", "failed", "deleted"}
	viewGroups    = slices.Sorted(maps.Keys(taskGroups)) // task_page's groups
	viewSorts     = func() []string {                    // task_page's sorts, each also descending
		out := []string{}
		for k := range taskSorts {
			out = append(out, k, "-"+k)
		}
		return slices.Sorted(slices.Values(out))
	}()
	viewKeys = map[string][]string{"tasks": {"group", "preset", "project", "q", "sel", "sort", "state"},
		"conversations": {"archived", "preset", "project", "q", "sel", "state"}, "results": {"project", "q", "state"}}
	schemeURL = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
)

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

// viewProblem checks a view object from an agent: unknown keys and bad values are errors, never dropped.
func viewProblem(v any) string {
	o, ok := v.(map[string]any)
	if !ok {
		return "view must be an object"
	}
	name, _ := o["view"].(string)
	if !slices.Contains(viewNames, name) {
		return "view.view must be one of " + strings.Join(viewNames, ", ")
	}
	strs := func(x any) bool {
		xs, ok := x.([]any)
		for _, i := range xs {
			if s, isStr := i.(string); !isStr || s == "" {
				return false
			}
		}
		return ok
	}
	keys := slices.Sorted(maps.Keys(o))
	for _, k := range keys {
		x := o[k]
		switch {
		case k == "view":
		case k == "focus":
			if s, _ := x.(string); s == "" {
				return "focus must be a non-empty string"
			}
		case k == "extra":
			m, ok := x.(map[string]any)
			for _, i := range m {
				if _, isStr := i.(string); !isStr {
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
			ok := strs(x)
			for _, i := range anys(x) {
				ok = ok && slices.Contains(states, i.(string))
			}
			if !ok {
				return "state must be a list of: " + strings.Join(states, ", ")
			}
		case k == "group":
			if s, _ := x.(string); !slices.Contains(viewGroups, s) {
				return "group must be one of " + strings.Join(viewGroups, ", ")
			}
		case k == "sort":
			if s, _ := x.(string); !slices.Contains(viewSorts, s) {
				return "sort must be one of " + strings.Join(viewSorts, ", ")
			}
		case k == "project":
			xs, ok := x.([]any)
			for _, i := range xs {
				if s, isStr := i.(string); i != nil && (!isStr || s == "") {
					ok = false
				}
			}
			if !ok {
				return "project must be a list of project ids or null"
			}
		case k == "sel":
			if !strs(x) {
				return "sel must be a list of ids"
			}
		case k == "q" || k == "preset":
			if _, ok := x.(string); !ok {
				return k + " must be a string"
			}
		case k == "archived":
			if _, ok := x.(bool); !ok {
				return "archived must be true or false"
			}
		}
	}
	return ""
}

// viewOf converts a view object that passed viewProblem.
func viewOf(o map[string]any) view {
	v := view{View: astr(o["view"]), Focus: astr(o["focus"]), Group: astr(o["group"]), Preset: astr(o["preset"]), Q: astr(o["q"]), Sel: strs(o["sel"]), Sort: astr(o["sort"]), State: strs(o["state"])}
	v.Archived, _ = o["archived"].(bool)
	for _, p := range anys(o["project"]) {
		if s, ok := p.(string); ok {
			v.Project = append(v.Project, &s)
		} else {
			v.Project = append(v.Project, nil)
		}
	}
	if m, ok := o["extra"].(map[string]any); ok {
		v.Extra = map[string]string{}
		for k, x := range m {
			v.Extra[k] = x.(string)
		}
	}
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
			if g := viewDec(raw, true); slices.Contains(viewGroups, g) {
				v.Group = g
			}
		case "sort":
			if x := viewDec(raw, true); slices.Contains(viewSorts, x) {
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
	if has("group") && v.Group != "" && v.Group != "priority" {
		ps["group"] = v.Group
	}
	if has("sort") && v.Sort != "" && v.Sort != defaultTaskSort {
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

func viewLink(q *Request, b doc) (doc, error) {
	v, hasView := b["view"]
	u, hasURL := b["url"]
	if hasView == hasURL {
		return nil, fail(400, "Expected exactly one of view or url")
	}
	if hasURL {
		s, ok := u.(string)
		if !ok {
			return nil, fail(400, "Expected url")
		}
		parsed, ok := parseView(s, browserOrigin)
		if !ok {
			return nil, fail(400, "Not a Fleet view URL")
		}
		return doc{"view": parsed}, nil
	}
	if p := viewProblem(v); p != "" {
		return nil, fail(400, "Invalid view: "+p)
	}
	path := formatView(viewOf(v.(map[string]any)))
	var full any
	if o := originOf(must(url.Parse(browserOrigin))); o != "" {
		full = o + path
	}
	return doc{"url": full, "path": path}, nil
}

func init() {
	agentDescriptions["view_link"] = "The operator-app link for a view ({view:{view:'tasks',state:['failed'],project:['fleet']}}), or the view of a link ({url}); returns {url, path} or {view}"
	agentTypedOp("view_link", true, func(q *Request, b *contract.ViewLinkRequest) (contract.ViewLinkReply, error) {
		out, err := viewLink(q, docOf(b))
		if err != nil {
			return contract.ViewLinkReply{}, err
		}
		return asT[contract.ViewLinkReply](out)
	})
}
