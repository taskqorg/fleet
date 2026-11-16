package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"fleet/contract"
	"fleet/obs"

	"github.com/jackc/pgx/v5"
)

// resourceDelta accumulates the support changes of every contribution applied to one project in one replacement
// transaction and writes them in one batch. Counts, references, multiplicities and decimal sums are additive, so the
// net change of a cohort leaves exactly the rows that applying its leaves one by one leaves. A cost whose amount is
// unknown is the exception: NULL absorbs every later sum until its row is deleted and recreated, so such a cost key
// keeps the original per-contribution statements in order. A subtract that would make a count, multiplicity or reason
// count negative still fails: CHECK constraints and resource_reasons_merge reject it.
type resourceDelta struct {
	project                                    string
	executions, history, otherCalls, otherErrs int64
	metrics                                    map[[2]string]*resourceMetricDelta
	extremes                                   map[resourceExtremeKey]int64
	tools                                      map[[2]string]*resourceCountDelta
	costs                                      map[[2]string]*resourceCostDelta
	prices                                     map[resourcePriceKey]int64
}
type resourceMetricDelta struct {
	present, expected int64
	sum               string
	reasons           map[string]int64
}
type resourceExtremeKey struct {
	metric string
	value  int64
}
type resourceCountDelta struct{ calls, errors, refs int64 }
type resourceCostOp struct {
	amount *string
	usages int64
	sign   int64
}
type resourceCostDelta struct {
	value        string
	usages, refs int64
	ops          []resourceCostOp // every change in order; written one by one when any amount is unknown
	unknown      bool
}
type resourcePriceKey struct {
	currency, basis, price string
	observed               int64
}

func newResourceDelta(project string) *resourceDelta {
	return &resourceDelta{project: project, metrics: map[[2]string]*resourceMetricDelta{}, extremes: map[resourceExtremeKey]int64{},
		tools: map[[2]string]*resourceCountDelta{}, costs: map[[2]string]*resourceCostDelta{}, prices: map[resourcePriceKey]int64{}}
}

func resourceSigned(v string, sign int64) string {
	if sign > 0 || v == "0" {
		return v
	}
	if strings.HasPrefix(v, "-") {
		return v[1:]
	}
	return "-" + v
}

// add records one included contribution with sign +1 (apply) or -1 (subtract).
func (d *resourceDelta) add(c contract.ResourceContributionV1, sign int64) error {
	if !c.Included || c.ProjectId == nil || *c.ProjectId != d.project {
		return nil
	}
	d.executions += sign
	if c.HistoryIncomplete {
		d.history += sign
	}
	d.otherCalls += sign * c.OtherCalls
	d.otherErrs += sign * c.OtherErrors
	for k, m := range c.Metrics {
		if !contract.ResourceMetricNameV1(k).Valid() {
			return errors.New("unknown contribution metric")
		}
		reason := deref(m.Reason)
		// Only the entire zero support tuple is a no-op. Measured zero, expected
		// missing values and reasons retain their presence/coverage/reference deltas.
		if m.Value == nil && !m.Expected && reason == "" && m.Basis.Valid() {
			continue
		}
		key := [2]string{k, string(m.Basis)}
		x := d.metrics[key]
		if x == nil {
			x = &resourceMetricDelta{sum: "0", reasons: map[string]int64{}}
			d.metrics[key] = x
		}
		if m.Value != nil {
			x.present += sign
			s, err := decimalArithmetic(x.sum, resourceSigned(*m.Value, sign), false)
			if err != nil {
				return err
			}
			x.sum = s
		}
		if m.Expected {
			x.expected += sign
		}
		if reason != "" {
			x.reasons[reason] += sign
		}
	}
	for _, v := range []struct {
		metric string
		value  *int64
	}{{"start", c.StartedMs}, {"end", c.EndedMs}, {"peakMemory", resourceInt(json.Number(deref(c.Metrics["peakMemoryBytes"].Value)))}} {
		if v.value != nil {
			d.extremes[resourceExtremeKey{v.metric, *v.value}] += sign
		}
	}
	for _, t := range c.Tools {
		key := [2]string{t.Connection, t.Name}
		x := d.tools[key]
		if x == nil {
			x = &resourceCountDelta{}
			d.tools[key] = x
		}
		x.calls += sign * t.Calls
		x.errors += sign * t.Errors
		x.refs += sign
	}
	for _, cost := range c.Costs {
		currency := deref(cost.Currency)
		key := [2]string{currency, string(cost.Basis)}
		x := d.costs[key]
		if x == nil {
			x = &resourceCostDelta{value: "0"}
			d.costs[key] = x
		}
		x.ops = append(x.ops, resourceCostOp{cost.Amount, cost.UnpricedCount, sign})
		if cost.Amount == nil {
			x.unknown = true
		} else {
			s, err := decimalArithmetic(x.value, resourceSigned(*cost.Amount, sign), false)
			if err != nil {
				return err
			}
			x.value = s
		}
		x.usages += sign * cost.UnpricedCount
		x.refs += sign
		for _, r := range cost.PriceRefs {
			d.prices[resourcePriceKey{currency, string(cost.Basis), r.PriceId, r.ObservedAt}] += sign
		}
	}
	return nil
}

