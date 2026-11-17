package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"fleet/contract"
)

// search: one find-anything operation for the Spotlight palette and for agents (OR-32). The query is free text plus
// field:value terms, the list search syntax. Each entity kind is one provider in searchProviders; a new kind plugs in
// by appending a provider. A provider that cannot apply a term of the query finds nothing, so `agent:claude` lists
// tasks, conversations and agents only. With the caller's project focus (OR-34), each kind's hits in it come first.
// A person's ignored projects (OR-34) are left out of every project kind (tasks, questions, conversations, results,
// projects) and of the list counts; a query that names a project (`project:<its id>`) or is a code jump names its target
// itself and finds it anyway, as a list whose view names a project shows it. An agent's search sees every project its grants allow.

type searchQuery struct {
	raw    string
	text   string
	fields map[string]string
	focus  []string // the caller's project focus ('-' is work outside projects); empty: every project
	// ignored: the calling person's ignored project ids ('-' is no project), left out of the hits.
	ignored []string
	// listIgnored: every ignored project, as the lists leave them out (a project: term lifts the ignore for hits only).
	listIgnored []string
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

// inFocus: an SQL condition that col (a project id) is in the focus ('-' matches no project).
func (s searchQuery) inFocus(p *[]any, col string) string {
	*p = append(*p, s.focus)
	return fmt.Sprintf("coalesce(%s,'-')=ANY($%d)", col, len(*p))
}

// focusFirst: the ORDER BY term that puts rows in the focus first, with its comma; nothing without a focus.
func (s searchQuery) focusFirst(p *[]any, col string) string {
	if len(s.focus) == 0 {
		return ""
	}
	return s.inFocus(p, col) + " DESC,"
}

// listFilter: the search, the focus and the ignored projects as a list's own filter (task_page, sessions_list: a null
// projectId or excludeProjectId is no project), as the app builds it for the list "All N" opens.
func (s searchQuery) listFilter() doc {
	f := doc{"search": s.raw}
	if len(s.focus) > 0 {
		f["projectId"] = nullProjects(s.focus)
	}
	if len(s.listIgnored) > 0 {
		f["excludeProjectId"] = nullProjects(s.listIgnored)
	}
	return f
}

// nullProjects: project ids with '-' (no project) as null, the list filter's form.
func nullProjects(ids []string) []any {
	ps := []any{}
	for _, x := range ids {
		if x == "-" {
			ps = append(ps, nil)
		} else {
			ps = append(ps, x)
		}
	}
	return ps
}

// focused: a hit is in the focus. Every hit is without a focus, and so is a kind that has no project (host, agent, pipeline).
func (s searchQuery) focused(h contract.Found) bool {
	switch {
	case len(s.focus) == 0 || h.Kind == contract.FoundKindHost || h.Kind == contract.FoundKindAgent || h.Kind == contract.FoundKindPipeline:
		return true
	case h.Kind == contract.FoundKindProject:
		return slices.Contains(s.focus, h.Id)
	case h.ProjectId == nil:
		return slices.Contains(s.focus, "-")
	}
	return slices.Contains(s.focus, *h.ProjectId)
}

type searchProvider func(q *Request, s searchQuery, limit int) ([]contract.Found, error)

var searchProviders = []searchProvider{searchCodes, searchTasks, searchQuestions, searchConversations, searchResults, searchProjects, searchHosts,
	searchDefinitions(contract.FoundKindAgent), searchDefinitions(contract.FoundKindPipeline)}

// searchCounters: the kinds whose list section takes the query (Tasks, Conversations, Results). Each counts its matches
// in the focus with its list's own conditions (taskWhere, sessionWhere, artifactSearch), so "All N" opens a list of N rows.
var searchCounters = map[contract.FoundKind]func(q *Request, s searchQuery) (int64, error){
	contract.FoundKindTask: func(q *Request, s searchQuery) (int64, error) {
		p := []any{}
		c, err := taskWhere(s.listFilter(), &p, true)
		if err != nil {
			return 0, err
		}
		return countOf(q, "SELECT count(*) FROM task t"+where(c), p...)
	},
	contract.FoundKindConversation: func(q *Request, s searchQuery) (int64, error) {
		p := []any{}
		c, err := sessionWhere(s.listFilter(), &p, true)
		if err != nil {
			return 0, err
		}
		return countOf(q, sessionBase+" SELECT count(*) FROM x"+where(c), p...)
	},
	contract.FoundKindResult: func(q *Request, s searchQuery) (int64, error) {
		p := []any{}
		c := projectNotIn(artifactSearch(s.raw, &p), &p, "project_id", s.listIgnored)
		c, p = projectIn(c, p, s.focus) // the bare column, as artifact_list filters, so its index serves
		return countOf(q, "SELECT count(*) FROM artifact"+where(c), p...)
	},
}

func countOf(q *Request, sql string, args ...any) (int64, error) {
	var n int64
	err := q.tx.QueryRow(q.ctx(), sql, args...).Scan(&n)
	return n, err
}

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
// agent: the harness of the task's latest execution (task_derived.last_agent; task_derived.agent is set only while one runs).
var taskSearchText = []string{"t.title", "t.id", "t.code"}
var taskSearchFields = map[string]string{"agent": "(SELECT last_agent FROM task_derived WHERE task_id=t.id)", "host": "(SELECT host FROM task_derived WHERE task_id=t.id)",
	"project": "t.project_id", "state": stateSQL}

const taskHitCols = "SELECT t.id,t.code,t.title," + stateSQL + " AS state,t.project_id,d.last_agent AS agent,d.host,t.updated_at AS at FROM task t LEFT JOIN task_derived d ON d.task_id=t.id"

func taskHits(rs []doc) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		out = append(out, contract.Found{Kind: contract.FoundKindTask, Id: astr(r["id"]), Code: optStr(astr(r["code"])), Title: astr(r["title"]), State: optStr(astr(r["state"])),
			ProjectId: optStr(astr(r["project_id"])), Agent: optStr(astr(r["agent"])), Host: optStr(astr(r["host"])), At: optStr(astr(r["at"]))})
	}
	return out
}

