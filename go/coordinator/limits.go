package main

// Subscription limits (docs/designs/fleet-people-access.md section 8, "Limit history, pool and projection"): history is
// the limit_reading events per (subscription, period); burn, projection, pace, pool and the empty times are derived
// from it on every read. Nothing is stored except the planWeight and lowPct settings on the managed-account.

import (
	"context"
	"encoding/json"
	"fleet/contract"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const hourMs = 3_600_000

// periodMs: the length of a Fleet period (5h, weekly, weekly-<model>, <N>m); 0 when unknown.
func periodMs(p string) int64 {
	switch {
	case p == "5h":
		return 5 * hourMs
	case p == "weekly" || strings.HasPrefix(p, "weekly-"):
		return 7 * 24 * hourMs
	case strings.HasSuffix(p, "m"):
		if n, err := strconv.ParseInt(strings.TrimSuffix(p, "m"), 10, 64); err == nil && n > 0 {
			return n * 60_000
		}
	}
	return 0
}

// limitObs is one reading of one (subscription, period).
type limitObs struct {
	at       int64
	pct      *float64
	status   *string
	resetsAt *int64
	source   string
}

// limitHistory: every limit_reading since `since`, per subscription and period, oldest first.
func limitHistory(ctx context.Context, a *App, since int64) (map[string]map[string][]limitObs, error) {
	rs, err := conn(ctx, a).Query(ctx, `SELECT x.account_id, r, d.updated_at
   FROM document d JOIN execution x ON x.id = d.data->>'executionId' CROSS JOIN LATERAL jsonb_array_elements(coalesce(d.data->'data'->'readings','[]'::jsonb)) r
   WHERE d.kind='event' AND d.data->>'type'='limit_reading' AND x.account_id IS NOT NULL AND d.updated_at >= $1
   ORDER BY d.updated_at, d.seq`, time.UnixMilli(since))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]map[string][]limitObs{}
	for rs.Next() {
		var account string
		var v map[string]any
		var at time.Time
		if err := rs.Scan(&account, &v, &at); err != nil {
			return nil, err
		}
		o := limitObs{at: at.UnixMilli(), source: "execution"}
		period, _ := v["period"].(string)
		if period == "" {
			period = "unknown"
		}
		if p, ok := v["pct"].(float64); ok && p >= 0 && p <= 100 {
			o.pct = &p
		}
		if s, ok := v["status"].(string); ok {
			o.status = &s
		}
		if t, ok := v["resetsAt"].(float64); ok {
			n := int64(t)
			o.resetsAt = &n
		}
		if out[account] == nil {
			out[account] = map[string][]limitObs{}
		}
		out[account][period] = append(out[account][period], o)
	}
	return out, rs.Err()
}

type limitOptions struct {
	now, from, to int64
	windowHours   float64
	margin        float64
}

// limitBurn: Σ drops in left (rises in pct) between consecutive readings inside the window ÷ window hours; resets
// (pct falls) add nothing. Nil with fewer than two percent readings in the window.
func limitBurn(obs []limitObs, o limitOptions) *float64 {
	start := o.now - int64(o.windowHours*hourMs)
	var prev *float64
	n, drop := 0, 0.0
	for _, x := range obs {
		if x.at < start || x.pct == nil {
			continue
		}
		if prev != nil && *x.pct > *prev {
			drop += *x.pct - *prev
		}
		prev, n = x.pct, n+1
	}
	if n < 2 {
		return nil
	}
	b := drop / o.windowHours
	return &b
}

// memberLine: left falls at burn from now to 0, returns to 100 at resetsAt, then falls at the same burn until it
// reaches 0 or one period after the reset (the next reset is unknown, so the line ends there). Fleet never guesses a
// reset time; without one the line runs to `to` or to 0. Nothing is drawn past `to`.
func memberLine(left, burn float64, resetsAt *int64, plen int64, o limitOptions) []contract.LimitPoint {
	pts := []contract.LimitPoint{{At: o.now, Left: left}}
	add := func(at int64, l float64) {
		if last := pts[len(pts)-1]; last.At != at || last.Left != l {
			pts = append(pts, contract.LimitPoint{At: at, Left: l})
		}
	}
	// fall from (from, l) until `until`; returns the left at `until`.
	fall := func(from int64, l float64, until int64) float64 {
		if burn <= 0 || until <= from {
			add(until, l)
			return l
		}
		if empty := from + int64(l/burn*hourMs); empty < until {
			add(empty, 0)
			add(until, 0)
			return 0
		}
		v := l - burn*float64(until-from)/hourMs
		add(until, v)
		return v
	}
	if o.to <= o.now {
		return pts
	}
	if resetsAt == nil || *resetsAt <= o.now || *resetsAt >= o.to {
		fall(o.now, left, o.to)
		return pts
	}
	fall(o.now, left, *resetsAt)
	pts = append(pts, contract.LimitPoint{At: *resetsAt, Left: 100})
	fall(*resetsAt, 100, min(o.to, *resetsAt+max(plen, 1)))
	return pts
}

