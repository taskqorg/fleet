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
	"path/filepath"
	"slices"
	"sort"
	"strings"

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
// visibility and operations, distinct actor; people administration stays owner-only).
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
		if g.Kind == "project" && (g.ID == projectID || g.ID == "*" && hasAllProjects(p)) {
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

func hasRole(p *Person, r string) bool { return p != nil && slices.Contains(p.Roles, r) }

func hasAllProjects(p *Person) bool {
	return (hasRole(p, "architect") || isViewer(p)) && slices.Contains(p.Grants, personGrant{"project", "*"})
}

// isViewer: a person with the viewer role only (docs/designs/fleet-people-access.md, Wall viewer): reads within the
// project grants, and nothing else (viewerMay).
func isViewer(p *Person) bool {
	return p != nil && len(p.Roles) == 1 && p.Roles[0] == "viewer" && !p.Owner
}

func hasPersonalLab(p *Person) bool {
	return p != nil && slices.Contains(p.Grants, personGrant{"lab", "personal"})
}

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
		return contract.MeReply{Email: &p.Email, Name: &p.Name, Roles: roles, Owner: p.Owner, PersonalLab: ptr(hasPersonalLab(p)), GrantsVersion: &g, ChangeLogRetentionDays: 7}, nil
	})
}

// people_list and people_set: people.json as operations (docs/designs/fleet-pm-agent.md §8). A non-owner sees only
// themself, as the person row-level policy does; only the owner sets the list, written atomically and reloaded.
func init() {
	op("people_list", func(q *Request, r *contract.PeopleListRequest) (contract.PeopleListReply, error) {
		out := contract.PeopleListReply{People: []map[string]json.RawMessage{}}
		manage := valueOr(r.Manage)
		if manage && (q.person == nil || !q.person.Owner) {
			return out, fail(403, "Only the owner manages people")
		}
		people := q.peopleSnapshot().list()
		sort.Slice(people, func(i, j int) bool { return people[i].Email < people[j].Email })
		for _, p := range people {
			if fullAccess(q.person) || p.Email == q.person.Email {
				out.People = append(out.People, recOf(publicPerson(p)))
			}
			if manage {
				initial, err := q.db().DefInitialMemberships(q.ctx(), &p.Email)
				if err != nil {
					return out, err
				}
				row := contract.HumanAccessRow{Email: p.Email, Name: p.Name, Roles: p.Roles, Owner: p.Owner,
					Machine: p.AccessCommonName != "" || !personEmail.MatchString(p.Email), InitialProjects: orEmpty(initial), PersonalLab: hasPersonalLab(p),
					Grants: []contract.HumanAccessGrant{}, Version: peopleRowVersion(p)}
				for _, g := range p.Grants {
					row.Grants = append(row.Grants, contract.HumanAccessGrant{Kind: g.Kind, Id: g.ID})
				}
				out.Roster = append(out.Roster, row)
			}
		}
		if manage {
			if err := cloudflareRoster(q, &out); err != nil {
				return out, err
			}
			sort.Slice(out.CloudflareRemovals, func(i, j int) bool { return out.CloudflareRemovals[i].Email < out.CloudflareRemovals[j].Email })
		}
		return out, nil
	})
	op("people_access_sync", peopleAccessSync)
	op("people_update", peopleUpdate)
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

func publicPerson(p *Person) *Person {
	out := *p
	out.AccessCommonName = ""
	return &out
}

func peopleRowVersion(p *Person) string {
	return sha256hex(jsonBytes(publicPerson(p)))
}

// Both writers and file reloads take the same lock. Recheck authority after its acquisition.
func peopleOwner(q *Request) error {
	if q.person == nil {
		return fail(403, "Only the owner sets people")
	}
	p, err := q.app.resolveAccess(q.ctx(), q.person.Email)
	if err != nil {
		return err
	}
	if !p.Owner {
		return fail(403, "Only the owner sets people")
	}
	return nil
}

func peopleSet(q *Request, people json.RawMessage) (int, error) {
	people = bytes.TrimSpace(people)
	if len(people) == 0 || people[0] != '[' {
		return 0, fail(400, "Expected people: the whole people.json list")
	}
	cloudflarePeopleMu.Lock()
	defer cloudflarePeopleMu.Unlock()
	peopleMu.Lock()
	defer peopleMu.Unlock()
	if err := peopleOwner(q); err != nil {
		return 0, err
	}
	previous, err := os.ReadFile(q.app.peopleFile)
	if err != nil {
		return 0, err
	}
	err = writePeople(q, previous, people, redactPeople(previous), redactPeople(people), nil)
	return q.app.peopleSnapshot().count(), err
}

func redactPeople(raw []byte) []map[string]json.RawMessage {
	var people []map[string]json.RawMessage
	_ = json.Unmarshal(raw, &people)
	for _, p := range people {
		for field := range p {
			if strings.EqualFold(field, "accessCommonName") {
				delete(p, field)
			}
		}
	}
	return people
}

func peopleUpdate(q *Request, r *contract.PeopleUpdateRequest) (contract.PeopleUpdateReply, error) {
	out := contract.PeopleUpdateReply{Email: strings.ToLower(strings.TrimSpace(r.Email))}
	if !personEmail.MatchString(out.Email) {
		return out, fail(400, "Enter a human sign-in email")
	}
	cloudflarePeopleMu.Lock()
	defer cloudflarePeopleMu.Unlock()
	intent := cloudflareIntent(q.app, !valueOr(r.Revoke))
	if err := updatePeopleRoster(q, out.Email, r, intent); err != nil {
		return out, err
	}
	state := finishCloudflareSync(q, out.Email, intent)
	out.Cloudflare = &state
	return out, nil
}

func updatePeopleRoster(q *Request, email string, r *contract.PeopleUpdateRequest, intent peopleCloudflareChange) error {
	peopleMu.Lock()
	defer peopleMu.Unlock()
	if err := peopleOwner(q); err != nil {
		return err
	}
	raw, err := os.ReadFile(q.app.peopleFile)
	if err != nil {
		return err
	}
	people, err := updateHuman(q.app.peopleSnapshot().person(email), raw, email, r)
	if err != nil {
		return err
	}
	recordIntent := func(tx *Request) error {
		return auditCloudflare(tx, email, nil, intent)
	}
	if !intent.Admit {
		// The file is replaced before its database projection. Keep the removal visible if the
		// coordinator stops between those writes and loads the new file on restart.
		if err := q.app.store.Write(q.ctx(), q, nil, recordIntent); err != nil {
			return err
		}
		recordIntent = nil
	}
	return writePeople(q, raw, people, redactPeople(raw), redactPeople(people), recordIntent)
}

func updateHuman(current *Person, raw []byte, email string, r *contract.PeopleUpdateRequest) ([]byte, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil || rows == nil {
		return nil, fail(409, "Fix the people file before editing the roster")
	}
	index := -1
	for i, row := range rows {
		p, err := personRow(row)
		if err != nil {
			return nil, fail(409, "Fix the people file before editing the roster")
		}
		if strings.EqualFold(p.Email, email) {
			if index >= 0 {
				return nil, fail(409, "Email appears more than once in the people file")
			}
			if p.Owner || p.AccessCommonName != "" {
				return nil, fail(403, "Owner and machine identities are managed separately")
			}
			p.Email = strings.ToLower(p.Email)
			p.Name = firstString(p.Name, p.Email)
			if current == nil || peopleRowVersion(p) != peopleRowVersion(current) || r.BaseVersion != peopleRowVersion(p) {
				return nil, fail(409, "Access changed. Reopen the person before saving")
			}
			index = i
		}
	}
	if index < 0 && (current != nil || r.BaseVersion != "" || valueOr(r.Revoke)) {
		return nil, fail(409, "Access changed. Reopen the person before saving")
	}
	if valueOr(r.Revoke) {
		return jsonBytes(slices.Delete(rows, index, index+1)), nil
	}
	p := Person{Email: email, Name: strings.TrimSpace(deref(r.Name)), Roles: unique(r.Roles), Grants: []personGrant{}}
	if index >= 0 {
		// Keep legacy full-access grants when editing other fields. This workspace never adds one.
		for _, g := range current.Grants {
			if g.Kind == "*" {
				p.Grants = append(p.Grants, g)
			}
		}
	}
	for _, id := range unique(r.Projects) {
		if strings.TrimSpace(id) != id || strings.ContainsAny(id, ",\r\n\t") {
			return nil, fail(400, "Invalid project grant")
		}
		p.Grants = append(p.Grants, personGrant{"project", id})
	}
	if valueOr(r.PersonalLab) {
		p.Grants = append(p.Grants, personGrant{"lab", "personal"})
	}
	next := json.RawMessage(jsonBytes(p))
	if _, err := personRow(next); err != nil {
		return nil, fail(400, err.Error())
	}
	if index < 0 {
		rows = append(rows, next)
	} else {
		rows[index] = next
	}
	return jsonBytes(rows), nil
}

func writePeople(q *Request, previous, next []byte, before, after any, change func(*Request) error) error {
	// A failed projection or audit rolls back the file as well as the transaction.
	write := func(raw []byte) (string, error) {
		f, err := os.CreateTemp(filepath.Dir(q.app.peopleFile), ".people-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		_, err = f.Write(raw)
		info, statErr := f.Stat()
		if err = errors.Join(err, statErr, f.Close()); err != nil {
			return "", err
		}
		return peopleFileKey(info), os.Rename(f.Name(), q.app.peopleFile)
	}
	key, err := write(next)
	if err != nil {
		return err
	}
	if _, err := q.app.setPeople(q.ctx(), next, true, func(req *Request) error {
		a := *req
		a.person, a.agent = q.person, q.agent
		if err := audit(&a, "people", "people", before, after, auditExtra{}); err != nil {
			return err
		}
		if change != nil {
			return change(&a)
		}
		return nil
	}); err != nil {
		if _, werr := write(previous); werr != nil {
			return errors.Join(err, werr)
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return err
		}
		return fail(400, err.Error())
	}
	peopleSeen = key
	return nil
}
