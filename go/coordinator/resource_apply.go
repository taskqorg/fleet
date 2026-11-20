package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/obs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// resourceDelta accumulates the support changes of every contribution applied to one project in one replacement
// transaction and writes them in one batch. Counts, references, multiplicities and decimal sums are additive, so the
// net change of a cohort leaves exactly the rows that applying its leaves one by one leaves. Cost values are the
// exception: an unknown amount makes the stored value NULL, which absorbs every later sum until the row is deleted at
// its last reference and recreated. A cost key is therefore netted only when the transaction only adds known amounts to
// it; a key with any unknown amount or any subtract keeps the per-contribution statements in order. A subtract that would make a count, multiplicity or reason
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
	sum               string // stored trimmed: a row deleted and recreated mid-transaction resets its scale, and every metric reader normalizes it
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
	scale        int // cost items return the stored value as is, so a netted (additions-only) value keeps numeric's scale
	usages, refs int64
	ops          []resourceCostOp // every change in order
	sequential   bool             // an unknown amount or a subtract: written one by one, see resourceDelta
}
type resourcePriceKey struct {
	currency, basis, price string
	observed               int64
}

func newResourceDelta(project string) *resourceDelta {
	return &resourceDelta{project: project, metrics: map[[2]string]*resourceMetricDelta{}, extremes: map[resourceExtremeKey]int64{},
		tools: map[[2]string]*resourceCountDelta{}, costs: map[[2]string]*resourceCostDelta{}, prices: map[resourcePriceKey]int64{}}
}

// resourceScale is the scale PostgreSQL numeric gives a decimal literal: fraction digits minus a decimal exponent.
func resourceScale(v string) int {
	mantissa, exponent := v, 0
	if i := strings.IndexAny(v, "eE"); i >= 0 {
		mantissa = v[:i]
		if n, err := strconv.Atoi(v[i+1:]); err == nil {
			exponent = n
		}
	}
	scale := 0
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		scale = len(mantissa) - i - 1
	}
	return max(0, scale-exponent)
}

// resourceAtScale pads a trimmed decimal to scale fraction digits, as PostgreSQL numeric addition would store it.
func resourceAtScale(v string, scale int) string {
	if have := resourceScale(v); have < scale {
		if have == 0 {
			v += "."
		}
		v += strings.Repeat("0", scale-have)
	}
	return v
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
	}{{"start", c.StartedMs}, {"end", c.EndedMs}, {"peakMemory", resourceInteger((*json.Number)(c.Metrics["peakMemoryBytes"].Value))}} {
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
		if cost.Amount == nil || sign < 0 || cost.UnpricedCount < 0 {
			x.sequential = true
		}
		if cost.Amount != nil {
			s, err := decimalArithmetic(x.value, resourceSigned(*cost.Amount, sign), false)
			if err != nil {
				return err
			}
			x.value, x.scale = s, max(x.scale, resourceScale(*cost.Amount))
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

// resourceBatch sends the typed batch queries (queries/resources.sql, :batch*) of one flush or header read in one
// round trip: each generated call queues its statements here, send sends them together, and the reads queued with
// read take their results in queue order. A generated result's Close is a no-op; send closes the batch once.
type resourceBatch struct {
	tx      pgx.Tx
	batch   pgx.Batch
	results pgx.BatchResults
	reads   []func() error
}

func newResourceBatch(q *Request) (*resourceBatch, *db.Queries) {
	b := &resourceBatch{tx: q.tx}
	return b, db.New(b)
}

func (b *resourceBatch) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return b.tx.Exec(ctx, sql, args...)
}
func (b *resourceBatch) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return b.tx.Query(ctx, sql, args...)
}
func (b *resourceBatch) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return b.tx.QueryRow(ctx, sql, args...)
}
func (b *resourceBatch) SendBatch(_ context.Context, x *pgx.Batch) pgx.BatchResults {
	b.batch.QueuedQueries = append(b.batch.QueuedQueries, x.QueuedQueries...)
	return resourceBatchResults{b}
}

type resourceBatchResults struct{ b *resourceBatch }

