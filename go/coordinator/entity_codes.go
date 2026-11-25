// Human entity codes (D17; docs/designs/fleet-data-foundation.md §2, Human codes): lookup by code and the project code.
// Codes are issued by the zcode triggers (schema/triggers.sql: tasks, questions, conversations, results, bots); this file
// only reads them and renames a project.
package main

import (
	"regexp"
	"slices"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// codePattern: the D17 grammar, matched case-insensitively (lookup is case-insensitive).
var codePattern = regexp.MustCompile(`(?i)\b(?:([TQECRB])-([A-Z][A-Z0-9]{1,4})-([1-9][0-9]*)|S-([A-Z][A-Z0-9]{1,4})-([A-DF-PRU-Z])([1-9][0-9]*))\b`)

var projectCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,4}$`)

// resolveCodes: the entities the codes name in the caller's scope (row-level security on entity_code and the entity's
// table), by code; an unknown or forbidden code is absent, and callers report both alike.
func resolveCodes(q *Request, codes []string) (map[string]contract.EntityCode, error) {
	rs, err := q.db().EntResolveCodes(q.ctx(), codes)
	out := map[string]contract.EntityCode{}
	for _, r := range rs {
		out[r.Code] = contract.EntityCode{Code: r.Code, Id: r.EntityID, Type: r.Type, CurrentCode: r.Current, Superseded: r.Current != r.Code}
	}
	return out, err
}

func init() {
	op("entity_resolve", func(q *Request, r *contract.EntityResolveRequest) (contract.EntityCode, error) {
		if !codePattern.MatchString(r.Code) || len(codePattern.FindString(r.Code)) != len(strings.TrimSpace(r.Code)) {
			return contract.EntityCode{}, fail(400, "Expected a code such as T-FLT-42")
		}
		code := strings.ToUpper(strings.TrimSpace(r.Code))
		found, err := resolveCodes(q, []string{code})
		if c, ok := found[code]; ok || err != nil {
			return c, err
		}
		return contract.EntityCode{}, errNotFound
	})
	op("entity_resolve_many", func(q *Request, r *contract.EntityResolveManyRequest) (contract.EntityResolveManyReply, error) {
		codes := []string{}
		for _, m := range codePattern.FindAllString(r.Text, 1000) { // ponytail: first 1000 codes of a text; raise if a real text holds more
			if m = strings.ToUpper(m); !slices.Contains(codes, m) {
				codes = append(codes, m)
			}
		}
		found, err := resolveCodes(q, codes)
		out := []contract.EntityCode{}
		for _, c := range codes {
			if x, ok := found[c]; ok {
				out = append(out, x)
			}
		}
		return contract.EntityResolveManyReply{Codes: out}, err
	})
	op("project_code_set", func(q *Request, r *contract.ProjectCodeSetRequest) (contract.ProjectCodeSetReply, error) {
		if q.person == nil || (!fullAccess(q.person) && !hasRole(q.person, "operator")) { // projectWrite's gate
			return contract.ProjectCodeSetReply{}, fail(403, "Authenticated operator required")
		}
		if !mayAccessProjectID(q.person, r.ProjectId) { // as project_update: another project's code is not this operator's
			return contract.ProjectCodeSetReply{}, fail(404, "Unknown project")
		}
		code := strings.ToUpper(strings.TrimSpace(r.Code))
		if !projectCodePattern.MatchString(code) || strings.Trim(code, "0123456789ABCDEF") == "" || code == "ALL" {
			return contract.ProjectCodeSetReply{}, fail(400, "Expected a project code: 2 to 5 letters or digits, a letter first, not hex-only")
		}
		if ok, err := q.db().EntProjectLive(q.ctx(), r.ProjectId); err != nil || !ok {
			if err == nil {
				err = fail(404, "Unknown project")
			}
			return contract.ProjectCodeSetReply{}, err
		}
		if ok, err := q.db().EntProjectCodeTaken(q.ctx(), db.EntProjectCodeTakenParams{Code: code, ProjectID: r.ProjectId}); err != nil || ok {
			if err == nil {
				err = fail(409, "Project code "+code+" is or was used; codes are never reused")
			}
			return contract.ProjectCodeSetReply{}, err
		}
		if err := q.db().EntProjectCodeSet(q.ctx(), db.EntProjectCodeSetParams{ProjectID: r.ProjectId, Code: code}); err != nil {
			if pgCode(err) == "23505" { // taken by a concurrent change after the check above
				err = fail(409, "Project code "+code+" is or was used; codes are never reused")
			}
			return contract.ProjectCodeSetReply{}, err
		}
		return contract.ProjectCodeSetReply{ProjectId: r.ProjectId, Code: code}, nil
	})
}