func lessPair(a, b [2]string) bool { return a[0] < b[0] || a[0] == b[0] && a[1] < b[1] }

func pairKeys[V any](m map[[2]string]V) [][2]string {
	keys := make([][2]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessPair(keys[i], keys[j]) })
	return keys
}

// resourceWrites queues statements into one batch; each result is read in queue order.
type resourceWrites struct {
	batch   pgx.Batch
	results []func(pgx.BatchResults) error
}

func (w *resourceWrites) exec(sql string, args ...any) {
	w.batch.Queue(sql, args...)
	w.results = append(w.results, func(r pgx.BatchResults) error { _, err := r.Exec(); return err })
}

// count queues a statement whose affected rows (sign +1 or -1) change an item count.
func (w *resourceWrites) count(total *int64, sign int64, sql string, args ...any) {
	w.batch.Queue(sql, args...)
	w.results = append(w.results, func(r pgx.BatchResults) error {
		ct, err := r.Exec()
		*total += sign * ct.RowsAffected()
		return err
	})
}

// selected queues a statement returning one count that changes an item count by sign per row.
func (w *resourceWrites) selected(total *int64, sign int64, sql string, args ...any) {
	w.batch.Queue(sql, args...)
	w.results = append(w.results, func(r pgx.BatchResults) error {
		var n int64
		err := r.QueryRow().Scan(&n)
		*total += sign * n
		return err
	})
}

// inserted queues an upsert RETURNING (xmax=0) and adds the rows it created to total.
func (w *resourceWrites) inserted(total *int64, sql string, args ...any) {
	w.batch.Queue(sql, args...)
	w.results = append(w.results, func(r pgx.BatchResults) error {
		rs, err := r.Query()
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var created bool
			if err = rs.Scan(&created); err != nil {
				return err
			}
			if created {
				*total++
			}
		}
		return rs.Err()
	})
}