// flatLine: a member with a known left and no burn holds its left until `to`.
func flatLine(left float64, o limitOptions) []contract.LimitPoint {
	return []contract.LimitPoint{{At: o.now, Left: left}, {At: max(o.to, o.now), Left: left}}
}

// firstZero: the first point at 0 strictly before `before` (0: no bound) on a line.
func firstZero(line []contract.LimitPoint, before int64) contract.LimitEmpty {
	if len(line) == 0 {
		return contract.LimitEmpty{State: contract.LimitEmptyStateUnknown}
	}
	for _, p := range line {
		if before != 0 && p.At >= before {
			break
		}
		if p.Left <= 0 {
			at := p.At
			return contract.LimitEmpty{State: contract.LimitEmptyStateAt, At: &at}
		}
	}
	return contract.LimitEmpty{State: contract.LimitEmptyStateNotBeforeEnd}
}

// valueBefore: a line's value just before t (the left side of a step at t).
func valueBefore(line []contract.LimitPoint, t int64) (float64, bool) {
	if len(line) == 0 || t <= line[0].At || t > line[len(line)-1].At {
		return valueAt(line, t)
	}
	for i := 1; i < len(line); i++ {
		a, b := line[i-1], line[i]
		if t <= b.At {
			if b.At == a.At {
				return a.Left, true
			}
			return a.Left + (b.Left-a.Left)*float64(t-a.At)/float64(b.At-a.At), true
		}
	}
	return line[len(line)-1].Left, true
}

// valueAt: a line's value at t (linear between points; at a step, the later point).
func valueAt(line []contract.LimitPoint, t int64) (float64, bool) {
	if len(line) == 0 || t < line[0].At || t > line[len(line)-1].At {
		return 0, false
	}
	v := line[0].Left
	for i := 1; i < len(line); i++ {
		a, b := line[i-1], line[i]
		if t < b.At {
			if b.At == a.At {
				return a.Left, true
			}
			return a.Left + (b.Left-a.Left)*float64(t-a.At)/float64(b.At-a.At), true
		}
		v = b.Left
	}
	return v, true
}

func limitPace(pct *float64, resetsAt *int64, plen int64, o limitOptions) contract.LimitPace {
	if pct == nil || resetsAt == nil || plen == 0 || *resetsAt <= o.now {
		return contract.LimitPaceUnknown
	}
	e := 1 - float64(*resetsAt-o.now)/float64(plen)
	switch {
	case *pct > e*100+o.margin:
		return contract.LimitPaceAhead
	case *pct < e*100-o.margin:
		return contract.LimitPaceUnder
	}
	return contract.LimitPaceOn
}

// limitAccount is what the computation needs of a managed-account.
type limitAccount struct {
	id, harness string
	weight      float64
	lowPct      *float64
}

// limitPeriods: the periods of one subscription with their history and projection.
func limitPeriods(a limitAccount, hist map[string][]limitObs, o limitOptions) []contract.LimitPeriod {
	names := make([]string, 0, len(hist))
	for p := range hist {
		names = append(names, p)
	}
	sort.Strings(names)
	out := []contract.LimitPeriod{}
	for _, name := range names {
		obs := hist[name]
		last := obs[len(obs)-1]
		plen := periodMs(name)
		p := contract.LimitPeriod{Period: name, Status: last.status, ResetsAt: last.resetsAt, Source: last.source, Pace: contract.LimitPaceUnknown,
			FirstEmpty: contract.LimitEmpty{State: contract.LimitEmptyStateUnknown}, History: []contract.LimitPoint{}, Projection: []contract.LimitPoint{}}
		at := last.at
		p.ReadingAt = &at
		for _, x := range obs {
			if x.pct != nil && x.at >= o.from {
				p.History = append(p.History, contract.LimitPoint{At: x.at, Left: 100 - *x.pct})
			}
		}
		// A reading whose period has ended is unknown: the percent no longer holds.
		expired := last.resetsAt != nil && *last.resetsAt <= o.now
		if last.pct != nil && !expired {
			pct, left := *last.pct, 100-*last.pct
			p.Pct, p.Left = &pct, &left
			p.Low = a.lowPct != nil && left < *a.lowPct
			p.Pace = limitPace(p.Pct, last.resetsAt, plen, o)
			p.Burn = limitBurn(obs, o)
			if p.Burn != nil {
				p.Projection = memberLine(left, *p.Burn, last.resetsAt, plen, o)
				before := int64(0)
				if last.resetsAt != nil {
					before = *last.resetsAt
				}
				p.FirstEmpty = firstZero(p.Projection, before)
			}
		}
		out = append(out, p)
	}
	return out
}

