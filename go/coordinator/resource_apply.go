package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/obs"
)

// resourceDelta accumulates the support changes of every contribution applied to one project in one replacement
// transaction, then writes each support table once. Sums and counts are additive, so applying the net change of a
// cohort leaves exactly the rows that applying its leaves one by one would leave; CHECK constraints still reject a
// subtract that would make a count or multiplicity negative. A cohort of n leaves therefore costs a fixed number of
// statements instead of a few dozen per leaf.
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
type resourceCostDelta struct {
	value        *string // nil once any applied amount is unknown, as value+NULL is NULL
	usages, refs int64
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
			zero := "0"
			x = &resourceCostDelta{value: &zero}
			d.costs[key] = x
		}
		if cost.Amount == nil {
			x.value = nil
		} else if x.value != nil {
			s, err := decimalArithmetic(*x.value, resourceSigned(*cost.Amount, sign), false)
			if err != nil {
				return err
			}
			x.value = &s
		}
		x.usages += sign * cost.UnpricedCount
		x.refs += sign
		for _, r := range cost.PriceRefs {
			d.prices[resourcePriceKey{currency, string(cost.Basis), r.PriceId, r.ObservedAt}] += sign
		}
	}
	return nil
}

// Deterministic key order for every support write (the package defines its own comparable, so no generics here).
func pairKeys[V any](m map[[2]string]V) [][2]string {
	keys := make([][2]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessPair(keys[i], keys[j]) })
	return keys
}

func lessPair(a, b [2]string) bool { return a[0] < b[0] || a[0] == b[0] && a[1] < b[1] }

// inserted runs an upsert RETURNING (xmax=0) and counts the rows it created.
func inserted(q *Request, sql string, args ...any) (int64, error) {
	rs, err := q.tx.Query(q.ctx(), sql, args...)
	if err != nil {
		return 0, err
	}
	defer rs.Close()
	n := int64(0)
	for rs.Next() {
		var created bool
		if err = rs.Scan(&created); err != nil {
			return 0, err
		}
		if created {
			n++
		}
	}
	return n, rs.Err()
}

// affected runs a statement and returns its row count.
func affected(q *Request, sql string, args ...any) (int64, error) {
	ct, err := q.tx.Exec(q.ctx(), sql, args...)
	return ct.RowsAffected(), err
}

