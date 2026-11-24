package main

import (
	"slices"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("list_spend", listSpend)
}

func listSpend(q *Request, r *contract.ListSpendRequest) (contract.ListSpendReply, error) {
	out := contract.ListSpendReply{Rows: map[string][]contract.CostFigure{}}
	if len(r.RowIds) == 0 {
		return out, nil
	}
	return resourceLineageQuery(q, func() (contract.ListSpendReply, error) {
		var p db.ListSpendCachedParams
		add := func(id string, leaves []string) {
			out.Rows[id] = []contract.CostFigure{}
			for _, leaf := range leaves {
				p.RowIds = append(p.RowIds, id)
				p.LeafIds = append(p.LeafIds, leaf)
			}
		}
		switch r.Kind {
		case "tasks":
			rows, err := q.db().AnalyticsTaskLineage(q.ctx(), db.AnalyticsTaskLineageParams{RowIds: r.RowIds, TotalOnly: true})
			if err != nil {
				return out, err
			}
			for _, row := range rows {
				add(row.ID, row.LeafIds)
			}
		case "sessions":
			rows, err := q.db().AnalyticsSessionLineage(q.ctx(), db.AnalyticsSessionLineageParams{RowIds: r.RowIds, TotalOnly: true})
			if err != nil {
				return out, err
			}
			for _, row := range rows {
				add(row.ID, row.LeafIds)
			}
		case "projects":
			ids, err := projectIDs(q)
			if err != nil {
				return out, err
			}
			for _, id := range ids {
				if slices.Contains(r.RowIds, id) {
					p.ProjectIds = append(p.ProjectIds, id)
					add(id, nil)
				}
			}
		}
		rows, err := q.db().ListSpendCached(q.ctx(), p)
		if err != nil {
			return out, err
		}
		figures := make(map[string][]resourceLeafRow, len(out.Rows))
		var scopes, keys, live []string
		for _, row := range rows {
			if row.LiveLeaf != "" {
				scopes, keys, live = append(scopes, "total"), append(keys, row.RowID), append(live, row.LiveLeaf)
				continue
			}
			f := contract.CostFigure{Basis: contract.ResourceBasisUnavailable}
			if row.Currency != "" {
				f.Value, f.Currency, f.Basis = &row.Value, &row.Currency, contract.ResourceBasis(row.Basis)
			}
			if row.Unpriced != 0 {
				f.Usages = &row.Unpriced
			}
			figures[row.RowID] = append(figures[row.RowID], resourceLeafRow{Costs: []contract.CostFigure{f}})
		}
		if len(live) != 0 {
			summaries, err := summarizeResourceQuery(q, scopes, keys, live)
			if err != nil {
				return out, err
			}
			for _, id := range keys {
				key := "total\x00" + id
				if s, ok := summaries[key]; ok {
					figures[id] = append(figures[id], resourceLeafRow{Costs: s.Cost})
					delete(summaries, key)
				}
			}
		}
		for id := range out.Rows {
			cost := mergeCostFigures(figures[id])
			for i := range cost {
				cost[i].Prices = nil
			}
			out.Rows[id] = cost
		}
		return out, nil
	})
}
