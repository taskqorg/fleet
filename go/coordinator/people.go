// People rules of a request (docs/designs/fleet-people-access.md): director, actor, project grants,
// the architect gate and the `me` operation.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fullAccess: nil (hosts, timers), the owner, or a person with grant {kind: "*"} (site-agents: owner-equivalent
// visibility and operations, distinct actor; only people_set stays the owner's).
func fullAccess(p *Person) bool {
	if p == nil || p.Owner {
		return true
	}
	for _, g := range p.Grants {
		if g.Kind == "*" {
			return true
		}
	}
	return false
}

func mayAccessProjectID(p *Person, projectID string) bool {
	if p == nil {
		return false
	}
	if fullAccess(p) {
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

// actor: the calling agent execution, else the request person, else the fallback (an execution, 'operator').
func actor(q *Request, fallback string) string {
	if q.agent != "" {
		return q.agent
	}
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
	if p == nil || fullAccess(p) {
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
	if p == nil || fullAccess(p) {
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
	gatedObjOp("me", true, func(q *Request) (any, error) {
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

// people_list and people_set: people.json as operations (docs/designs/fleet-pm-agent.md §8). A non-owner sees only
// themself, as the person row-level policy does; only the owner sets the list, written atomically and reloaded.
func init() {
	gatedObjOp("people_list", true, func(q *Request) (any, error) {
		out := []*Person{}
		for _, p := range q.app.people {
			if q.person == nil || fullAccess(q.person) || p.Email == q.person.Email {
				c := *p
				c.AccessCommonName = "" // the service token's client ID stays in the deployed file
				out = append(out, &c)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
		return doc{"people": out}, nil
	})
	// setting_changes: the audit of settings, newest first, optionally of one target (host:<id>, people, ...).
	gatedObjOp("setting_changes", true, func(q *Request) (any, error) {
		// The people list is the owner's: its audit (with service-token client IDs) is not shown to architects or site-agents.
		owner := q.person == nil || q.person.Owner
		b := body(q)
		limit := pageLimit(b["limit"], 100)
		rs, err := rowsOf(q, "SELECT seq,data FROM document WHERE kind='setting-change' AND ($1='' OR data->>'target'=$1) AND ($2 OR data->>'target'<>'people') AND ($3::bigint IS NULL OR seq<$3) ORDER BY seq DESC LIMIT $4",
			astr(b["target"]), owner, b["before"], limit+1)
		out := []any{}
		var next any
		for i, r := range rs {
			if i == limit {
				next = rs[i-1]["seq"]
				break
			}
			out = append(out, r["data"])
		}
		return doc{"changes": out, "nextBefore": next}, err
	})
	longOp("people_set", func(q *Request) (any, error) {
		if q.person != nil && !q.person.Owner {
			return nil, fail(403, "Only the owner sets people")
		}
		var b struct {
			People json.RawMessage `json:"people"`
		}
		if json.Unmarshal(q.body, &b) != nil || len(b.People) == 0 || b.People[0] != '[' {
			return nil, fail(400, "Expected people: the whole people.json list")
		}
		previous, err := os.ReadFile(q.app.peopleFile)
		if err != nil {
			return nil, err
		}
		// The file first, so a failed write leaves Fleet on the old list; a failed apply puts the old file back.
		write := func(raw []byte) error {
			tmp := q.app.peopleFile + ".tmp"
			if err := os.WriteFile(tmp, raw, 0o600); err != nil {
				return err
			}
			return os.Rename(tmp, q.app.peopleFile)
		}
		if err := write(b.People); err != nil {
			return nil, err
		}
		if _, err := q.app.setPeople(q.ctx(), b.People, true); err != nil {
			if werr := write(previous); werr != nil {
				return nil, errors.Join(err, werr)
			}
			var pe *pgconn.PgError
			if errors.As(err, &pe) {
				return nil, err
			}
			return nil, fail(400, err.Error())
		}
		var prev, next any
		json.Unmarshal(previous, &prev)
		json.Unmarshal(b.People, &next)
		err = q.app.store.Tx(q.ctx(), nil, true, "", func(tx pgx.Tx) error {
			q.tx = tx
			return put(q, "setting-change", doc{"id": newID(), "target": "people", "field": "people", "previous": prev, "value": next, "actor": actor(q, "operator"), "at": nowMs()})
		})
		return doc{"people": len(q.app.people)}, err
	})
}