// flush writes the accumulated support changes: per table one upsert for keys gaining references, one delete for
// keys whose references reach zero and one update for the rest, all in a deterministic key order.
func (d *resourceDelta) flush(q *Request) (resultErr error) {
	_, span := obs.Start(q.ctx(), "resource.apply", "internal")
	defer func() { span.End(resultErr) }()
	stage := time.Now()
	mark := func(name string) {
		span.Set("fleet.resource."+name+"_us", strconv.FormatInt(time.Since(stage).Microseconds(), 10))
		stage = time.Now()
	}
	p := d.project
	if d.executions != 0 || d.history != 0 || d.otherCalls != 0 || d.otherErrs != 0 {
		if err := exec(q, `UPDATE resource_project_summary SET executions=executions+$2,history_incomplete=history_incomplete+$3,
  other_calls=other_calls+$4,other_errors=other_errors+$5 WHERE project_id=$1`, p, d.executions, d.history, d.otherCalls, d.otherErrs); err != nil {
			return err
		}
	}
	mark("apply_summary")

	// CHECK constraints apply to an INSERT's proposed row before ON CONFLICT, so a key losing presence or coverage is
	// updated in place instead (its row exists unless the ledger is corrupt).
	var mNames, mBases, mSums, mReasons, uNames, uBases, uSums, uReasons []string
	var mPresent, mExpected, uPresent, uExpected []int64
	for _, k := range pairKeys(d.metrics) {
		x := d.metrics[k]
		reasons := map[string]int64{}
		for r, n := range x.reasons {
			if n != 0 {
				reasons[r] = n
			}
		}
		if x.present == 0 && x.expected == 0 && x.sum == "0" && len(reasons) == 0 {
			continue
		}
		r := string(jsonBytes(reasons))
		if x.present >= 0 && x.expected >= 0 {
			mNames, mBases, mPresent, mExpected, mSums, mReasons = append(mNames, k[0]), append(mBases, k[1]), append(mPresent, x.present), append(mExpected, x.expected), append(mSums, x.sum), append(mReasons, r)
		} else {
			uNames, uBases, uPresent, uExpected, uSums, uReasons = append(uNames, k[0]), append(uBases, k[1]), append(uPresent, x.present), append(uExpected, x.expected), append(uSums, x.sum), append(uReasons, r)
		}
	}
	const mergeReasons = `(SELECT coalesce(jsonb_object_agg(k,v),'{}'::jsonb) FROM (SELECT k,sum(v) v FROM (
   SELECT key k,value::bigint v FROM jsonb_each_text(r.reasons) UNION ALL SELECT key,value::bigint FROM jsonb_each_text(%s)) x
   GROUP BY k HAVING sum(v)<>0) y)`
	if len(mNames) > 0 {
		// Reasons are counted per key; a key whose count returns to zero disappears, as a single subtract removed it.
		if err := exec(q, `INSERT INTO resource_metric AS r(project_id,metric,basis,present_count,expected_count,sum_value,reasons)
 SELECT $1,d.m::resource_metric_name,d.b,d.p,d.e,d.s::numeric,d.r::jsonb FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::text[],$7::text[]) d(m,b,p,e,s,r)
 ON CONFLICT(project_id,metric,basis) DO UPDATE SET present_count=r.present_count+excluded.present_count,
  expected_count=r.expected_count+excluded.expected_count,sum_value=r.sum_value+excluded.sum_value,
  reasons=`+strings.Replace(mergeReasons, "%s", "excluded.reasons", 1), p, mNames, mBases, mPresent, mExpected, mSums, mReasons); err != nil {
			return err
		}
	}
	if len(uNames) > 0 {
		if err := exec(q, `UPDATE resource_metric r SET present_count=r.present_count+d.p,expected_count=r.expected_count+d.e,sum_value=r.sum_value+d.s::numeric,
  reasons=`+strings.Replace(mergeReasons, "%s", "d.r::jsonb", 1)+`
 FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::text[],$7::text[]) d(m,b,p,e,s,r)
 WHERE r.project_id=$1 AND r.metric=d.m::resource_metric_name AND r.basis=d.b`, p, uNames, uBases, uPresent, uExpected, uSums, uReasons); err != nil {
			return err
		}
		mNames, mBases = append(mNames, uNames...), append(mBases, uBases...)
	}
	if len(mNames) > 0 {
		if err := exec(q, `DELETE FROM resource_metric r USING unnest($2::text[],$3::text[]) d(m,b)
 WHERE r.project_id=$1 AND r.metric=d.m::resource_metric_name AND r.basis=d.b AND r.present_count=0 AND r.expected_count=0 AND r.reasons='{}'`, p, mNames, mBases); err != nil {
			return err
		}
	}
	mark("apply_metrics")

	var eUp, eDown resourceRefs
	ekeys := make([]resourceExtremeKey, 0, len(d.extremes))
	for k := range d.extremes {
		ekeys = append(ekeys, k)
	}
	sort.Slice(ekeys, func(i, j int) bool {
		return ekeys[i].metric < ekeys[j].metric || ekeys[i].metric == ekeys[j].metric && ekeys[i].value < ekeys[j].value
	})
	for _, k := range ekeys {
		eUp.split(&eDown, d.extremes[k], k.metric, strconv.FormatInt(k.value, 10))
	}
	if len(eUp.delta) > 0 {
		if err := exec(q, `INSERT INTO resource_extreme AS r(project_id,metric,value,multiplicity) SELECT $1,d.m,d.v::bigint,d.n FROM unnest($2::text[],$3::text[],$4::bigint[]) d(m,v,n)
 ON CONFLICT(project_id,metric,value) DO UPDATE SET multiplicity=r.multiplicity+excluded.multiplicity`, p, eUp.a, eUp.b, eUp.delta); err != nil {
			return err
		}
	}
	if len(eDown.delta) > 0 {
		if err := exec(q, `DELETE FROM resource_extreme r USING unnest($2::text[],$3::text[],$4::bigint[]) d(m,v,n)
 WHERE r.project_id=$1 AND r.metric=d.m AND r.value=d.v::bigint AND r.multiplicity+d.n=0`, p, eDown.a, eDown.b, eDown.delta); err != nil {
			return err
		}
		if err := exec(q, `UPDATE resource_extreme r SET multiplicity=r.multiplicity+d.n FROM unnest($2::text[],$3::text[],$4::bigint[]) d(m,v,n)
 WHERE r.project_id=$1 AND r.metric=d.m AND r.value=d.v::bigint`, p, eDown.a, eDown.b, eDown.delta); err != nil {
			return err
		}
	}
	mark("apply_extremes")

	toolCount, err := d.flushTools(q)
	if err != nil {
		return err
	}
	mark("apply_tools")
	costCount, priceCount, err := d.flushCosts(q)
	if err != nil {
		return err
	}
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
		if err := exec(q, `UPDATE resource_project_summary SET `+strings.Join(set, ",")+` WHERE project_id=$1`, args...); err != nil {
			return err
		}
	}
	mark("apply_costs_and_prices")
	return nil
}

