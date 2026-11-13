// Definition usage (fleet-definition-editors.md, definition_usage): runs of the executions that pinned a definition, in
// a window of days, overall and per revision, and the definitions that name it.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"fleet/contract"
)

const usageSQL = `SELECT d->>'id' AS id, (d->>'revision')::int AS rev, GROUPING((d->>'revision')::int) AS total, count(*), count(*) FILTER (WHERE e.state='succeeded'),
   (percentile_disc(0.5) WITHIN GROUP (ORDER BY coalesce(t.wall_ms, CASE WHEN e.started_at IS NOT NULL AND e.ended_at IS NOT NULL THEN greatest(0, (extract(epoch FROM e.ended_at - e.started_at)*1000)::bigint) END)))::bigint,
   coalesce(sum(coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)),0)::bigint
 FROM execution e CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d LEFT JOIN execution_totals t ON t.execution_id=e.id
 WHERE e.definitions @> $1 AND d->>'kind'=$2 AND ($3='' OR d->>'id'=$3) AND coalesce((d->>'draft')::boolean,false)=false
   AND coalesce(e.started_at, e.updated_at) >= now() - make_interval(days => $4::int)
 GROUP BY d->>'id', ROLLUP ((d->>'revision')::int)`

// usageWindows: runs, successes, median wall time and tokens of the executions that pinned a saved revision of kind/id
// (id "" for every definition of the kind) in the last days, per definition and per revision; draft runs are not a
// revision's runs; rows outside the caller's scope are not counted (row security).
func usageWindows(q *Request, kind, id string, days int64) (map[string]contract.Window, map[string][]contract.RevisionWindow, error) {
	pin := map[string]string{"kind": kind}
	if id != "" {
		pin["id"] = id
	}
	totRes, revRes, err := definitionUsageResources(q, kind, id, days)
	if err != nil {
		return nil, nil, err
	}
	totals, byRev := map[string]contract.Window{}, map[string][]contract.RevisionWindow{}
	rows, err := q.tx.Query(q.ctx(), usageSQL, jsonBytes([]map[string]string{pin}), kind, id, days)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	attach := func(w *contract.Window, res contract.ResourceSummary, ok bool) {
		if !ok {
			res = emptyResourceSummary()
		}
		resCopy := res
		w.Resources = &resCopy
		if res.Tokens.Total != nil {
			w.Tokens = *res.Tokens.Total
		}
		if res.Cost != nil {
			w.Cost = res.Cost
		} else {
			w.Cost = []contract.CostFigure{}
		}
	}
	for rows.Next() {
		var did string
		var rev, median *int64
		var grouping int
		w := contract.Window{CostBasis: ptr("tokens")}
		if err := rows.Scan(&did, &rev, &grouping, &w.Runs, &w.Succeeded, &median, &w.Tokens); err != nil {
			return nil, nil, err
		}
		w.WallMsMedian = median
		if grouping == 1 {
			res, ok := totRes[did]
			attach(&w, res, ok)
			totals[did] = w
		} else if rev != nil {
			res, ok := revRes[did][*rev]
			attach(&w, res, ok)
			byRev[did] = append(byRev[did], contract.RevisionWindow{Revision: *rev, Window: w})
		}
	}
	for k := range byRev {
		sort.Slice(byRev[k], func(i, j int) bool { return byRev[k][i].Revision > byRev[k][j].Revision })
	}
	return totals, byRev, rows.Err()
}

// usedByDefinitions: the definitions that name kind/id: pipelines whose agent steps run the profile, the catalogue when one
// of its methods names the pipeline or the profile.
func usedByDefinitions(q *Request, kind, id string) ([]contract.DefinitionKey, error) {
	out := []contract.DefinitionKey{}
	if kind == "catalogue" {
		return out, nil
	}
	if kind == "agent" {
		pipes, err := headTexts(q, "pipeline")
		if err != nil {
			return nil, err
		}
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
	cat, err := catalogueHead(q)
	var he *HTTPError
	if errors.As(err, &he) && he.Code == 404 {
		return out, nil // no catalogue
	} else if err != nil {
		return nil, err
	}
	{
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
		if *r.Days < 1 || *r.Days > 36500 {
			return out, fail(400, "days: 1 to 36500")
		}
		days = *r.Days
	}
	totals, byRev, err := usageWindows(q, string(r.Kind), r.Id, days)
	if err != nil {
		return out, fmt.Errorf("usage: %w", err)
	}
	out.Total, out.ByRevision = totals[r.Id], byRev[r.Id]
	if out.Total.CostBasis == nil {
		out.Total.CostBasis = ptr("tokens")
	}
	if out.Total.Cost == nil {
		out.Total.Cost = []contract.CostFigure{}
	}
	if out.Total.Resources == nil {
		empty := emptyResourceSummary()
		out.Total.Resources = &empty
	}
	if out.ByRevision == nil {
		out.ByRevision = []contract.RevisionWindow{}
	}
	out.UsedBy, err = usedByDefinitions(q, string(r.Kind), r.Id)
	return out, err
}

func init() {
	gatedObjOp("definition_usage", true, typedOp(definitionUsage))
}
