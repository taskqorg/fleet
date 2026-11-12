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

	"fleet/contract"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Published projections are immutable; callers receive copies, and one request keeps one projection.
type peopleSnapshot struct{ byEmail map[string]*Person }

func copyPerson(p *Person) *Person {
	if p == nil {
		return nil
	}
	c := *p
	c.Roles = slices.Clone(p.Roles)
	c.Grants = slices.Clone(p.Grants)
	return &c
}
func (a *App) publishPeople(people map[string]*Person) {
	snapshot := &peopleSnapshot{byEmail: make(map[string]*Person, len(people))}
	for email, p := range people {
		snapshot.byEmail[email] = copyPerson(p)
	}
	a.people.Store(snapshot)
}
func (a *App) peopleSnapshot() *peopleSnapshot {
	if snapshot := a.people.Load(); snapshot != nil {
		return snapshot
	}
	return &peopleSnapshot{}
}
func (q *Request) peopleSnapshot() *peopleSnapshot {
	if q.peopleView == nil {
		q.peopleView = q.app.peopleSnapshot()
	}
	return q.peopleView
}
func (s *peopleSnapshot) person(email string) *Person { return copyPerson(s.byEmail[email]) }
func (s *peopleSnapshot) count() int                  { return len(s.byEmail) }
func (s *peopleSnapshot) list() []*Person {
	out := make([]*Person, 0, len(s.byEmail))
	for _, p := range s.byEmail {
		out = append(out, copyPerson(p))
	}
	return out
}

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
	for _, p := range q.peopleSnapshot().list() {
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

// ownerOnly fails closed for every caller kind: a browser person or an agent tool call (which acts as its run's
// director, or as the requester of taken work) needs full access; a call with no person is refused.
func ownerOnly(q *Request, msg string) error {
	if q.person == nil || !fullAccess(q.person) {
		return fail(403, msg)
	}
	return nil
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
	b, _ := json.Marshal([]any{roles, p.Owner, gs, p.AccessVersion})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

var (
	architectOps    = regexp.MustCompile(`^(account_|lab_|connection|recovery|reconcil|transport_|artifact_rule|profiles_list|price_add|definition_|agent_effective$|agent_compatibility$|agent_test$|harness_models$)`)
	architectRoutes = regexp.MustCompile(`^/api/(lab|accounts|recovery|reconciliation|recovery-tools|profiles)(/|$)`)
)

func hasRole(p *Person, r string) bool { return slices.Contains(p.Roles, r) }

// guardOp: the architect gate on operation names (a holder's own subscription passes) and the project grant of a named project.
func (a *App) guardOp(q *Request, name string, body json.RawMessage) error {
	p := q.person
	var request doc
	_ = json.Unmarshal(body, &request)
	if request["kind"] == "project" && strings.HasPrefix(name, "definition_") {
		switch name {
		case "definition_list", "definition_get", "definition_history", "definition_diff", "definition_usage":
		default:
			return fail(403, "Use project_create or project_update")
		}
	}
	if p == nil || fullAccess(p) {
		return nil
	}
	var b map[string]any
	json.Unmarshal(body, &b)
	if strings.HasPrefix(name, "definition_") {
		if b["kind"] == "project" {
			if name != "definition_list" && !mayAccessProjectID(p, astr(b["id"])) {
				return fail(404, "Unknown project")
			}
			switch name {
			case "definition_list", "definition_get", "definition_history", "definition_diff", "definition_usage":
				return nil
			default:
				return fail(403, "Use project_create or project_update")
			}
		}
		if name == "definition_export" {
			for _, k := range anys(b["keys"]) {
				m := mapOf(k)
				if m["kind"] == "project" && !mayAccessProjectID(p, astr(m["id"])) {
					return fail(404, "Unknown project")
				}
			}
		}
	}
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
	gatedObjOp("me", true, typedOp(func(q *Request, _ *contract.MeRequest) (contract.MeReply, error) {
		p := q.person
		if p == nil {
			return contract.MeReply{Roles: []string{}, ChangeLogRetentionDays: 7}, nil
		}
		roles := p.Roles
		if roles == nil {
			roles = []string{}
		}
		g := grantsVersion(p)
		return contract.MeReply{Email: &p.Email, Name: &p.Name, Roles: roles, Owner: p.Owner, GrantsVersion: &g, ChangeLogRetentionDays: 7}, nil
	}))
}

// people_list and people_set: people.json as operations (docs/designs/fleet-pm-agent.md §8). A non-owner sees only
// themself, as the person row-level policy does; only the owner sets the list, written atomically and reloaded.
func init() {
	gatedObjOp("people_list", true, shaped[contract.PeopleListRequest, contract.PeopleListReply](func(q *Request) (any, error) {
		out := []*Person{}
		for _, p := range q.peopleSnapshot().list() {
			if q.person == nil || fullAccess(q.person) || p.Email == q.person.Email {
				c := *p
				c.AccessCommonName = "" // the service token's client ID stays in the deployed file
				out = append(out, &c)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
		return doc{"people": out}, nil
	}))
	// setting_changes: the audit of settings, newest first, optionally of one target (host:<id>, people, ...).
	gatedObjOp("setting_changes", true, shaped[contract.SettingChangesRequest, contract.SettingChangesReply](func(q *Request) (any, error) {
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
	}))
	longOp("people_set", checkFirst(func(q *Request) error {
		if q.person != nil && !q.person.Owner {
			return fail(403, "Only the owner sets people")
		}
		return nil
	}, typedOp(func(q *Request, _ *contract.PeopleSetRequest) (contract.PeopleSetReply, error) {
		// The handler reads the body as sent: people.json keeps the owner's bytes (the struct only checked them).
		out, err := peopleSet(q)
		return replyAs[contract.PeopleSetReply](out, err)
	})))
}

func peopleSet(q *Request) (any, error) {
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
	return doc{"people": q.app.peopleSnapshot().count()}, err
}