// resourceRefs holds keys split by the sign of their reference delta.
type resourceRefs struct {
	a, b  []string
	delta []int64
}

func (up *resourceRefs) split(down *resourceRefs, delta int64, a, b string) {
	switch {
	case delta > 0:
		up.a, up.b, up.delta = append(up.a, a), append(up.b, b), append(up.delta, delta)
	case delta < 0:
		down.a, down.b, down.delta = append(down.a, a), append(down.b, b), append(down.delta, delta)
	}
}

// flushTools returns the change of the project's tool row count.
func (d *resourceDelta) flushTools(q *Request) (int64, error) {
	p := d.project
	var up, rest struct {
		c, n              []string
		calls, errs, refs []int64
	}
	for _, k := range pairKeys(d.tools) {
		x := d.tools[k]
		t := &rest
		if x.refs > 0 && x.calls >= 0 && x.errors >= 0 {
			t = &up
		} else if x.refs == 0 && x.calls == 0 && x.errors == 0 {
			continue
		}
		t.c, t.n, t.calls, t.errs, t.refs = append(t.c, k[0]), append(t.n, k[1]), append(t.calls, x.calls), append(t.errs, x.errors), append(t.refs, x.refs)
	}
	count := int64(0)
	if len(up.c) > 0 {
		n, err := inserted(q, `INSERT INTO resource_tool AS r(project_id,connection,name,calls,errors,references_count)
 SELECT $1,d.c,d.n,d.calls,d.errs,d.refs FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::bigint[]) d(c,n,calls,errs,refs)
 ON CONFLICT(project_id,connection,name) DO UPDATE SET calls=r.calls+excluded.calls,errors=r.errors+excluded.errors,references_count=r.references_count+excluded.references_count
 RETURNING (xmax=0)`, p, up.c, up.n, up.calls, up.errs, up.refs)
		if err != nil {
			return 0, err
		}
		count += n
	}
	if len(rest.c) > 0 {
		n, err := affected(q, `DELETE FROM resource_tool r USING unnest($2::text[],$3::text[],$4::bigint[]) d(c,n,refs)
 WHERE r.project_id=$1 AND r.connection=d.c AND r.name=d.n AND r.references_count+d.refs=0`, p, rest.c, rest.n, rest.refs)
		if err != nil {
			return 0, err
		}
		count -= n
		if err = exec(q, `UPDATE resource_tool r SET calls=r.calls+d.calls,errors=r.errors+d.errs,references_count=r.references_count+d.refs
 FROM unnest($2::text[],$3::text[],$4::bigint[],$5::bigint[],$6::bigint[]) d(c,n,calls,errs,refs)
 WHERE r.project_id=$1 AND r.connection=d.c AND r.name=d.n`, p, rest.c, rest.n, rest.calls, rest.errs, rest.refs); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// flushCosts returns the change of the project's cost and price-reference row counts.
func (d *resourceDelta) flushCosts(q *Request) (int64, int64, error) {
	p := d.project
	var up, rest struct {
		c, b         []string
		value        []*string
		usages, refs []int64
	}
	for _, k := range pairKeys(d.costs) {
		x := d.costs[k]
		t := &rest
		if x.refs > 0 && x.usages >= 0 {
			t = &up
		} else if x.refs == 0 && x.usages == 0 && x.value != nil && *x.value == "0" {
			continue
		}
		t.c, t.b, t.value, t.usages, t.refs = append(t.c, k[0]), append(t.b, k[1]), append(t.value, x.value), append(t.usages, x.usages), append(t.refs, x.refs)
	}
	costs := int64(0)
	if len(up.c) > 0 {
		n, err := inserted(q, `INSERT INTO resource_cost AS r(project_id,currency,basis,value,usages,references_count)
 SELECT $1,d.c,d.b,d.v::numeric,d.u,d.refs FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,v,u,refs)
 ON CONFLICT(project_id,currency,basis) DO UPDATE SET value=r.value+excluded.value,usages=r.usages+excluded.usages,references_count=r.references_count+excluded.references_count
 RETURNING (xmax=0)`, p, up.c, up.b, up.value, up.usages, up.refs)
		if err != nil {
			return 0, 0, err
		}
		costs += n
	}
	if len(rest.c) > 0 {
		n, err := affected(q, `DELETE FROM resource_cost r USING unnest($2::text[],$3::text[],$4::bigint[]) d(c,b,refs)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b AND r.references_count+d.refs=0`, p, rest.c, rest.b, rest.refs)
		if err != nil {
			return 0, 0, err
		}
		costs -= n
		if err = exec(q, `UPDATE resource_cost r SET value=r.value+d.v::numeric,usages=r.usages+d.u,references_count=r.references_count+d.refs
 FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,v,u,refs)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b`, p, rest.c, rest.b, rest.value, rest.usages, rest.refs); err != nil {
			return 0, 0, err
		}
	}
	var pUp, pDown struct {
		c, b, id  []string
		at, delta []int64
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
	prices := int64(0)
	if len(pUp.c) > 0 {
		n, err := inserted(q, `INSERT INTO resource_price_ref AS r(project_id,currency,basis,price_id,observed_at,references_count)
 SELECT $1,d.c,d.b,d.id,d.at,d.n FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,id,at,n)
 ON CONFLICT(project_id,currency,basis,price_id,observed_at) DO UPDATE SET references_count=r.references_count+excluded.references_count
 RETURNING (xmax=0)`, p, pUp.c, pUp.b, pUp.id, pUp.at, pUp.delta)
		if err != nil {
			return 0, 0, err
		}
		prices += n
	}
	if len(pDown.c) > 0 {
		n, err := affected(q, `DELETE FROM resource_price_ref r USING unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,id,at,n)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b AND r.price_id=d.id AND r.observed_at=d.at AND r.references_count+d.n=0`, p, pDown.c, pDown.b, pDown.id, pDown.at, pDown.delta)
		if err != nil {
			return 0, 0, err
		}
		prices -= n
		if err = exec(q, `UPDATE resource_price_ref r SET references_count=r.references_count+d.n
 FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[]) d(c,b,id,at,n)
 WHERE r.project_id=$1 AND r.currency=d.c AND r.basis=d.b AND r.price_id=d.id AND r.observed_at=d.at`, p, pDown.c, pDown.b, pDown.id, pDown.at, pDown.delta); err != nil {
			return 0, 0, err
		}
	}
	return costs, prices, nil
}
