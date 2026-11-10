// People rules of a request (packages/coordinator/people.ts, app.ts preHandler): director, actor, project grants,
// the architect gate and the `me` operation.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
)

func mayAccessProjectID(p *Person, projectID string) bool {
	if p == nil {
		return false
	}
	if p.Owner {
		return true
	}
	if projectID == "" {
		return false
	}
	for _, g := range p.Grants {
		if g.Kind == "project" && g.ID == projectID {
			return true
		}
	}
	return false
}

func ownerEmail(q *Request) string {
	for _, p := range q.app.people {
		if p.Owner {
			return p.Email
		}
	}
	return ""
}

// director: the request person, else the owner (host agents, timers).
func director(q *Request) string {
	if q.person != nil {
		return q.person.Email
	}
	return ownerEmail(q)
}

// actor: the request person, else the fallback (an execution, 'operator').
func actor(q *Request, fallback string) string {
	if q.person != nil {
		return q.person.Email
	}
	return fallback
}

func grantsVersion(p *Person) string {
	roles := slices.Clone(p.Roles)
	sort.Strings(roles)
	gs := []string{}
	for _, g := range p.Grants {
		gs = append(gs, g.Kind+":"+g.ID)
	}
	sort.Strings(gs)
	b, _ := json.Marshal([]any{roles, p.Owner, gs})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

var (
	architectOps    = regexp.MustCompile(`^(account_|lab_|connection|recovery|reconcil|transport_|artifact_rule|profiles_list)`)
	architectRoutes = regexp.MustCompile(`^/api/(lab|accounts|recovery|reconciliation|recovery-tools|profiles)(/|$)`)
)

func hasRole(p *Person, r string) bool { return slices.Contains(p.Roles, r) }

// guardOp: the architect gate on operation names (a holder's own subscription passes) and the project grant of a named project.
func (a *App) guardOp(q *Request, name string, body json.RawMessage) error {
	p := q.person
	if p == nil || p.Owner {
		return nil
	}
	var b map[string]any
	json.Unmarshal(body, &b)
	if architectOps.MatchString(name) && !hasRole(p, "architect") && !a.ownSubscription(q, name, b) {
		return fail(403, "Architect role required")
	}
	if id, ok := b["projectId"].(string); ok && !mayAccessProjectID(p, id) {
		return fail(404, "Unknown project")
	}
	return nil
}

// guardRoute: the same gate on routes (architect routes, /api/projects/{id}, ?projectId=, tools/call arguments).
func (a *App) guardRoute(q *Request) error {
	p := q.person
	if p == nil || p.Owner {
		return nil
	}
	path := q.r.URL.Path
	if path == "/api/tools" || path == "/api/project-tools" {
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		json.Unmarshal(q.body, &rpc)
		if rpc.Method == "tools/call" {
			return a.guardOp(q, rpc.Params.Name, rpc.Params.Arguments)
		}
		return nil
	}
	if strings.HasPrefix(path, "/api/project-operations/") {
		return a.guardOp(q, q.r.PathValue("name"), q.body)
	}
	if architectRoutes.MatchString(path) && !hasRole(p, "architect") {
		return fail(403, "Architect role required")
	}
	id := q.r.URL.Query().Get("projectId")
	if strings.HasPrefix(path, "/api/projects/") && q.r.PathValue("id") != "" {
		id = q.r.PathValue("id")
	}
	var b map[string]any
	json.Unmarshal(q.body, &b)
	if s, ok := b["projectId"].(string); ok && id == "" {
		id = s
	}
	if id != "" && !mayAccessProjectID(p, id) {
		return fail(404, "Unknown project")
	}
	return nil
}

func (a *App) ownSubscription(q *Request, name string, b map[string]any) bool {
	if !strings.HasPrefix(name, "account_") {
		return false
	}
	if name == "account_list" || name == "account_create" {
		return true
	}
	id, _ := b["accountId"].(string)
	if id == "" {
		return false
	}
	var holder string
	a.store.pool.QueryRow(q.ctx(), "SELECT coalesce(data->>'holder','') FROM document WHERE kind='managed-account' AND id=$1", id).Scan(&holder)
	return holder == q.person.Email
}

func init() {
	bop("me", true, func(q *Request) (any, error) {
		p := q.person
		if p == nil {
			return doc{"email": nil, "name": nil, "roles": []string{}, "owner": false, "grantsVersion": nil, "changeLogRetentionDays": 7}, nil
		}
		roles := p.Roles
		if roles == nil {
			roles = []string{}
		}
		return doc{"email": p.Email, "name": p.Name, "roles": roles, "owner": p.Owner, "grantsVersion": grantsVersion(p), "changeLogRetentionDays": 7}, nil
	})
}
