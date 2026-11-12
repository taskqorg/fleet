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
	text   string
	fields map[string]string
}

// searchFields: the field:value terms the syntax knows; any other `word:rest` stays free text (a URL, a time).
var searchFields = []string{"agent", "host", "state", "project"}

func parseSearchQuery(s string) searchQuery {
	q := searchQuery{fields: map[string]string{}}
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

var searchProviders = []searchProvider{searchTasks, searchProjects, searchHosts}

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

// searchTasks: title or id; an id that starts with the text comes first (jump by code), then recently changed.
func searchTasks(q *Request, s searchQuery, limit int) ([]contract.Found, error) {
	p, c := []any{}, []string{}
	if s.text != "" {
		// $1: the whole text as an id prefix (jump by code); then each word narrows by title or id.
		p = append(p, strings.TrimSuffix(likePattern(s.text)[1:], "%")+"%")
		for _, w := range strings.Fields(s.text) {
			p = append(p, likePattern(w))
			c = append(c, fmt.Sprintf("(lower(t.title) LIKE $%d OR lower(t.id) LIKE $%d)", len(p), len(p)))
		}
	}
	for _, f := range [][2]string{{"agent", "d.agent"}, {"host", "d.host"}, {"project", "t.project_id"}} {
		if v, ok := s.fields[f[0]]; ok {
			p = append(p, likePattern(v))
			c = append(c, fmt.Sprintf("lower(coalesce(%s,'')) LIKE $%d", f[1], len(p)))
		}
	}
	if v, ok := s.fields["state"]; ok {
		p = append(p, strings.ToLower(v))
		c = append(c, fmt.Sprintf(stateSQL+"=$%d", len(p)))
	}
	first := "t.id IS NULL"
	if s.text != "" {
		first = "lower(t.id) LIKE $1"
	}
	rs, err := rowsOf(q, "SELECT t.id,t.title,"+stateSQL+" AS state,t.project_id,d.agent,d.host FROM task t LEFT JOIN task_derived d ON d.task_id=t.id"+
		where(c)+" ORDER BY "+first+" DESC,t.updated_at DESC,t.id LIMIT "+fmt.Sprint(limit), p...)
	out := []contract.Found{}
	for _, r := range rs {
		out = append(out, contract.Found{Kind: contract.FoundKindTask, Id: astr(r["id"]), Title: astr(r["title"]), State: optStr(astr(r["state"])),
			ProjectId: optStr(astr(r["project_id"])), Agent: optStr(astr(r["agent"])), Host: optStr(astr(r["host"]))})
	}
	return out, err
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
			hits = append(hits, h...)
		}
		return contract.SearchReply{Hits: hits}, nil
	}))
}
