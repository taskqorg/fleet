package main

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"fleet/contract"
	"fleet/coordinator/db"
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

// searchTerms: the query as the search queries' parameters (queries/entities.sql): each free-text word and each
// agent, host or project term a LIKE pattern (likePattern; the list search syntax, searchWhere), the state term exact.
type searchTerms struct {
	words                       []string
	agent, host, project, state *string
}

func (s searchQuery) terms() searchTerms {
	t := searchTerms{words: []string{}}
	for _, w := range strings.Fields(s.text) {
		t.words = append(t.words, likePattern(w))
	}
	like := func(k string) *string {
		if v, ok := s.fields[k]; ok {
			return ptr(likePattern(v))
		}
		return nil
	}
	t.agent, t.host, t.project = like("agent"), like("host"), like("project")
	if v, ok := s.fields["state"]; ok {
		t.state = ptr(strings.ToLower(v))
	}
	return t
}

// projectsOut: ignored project ids as the queries leave them out: the named ones, and whether '-' (no project) is among them.
func projectsOut(ids []string) ([]string, bool) {
	named := slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == "-" })
	return named, len(named) < len(ids)
}

// focusOf: the focus as the queries order and count by it ('-' is work outside projects), never null.
func (s searchQuery) focusOf() []string {
	return append([]string{}, s.focus...)
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
// in the focus with its list's own conditions (taskWhere, sessionWhere, artifactSearch, mirrored in queries/entities.sql),
// so "All N" opens a list of N rows.
var searchCounters = map[contract.FoundKind]func(q *Request, s searchQuery) (int64, error){
	contract.FoundKindTask: func(q *Request, s searchQuery) (int64, error) {
		t := s.terms()
		named, none := projectsOut(s.listIgnored)
		return q.db().EntCountTasks(q.ctx(), db.EntCountTasksParams{Words: t.words, Agent: t.agent, Host: t.host, Project: t.project, State: t.state,
			Focused: len(s.focus) > 0, Focus: s.focusOf(), Ignored: named, IgnoreNone: none})
	},
	contract.FoundKindConversation: func(q *Request, s searchQuery) (int64, error) {
		t := s.terms()
		named, none := projectsOut(s.listIgnored)
		return q.db().EntCountSessions(q.ctx(), db.EntCountSessionsParams{Words: t.words, Agent: t.agent, Host: t.host, Project: t.project, State: t.state,
			Focused: len(s.focus) > 0, Focus: s.focusOf(), Ignored: named, IgnoreNone: none})
	},
	contract.FoundKindResult: func(q *Request, s searchQuery) (int64, error) {
		t := s.terms()
		if t.agent != nil || t.host != nil { // results have no agent or host column: a term on one matches nothing
			return 0, nil
		}
		named, none := projectsOut(s.listIgnored)
		return q.db().EntCountResults(q.ctx(), db.EntCountResultsParams{Words: t.words, Project: t.project, State: t.state,
			Focused: len(s.focus) > 0, Focus: s.focusOf(), Ignored: named, IgnoreNone: none})
	},
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

// noLimit: the limit of a code jump's reads, which name their hits (at most the search limit of codes).
const noLimit = 1000

func taskHits(rs []db.EntSearchTasksRow) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		out = append(out, contract.Found{Kind: contract.FoundKindTask, Id: r.ID, Code: optStr(deref(r.Code)), Title: r.Title, State: optStr(r.State),
			ProjectId: optStr(deref(r.ProjectID)), Agent: optStr(deref(r.Agent)), Host: optStr(deref(r.Host)), At: optStr(cardTime(&r.At))})
	}
	return out
}

// searchTasks: tasks in the focus first, then an id or code that starts with the text, then recently changed.
func searchTasks(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	var prefix *string
	if s.text != "" {
		prefix = ptr(strings.TrimSuffix(likePattern(s.text)[1:], "%") + "%")
	}
	t := s.terms()
	named, none := projectsOut(s.ignored)
	rs, err := q.db().EntSearchTasks(q.ctx(), db.EntSearchTasksParams{Words: t.words, Agent: t.agent, Host: t.host, Project: t.project, State: t.state,
		Ignored: named, IgnoreNone: none, Focus: s.focusOf(), Prefix: prefix, Lim: int32(limit)})
	return taskHits(rs), err
}

