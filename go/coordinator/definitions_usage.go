// Definition usage (fleet-definition-editors.md, definition_usage): runs of the executions that pinned a definition, in
// a window of days, overall and per revision, and the definitions that name it.
package main

import (
	"encoding/json"
	"fmt"

	"fleet/contract"
)

// usageWindows: runs, successes, median wall time and tokens of the executions that pinned kind/id in the last days,
// overall and per revision; rows outside the caller's scope are not counted (row security).
func usageWindows(q *Request, kind, id string, days int64) (contract.Window, []contract.RevisionWindow, error) {
	pin := jsonBytes([]map[string]string{{"kind": kind, "id": id}})
	total, byRev := contract.Window{CostBasis: ptr("tokens")}, []contract.RevisionWindow{}
	rows, err := q.tx.Query(q.ctx(), `SELECT (d->>'revision')::int AS rev, count(*), count(*) FILTER (WHERE e.state='succeeded'),
	   (percentile_disc(0.5) WITHIN GROUP (ORDER BY t.wall_ms))::bigint, coalesce(sum(coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)),0)::bigint
	 FROM execution e CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d LEFT JOIN execution_totals t ON t.execution_id=e.id
	 WHERE e.definitions @> $1 AND d->>'kind'=$2 AND d->>'id'=$3 AND coalesce(e.started_at, e.updated_at) >= now() - make_interval(days => $4)
	 GROUP BY ROLLUP ((d->>'revision')::int) ORDER BY 1 DESC NULLS FIRST`, pin, kind, id, days)
	if err != nil {
		return total, byRev, err
	}
	defer rows.Close()
	for rows.Next() {
		var rev, median *int64
		var w contract.Window
		if err := rows.Scan(&rev, &w.Runs, &w.Succeeded, &median, &w.Tokens); err != nil {
			return total, byRev, err
		}
		w.WallMsMedian, w.CostBasis = median, ptr("tokens")
		if rev == nil {
			total = w
		} else {
			byRev = append(byRev, contract.RevisionWindow{Revision: *rev, Window: w})
		}
	}
	return total, byRev, rows.Err()
}

// usedByDefinitions: the definitions that name kind/id: pipelines whose agent steps run the profile, the catalogue when one
// of its methods names the pipeline or the profile.
func usedByDefinitions(q *Request, kind, id string) ([]contract.DefinitionKey, error) {
	out := []contract.DefinitionKey{}
	if kind == "catalogue" {
		return out, nil
	}
	pipes, err := headTexts(q, "pipeline")
	if err != nil {
		return nil, err
	}
	if kind == "agent" {
		for _, h := range pipes {
			var d doc
			json.Unmarshal(h.text, &d)
			for _, s := range anys(d["steps"]) {
				if astr(mapOf(s)["profile"]) == id {
					out = append(out, contract.DefinitionKey{Kind: contract.DefinitionKindPipeline, Id: h.id})
					break
				}
			}
		}
	}
	if cat, err := catalogueHead(q); err == nil {
		for _, m := range anys(cat["methods"]) {
			mm := mapOf(m)
			if kind == "pipeline" && astr(mm["pipeline"]) == id+".json" || kind == "agent" && astr(mm["agentProfile"]) == id {
				out = append(out, contract.DefinitionKey{Kind: contract.DefinitionKindCatalogue, Id: "catalogue"})
				break
			}
		}
	}
	return out, nil
}

func definitionUsage(q *Request, r *contract.DefinitionUsageRequest) (contract.DefinitionUsageReply, error) {
	var out contract.DefinitionUsageReply
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	if h, err := headOf(q, string(r.Kind), r.Id, false); err != nil || h == nil {
		if err == nil {
			err = fail(404, "Unknown definition")
		}
		return out, err
	}
	days := int64(30)
	if r.Days != nil {
		if *r.Days < 1 {
			return out, fail(400, "days: 1 or more")
		}
		days = *r.Days
	}
	var err error
	if out.Total, out.ByRevision, err = usageWindows(q, string(r.Kind), r.Id, days); err != nil {
		return out, fmt.Errorf("usage: %w", err)
	}
	out.UsedBy, err = usedByDefinitions(q, string(r.Kind), r.Id)
	return out, err
}

func init() {
	gatedObjOp("definition_usage", true, typedOp(definitionUsage))
}