// searchTasks: tasks in the focus first, then an id or code that starts with the text, then recently changed.
func searchTasks(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	p, first := []any{}, "t.id IS NULL"
	if s.text != "" {
		p, first = append(p, strings.TrimSuffix(likePattern(s.text)[1:], "%")+"%"), "(lower(t.id) LIKE $1 OR lower(coalesce(t.code,'')) LIKE $1)"
	}
	c, err := taskWhere(doc{"search": s.raw}, &p, true) // task_page's own conditions, as its count
	if err != nil {
		return nil, err
	}
	c = projectNotIn(c, &p, "t.project_id", s.ignored)
	f := s.focusFirst(&p, "t.project_id")
	rs, err := rowsOf(q, taskHitCols+where(c)+" ORDER BY "+f+first+" DESC,t.updated_at DESC,t.id LIMIT "+fmt.Sprint(limit), p...)
	return taskHits(rs), err
}

const questionHitCols = "SELECT x.id,x.code,x.text,x.state::text AS state,x.project_id,x.task_id,x.session_id,x.asked_at AS at FROM question x"

func questionHits(rs []doc) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		t := []rune(astr(r["text"]))
		out = append(out, contract.Found{Kind: contract.FoundKindQuestion, Id: astr(r["id"]), Code: optStr(astr(r["code"])), Title: string(t[:min(200, len(t))]),
			State: optStr(astr(r["state"])), ProjectId: optStr(astr(r["project_id"])), TaskId: optStr(astr(r["task_id"])), SessionId: optStr(astr(r["session_id"])), At: optStr(astr(r["at"]))})
	}
	return out
}

// searchQuestions: questions by text or code. The focus first; inside it open ones first, answered and withdrawn ones
// after (an old decision is found by its words too); then recently asked. Only project and state terms apply.
func searchQuestions(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	if !s.only("project", "state") {
		return nil, nil
	}
	p := []any{}
	c := projectNotIn(searchWhere(s.raw, &p, []string{"x.text", "x.code"}, map[string]string{"project": "x.project_id", "state": "x.state::text"}, true), &p, "x.project_id", s.ignored)
	f := s.focusFirst(&p, "x.project_id")
	rs, err := rowsOf(q, questionHitCols+where(c)+" ORDER BY "+f+"x.state='open' DESC,x.seq DESC LIMIT "+fmt.Sprint(limit), p...)
	return questionHits(rs), err
}

const sessionHitCols = sessionBase + " SELECT x.id,x.code,x.first_message,x.state,x.project_id," + sessionAgent + " AS agent,x.activity_at AS at FROM x"

// sessionHits: conversation hits; words (the query's free text) give each the snippet of its first message around them.
func sessionHits(rs []doc, words string) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		t := []rune(firstString(astr(r["first_message"]), astr(r["code"]), astr(r["id"])))
		out = append(out, contract.Found{Kind: contract.FoundKindConversation, Id: astr(r["id"]), Code: optStr(astr(r["code"])), Title: string(t[:min(200, len(t))]),
			State: optStr(astr(r["state"])), ProjectId: optStr(astr(r["project_id"])), Agent: optStr(astr(r["agent"])), At: optStr(astr(r["at"])),
			Snippet: optStr(matchSnippet(astr(r["first_message"]), words))})
	}
	return out
}

