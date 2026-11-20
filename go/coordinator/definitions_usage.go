// Definition usage (fleet-definition-editors.md, definition_usage): runs of the executions that pinned a definition, in
// a window of days, overall and per revision, and the definitions that name it.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"fleet/contract"
	"fleet/coordinator/db"
)

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
	rows, err := q.db().DefUsage(q.ctx(), db.DefUsageParams{Pin: jsonBytes([]map[string]string{pin}), Kind: kind, ID: id, Days: int32(days)})
	if err != nil {
		return nil, nil, err
	}
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
	for _, r := range rows {
		did := r.ID
		w := contract.Window{CostBasis: ptr("tokens"), Runs: r.Runs, Succeeded: r.Succeeded, Tokens: r.Tokens}
		if r.Median >= 0 {
			median := r.Median
			w.WallMsMedian = &median
		}
		if r.Total == 1 {
			res, ok := totRes[did]
			attach(&w, res, ok)
			totals[did] = w
		} else if r.Rev >= 0 {
			res, ok := revRes[did][r.Rev]
			attach(&w, res, ok)
			byRev[did] = append(byRev[did], contract.RevisionWindow{Revision: r.Rev, Window: w})
		}
	}
	for k := range byRev {
		sort.Slice(byRev[k], func(i, j int) bool { return byRev[k][i].Revision > byRev[k][j].Revision })
	}
	return totals, byRev, nil
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
			var d struct {
				Steps []struct {
					Profile any `json:"profile"`
				} `json:"steps"`
			}
			json.Unmarshal(h.text, &d)
			for _, s := range d.Steps {
				if s.Profile == id {
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
	var methods []struct {
		Pipeline     any `json:"pipeline"`
		AgentProfile any `json:"agentProfile"`
	}
	json.Unmarshal(jsonBytes(cat["methods"]), &methods)
	for _, m := range methods {
		if kind == "pipeline" && m.Pipeline == id+".json" || kind == "agent" && m.AgentProfile == id {
			out = append(out, contract.DefinitionKey{Kind: contract.DefinitionKindCatalogue, Id: "catalogue"})
			break
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
	op("definition_usage", definitionUsage)
}
