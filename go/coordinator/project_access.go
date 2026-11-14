package main

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

// resolveAccess never mutates the file projection or borrows the owner's identity.
func (a *App) resolveAccess(ctx context.Context, email string) (*Person, error) {
	var out *Person
	err := a.store.Tx(ctx, nil, false, "", func(tx pgx.Tx) error { var err error; out, err = resolveAccessTx(ctx, tx, email); return err })
	return out, err
}
func resolveAccessTx(ctx context.Context, tx pgx.Tx, email string) (*Person, error) {
	return resolveAccessQuery(ctx, tx, email, false)
}

func resolveAccessShareTx(ctx context.Context, tx pgx.Tx, email string) (*Person, error) {
	return resolveAccessQuery(ctx, tx, email, true)
}

func resolveAccessQuery(ctx context.Context, tx pgx.Tx, email string, share bool) (*Person, error) {
	var out Person
	err := func() error {
		forShare := ""
		if share {
			forShare = " FOR SHARE"
		}
		if err := tx.QueryRow(ctx, "SELECT email,name,roles,owner,loaded_at::text FROM person WHERE email=$1"+forShare, email).Scan(&out.Email, &out.Name, &out.Roles, &out.Owner, &out.AccessVersion); err != nil {
			return err
		}
		var membershipVersion string
		if err := tx.QueryRow(ctx, `SELECT coalesce(string_agg(id||':'||access_version::text,',' ORDER BY id),'') FROM definition WHERE kind='project' AND initial_member=$1`, email).Scan(&membershipVersion); err != nil {
			return err
		}
		out.AccessVersion += "/" + membershipVersion
		grants := []doc{}
		seen := map[string]struct{}{}
		addGrant := func(kind, id string) {
			k := kind + "\x00" + id
			if _, ok := seen[k]; ok {
				return
			}
			seen[k] = struct{}{}
			grants = append(grants, doc{"kind": kind, "id": id})
		}
		rows, err := tx.Query(ctx, `SELECT object_kind,object_id FROM person_grant WHERE email=$1`+forShare, email)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, id string
			if err := rows.Scan(&kind, &id); err != nil {
				rows.Close()
				return err
			}
			addGrant(kind, id)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		defRows, err := tx.Query(ctx, `SELECT 'project',id FROM definition WHERE kind='project' AND initial_member=$1 AND initial_member_revoked_at IS NULL`, email)
		if err != nil {
			return err
		}
		for defRows.Next() {
			var kind, id string
			if err := defRows.Scan(&kind, &id); err != nil {
				defRows.Close()
				return err
			}
			addGrant(kind, id)
		}
		if err = defRows.Err(); err != nil {
			defRows.Close()
			return err
		}
		defRows.Close()
		raw, _ := json.Marshal(grants)
		return json.Unmarshal(raw, &out.Grants)
	}()
	if err == pgx.ErrNoRows {
		return nil, fail(403, "Unknown person")
	}
	return &out, err
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
		settings := doc{}
		for k, v := range p {
			if k != "settings" && k != "unknownKeys" && k != "context" {
				settings[k] = v
			}
		}
		texts := []any{}
		contexts, _ := p["context"].([]doc)
		for _, f := range contexts {
			m := mapOf(f)
			texts = append(texts, doc{"path": m["path"], "text": m["text"]})
		}
		text, err := canonicalText(doc{"settings": settings, "contextFiles": texts})
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
		_, err = q.tx.Exec(q.ctx(), "SELECT set_config('fleet.project_ids',$1,true),set_config('fleet.person',$2,true)", ids, person)
	}
	return err
}

func projectAgentArguments(q *Request, args doc, executionID string) (doc, error) {
	e, err := get(q, executionID, "execution")
	if err != nil {
		return nil, err
	}
	session, err := get(q, e["sessionId"], "session")
	if err != nil {
		return nil, err
	}
	if _, focused := session["focus"]; focused {
		project, err := focusProject(q, session, args["projectId"])
		if err != nil {
			return nil, err
		}
		args["projectId"] = project
		return args, nil
	}
	project := astr(session["projectId"])
	if args["projectId"] == nil {
		args["projectId"] = project
	}
	if project == "" || args["projectId"] != project || !mayAccessProjectID(q.person, project) {
		return nil, fail(403, "Project outside the session focus")
	}
	return args, nil
}
