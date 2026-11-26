package main

// The price table (docs/designs/fleet-analytics.md, Price table): what one unit of a model or connection tool costs, from
// one source, on one date. Append-only. Usage times a price entry gives an estimated cost; no entry gives unavailable.

import (
	"errors"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

var priceUnits = []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"}

func priceView(r db.PriceEntry) contract.PriceEntry {
	price, _ := strconv.ParseFloat(r.Price, 64)
	return contract.PriceEntry{Id: r.ID, Subject: r.Subject, Unit: r.Unit, Price: price, Currency: r.Currency, Source: r.Source,
		SourceRef: r.SourceRef, ObservedAt: r.ObservedAt.UnixMilli(), EffectiveFrom: r.EffectiveFrom.UnixMilli(), CreatedBy: r.CreatedBy}
}

// priceAdd: one entry. subject is a resolved model ID or <connection>/<tool>; unit a token unit or the tool's catalogue
// unit; effectiveFrom defaults to observedAt, which defaults to now, and may be earlier so a price covers past work.
func priceAdd(q *Request, b *contract.PriceAddRequest) (contract.PriceEntry, error) {
	var none contract.PriceEntry
	subject, unit := b.Subject, b.Unit
	if subject == "" || len([]rune(subject)) > 200 {
		return none, fail(400, "Expected subject")
	}
	if strings.HasSuffix(subject, "-latest") {
		return none, fail(400, "subject is a resolved model ID, never an alias")
	}
	if unit == "" || len([]rune(unit)) > 80 {
		return none, fail(400, "Expected unit")
	}
	// <connection>/<tool> when the connection exists (its tool must exist and the unit is its catalogue unit); otherwise
	// a model ID, which may itself contain a slash, priced in a token unit.
	conn, tool, isTool := strings.Cut(subject, "/")
	provider, found := "", false
	if isTool {
		var err error
		if provider, found, err = connectionProvider(q, conn); err != nil {
			return none, err
		}
	}
	if found {
		want := gatewayProviders[provider].Tools[tool].Unit
		if want == "" {
			return none, fail(400, "Connection "+conn+" has no tool "+tool)
		}
		if want != unit {
			return none, fail(400, "The unit of "+subject+" is "+want)
		}
	} else if !inList(priceUnits, unit) {
		return none, fail(400, "A model's unit is one of "+strings.Join(priceUnits, ", "))
	}
	if b.Price < 0 {
		return none, fail(400, "Expected price, a number >= 0")
	}
	if !currencyCode.MatchString(b.Currency) {
		return none, fail(400, "currency is an ISO 4217 code such as USD")
	}
	if !inList([]string{"vendor", "snapshot", "owner"}, b.Source) {
		return none, fail(400, "source is vendor, snapshot or owner")
	}
	observed := time.Now()
	if b.ObservedAt != nil {
		observed = time.UnixMilli(*b.ObservedAt)
	}
	effective := observed
	if b.EffectiveFrom != nil {
		effective = time.UnixMilli(*b.EffectiveFrom)
	}
	by := actor(q, "operator")
	var r db.PriceEntry
	err := q.app.store.Write(q.ctx(), q, nil, func(q *Request) (err error) {
		r, err = q.db().PriceInsert(q.ctx(), db.PriceInsertParams{ID: newID(), Subject: subject, Unit: unit,
			Price: strconv.FormatFloat(b.Price, 'f', -1, 64), Currency: b.Currency, Source: b.Source, SourceRef: b.SourceRef,
			ObservedAt: observed, EffectiveFrom: effective, CreatedBy: &by})
		return err
	})
	if err != nil {
		return none, err
	}
	return priceView(r), nil
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// connectionProvider: the provider of the API connection name, found false when there is none.
func connectionProvider(q *Request, name string) (string, bool, error) {
	var p string
	err := q.app.store.Read(q.ctx(), q, func(q *Request) (err error) {
		p, err = q.db().PriceConnectionProvider(q.ctx(), "connection:"+name)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return p, err == nil, err
}

func pricesList(q *Request, b *contract.PricesListRequest) (contract.PricesListReply, error) {
	out := contract.PricesListReply{Prices: []contract.PriceEntry{}}
	err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		rs, err := q.db().PricesList(q.ctx(), deref(b.Subject))
		for _, r := range rs {
			out.Prices = append(out.Prices, priceView(r))
		}
		return err
	})
	return out, err
}

// pricedByTokens: whether an execution's cost is its tokens priced by model and token kind (an agent run without a
// connection cost, a model tool call whose reply named its model: gateway usageFields), else its connection usage priced
// by <connection>/<tool> and unit. The SQL leaves compute the same as by_tokens.
func pricedByTokens(kind string, model *string, connectionCost bool) bool {
	if kind == "tool_run" {
		return deref(model) != ""
	}
	return !connectionCost
}

// usage: one measured amount of a unit, for a subject, at a time (a tool_run's connection_cost).
type usage struct {
	subject, unit string
	amount        *float64
	at            time.Time
}

// estimateCost: the cost of a set of usages against the price entries, one figure per currency, each with basis estimated and the price entries
// used; usages with no price give one figure {value: null, basis: unavailable} (never 0). No usage gives none.
func estimateCost(rs []db.PriceEntry, us []usage) []contract.CostFigure {
	if len(us) == 0 {
		return []contract.CostFigure{}
	}
	type sum struct {
		value  *big.Float
		prices map[string]int64
	}
	byCurrency, unpriced := map[string]*sum{}, 0
	for _, u := range us {
		if u.amount == nil {
			unpriced++ // a call whose provider reported no amount
			continue
		}
		var hit *db.PriceEntry
		for i := range rs {
			if r := &rs[i]; r.Subject == u.subject && r.Unit == u.unit && !r.EffectiveFrom.After(u.at) {
				hit = r
				break
			}
		}
		if hit == nil {
			unpriced++
			continue
		}
		c := hit.Currency
		if byCurrency[c] == nil {
			byCurrency[c] = &sum{new(big.Float), map[string]int64{}}
		}
		p, _ := new(big.Float).SetString(hit.Price)
		if p == nil {
			unpriced++
			continue
		}
		byCurrency[c].value.Add(byCurrency[c].value, new(big.Float).Mul(p, big.NewFloat(*u.amount)))
		byCurrency[c].prices[hit.ID] = hit.ObservedAt.UnixMilli()
	}
	out := []contract.CostFigure{}
	currencies := make([]string, 0, len(byCurrency))
	for c := range byCurrency {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	for _, c := range currencies {
		v, _ := byCurrency[c].value.Float64()
		ids := make([]string, 0, len(byCurrency[c].prices))
		for id := range byCurrency[c].prices {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		prices := make([]contract.CostPriceRef, 0, len(ids))
		for _, id := range ids {
			prices = append(prices, contract.CostPriceRef{Id: id, ObservedAt: byCurrency[c].prices[id]})
		}
		curr, val := c, v
		out = append(out, contract.CostFigure{
			Value:    &val,
			Currency: &curr,
			Basis:    contract.ResourceBasisEstimated,
			Prices:   prices,
		})
	}
	if unpriced > 0 {
		uCount := int64(unpriced)
		reason := unavailableCostReason
		out = append(out, contract.CostFigure{
			Value:    nil,
			Currency: nil,
			Basis:    contract.ResourceBasisUnavailable,
			Reason:   &reason,
			Usages:   &uCount,
		})
	}
	sortCostFigures(out)
	return out
}

// priceEntries: the whole table, newest in force first, read once per estimate.
func priceEntries(q *Request) ([]db.PriceEntry, error) {
	var rs []db.PriceEntry
	err := q.app.store.Read(q.ctx(), q, func(q *Request) (err error) {
		rs, err = q.db().PriceEntries(q.ctx())
		return err
	})
	return rs, err
}

func init() {
	op("price_add", priceAdd)
	op("prices_list", pricesList)
}
