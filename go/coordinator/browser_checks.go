// Browser request checks ported from the TypeScript onRequest hook (packages/coordinator/app.ts) and the people file
// re-read (packages/coordinator/people.ts). authorize (main.go) calls both; host requests skip the origin check.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// FLEET_BROWSER_ORIGIN: the public origin of the operator app (https://fleet.example). FLEET_BROWSER_PROXY_AUTH=true: the
// Access guard injects the browser Bearer, so the cookie alone authenticates and every write must prove its origin.
var browserOrigin, browserProxyAuth = os.Getenv("FLEET_BROWSER_ORIGIN"), os.Getenv("FLEET_BROWSER_PROXY_AUTH") == "true"

func init() {
	if browserProxyAuth && browserOrigin == "" {
		log.Fatal("Proxy browser auth requires configured browser origin")
	}
}

// checkOrigin: with proxy auth a write must carry Origin equal to the configured origin; any Origin that is not the
// configured one (or this server's own scheme://host when none is configured) is refused.
func checkOrigin(q *Request) error {
	origin, m := q.r.Header.Get("Origin"), q.r.Method
	if browserProxyAuth && m != "GET" && m != "HEAD" && m != "OPTIONS" && origin != browserOrigin {
		return fail(403, "Origin required")
	}
	own := browserOrigin
	if own == "" {
		scheme := "http"
		if q.r.TLS != nil {
			scheme = "https"
		}
		own = scheme + "://" + q.r.Host
	}
	if origin != "" && origin != own {
		return fail(403, "Origin rejected")
	}
	return nil
}

// The people file is re-read when its mtime or size changes; a broken file keeps the last valid load (after a restart:
// the person rows) and raises one open needs-you question. The key is recorded only after the load was applied.
// The key holds the inode too, so an atomic replace that keeps mtime and size is seen.
var peopleMu sync.Mutex
var peopleSeen string

func (a *App) refreshPeople(ctx context.Context) error {
	peopleMu.Lock()
	defer peopleMu.Unlock()
	key, problem, partial := "missing", "", false
	raw, err := []byte(nil), error(nil)
	if st, serr := os.Stat(a.peopleFile); serr != nil {
		problem = "cannot read the file (" + serr.Error() + ")"
	} else {
		key = fmt.Sprint(st.ModTime().UnixNano(), ":", st.Size(), ":", inodeOf(st))
	}
	if key == peopleSeen {
		return nil
	}
	if problem == "" {
		if raw, err = os.ReadFile(a.peopleFile); err != nil {
			problem = err.Error()
		}
	}
	if problem == "" {
		var skipped []string
		skipped, err = a.setPeople(ctx, raw, false)
		if err != nil && pgCode(err) != "" {
			return err // a database failure is not a broken file: try again on the next request
		}
		if err != nil {
			problem = err.Error()
		} else if len(skipped) > 0 {
			problem, partial = strings.Join(skipped, "; "), true
		}
	}
	if problem != "" {
		if a.people.Load() == nil {
			if err := a.peopleFromRows(ctx); err != nil {
				return err
			}
		}
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
			var n int
			if err := q.tx.QueryRow(ctx, "SELECT count(*) FROM question WHERE state='open' AND data->>'peopleFile'='true'").Scan(&n); err != nil || n > 0 {
				return err
			}
			_, err := askRow(q, "operator_decision", peopleQuestion(problem, partial), nil, []any{"fixed"}, "fleet", doc{"peopleFile": true})
			return err
		}); err != nil {
			return err
		}
	}
	peopleSeen = key
	return nil
}

func peopleQuestion(problem string, partial bool) string {
	if partial {
		return "people.json has entries Fleet left out: " + problem + ". Everyone else in the file is loaded."
	}
	return "people.json is not valid: " + problem + ". Fleet keeps the last valid list of people until the file is fixed."
}

// peopleFromRows: after a restart with a broken file, the rows of the last valid load are that load.
func (a *App) peopleFromRows(ctx context.Context) error {
	people := map[string]*Person{}
	return a.store.Tx(ctx, nil, false, "", func(tx pgx.Tx) error {
		rows, _ := tx.Query(ctx, "SELECT email,name,roles,owner FROM person")
		for rows.Next() {
			p := &Person{}
			if err := rows.Scan(&p.Email, &p.Name, &p.Roles, &p.Owner); err != nil {
				return err
			}
			people[p.Email] = p
		}
		if err := rows.Err(); err != nil {
			return err
		}
		grants, _ := tx.Query(ctx, "SELECT email,object_kind,object_id FROM person_grant")
		for grants.Next() {
			var email, kind, id string
			if err := grants.Scan(&email, &kind, &id); err != nil {
				return err
			}
			if p := people[email]; p != nil {
				p.Grants = append(p.Grants, struct {
					Kind string `json:"kind"`
					ID   string `json:"id"`
				}{kind, id})
			}
		}
		if err := grants.Err(); err != nil {
			return err
		}
		a.publishPeople(people)
		return nil
	})
}