// Snippet geometry: a match within the first snippetShown characters is in the title's visible start, so no snippet; a
// snippet is up to snippetLen characters from the word start at most snippetLead characters before the match, so the
// match comes first in the line, where a narrow row still shows it.
const snippetShown, snippetLen, snippetLead = 24, 80, 8

// matchSnippet: up to snippetLen characters of text (whitespace collapsed) from just before the first match of a word of
// words that the title does not show (its first match lies past the start), with an ellipsis where it is cut; "" when
// every word found is in the start or none is found.
func matchSnippet(text, words string) string {
	c := strings.Join(strings.Fields(text), " ")
	low := strings.ToLower(c) // rune by rune: the same rune offsets as c
	at := -1
	for _, w := range strings.Fields(strings.ToLower(words)) {
		if i := strings.Index(low, w); i >= 0 {
			if r := utf8.RuneCountInString(low[:i]); r >= snippetShown && (at < 0 || r < at) {
				at = r
			}
		}
	}
	if at < 0 {
		return ""
	}
	t := []rune(c)
	from := at - snippetLead
	if i := slices.Index(t[from:at], ' '); t[from-1] != ' ' && i >= 0 { // start at a word; a long token is cut
		from += i + 1
	}
	to := min(len(t), from+snippetLen)
	out := "…" + strings.TrimSpace(string(t[from:to]))
	if to < len(t) {
		out += "…"
	}
	return out
}

// searchConversations: conversations (sessions) with the sessions_list search: every field term applies, and archived
// ones are left out as the list leaves them out. The focus first, then latest activity.
func searchConversations(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	p := []any{}
	c, err := sessionWhere(doc{"search": s.raw}, &p, true)
	if err != nil {
		return nil, err
	}
	c = projectNotIn(c, &p, "x.project_id", s.ignored)
	f := s.focusFirst(&p, "x.project_id")
	rs, err := rowsOf(q, sessionHitCols+where(c)+" ORDER BY "+f+"x.activity_at DESC,x.id LIMIT "+fmt.Sprint(limit), p...)
	return sessionHits(rs, s.text), err
}

// artifactSearch: the conditions of a results search as artifact_list applies them: deleted ones left out; results have
// no agent or host column.
func artifactSearch(raw string, p *[]any) []string {
	return append([]string{"tombstoned_at IS NULL"}, searchWhere(raw, p, []string{"name"}, map[string]string{"project": "project_id", "state": "state::text"}, true)...)
}

// searchResults: results (artifacts) by name, the focus first, then newest. Only project and state terms apply.
func searchResults(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	if !s.only("project", "state") {
		return nil, nil
	}
	p := []any{}
	c := projectNotIn(artifactSearch(s.raw, &p), &p, "project_id", s.ignored)
	f := s.focusFirst(&p, "project_id")
	rs, err := rowsOf(q, "SELECT id,name,state::text AS state,project_id,task_id,created_at AS at FROM artifact"+where(c)+" ORDER BY "+f+"seq DESC LIMIT "+fmt.Sprint(limit), p...)
	out := []contract.Found{}
	for _, r := range rs {
		out = append(out, contract.Found{Kind: contract.FoundKindResult, Id: astr(r["id"]), Title: firstString(astr(r["name"]), astr(r["id"])), State: optStr(astr(r["state"])),
			ProjectId: optStr(astr(r["project_id"])), TaskId: optStr(astr(r["task_id"])), At: optStr(astr(r["at"]))})
	}
	return out, err
}

// searchDefinitions: live agent or pipeline definitions by id or name. An agent takes the agent: term (its configured
// harness); a pipeline takes no field term.
func searchDefinitions(kind contract.FoundKind) searchProvider {
	return func(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
		fields := []string{}
		if kind == contract.FoundKindAgent {
			fields = append(fields, "agent")
		}
		// Definitions are the architect's to read (people.go architectOps); an agent without a person passes, as for definition_list.
		if !s.only(fields...) || q.person != nil && !fullAccess(q.person) && !hasRole(q.person, "architect") {
			return nil, nil
		}
		xs, err := definitionRows(q, "d.kind=$1 AND d.retired_at IS NULL", 0, string(kind))
		out := []contract.Found{}
		for _, x := range xs {
			name, harness := definitionName(x), ""
			if kind == contract.FoundKindAgent {
				harness = profileHarness(x.Content)
			}
			if len(out) < limit && hasText(x.ID+" "+name, s.text) && hasText(harness, s.fields["agent"]) {
				out = append(out, contract.Found{Kind: kind, Id: x.ID, Title: name, Agent: optStr(harness), At: optStr(x.At.UTC().Format(time.RFC3339Nano))})
			}
		}
		return out, err
	}
}