func (r resourceBatchResults) Exec() (pgconn.CommandTag, error) { return r.b.results.Exec() }
func (r resourceBatchResults) Query() (pgx.Rows, error)         { return r.b.results.Query() }
func (r resourceBatchResults) QueryRow() pgx.Row                { return r.b.results.QueryRow() }
func (r resourceBatchResults) Close() error                     { return nil }

// read queues the reading of the statements queued last.
func (b *resourceBatch) read(f func() error) { b.reads = append(b.reads, f) }

// send sends every queued statement and runs the reads in order. Closing consumes even unread results, so a late SQL
// error is returned too.
func (b *resourceBatch) send(ctx context.Context) error {
	if len(b.batch.QueuedQueries) == 0 {
		return nil
	}
	b.results = b.tx.SendBatch(ctx, &b.batch)
	for _, read := range b.reads {
		if err := read(); err != nil {
			b.results.Close()
			return err
		}
	}
	return b.results.Close()
}

// firstErr keeps the first error a batch read reports.
func firstErr(err *error) func(int, error) {
	return func(_ int, e error) {
		if *err == nil {
			*err = e
		}
	}
}

// created adds the rows an upsert created (it returns xmax=0 per row) to total.
func created(total *int64, err *error) func(int, []bool, error) {
	return func(_ int, rows []bool, e error) {
		for _, c := range rows {
			if c {
				*total++
			}
		}
		if *err == nil {
			*err = e
		}
	}
}

// removed subtracts the rows a delete removed from total.
func removed(total *int64, err *error) func(int, int64, error) {
	return func(_ int, n int64, e error) {
		*total -= n
		if *err == nil {
			*err = e
		}
	}
}