// limitPools: per harness and period, the weighted pool over members with a known percent.
func limitPools(accounts []limitAccount, periods map[string][]contract.LimitPeriod, o limitOptions) []contract.LimitPool {
	type key struct{ harness, period string }
	pools := map[key]*contract.LimitPool{}
	lines := map[key][][]contract.LimitPoint{}
	weights := map[key][]float64{}
	firsts := map[key][]contract.LimitEmpty{}
	noBurn := map[key]bool{}
	var keys []key
	for _, a := range accounts {
		for _, p := range periods[a.id] {
			k := key{a.harness, p.Period}
			pl := pools[k]
			if pl == nil {
				pl = &contract.LimitPool{Harness: a.harness, Period: p.Period, Members: []string{}, Unknown: []string{}, Projection: []contract.LimitPoint{},
					FirstEmpty: contract.LimitEmpty{State: contract.LimitEmptyStateUnknown}, PoolEmpty: contract.LimitEmpty{State: contract.LimitEmptyStateUnknown}}
				pools[k] = pl
				keys = append(keys, k)
			}
			if p.Left == nil {
				pl.Unknown = append(pl.Unknown, a.id)
				continue
			}
			pl.Members = append(pl.Members, a.id)
			pl.Capacity += a.weight * 100
			pl.Left += a.weight * *p.Left
			line := p.Projection
			if p.Burn != nil {
				b := a.weight * *p.Burn
				if pl.Burn == nil {
					pl.Burn = new(float64)
				}
				*pl.Burn += b
			} else {
				line = flatLine(*p.Left, o) // no burn known: held at its current left
				noBurn[k] = true
			}
			lines[k] = append(lines[k], line)
			weights[k] = append(weights[k], a.weight)
			firsts[k] = append(firsts[k], p.FirstEmpty)
			if p.ResetsAt != nil && *p.ResetsAt > o.now && (pl.NextReset.At == nil || *p.ResetsAt < *pl.NextReset.At) {
				at := *p.ResetsAt
				left := *p.Left
				if v, ok := valueAt(p.Projection, at-1); ok {
					left = v
				}
				restore := a.weight * (100 - left)
				pl.NextReset = contract.LimitReset{At: &at, Restore: &restore}
			}
		}
	}
	out := []contract.LimitPool{}
	for _, k := range keys {
		pl := pools[k]
		// A subscription of this harness with no reading of this period is unknown too.
		for _, a := range accounts {
			if a.harness == k.harness && !slices.Contains(pl.Members, a.id) && !slices.Contains(pl.Unknown, a.id) {
				pl.Unknown = append(pl.Unknown, a.id)
			}
		}
		// first empty: the earliest member empty before its own reset; "not before the end" only when every member
		// is known not to empty.
		pl.FirstEmpty = contract.LimitEmpty{State: contract.LimitEmptyStateUnknown}
		if len(firsts[k]) > 0 && len(pl.Unknown) == 0 {
			all := true
			for _, f := range firsts[k] {
				if f.State == contract.LimitEmptyStateAt && (pl.FirstEmpty.At == nil || *f.At < *pl.FirstEmpty.At) {
					pl.FirstEmpty = f
				}
				all = all && f.State == contract.LimitEmptyStateNotBeforeEnd
			}
			if pl.FirstEmpty.At == nil && all {
				pl.FirstEmpty = contract.LimitEmpty{State: contract.LimitEmptyStateNotBeforeEnd}
			}
		}
		if pl.Capacity > 0 {
			pct := pl.Left / pl.Capacity * 100
			pl.LeftPct = &pct
		}
		// The pool line: the weighted sum of member lines while every member line has a value; it ends where the first
		// member line ends. Members without a burn are held at their current left. A member reset is a step: both sides
		// are sampled.
		ls := lines[k]
		if len(ls) > 0 {
			end := int64(math.MaxInt64)
			times := map[int64]bool{}
			for _, l := range ls {
				end = min(end, l[len(l)-1].At)
				for _, p := range l {
					times[p.At] = true
				}
			}
			var ts []int64
			for t := range times {
				if t <= end {
					ts = append(ts, t)
				}
			}
			sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
			for _, t := range ts {
				before, at := 0.0, 0.0
				for i, l := range ls {
					b, _ := valueBefore(l, t)
					v, _ := valueAt(l, t)
					before += weights[k][i] * b
					at += weights[k][i] * v
				}
				if before != at {
					pl.Projection = append(pl.Projection, contract.LimitPoint{At: t, Left: before})
				}
				pl.Projection = append(pl.Projection, contract.LimitPoint{At: t, Left: at})
			}
			pl.PoolEmpty = firstZero(pl.Projection, 0)
			if pl.PoolEmpty.State != contract.LimitEmptyStateAt && (noBurn[k] || len(pl.Unknown) > 0) {
				pl.PoolEmpty = contract.LimitEmpty{State: contract.LimitEmptyStateUnknown}
			}
		}
		out = append(out, *pl)
	}
	return out
}

