package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"fleet/contract"
)

// search: one find-anything operation for the Spotlight palette and for agents (OR-32). The query is free text plus
// field:value terms, the list search syntax. Each entity kind is one provider in searchProviders; a new kind plugs in
// by appending a provider. A provider that cannot apply a term of the query finds nothing, so `agent:claude` lists tasks only.

type searchQuery struct {
	raw    string
	text   string
	fields map[string]string
}

// searchFields: the field:value terms the syntax knows; any other `word:rest` stays free text (a URL, a time).
var searchFields = []string{"agent", "host", "state", "project"}

func parseSearchQuery(s string) searchQuery {
	q := searchQuery{raw: s, fields: map[string]string{}}
	words := []string{}
	for _, w := range strings.Fields(s) {
		if k, v, ok := strings.Cut(w, ":"); ok && v != "" && slices.Contains(searchFields, strings.ToLower(k)) {
			q.fields[strings.ToLower(k)] = v
			continue
		}
		words = append(words, w)
	}
	q.text = strings.Join(words, " ")
	return q
}

// only: the query uses no field outside allowed.
func (s searchQuery) only(allowed ...string) bool {
	for k := range s.fields {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return true
}

type searchProvider func(q *Request, s searchQuery, limit int) ([]contract.Found, error)

var searchProviders = []searchProvider{searchCodes, searchTasks, searchQuestions, searchProjects, searchHosts}

// hasText: every word of needle is in hay (case-insensitive); an empty needle matches.
func hasText(hay, needle string) bool {
	for _, w := range strings.Fields(strings.ToLower(needle)) {
		if !strings.Contains(strings.ToLower(hay), w) {
			return false
		}
	}
	return true
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// searchWhere: the list search syntax (parseSearchQuery) as SQL conditions, one syntax for Spotlight and every list
// (OR-36): each word must be in one of the text columns; each field term in its column (state exact, the others contain).
// A field the list has no column for matches nothing, never everything.
func searchWhere(raw string, p *[]any, text []string, fields map[string]string, withState bool) []string {
	s, c := parseSearchQuery(raw), []string{}
	for _, w := range strings.Fields(s.text) {
		*p = append(*p, likePattern(w))
		o := []string{}
		for _, col := range text {
			o = append(o, fmt.Sprintf("lower(coalesce(%s,'')) LIKE $%d", col, len(*p)))
		}
		c = append(c, "("+strings.Join(o, " OR ")+")")
	}
	for _, k := range searchFields {
		v, ok := s.fields[k]
		if !ok {
			continue
		}
		col, has := fields[k]
		switch {
		case !has:
			c = append(c, "false")
		case k == "state" && !withState: // per-state counts: every state, like the state filter
		case k == "state":
			*p = append(*p, strings.ToLower(v))
			c = append(c, fmt.Sprintf("%s=$%d", col, len(*p)))
		default:
			*p = append(*p, likePattern(v))
			c = append(c, fmt.Sprintf("lower(coalesce(%s,'')) LIKE $%d", col, len(*p)))
		}
	}
	return c
}

// Task columns of the search syntax; agent and host are subqueries, so every task query can use them without a join.
var taskSearchText = []string{"t.title", "t.id", "t.code"}
var taskSearchFields = map[string]string{"agent": "(SELECT agent FROM task_derived WHERE task_id=t.id)", "host": "(SELECT host FROM task_derived WHERE task_id=t.id)",
	"project": "t.project_id", "state": stateSQL}

const taskHitCols = "SELECT t.id,t.code,t.title," + stateSQL + " AS state,t.project_id,d.agent,d.host FROM task t LEFT JOIN task_derived d ON d.task_id=t.id"

func taskHits(rs []doc) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		out = append(out, contract.Found{Kind: contract.FoundKindTask, Id: astr(r["id"]), Code: optStr(astr(r["code"])), Title: astr(r["title"]), State: optStr(astr(r["state"])),
			ProjectId: optStr(astr(r["project_id"])), Agent: optStr(astr(r["agent"])), Host: optStr(astr(r["host"]))})
	}
	return out
}

// searchTasks: an id or code that starts with the text comes first, then recently changed.
func searchTasks(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	p, first := []any{}, "t.id IS NULL"
	if s.text != "" {
		p, first = append(p, strings.TrimSuffix(likePattern(s.text)[1:], "%")+"%"), "(lower(t.id) LIKE $1 OR lower(coalesce(t.code,'')) LIKE $1)"
	}
	c := searchWhere(s.raw, &p, taskSearchText, taskSearchFields, true)
	rs, err := rowsOf(q, taskHitCols+where(c)+" ORDER BY "+first+" DESC,t.updated_at DESC,t.id LIMIT "+fmt.Sprint(limit), p...)
	return taskHits(rs), err
}

const questionHitCols = "SELECT x.id,x.code,x.text,x.state::text AS state,x.project_id,x.task_id,x.session_id FROM question x"

func questionHits(rs []doc) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		t := []rune(astr(r["text"]))
		out = append(out, contract.Found{Kind: contract.FoundKindQuestion, Id: astr(r["id"]), Code: optStr(astr(r["code"])), Title: string(t[:min(200, len(t))]),
			State: optStr(astr(r["state"])), ProjectId: optStr(astr(r["project_id"])), TaskId: optStr(astr(r["task_id"])), SessionId: optStr(astr(r["session_id"]))})
	}
	return out
}

