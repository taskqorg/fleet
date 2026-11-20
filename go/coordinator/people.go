// People rules of a request (docs/designs/fleet-people-access.md): director, actor, project grants,
// the architect gate and the `me` operation.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"

	"fleet/contract"
	"fleet/coordinator/db"

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

func hasRole(p *Person, r string) bool { return slices.Contains(p.Roles, r) }

// projectDefinitionRead: a definition read that a project grant allows for kind project, without the architect role
// (declaredAuth checks the grant and lets it past the declared architect rule).
func projectDefinitionRead(name string) bool {
	switch name {
	case "definition_list", "definition_get", "definition_history", "definition_diff", "definition_usage":
		return true
	}
	return false
}

// routeGate: the person gate of the remaining REST routes: a project named by ?projectId= or a body projectId must be
// granted (404). Operations are authorized only by their declared auth (declaredAuth).
func routeGate(q *Request) error {
	p := q.person
	if p == nil || fullAccess(p) {
		return nil
	}
	id := q.r.URL.Query().Get("projectId")
	var b struct {
		ProjectID any `json:"projectId"`
	}
	json.Unmarshal(q.body, &b)
	if s, ok := b.ProjectID.(string); ok && id == "" {
		id = s
	}
	if id != "" && !mayAccessProjectID(p, id) {
		return fail(404, "Unknown project")
	}
	return nil
}

// holders: for each holder:<kind> rule (declaredAuth), the holder of the object the request names, and whether it exists.
var holders = map[string]func(q *Request) (holder string, found bool, err error){
	"account": func(q *Request) (string, bool, error) {
		id := jsonPathString(q.body, "accountId")
		if id == "" {
			return "", false, nil
		}
		// Its own read on the request transaction's connection: the account row may be outside the person's scope.
		ctx := withTx(q)
		holder, err := connDB(ctx, q.app).PeopleAccountHolder(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return holder, err == nil, err
	},
	"connection": func(q *Request) (string, bool, error) {
		n := jsonPathString(q.body, "connection")
		if n == "" {
			// connections_key_state names its key: the handler checks the key's connection.
			return q.person.Email, true, nil
		}
		holder, err := q.db().PeopleConnectionHolder(q.ctx(), "connection:"+n)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return holder, err == nil, err
	},
	"host": func(q *Request) (string, bool, error) {
		id := jsonPathString(q.body, "id")
		if id == "" {
			return "", false, nil
		}
		h, err := liveHost(q, id)
		if err != nil || h == nil {
			return "", false, err
		}
		return deref(h.Holder), true, nil
	},
}

func init() {
	op("me", func(q *Request, _ *contract.MeRequest) (contract.MeReply, error) {
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
	})
}

// people_list and people_set: people.json as operations (docs/designs/fleet-pm-agent.md §8). A non-owner sees only
// themself, as the person row-level policy does; only the owner sets the list, written atomically and reloaded.
func init() {
	op("people_list", func(q *Request, _ *contract.PeopleListRequest) (contract.PeopleListReply, error) {
		out := []map[string]json.RawMessage{}
		for _, p := range q.peopleSnapshot().list() {
			if fullAccess(q.person) || p.Email == q.person.Email {
				c := *p
				c.AccessCommonName = "" // the service token's client ID stays in the deployed file
				out = append(out, recOf(&c))
			}
		}
		sort.Slice(out, func(i, j int) bool { return string(out[i]["email"]) < string(out[j]["email"]) })
		return contract.PeopleListReply{People: out}, nil
	})
	// setting_changes: the audit of settings, newest first, optionally of one target (host:<id>, people, ...).
	op("setting_changes", func(q *Request, r *contract.SettingChangesRequest) (contract.SettingChangesReply, error) {
		// The people list is the owner's: its audit (with service-token client IDs) is not shown to architects or site-agents.
		owner := q.person.Owner
		var given any
		if r.Limit != nil {
			given = *r.Limit
		}
		limit := pageLimit(given, 100)
		rs, err := q.db().AuditRecords(q.ctx(), db.AuditRecordsParams{Target: deref(r.Target), Owner: owner, Before: r.Before, Lim: int32(limit + 1)})
		out := contract.SettingChangesReply{Changes: []map[string]json.RawMessage{}}
		for i, row := range rs {
			if i == limit {
				n := rs[i-1].Seq
				out.NextBefore = &n
				break
			}
			change := map[string]json.RawMessage{}
			if err := json.Unmarshal(row.Data, &change); err != nil {
				return out, err
			}
			out.Changes = append(out.Changes, change)
		}
		return out, err
	})
	// The owner only (stricter than the declared owner rule, which full access passes); people.json keeps the owner's bytes.
	op("people_set", func(q *Request, r *contract.PeopleSetRequest) (contract.PeopleSetReply, error) {
		n, err := peopleSet(q, r.People)
		return contract.PeopleSetReply{People: int64(n)}, err
	})
}

func peopleSet(q *Request, people json.RawMessage) (int, error) {
	if !q.person.Owner {
		return 0, fail(403, "Only the owner sets people")
	}
	b := struct{ People json.RawMessage }{bytes.TrimSpace(people)}
	if len(b.People) == 0 || b.People[0] != '[' {
		return 0, fail(400, "Expected people: the whole people.json list")
	}
	previous, err := os.ReadFile(q.app.peopleFile)
	if err != nil {
		return 0, err
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
		return 0, err
	}
	if _, err := q.app.setPeople(q.ctx(), b.People, true); err != nil {
		if werr := write(previous); werr != nil {
			return 0, errors.Join(err, werr)
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return 0, err
		}
		return 0, fail(400, err.Error())
	}
	var prev, next any
	json.Unmarshal(previous, &prev)
	json.Unmarshal(b.People, &next)
	err = q.app.store.Tx(q.ctx(), nil, true, "", func(tx pgx.Tx) error {
		q.tx = tx
		return audit(q, "people", "people", prev, next, auditExtra{})
	})
	return q.app.peopleSnapshot().count(), err
}
