package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
)

// People: people.json as packages/coordinator/people.ts reads it, re-read when mtime, size or inode change. A malformed
// file keeps the last valid load (after a restart: the person rows). The TypeScript coordinator alone writes the person
// rows and raises the needs-you question for a broken file; Go only reads.
type People struct {
	file  string
	store *Store
	mu    sync.Mutex
	seen  string
	byKey map[string]*Person
}

type Grant struct{ Kind, ID string }

type Person struct {
	Email  string
	Roles  []string
	Owner  bool
	Grants []Grant
}

func (p *Person) mayAccessProject(id string) bool {
	if p.Owner {
		return true
	}
	for _, g := range p.Grants {
		if g.Kind == "project" && g.ID == id && id != "" {
			return true
		}
	}
	return false
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)

// parsePeople: parsePeople in people.ts, the same checks and messages.
func parsePeople(raw []byte) (map[string]*Person, error) {
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("people.json must be a list")
	}
	out := map[string]*Person{}
	owners := 0
	for _, p := range list {
		e, _ := p["email"].(string)
		if p == nil || !emailPattern.MatchString(e) {
			return nil, errors.New("Each person needs an e-mail")
		}
		email := strings.ToLower(e)
		if out[email] != nil {
			return nil, errors.New("Duplicate person " + email)
		}
		roles, _ := p["roles"].([]any)
		x := &Person{Email: email}
		for _, r := range roles {
			s, _ := r.(string)
			if s != "operator" && s != "architect" {
				roles = nil
				break
			}
			x.Roles = append(x.Roles, s)
		}
		if len(roles) == 0 {
			return nil, fmt.Errorf("Roles of %s must be operator and/or architect", email)
		}
		if o, ok := p["owner"]; ok {
			b, isBool := o.(bool)
			if !isBool {
				return nil, fmt.Errorf("owner of %s must be true or false", email)
			}
			x.Owner = b
		}
		if g, ok := p["grants"]; ok && g != nil {
			gs, isList := g.([]any)
			if !isList {
				return nil, fmt.Errorf("Grants of %s must be [{kind, id}]", email)
			}
			for _, v := range gs {
				m, _ := v.(map[string]any)
				k, ok1 := m["kind"].(string)
				id, ok2 := m["id"].(string)
				if !ok1 || !ok2 {
					return nil, fmt.Errorf("Grants of %s must be [{kind, id}]", email)
				}
				x.Grants = append(x.Grants, Grant{k, id})
			}
		}
		if x.Owner {
			owners++
		}
		out[email] = x
	}
	if owners != 1 {
		return nil, errors.New("Exactly one person must have owner: true")
	}
	return out, nil
}

func (ps *People) personFor(ctx context.Context, email string) *Person {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	key := "missing"
	if st, err := os.Stat(ps.file); err == nil {
		key = fmt.Sprint(st.ModTime().UnixNano(), ":", st.Size(), ":", inode(st))
	}
	if key != ps.seen {
		next, err := func() (map[string]*Person, error) {
			raw, err := os.ReadFile(ps.file)
			if err != nil {
				return nil, err
			}
			return parsePeople(raw)
		}()
		if err == nil {
			ps.byKey, ps.seen = next, key
		} else if ps.byKey == nil {
			if rows, rerr := ps.fromRows(ctx); rerr == nil {
				ps.byKey, ps.seen = rows, key
			} else {
				log.Printf("people: %v; %v", err, rerr)
			}
		} else {
			ps.seen = key
		}
	}
	return ps.byKey[strings.ToLower(email)]
}

func (ps *People) fromRows(ctx context.Context) (map[string]*Person, error) {
	out := map[string]*Person{}
	rows, err := ps.store.pool.Query(ctx, "SELECT email,roles,owner FROM person")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		p := &Person{}
		if err := rows.Scan(&p.Email, &p.Roles, &p.Owner); err != nil {
			return nil, err
		}
		out[p.Email] = p
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	gs, err := ps.store.pool.Query(ctx, "SELECT email,object_kind,object_id FROM person_grant")
	if err != nil {
		return nil, err
	}
	for gs.Next() {
		var e string
		var g Grant
		if err := gs.Scan(&e, &g.Kind, &g.ID); err != nil {
			return nil, err
		}
		if p := out[e]; p != nil {
			p.Grants = append(p.Grants, g)
		}
	}
	return out, gs.Err()
}

// scopeOf: the owner works in '*' (nil); anyone else in the projects of their grants, plus null-project rows for architects.
func scopeOf(p *Person) *Scope {
	if p == nil || p.Owner {
		return nil
	}
	ids := []string{}
	for _, g := range p.Grants {
		if g.Kind == "project" {
			ids = append(ids, g.ID)
		}
	}
	prefix := ""
	for _, r := range p.Roles {
		if r == "architect" {
			prefix = ","
		}
	}
	return &Scope{ProjectIDs: prefix + strings.Join(ids, ","), Person: p.Email}
}