func questionHits(rs []db.EntSearchQuestionsRow) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		t := []rune(r.Text)
		out = append(out, contract.Found{Kind: contract.FoundKindQuestion, Id: r.ID, Code: optStr(deref(r.Code)), Title: string(t[:min(200, len(t))]),
			State: optStr(r.State), ProjectId: optStr(deref(r.ProjectID)), TaskId: optStr(deref(r.TaskID)), SessionId: optStr(deref(r.SessionID)), At: optStr(cardTime(&r.At))})
	}
	return out
}

// searchQuestions: questions by text or code. The focus first; inside it open ones first, answered and withdrawn ones
// after (an old decision is found by its words too); then recently asked. Only project and state terms apply.
func searchQuestions(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	if !s.only("project", "state") {
		return nil, nil
	}
	t := s.terms()
	named, none := projectsOut(s.ignored)
	rs, err := q.db().EntSearchQuestions(q.ctx(), db.EntSearchQuestionsParams{Words: t.words, Project: t.project, State: t.state,
		Ignored: named, IgnoreNone: none, Focus: s.focusOf(), Lim: int32(limit)})
	return questionHits(rs), err
}

// sessionHits: conversation hits; words (the query's free text) give each the snippet of its first message around them.
func sessionHits(rs []db.EntSearchSessionsRow, words string) []contract.Found {
	out := []contract.Found{}
	for _, r := range rs {
		var at *time.Time
		if r.At.Valid {
			at = &r.At.Time
		}
		t := []rune(firstString(r.Title, plainTitle(r.FirstMessage), deref(r.Code), r.ID))
		out = append(out, contract.Found{Kind: contract.FoundKindConversation, Id: r.ID, Code: optStr(deref(r.Code)), Title: string(t[:min(200, len(t))]),
			State: optStr(r.State), ProjectId: optStr(deref(r.ProjectID)), Agent: optStr(r.Agent), At: optStr(cardTime(at)),
			Snippet: optStr(matchSnippet(r.FirstMessage, words))})
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
	t := s.terms()
	named, none := projectsOut(s.ignored)
	rs, err := q.db().EntSearchSessions(q.ctx(), db.EntSearchSessionsParams{Words: t.words, Agent: t.agent, Host: t.host, Project: t.project, State: t.state,
		Ignored: named, IgnoreNone: none, Focus: s.focusOf(), Lim: int32(limit)})
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
	t := s.terms()
	named, none := projectsOut(s.ignored)
	rs, err := q.db().EntSearchResults(q.ctx(), db.EntSearchResultsParams{Words: t.words, Project: t.project, State: t.state,
		Ignored: named, IgnoreNone: none, Focus: s.focusOf(), Lim: int32(limit)})
	out := []contract.Found{}
	for _, r := range rs {
		out = append(out, contract.Found{Kind: contract.FoundKindResult, Id: r.ID, Title: firstString(r.Name, r.ID), State: optStr(r.State),
			ProjectId: optStr(deref(r.ProjectID)), TaskId: optStr(deref(r.TaskID)), At: optStr(cardTime(&r.At))})
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
	none := []string{}
	ts, err := q.db().EntSearchTasks(q.ctx(), db.EntSearchTasksParams{Ids: tasks, Words: none, Ignored: none, Focus: none, Lim: noLimit})
	if err != nil {
		return nil, err
	}
	qs, err := q.db().EntSearchQuestions(q.ctx(), db.EntSearchQuestionsParams{Ids: questions, Words: none, Ignored: none, Focus: none, Lim: noLimit})
	if err != nil {
		return nil, err
	}
	cs, err := q.db().EntSearchSessions(q.ctx(), db.EntSearchSessionsParams{Ids: sessions, Words: none, Ignored: none, Focus: none, Lim: noLimit}) // host command sessions are no conversations
	if err != nil {
		return nil, err
	}
	hits := append(append(taskHits(ts), questionHits(qs)...), sessionHits(cs, s.text)...)
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
		id, name := r.id, firstString(projectName(r.text), r.id)
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
	hs, err := q.db().EntSearchHosts(q.ctx())
	out := []contract.Found{}
	for _, h := range hs {
		id, name := h.HostID, firstString(h.Name, h.HostID)
		if len(out) < limit && (hasText(id, s.fields["host"]) || hasText(name, s.fields["host"])) && hasText(id+" "+name, s.text) {
			out = append(out, contract.Found{Kind: contract.FoundKindHost, Id: id, Title: name})
		}
	}
	return out, err
}

func init() {
	op("search", func(q *Request, r *contract.SearchRequest) (contract.SearchReply, error) {
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
	})
}
