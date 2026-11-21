package main

import (
	"context"
	"errors"
	"maps"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// resolveAccess never mutates the file projection or borrows the owner's identity.
func (a *App) resolveAccess(ctx context.Context, email string) (*Person, error) {
	s := a.store
	s.authMu.Lock()
	generation := s.authGeneration
	if s.authWrites == 0 {
		if cached := s.authPeople[email]; cached != nil {
			out := copyPerson(cached)
			s.authMu.Unlock()
			return out, nil
		}
	}
	s.authMu.Unlock()
	var out *Person
	err := s.Tx(ctx, nil, false, "", func(tx pgx.Tx) error { var err error; out, err = resolveAccessTx(ctx, tx, email); return err })
	if err == nil {
		s.authMu.Lock()
		if s.authWrites == 0 && s.authGeneration == generation {
			if len(s.authPeople) >= 1024 {
				s.authPeople = nil
			}
			if s.authPeople == nil {
				s.authPeople = map[string]*Person{}
			}
			s.authPeople[email] = copyPerson(out)
		}
		s.authMu.Unlock()
	}
	return out, err
}
func resolveAccessTx(ctx context.Context, tx pgx.Tx, email string) (*Person, error) {
	return resolveAccessQuery(ctx, tx, email, false)
}

func resolveAccessShareTx(ctx context.Context, tx pgx.Tx, email string) (*Person, error) {
	return resolveAccessQuery(ctx, tx, email, true)
}

// personGrant: one grant of a Person (main.go).
type personGrant = struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func resolveAccessQuery(ctx context.Context, tx pgx.Tx, email string, share bool) (*Person, error) {
	d := db.New(tx)
	out := Person{Grants: []personGrant{}}
	err := func() error {
		var grants []db.DefGrantsRow
		if share {
			p, err := d.DefPersonShare(ctx, email)
			if err != nil {
				return err
			}
			out.Email, out.Name, out.Roles, out.Owner, out.AccessVersion = p.Email, p.Name, p.Roles, p.Owner, p.LoadedAt
			gs, err := d.DefGrantsShare(ctx, email)
			if err != nil {
				return err
			}
			for _, g := range gs {
				grants = append(grants, db.DefGrantsRow(g))
			}
		} else {
			p, err := d.DefPerson(ctx, email)
			if err != nil {
				return err
			}
			out.Email, out.Name, out.Roles, out.Owner, out.AccessVersion = p.Email, p.Name, p.Roles, p.Owner, p.LoadedAt
			if grants, err = d.DefGrants(ctx, email); err != nil {
				return err
			}
		}
		membershipVersion, err := d.DefMembershipVersion(ctx, &email)
		if err != nil {
			return err
		}
		out.AccessVersion += "/" + membershipVersion
		seen := map[personGrant]bool{}
		addGrant := func(kind, id string) {
			if g := (personGrant{Kind: kind, ID: id}); !seen[g] {
				seen[g] = true
				out.Grants = append(out.Grants, g)
			}
		}
		for _, g := range grants {
			addGrant(g.ObjectKind, g.ObjectID)
		}
		projects, err := d.DefInitialMemberships(ctx, &email)
		if err != nil {
			return err
		}
		for _, id := range projects {
			addGrant("project", id)
		}
		return nil
	}()
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(403, "Unknown person")
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func seedProjectDefinitions() ([]definitionFile, map[string]error) {
	files := []definitionFile{}
	broken := map[string]error{}
	for _, id := range shippedProjectIDs() {
		p, err := shippedProjectContext(id)
		if err != nil {
			broken["project/"+id] = err
			continue
		}
		settings := maps.Clone(p) // the open project settings as shippedProjectContext (projects.go) reads them
		delete(settings, "settings")
		delete(settings, "unknownKeys")
		delete(settings, "context")
		type contextFile struct {
			Path any `json:"path"`
			Text any `json:"text"`
		}
		texts := []contextFile{}
		contexts, _ := p["context"].([]doc)
		for _, f := range contexts {
			texts = append(texts, contextFile{Path: f["path"], Text: f["text"]})
		}
		text, err := canonicalText(jsonBytes(struct {
			Settings     doc           `json:"settings"`
			ContextFiles []contextFile `json:"contextFiles"`
		}{settings, texts}))
		if err != nil {
			broken["project/"+id] = err
			continue
		}
		files = append(files, definitionFile{"project", id, "config/projects/" + id + "/project.json", []byte(text)})
	}
	return files, broken
}

func refreshRequestAccess(q *Request) error {
	if q.person == nil {
		return nil
	}
	var p *Person
	var err error
	if q.tx != nil {
		p, err = resolveAccessTx(q.ctx(), q.tx, q.person.Email)
	} else {
		p, err = q.app.resolveAccess(q.ctx(), q.person.Email)
	}
	if err != nil {
		return err
	}
	q.person = p
	if q.tx != nil {
		sc := scopeOf(p)
		ids, person := "*", ""
		if sc != nil {
			ids, person = sc.ProjectIDs, sc.Person
		}
		err = q.db().DefSetScope(q.ctx(), db.DefSetScopeParams{Ids: ids, Person: person})
	}
	return err
}

func projectAgentArguments(q *Request, args doc, executionID string, preparedRead bool) (doc, error) {
	e, err := recGet[doc](q, recExecution, executionID)
	if err != nil {
		return nil, err
	}
	sessionID, _ := e["sessionId"].(string)
	session, err := recGet[doc](q, recSession, sessionID)
	if err != nil {
		return nil, err
	}
	if _, focused := session["focus"]; focused {
		resolver := focusProject
		if preparedRead {
			resolver = focusProjectID
		}
		project, err := resolver(q, session, args["projectId"])
		if err != nil {
			return nil, err
		}
		args["projectId"] = project
		return args, nil
	}
	project, _ := session["projectId"].(string)
	if preparedRead && project == "" {
		return nil, fail(400, "Expected projectId")
	}
	if args["projectId"] == nil {
		args["projectId"] = project
	}
	if project == "" || args["projectId"] != project || !mayAccessProjectID(q.person, project) {
		return nil, fail(403, "Project outside the session focus")
	}
	return args, nil
}