// searchQuestions: questions by text or code; open ones first, then recently asked. Only project and state terms apply.
func searchQuestions(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	if !s.only("project", "state") {
		return nil, nil
	}
	p := []any{}
	c := searchWhere(s.raw, &p, []string{"x.text", "x.code"}, map[string]string{"project": "x.project_id", "state": "x.state::text"}, true)
	rs, err := rowsOf(q, questionHitCols+where(c)+" ORDER BY x.state='open' DESC,x.seq DESC LIMIT "+fmt.Sprint(limit), p...)
	return questionHits(rs), err
}

// searchCodes: a query of D17 codes only (tasks T-, questions Q-, conversations C-) (no field terms, no other words) jumps to those entities, resolved with their
// aliases (an old code after a project rename still resolves); hits carry the current code. A code among other words
// or terms is left to the task and question providers, which apply every term.
func searchCodes(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	codes := []string{}
	for _, m := range codePattern.FindAllString(s.text, 50) {
		codes = append(codes, strings.ToUpper(m))
	}
	if len(codes) == 0 || len(s.fields) > 0 || strings.TrimSpace(codePattern.ReplaceAllString(s.text, "")) != "" {
		return nil, nil
	}
	found, err := resolveCodes(q, codes)
	if err != nil {
		return nil, err
	}
	tasks, questions, sessions := []string{}, []string{}, []string{}
	for _, code := range codes {
		if e, ok := found[code]; ok && e.Type == "T" {
			tasks = append(tasks, e.Id)
		} else if ok && e.Type == "Q" {
			questions = append(questions, e.Id)
		} else if ok && e.Type == "C" {
			sessions = append(sessions, e.Id)
		}
	}
	ts, err := rowsOf(q, taskHitCols+" WHERE t.id=ANY($1)", tasks)
	if err != nil {
		return nil, err
	}
	qs, err := rowsOf(q, questionHitCols+" WHERE x.id=ANY($1)", questions)
	if err != nil {
		return nil, err
	}
	cs, err := rowsOf(q, sessionBase+" SELECT x.id,x.code,x.first_message,x.state,x.project_id FROM x WHERE x.id=ANY($1)", sessions)
	if err != nil {
		return nil, err
	}
	hits := append(taskHits(ts), questionHits(qs)...)
	for _, r := range cs {
		t := []rune(firstString(astr(r["first_message"]), astr(r["code"])))
		hits = append(hits, contract.Found{Kind: contract.FoundKindConversation, Id: astr(r["id"]), Code: optStr(astr(r["code"])), Title: string(t[:min(200, len(t))]),
			State: optStr(astr(r["state"])), ProjectId: optStr(astr(r["project_id"]))})
	}
	byID := map[string]contract.Found{}
	for _, h := range hits {
		byID[string(h.Kind)+":"+h.Id] = h
	}
	kinds := map[string]string{"T": "task", "Q": "question", "C": "conversation"}
	out := []contract.Found{}
	for _, code := range codes { // in query order
		e := found[code]
		if h, ok := byID[kinds[e.Type]+":"+e.Id]; ok && !slices.ContainsFunc(out, func(y contract.Found) bool { return y.Kind == h.Kind && y.Id == h.Id }) {
			out = append(out, h)
		}
	}
	return out, nil
}

func searchProjects(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	if !s.only("project") || s.text == "" && s.fields["project"] == "" {
		return nil, nil
	}
	// The project head texts carry the name in settings; one query for all projects, filtered by grants.
	rows, err := headTexts(q, "project")
	if err != nil {
		return nil, err
	}
	out := []contract.Found{}
	for _, r := range rows {
		if len(out) >= limit {
			break
		}
		if q.person != nil && !mayAccessProjectID(q.person, r.id) {
			continue
		}
		var b doc
		_ = json.Unmarshal(r.text, &b)
		id, name := r.id, firstString(astr(mapOf(b["settings"])["name"]), r.id)
		if hasText(id, s.fields["project"]) && hasText(id+" "+name, s.text) {
			out = append(out, contract.Found{Kind: contract.FoundKindProject, Id: id, Title: name})
		}
	}
	return out, nil
}

func searchHosts(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	if !s.only("host") || s.text == "" && s.fields["host"] == "" {
		return nil, nil
	}
	// ponytail: configured hosts only; a host known only from its observations is not found until it is registered.
	hs, err := hostList(q)
	out := []contract.Found{}
	for _, h := range hs {
		id, name := astr(h["id"]), firstString(astr(h["name"]), astr(h["id"]))
		if len(out) < limit && (hasText(id, s.fields["host"]) || hasText(name, s.fields["host"])) && hasText(id+" "+name, s.text) {
			out = append(out, contract.Found{Kind: contract.FoundKindHost, Id: id, Title: name})
		}
	}
	return out, err
}

func init() {
	gatedObjOp("search", true, typedOp(func(q *Request, r *contract.SearchRequest) (contract.SearchReply, error) {
		limit := 8
		if r.Limit != nil {
			if *r.Limit < 1 {
				return contract.SearchReply{}, fail(400, "Expected limit of at least 1")
			}
			limit = min(int(*r.Limit), 50)
		}
		if len(r.Query) > 500 {
			return contract.SearchReply{}, fail(400, "Query too long")
		}
		s := parseSearchQuery(r.Query)
		hits := []contract.Found{}
		if s.text == "" && len(s.fields) == 0 {
			return contract.SearchReply{Hits: hits}, nil
		}
		for _, p := range searchProviders {
			h, err := p(q, s, limit)
			if err != nil {
				return contract.SearchReply{}, err
			}
			for _, x := range h { // a code hit is not repeated by the task or question provider
				if !slices.ContainsFunc(hits, func(y contract.Found) bool { return y.Kind == x.Kind && y.Id == x.Id }) {
					hits = append(hits, x)
				}
			}
		}
		return contract.SearchReply{Hits: hits}, nil
	}))
}