func subscriptionLimits(q *Request, req *contract.SubscriptionLimitsRequest) (contract.SubscriptionLimitsReply, error) {
	var none contract.SubscriptionLimitsReply
	now := nowMs()
	o := limitOptions{now: now, from: now - 7*24*hourMs, to: now + 7*24*hourMs, windowHours: 3, margin: 5}
	if req.WindowHours != nil {
		if !(*req.WindowHours > 0 && *req.WindowHours*hourMs < float64(now)) {
			return none, fail(400, "windowHours: a positive number of hours since the epoch at most")
		}
		o.windowHours = *req.WindowHours
	}
	if req.From != nil {
		o.from = *req.From
	}
	if req.To != nil {
		o.to = *req.To
	}
	if o.from > o.to {
		return none, fail(400, "from must not be after to")
	}
	if req.PaceMargin != nil {
		o.margin = *req.PaceMargin
	}
	ctx := withTx(q)
	latest, err := latestReadings(ctx, q.app)
	if err != nil {
		return none, err
	}
	hist, err := limitHistory(ctx, q.app, min(o.from, now-int64(o.windowHours*hourMs)))
	if err != nil {
		return none, err
	}
	rows, err := rowsCtx(ctx, conn(ctx, q.app), "SELECT data FROM document WHERE kind='managed-account' ORDER BY seq")
	if err != nil {
		return none, err
	}
	var accounts []limitAccount
	periods := map[string][]contract.LimitPeriod{}
	reply := contract.SubscriptionLimitsReply{ServerNow: now, WindowHours: o.windowHours, From: o.from, To: o.to, Subscriptions: []contract.LimitSubscription{}}
	for _, r := range rows {
		x, _ := r["data"].(map[string]any)
		a := limitAccount{id: astr(x["id"]), harness: astr(x["harness"]), weight: 1}
		if w, ok := x["planWeight"].(float64); ok && w > 0 {
			a.weight = w
		}
		if l, ok := x["lowPct"].(float64); ok {
			a.lowPct = &l
		}
		accounts = append(accounts, a)
		periods[a.id] = limitPeriods(a, hist[a.id], o)
		readings, err := asT[[]contract.SubscriptionReading](latest[a.id]) // Reading's JSON is the contract's SubscriptionReading
		if err != nil {
			return none, err
		}
		if readings == nil {
			readings = []contract.SubscriptionReading{}
		}
		raw := func(k string) json.RawMessage { b, _ := json.Marshal(x[k]); return b }
		var sw *float64
		if v, ok := x["switchPct"].(float64); ok {
			sw = &v
		}
		reply.Subscriptions = append(reply.Subscriptions, contract.LimitSubscription{Id: a.id, Name: raw("name"), Harness: a.harness, Holder: raw("holder"),
			Intake: raw("intake"), SwitchPct: sw, PlanWeight: a.weight, LowPct: a.lowPct, Readings: readings, Periods: periods[a.id]})
	}
	reply.Pools = limitPools(accounts, periods, o)
	return reply, nil
}