// Per table, keys whose every component only grows take one upsert; keys whose references reach zero are deleted;
// the rest are updated in place (queries/resources.sql). The project advisory lock is held, so row order needs no care.
func (d *resourceDelta) flush(q *Request) (resultErr error) {
	_, span := obs.Start(q.ctx(), "resource.apply", "internal")
	defer func() { span.End(resultErr) }()
	ctx, p := q.ctx(), d.project
	w, bq := newResourceBatch(q)
	var toolCount, costCount, priceCount int64
	exec := func(r interface{ Exec(func(int, error)) }) {
		w.read(func() (err error) { r.Exec(firstErr(&err)); return })
	}
	if d.executions != 0 || d.history != 0 || d.otherCalls != 0 || d.otherErrs != 0 {
		exec(bq.ResourceSummaryAdd(ctx, []db.ResourceSummaryAddParams{{ProjectID: p, Executions: d.executions, History: d.history, OtherCalls: d.otherCalls, OtherErrors: d.otherErrs}}))
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
		exec(bq.ResourceMetricsGain(ctx, []db.ResourceMetricsGainParams{{ProjectID: p, Metrics: up.m, Bases: up.b, Present: up.p, Expected: up.e, Sums: up.s, Reasons: up.r}}))
	}
	if len(down.m) > 0 {
		exec(bq.ResourceMetricsLose(ctx, []db.ResourceMetricsLoseParams{{ProjectID: p, Metrics: down.m, Bases: down.b, Present: down.p, Expected: down.e, Sums: down.s, Reasons: down.r}}))
	}
	if touched := append(append([]string{}, up.m...), down.m...); len(touched) > 0 {
		exec(bq.ResourceMetricsPrune(ctx, []db.ResourceMetricsPruneParams{{ProjectID: p, Metrics: touched, Bases: append(append([]string{}, up.b...), down.b...)}}))
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
		exec(bq.ResourceExtremesGain(ctx, []db.ResourceExtremesGainParams{{ProjectID: p, Metrics: eUpM, Vals: eUpV, Deltas: eUpN}}))
	}
	if len(eDownM) > 0 {
		exec(bq.ResourceExtremesDrop(ctx, []db.ResourceExtremesDropParams{{ProjectID: p, Metrics: eDownM, Vals: eDownV, Deltas: eDownN}}))
		exec(bq.ResourceExtremesLose(ctx, []db.ResourceExtremesLoseParams{{ProjectID: p, Metrics: eDownM, Vals: eDownV, Deltas: eDownN}}))
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
		r := bq.ResourceToolsGain(ctx, []db.ResourceToolsGainParams{{ProjectID: p, Connections: tUp.a, Names: tUp.b, Calls: tUp.calls, Errs: tUp.errs, Refs: tUp.refs}})
		w.read(func() (err error) { r.Query(created(&toolCount, &err)); return })
	}
	if len(tRest.a) > 0 {
		r := bq.ResourceToolsDrop(ctx, []db.ResourceToolsDropParams{{ProjectID: p, Connections: tRest.a, Names: tRest.b, Refs: tRest.refs}})
		w.read(func() (err error) { r.QueryRow(removed(&toolCount, &err)); return })
		exec(bq.ResourceToolsLose(ctx, []db.ResourceToolsLoseParams{{ProjectID: p, Connections: tRest.a, Names: tRest.b, Calls: tRest.calls, Errs: tRest.errs, Refs: tRest.refs}}))
	}

	type costRows struct {
		c, b, v      []string
		usages, refs []int64
	}
	var cUp costRows
	for _, k := range pairKeys(d.costs) {
		c := d.costs[k]
		if c.sequential {
			for _, op := range c.ops {
				if op.sign > 0 {
					r := bq.ResourceCostAdd(ctx, []db.ResourceCostAddParams{{ProjectID: p, Currency: k[0], Basis: k[1], Amount: op.amount, Usages: op.usages}})
					w.read(func() (err error) { r.Query(created(&costCount, &err)); return })
				} else {
					r := bq.ResourceCostSubtract(ctx, []db.ResourceCostSubtractParams{{ProjectID: p, Currency: k[0], Basis: k[1], Amount: op.amount, Usages: op.usages}})
					w.read(func() (err error) { r.QueryRow(removed(&costCount, &err)); return })
				}
			}
			continue
		}
		t := &cUp // only known additions remain netted (see resourceDelta)
		t.c, t.b, t.v, t.usages, t.refs = append(t.c, k[0]), append(t.b, k[1]), append(t.v, resourceAtScale(c.value, c.scale)), append(t.usages, c.usages), append(t.refs, c.refs)
	}
	if len(cUp.c) > 0 {
		r := bq.ResourceCostsGain(ctx, []db.ResourceCostsGainParams{{ProjectID: p, Currencies: cUp.c, Bases: cUp.b, Vals: cUp.v, Usages: cUp.usages, Refs: cUp.refs}})
		w.read(func() (err error) { r.Query(created(&costCount, &err)); return })
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
		r := bq.ResourcePricesGain(ctx, []db.ResourcePricesGainParams{{ProjectID: p, Currencies: pUp.c, Bases: pUp.b, PriceIds: pUp.id, Observed: pUp.at, Deltas: pUp.delta}})
		w.read(func() (err error) { r.Query(created(&priceCount, &err)); return })
	}
	if len(pDown.c) > 0 {
		r := bq.ResourcePricesDrop(ctx, []db.ResourcePricesDropParams{{ProjectID: p, Currencies: pDown.c, Bases: pDown.b, PriceIds: pDown.id, Observed: pDown.at, Deltas: pDown.delta}})
		w.read(func() (err error) { r.QueryRow(removed(&priceCount, &err)); return })
		exec(bq.ResourcePricesLose(ctx, []db.ResourcePricesLoseParams{{ProjectID: p, Currencies: pDown.c, Bases: pDown.b, PriceIds: pDown.id, Observed: pDown.at, Deltas: pDown.delta}}))
	}

	statements := len(w.batch.QueuedQueries)
	if err := w.send(ctx); err != nil {
		return err
	}
	span.Set("fleet.resource.apply_statements", strconv.Itoa(statements))
	// Only a changed item count is written.
	if toolCount != 0 || costCount != 0 || priceCount != 0 {
		return q.db().ResourceSummaryItems(ctx, db.ResourceSummaryItemsParams{ProjectID: p, Tools: toolCount, Costs: costCount, Prices: priceCount})
	}
	return nil
}
