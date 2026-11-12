// Human entity codes (D17; docs/designs/fleet-data-foundation.md §2, Human codes): lookup by code and the project code.
// Codes are issued by the task and question triggers of migration 39; this file only reads them and renames a project.
package main

import (
	"regexp"
	"strings"

	"fleet/contract"
)

// codePattern: the D17 grammar, matched case-insensitively (lookup is case-insensitive).
var codePattern = regexp.MustCompile(`(?i)\b(?:([TQE])-([A-Z][A-Z0-9]{1,4})-([1-9][0-9]*)|S-([A-Z][A-Z0-9]{1,4})-([A-DF-PRU-Z])([1-9][0-9]*))\b`)

var projectCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,4}$`)

// resolveCode: the entity a code names in the caller's scope (row-level security on entity_code and the entity's
// table); nil when unknown or forbidden, which callers report alike.
func resolveCode(q *Request, code string) (*contract.EntityCode, error) {
	code = strings.ToUpper(code)
	r, err := first(q, `SELECT e.code,e.entity_id,e.type,coalesce(t.code,x.code) AS current FROM entity_code e
	 LEFT JOIN task t ON e.type='T' AND t.id=e.entity_id LEFT JOIN question x ON e.type='Q' AND x.id=e.entity_id
	 WHERE e.code=$1 AND coalesce(t.code,x.code) IS NOT NULL`, code)
	if err != nil || r == nil {
		return nil, err
	}
	cur := astr(r["current"])
	return &contract.EntityCode{Code: code, Id: astr(r["entity_id"]), Type: astr(r["type"]), CurrentCode: cur, Superseded: cur != code}, nil
}

func init() {
	gatedObjOp("entity_resolve", true, typedOp(func(q *Request, r *contract.EntityResolveRequest) (contract.EntityCode, error) {
		if !codePattern.MatchString(r.Code) || len(codePattern.FindString(r.Code)) != len(strings.TrimSpace(r.Code)) {
			return contract.EntityCode{}, fail(400, "Expected a code such as T-FLT-42")
		}
		c, err := resolveCode(q, strings.TrimSpace(r.Code))
		if err == nil && c == nil {
			err = errNotFound
		}
		if err != nil {
			return contract.EntityCode{}, err
		}
		return *c, nil
	}))
	gatedObjOp("entity_resolve_many", true, typedOp(func(q *Request, r *contract.EntityResolveManyRequest) (contract.EntityResolveManyReply, error) {
		out, seen := []contract.EntityCode{}, map[string]bool{}
		for _, m := range codePattern.FindAllString(r.Text, 200) { // ponytail: first 200 codes of a text; raise if a real text holds more
			m = strings.ToUpper(m)
			if seen[m] {
				continue
			}
			seen[m] = true
			c, err := resolveCode(q, m)
			if err != nil {
				return contract.EntityResolveManyReply{}, err
			}
			if c != nil {
				out = append(out, *c)
			}
		}
		return contract.EntityResolveManyReply{Codes: out}, nil
	}))
	gatedObjOp("project_code_set", false, typedOp(func(q *Request, r *contract.ProjectCodeSetRequest) (contract.ProjectCodeSetReply, error) {
		if q.person == nil || (!fullAccess(q.person) && !hasRole(q.person, "operator")) { // projectWrite's gate
			return contract.ProjectCodeSetReply{}, fail(403, "Authenticated operator required")
		}
		code := strings.ToUpper(strings.TrimSpace(r.Code))
		if !projectCodePattern.MatchString(code) || strings.ContainsAny(code, "IOSZB") || strings.Trim(code, "0123456789ABCDEF") == "" || code == "ALL" {
			return contract.ProjectCodeSetReply{}, fail(400, "Expected a project code: 2 to 5 letters or digits, a letter first, not hex-only, no I, O, S, Z or B")
		}
		if ok, err := has(q, "SELECT 1 FROM definition WHERE kind='project' AND id=$1 AND retired_at IS NULL", r.ProjectId); err != nil || !ok {
			if err == nil {
				err = fail(404, "Unknown project")
			}
			return contract.ProjectCodeSetReply{}, err
		}
		if ok, err := has(q, "SELECT 1 FROM project_code WHERE code=$1 AND NOT (project_id=$2 AND current)", code, r.ProjectId); err != nil || ok {
			if err == nil {
				err = fail(409, "Project code "+code+" is or was used; codes are never reused")
			}
			return contract.ProjectCodeSetReply{}, err
		}
		if err := exec(q, "SELECT fleet_project_code_set($1,$2)", r.ProjectId, code); err != nil {
			return contract.ProjectCodeSetReply{}, err
		}
		return contract.ProjectCodeSetReply{ProjectId: r.ProjectId, Code: code}, nil
	}))
}