// searchCodes: a query of D17 codes only (tasks T-, questions Q-, conversations C-) (no field terms, no other words) jumps to those entities, resolved with their
// aliases (an old code after a project rename still resolves); hits carry the current code. A code among other words
// or terms is left to the task and question providers, which apply every term.
func searchCodes(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	codes := []string{}
	for _, m := range codePattern.FindAllString(s.text, limit) {
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
	cs, err := rowsOf(q, sessionHitCols+" WHERE x.id=ANY($1)", sessions)
	if err != nil {
		return nil, err
	}
	hits := append(append(taskHits(ts), questionHits(qs)...), sessionHits(cs, "")...)
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
		if q.person != nil && !mayAccessProjectID(q.person, r.id) || slices.Contains(s.ignored, r.id) {
			continue
		}
		var b doc
		_ = json.Unmarshal(r.text, &b)
		id, name := r.id, firstString(astr(mapOf(b["settings"])["name"]), r.id)
		if hasText(id, s.fields["project"]) && hasText(id+" "+name, s.text) {
			out = append(out, contract.Found{Kind: contract.FoundKindProject, Id: id, Title: name})
		}
	}
	// Projects in the focus first, then by id.
	slices.SortStableFunc(out, func(a, b contract.Found) int { return boolOrder(s.focused(b)) - boolOrder(s.focused(a)) })
	return out[:min(limit, len(out))], nil
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
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
		if len(r.ProjectIds) > 1000 {
			return contract.SearchReply{}, fail(400, "Expected at most 1000 projectIds")
		}
		s := parseSearchQuery(r.Query)
		s.focus = r.ProjectIds
		hits, counts, yes := []contract.Found{}, map[string]int64{}, true
		if s.text == "" && len(s.fields) == 0 {
			return contract.SearchReply{Hits: hits, Counts: counts}, nil
		}
		if q.person != nil && q.agent == "" {
			// A failed read fails the search: the operation's transaction cannot go on after a failed statement.
			ps, err := personSettings(q, q.person.Email)
			if err != nil {
				return contract.SearchReply{}, err
			}
			// A project: term naming an ignored project exactly finds it; a term that only contains part of an id does not.
			s.ignored = slices.DeleteFunc(slices.Clone(ps.Ignored), func(id string) bool { return strings.EqualFold(id, s.fields["project"]) })
			s.listIgnored = ps.Ignored
		}
		// coded: kinds a code jump found; jumped: those hits, which lead whatever the focus (an exact code is what was asked for).
		coded, jumped := map[contract.FoundKind]bool{}, map[string]bool{}
		for i, p := range searchProviders {
			h, err := p(q, s, limit)
			if err != nil {
				return contract.SearchReply{}, err
			}
			for _, x := range h {
				coded[x.Kind] = coded[x.Kind] || i == 0 // searchCodes
				if i == 0 {
					jumped[string(x.Kind)+":"+x.Id] = true
				}
			}
			for _, x := range h { // a code hit is not repeated by the task or question provider
				if !slices.ContainsFunc(hits, func(y contract.Found) bool { return y.Kind == x.Kind && y.Id == x.Id }) {
					hits = append(hits, x)
				}
			}
		}
		for i := range hits {
			if !s.focused(hits[i]) && !jumped[string(hits[i].Kind)+":"+hits[i].Id] {
				hits[i].Outside = &yes
			}
		}
		// A list count: the kind's hits in the focus while it found fewer than the limit by the list's own search, else one count query.
		for kind, count := range searchCounters {
			n, all := int64(0), 0
			for _, h := range hits {
				if h.Kind == kind {
					all++
					if s.focused(h) {
						n++
					}
				}
			}
			if all == 0 {
				continue
			}
			// A lifted ignore finds hits the list leaves out: then the count is the list's own too.
			if all >= limit || coded[kind] || len(s.ignored) < len(s.listIgnored) {
				var err error
				if n, err = count(q, s); err != nil {
					return contract.SearchReply{}, err
				}
			}
			counts[string(kind)] = n
		}
		return contract.SearchReply{Hits: hits, Counts: counts}, nil
	}))
}