// Per table, keys whose every component only grows take one upsert; keys whose references reach zero are deleted;
// the rest are updated in place. CHECK constraints apply to an INSERT's proposed row before ON CONFLICT, so a key
// that loses anything never takes the insert path. The project advisory lock is held, so row order needs no care.
func (d *resourceDelta) flush(q *Request) (resultErr error) {
	_, span := obs.Start(q.ctx(), "resource.apply", "internal")
	defer func() { span.End(resultErr) }()
	p := d.project
	w := &resourceWrites{}
	var toolCount, costCount, priceCount int64
	if d.executions != 0 || d.history != 0 || d.otherCalls != 0 || d.otherErrs != 0 {
		w.exec(`UPDATE resource_project_summary SET executions=executions+$2,history_incomplete=history_incomplete+$3,
  other_calls=other_calls+$4,other_errors=other_errors+$5 WHERE project_id=$1`, p, d.executions, d.history, d.otherCalls, d.otherErrs)
	}

	type metricRows struct {
		m, b, s, r []string
		p, e       []int64
	}
	var up, down metricRows
	for _, k := range pairKeys(d.metrics) {
		x := d.metrics[k]
		reasons, gains := map[string]int64{}, x.present >= 0 && x.expected >= 0
		for r, n := range x.reasons {
			if n != 0 {
				reasons[r] = n
				gains = gains && n > 0
			}
		}
		if x.present == 0 && x.expected == 0 && x.sum == "0" && len(reasons) == 0 {
			continue
		}
		t := &down
		if gains {
			t = &up
		}
		t.m, t.b, t.p, t.e, t.s, t.r = append(t.m, k[0]), append(t.b, k[1]), append(t.p, x.present), append(t.e, x.expected), append(t.s, x.sum), append(t.r, string(jsonBytes(reasons)))
	}
	if len(up.m) > 0 {
		w.exec(`INSERT INTO resource_metric AS r(project_id,metric,basis,present_count,expected_count,sum_value,reasons)
 SELECT $1,d.m::resource_metric_name,d.b,d.p,d.e,d.s::numeric,d.r::jsonb FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::text[],$7::text[]) d(m,b,p,e,s,r)
 ON CONFLICT(project_id,metric,basis) DO UPDATE SET present_count=r.present_count+excluded.present_count,
  expected_count=r.expected_count+excluded.expected_count,sum_value=r.sum_value+excluded.sum_value,reasons=resource_reasons_merge(r.reasons,excluded.reasons)`,
			p, up.m, up.b, up.p, up.e, up.s, up.r)
	}
	if len(down.m) > 0 {
		w.exec(`UPDATE resource_metric r SET present_count=r.present_count+d.p,expected_count=r.expected_count+d.e,sum_value=r.sum_value+d.s::numeric,
  reasons=resource_reasons_merge(r.reasons,d.r::jsonb)
 FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::text[],$7::text[]) d(m,b,p,e,s,r)
 WHERE r.project_id=$1 AND r.metric=d.m::resource_metric_name AND r.basis=d.b`, p, down.m, down.b, down.p, down.e, down.s, down.r)
		// Only a key that lost something can become empty.
		w.exec(`DELETE FROM resource_metric r USING unnest($2::text[],$3::text[]) d(m,b)
 WHERE r.project_id=$1 AND r.metric=d.m::resource_metric_name AND r.basis=d.b AND r.present_count=0 AND r.expected_count=0 AND r.reasons='{}'`, p, down.m, down.b)
	}

	ekeys := make([]resourceExtremeKey, 0, len(d.extremes))
	for k := range d.extremes {
		ekeys = append(ekeys, k)
	}
	sort.Slice(ekeys, func(i, j int) bool {
		return ekeys[i].metric < ekeys[j].metric || ekeys[i].metric == ekeys[j].metric && ekeys[i].value < ekeys[j].value
	})
	var eUpM, eDownM []string
	var eUpV, eUpN, eDownV, eDownN []int64
	for _, k := range ekeys {
		if n := d.extremes[k]; n > 0 {
			eUpM, eUpV, eUpN = append(eUpM, k.metric), append(eUpV, k.value), append(eUpN, n)
		} else if n < 0 {
			eDownM, eDownV, eDownN = append(eDownM, k.metric), append(eDownV, k.value), append(eDownN, n)
		}
	}
	if len(eUpM) > 0 {
		w.exec(`INSERT INTO resource_extreme AS r(project_id,metric,value,multiplicity) SELECT $1,d.m,d.v,d.n FROM unnest($2::text[],$3::bigint[],$4::bigint[]) d(m,v,n)
 ON CONFLICT(project_id,metric,value) DO UPDATE SET multiplicity=r.multiplicity+excluded.multiplicity`, p, eUpM, eUpV, eUpN)
	}
	if len(eDownM) > 0 {
		w.exec(`DELETE FROM resource_extreme r USING unnest($2::text[],$3::bigint[],$4::bigint[]) d(m,v,n)
 WHERE r.project_id=$1 AND r.metric=d.m AND r.value=d.v AND r.multiplicity+d.n=0`, p, eDownM, eDownV, eDownN)
		w.exec(`UPDATE resource_extreme r SET multiplicity=r.multiplicity+d.n FROM unnest($2::text[],$3::bigint[],$4::bigint[]) d(m,v,n)
 WHERE r.project_id=$1 AND r.metric=d.m AND r.value=d.v`, p, eDownM, eDownV, eDownN)
	}

	type countRows struct {
		a, b              []string
		calls, errs, refs []int64
	}
	var tUp, tRest countRows
	for _, k := range pairKeys(d.tools) {
		x := d.tools[k]
		t := &tRest
		if x.refs > 0 && x.calls >= 0 && x.errors >= 0 {
			t = &tUp
		} else if x.refs == 0 && x.calls == 0 && x.errors == 0 {
			continue
		}
		t.a, t.b, t.calls, t.errs, t.refs = append(t.a, k[0]), append(t.b, k[1]), append(t.calls, x.calls), append(t.errs, x.errors), append(t.refs, x.refs)
	}
	if len(tUp.a) > 0 {
		w.inserted(&toolCount, `INSERT INTO resource_tool AS r(project_id,connection,name,calls,errors,references_count)
 SELECT $1,d.c,d.n,d.calls,d.errs,d.refs FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::bigint[]) d(c,n,calls,errs,refs)
 ON CONFLICT(project_id,connection,name) DO UPDATE SET calls=r.calls+excluded.calls,errors=r.errors+excluded.errors,references_count=r.references_count+excluded.references_count
 RETURNING (xmax=0)`, p, tUp.a, tUp.b, tUp.calls, tUp.errs, tUp.refs)
	}
	if len(tRest.a) > 0 {
		w.count(&toolCount, -1, `DELETE FROM resource_tool r USING unnest($2::text[],$3::text[],$4::bigint[]) d(c,n,refs)
 WHERE r.project_id=$1 AND r.connection=d.c AND r.name=d.n AND r.references_count+d.refs=0`, p, tRest.a, tRest.b, tRest.refs)
		w.exec(`UPDATE resource_tool r SET calls=r.calls+d.calls,errors=r.errors+d.errs,references_count=r.references_count+d.refs
 FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::bigint[]) d(c,n,calls,errs,refs)
 WHERE r.project_id=$1 AND r.connection=d.c AND r.name=d.n`, p, tRest.a, tRest.b, tRest.calls, tRest.errs, tRest.refs)
	}

	type costRows struct {
		c, b, v      []string
		usages, refs []int64
	}
	var cUp, cRest costRows
	for _, k := range pairKeys(d.costs) {
		x := d.costs[k]
		if x.unknown {
			for _, op := range x.ops {
				if op.sign > 0 {
					w.inserted(&costCount, `INSERT INTO resource_cost(project_id,currency,basis,value,usages,references_count) VALUES($1,$2,$3,$4::numeric,$5,1)
    ON CONFLICT(project_id,currency,basis) DO UPDATE SET value=resource_cost.value+excluded.value,usages=resource_cost.usages+excluded.usages,references_count=resource_cost.references_count+1 RETURNING (xmax=0)`,
						p, k[0], k[1], op.amount, op.usages)
				} else {
					// One statement: the last reference deletes the row (a later add recreates its value), others subtract.
					w.selected(&costCount, -1, `WITH gone AS (DELETE FROM resource_cost WHERE project_id=$1 AND currency=$2 AND basis=$3 AND references_count=1 RETURNING 1),
 kept AS (UPDATE resource_cost SET value=value-$4::numeric,usages=usages-$5,references_count=references_count-1 WHERE project_id=$1 AND currency=$2 AND basis=$3 AND references_count>1 RETURNING 1)
 SELECT count(*) FROM gone`, p, k[0], k[1], op.amount, op.usages)
				}
			}
			continue
		}
		t := &cRest
		if x.refs > 0 && x.usages >= 0 {
			t = &cUp
		} else if x.refs == 0 && x.usages == 0 && x.value == "0" {
			continue
		}
		t.c, t.b, t.v, t.usages, t.refs = append(t.c, k[0]), append(t.b, k[1]), append(t.v, x.value), append(t.usages, x.usages), append(t.refs, x.refs)
	}
	if len(cUp.c) > 0 {
		w.inserted(&costCount, `INSERT INTO resource_cost AS r(project_id,currency,basis,value,usages,references_count)
 SELECT $1,d.c,d.b,d.v::numeric,d.u,d.refs FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,v,u,refs)
 ON CONFLICT(project_id,currency,basis) DO UPDATE SET value=r.value+excluded.value,usages=r.usages+excluded.usages,references_count=r.references_count+excluded.references_count
 RETURNING (xmax=0)`, p, cUp.c, cUp.b, cUp.v, cUp.usages, cUp.refs)
	}
	if len(cRest.c) > 0 {
		w.count(&costCount, -1, `DELETE FROM resource_cost r USING unnest($2::text[],$3::text[],$4::bigint[]) d(c,b,refs)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b AND r.references_count+d.refs=0`, p, cRest.c, cRest.b, cRest.refs)
		w.exec(`UPDATE resource_cost r SET value=r.value+d.v::numeric,usages=r.usages+d.u,references_count=r.references_count+d.refs
 FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,v,u,refs)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b`, p, cRest.c, cRest.b, cRest.v, cRest.usages, cRest.refs)
	}

	pkeys := make([]resourcePriceKey, 0, len(d.prices))
	for k := range d.prices {
		pkeys = append(pkeys, k)
	}
	sort.Slice(pkeys, func(i, j int) bool {
		a, b := pkeys[i], pkeys[j]
		if a.currency != b.currency {
			return a.currency < b.currency
		}
		if a.basis != b.basis {
			return a.basis < b.basis
		}
		if a.price != b.price {
			return a.price < b.price
		}
		return a.observed < b.observed
	})
	var pUp, pDown struct {
		c, b, id  []string
		at, delta []int64
	}
	for _, k := range pkeys {
		n := d.prices[k]
		t := &pDown
		if n > 0 {
			t = &pUp
		} else if n == 0 {
			continue
		}
		t.c, t.b, t.id, t.at, t.delta = append(t.c, k.currency), append(t.b, k.basis), append(t.id, k.price), append(t.at, k.observed), append(t.delta, n)
	}
	if len(pUp.c) > 0 {
		w.inserted(&priceCount, `INSERT INTO resource_price_ref AS r(project_id,currency,basis,price_id,observed_at,references_count)
 SELECT $1,d.c,d.b,d.id,d.at,d.n FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,id,at,n)
 ON CONFLICT(project_id,currency,basis,price_id,observed_at) DO UPDATE SET references_count=r.references_count+excluded.references_count
 RETURNING (xmax=0)`, p, pUp.c, pUp.b, pUp.id, pUp.at, pUp.delta)
	}
	if len(pDown.c) > 0 {
		w.count(&priceCount, -1, `DELETE FROM resource_price_ref r USING unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,id,at,n)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b AND r.price_id=d.id AND r.observed_at=d.at AND r.references_count+d.n=0`, p, pDown.c, pDown.b, pDown.id, pDown.at, pDown.delta)
		w.exec(`UPDATE resource_price_ref r SET references_count=r.references_count+d.n
 FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,id,at,n)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b AND r.price_id=d.id AND r.observed_at=d.at`, p, pDown.c, pDown.b, pDown.id, pDown.at, pDown.delta)
	}

	if len(w.results) > 0 {
		results := q.tx.SendBatch(q.ctx(), &w.batch)
		for _, read := range w.results {
			if err := read(results); err != nil {
				results.Close()
				return err
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
	}
	span.Set("fleet.resource.apply_statements", strconv.Itoa(len(w.results)))
	// Only changed item counts are written: their columns carry update triggers.
	var set []string
	args := []any{p}
	for _, c := range []struct {
		column string
		delta  int64
	}{{"tool_count", toolCount}, {"cost_count", costCount}, {"price_count", priceCount}} {
		if c.delta != 0 {
			args = append(args, c.delta)
			set = append(set, c.column+"="+c.column+"+$"+strconv.Itoa(len(args)))
		}
	}
	if len(set) > 0 {
		return exec(q, `UPDATE resource_project_summary SET `+strings.Join(set, ",")+` WHERE project_id=$1`, args...)
	}
	return nil
}
