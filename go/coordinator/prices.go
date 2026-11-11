package main

// The price table (docs/designs/fleet-analytics.md, Price table): what one unit of a model or connection tool costs, from
// one source, on one date. Append-only. Usage times a price entry gives an estimated cost; no entry gives unavailable.

import (
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// priceCols reads price as text: exact for the estimate (numeric), a number in the view.
const priceCols = "id,subject,unit,price::text AS price,currency,source,source_ref,observed_at,effective_from,created_by"

var priceUnits = []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"}

func priceView(r doc) doc {
	price, _ := strconv.ParseFloat(astr(r["price"]), 64)
	return doc{"id": r["id"], "subject": r["subject"], "unit": r["unit"], "price": price, "currency": r["currency"], "source": r["source"],
		"sourceRef": r["source_ref"], "observedAt": msOf(r["observed_at"]), "effectiveFrom": msOf(r["effective_from"]), "createdBy": r["created_by"]}
}

// priceAdd: one entry. subject is a resolved model ID or <connection>/<tool>; unit a token unit or the tool's catalogue
// unit; effectiveFrom defaults to observedAt, which defaults to now, and may be earlier so a price covers past work.
func priceAdd(q *Request, b doc) (any, error) {
	if !keysOnly(b, "subject", "unit", "price", "currency", "source", "sourceRef", "observedAt", "effectiveFrom") {
		return nil, fail(400, "Expected subject, unit, price, currency, source, sourceRef, observedAt, effectiveFrom")
	}
	subject, err := strArg(b, "subject", "subject", 200)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(subject, "-latest") {
		return nil, fail(400, "subject is a resolved model ID, never an alias")
	}
	unit, err := strArg(b, "unit", "unit", 80)
	if err != nil {
		return nil, err
	}
	if conn, tool, isTool := strings.Cut(subject, "/"); isTool {
		if want := astr(mapOf(providerToolOf(q, conn, tool))["unit"]); want != "" && want != unit {
			return nil, fail(400, "The unit of "+subject+" is "+want)
		}
	} else if !inList(priceUnits, unit) {
		return nil, fail(400, "A model's unit is one of "+strings.Join(priceUnits, ", "))
	}
	price, ok := numOK(b["price"])
	if !ok || price < 0 {
		return nil, fail(400, "Expected price, a number >= 0")
	}
	currency, err := strArg(b, "currency", "currency", 8)
	if err != nil {
		return nil, err
	}
	source := astr(b["source"])
	if !inList([]string{"vendor", "snapshot", "owner"}, source) {
		return nil, fail(400, "source is vendor, snapshot or owner")
	}
	observed, effective := time.Now(), time.Time{}
	if b["observedAt"] != nil {
		ms, ok := intOK(b["observedAt"])
		if !ok {
			return nil, fail(400, "observedAt is epoch milliseconds")
		}
		observed = time.UnixMilli(ms)
	}
	effective = observed
	if b["effectiveFrom"] != nil {
		ms, ok := intOK(b["effectiveFrom"])
		if !ok {
			return nil, fail(400, "effectiveFrom is epoch milliseconds")
		}
		effective = time.UnixMilli(ms)
	}
	id := newID()
	if err := exec(q, "INSERT INTO price_entry(id,subject,unit,price,currency,source,source_ref,observed_at,effective_from,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
		id, subject, unit, b["price"], strings.ToUpper(currency), source, b["sourceRef"], observed, effective, actor(q, "operator")); err != nil {
		return nil, err
	}
	rs, err := queryRows(q, "SELECT "+priceCols+" FROM price_entry WHERE id=$1", id)
	if err != nil {
		return nil, err
	}
	return priceView(rs[0]), nil
}

// providerToolOf: the catalogue entry of a connection's tool, nil when unknown.
func providerToolOf(q *Request, connection, tool string) any {
	c, err := maybe(q, "connection:"+connection, "api-connection")
	if err != nil || c == nil {
		return nil
	}
	return providerTools(astr(c["provider"]))[tool]
}

func pricesList(q *Request, b doc) (any, error) {
	rs, err := queryRows(q, "SELECT "+priceCols+" FROM price_entry WHERE $1='' OR subject=$1 ORDER BY subject,unit,effective_from DESC,seq DESC", astr(b["subject"]))
	out := []any{}
	for _, r := range rs {
		out = append(out, priceView(r))
	}
	return doc{"prices": out}, err
}

// usage: one measured amount of a unit, for a subject, at a time (a tool_run's connection_cost).
type usage struct {
	subject, unit string
	amount        float64
	at            time.Time
}

// estimateCost: the cost of a set of usages, one figure per currency, each with basis estimated and the price entries
// used; usages with no price give one figure {value: null, basis: unavailable} (never 0). No usage gives none.
func estimateCost(q *Request, us []usage) ([]any, error) {
	if len(us) == 0 {
		return []any{}, nil
	}
	rs, err := queryRows(q, "SELECT "+priceCols+" FROM price_entry ORDER BY effective_from DESC,seq DESC")
	if err != nil {
		return nil, err
	}
	type sum struct {
		value  *big.Float
		prices map[string]any
	}
	byCurrency, unpriced := map[string]*sum{}, 0
	for _, u := range us {
		var hit doc
		for _, r := range rs {
			eff, _ := r["effective_from"].(time.Time)
			if r["subject"] == u.subject && r["unit"] == u.unit && !eff.After(u.at) {
				hit = r
				break
			}
		}
		if hit == nil {
			unpriced++
			continue
		}
		c := astr(hit["currency"])
		if byCurrency[c] == nil {
			byCurrency[c] = &sum{new(big.Float), map[string]any{}}
		}
		p, _ := new(big.Float).SetString(astr(hit["price"]))
		if p == nil {
			unpriced++
			continue
		}
		byCurrency[c].value.Add(byCurrency[c].value, new(big.Float).Mul(p, big.NewFloat(u.amount)))
		byCurrency[c].prices[astr(hit["id"])] = msOf(hit["observed_at"])
	}
	out := []any{}
	currencies := make([]string, 0, len(byCurrency))
	for c := range byCurrency {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	for _, c := range currencies {
		v, _ := byCurrency[c].value.Float64()
		prices := []any{}
		for _, id := range sortedKeysOf(byCurrency[c].prices) {
			prices = append(prices, doc{"id": id, "observedAt": byCurrency[c].prices[id]})
		}
		out = append(out, doc{"value": v, "currency": c, "basis": "estimated", "prices": prices})
	}
	if unpriced > 0 {
		out = append(out, doc{"value": nil, "currency": nil, "basis": "unavailable", "reason": "no price entry", "usages": unpriced})
	}
	return out, nil
}

func init() {
	gatedObjOp("price_add", false, func(q *Request) (any, error) { return priceAdd(q, body(q)) })
	gatedObjOp("prices_list", true, func(q *Request) (any, error) { return pricesList(q, body(q)) })
}
