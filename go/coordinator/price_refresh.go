package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

type providerObservation struct {
	result contract.PriceRefreshProvider
	rates  []contract.PriceRefreshChange
}

type providerFetcher func(context.Context, string) (string, error)

func priceRefresh(q *Request, b *contract.PriceRefreshRequest) (contract.PriceRefreshReply, error) {
	return priceRefreshWith(q, b, fetchProviderPrice, time.Now)
}

func priceRefreshWith(q *Request, b *contract.PriceRefreshRequest, fetch providerFetcher, clock func() time.Time) (contract.PriceRefreshReply, error) {
	var out contract.PriceRefreshReply
	if b.PlanId != nil {
		if id, err := hex.DecodeString(*b.PlanId); err != nil || len(id) != sha256.Size {
			return out, fail(400, "Expected the planId from a pricing preview")
		}
	}
	observations := make([]providerObservation, 0, len(providerPriceSources))
	for _, source := range providerPriceSources {
		if err := q.ctx().Err(); err != nil {
			return out, err
		}
		page, err := fetch(q.ctx(), source.document)
		at := clock()
		o := providerObservation{result: contract.PriceRefreshProvider{
			Provider: source.name, SourceUrl: source.url, ObservedAt: at.UnixMilli(),
			Gaps: []string{}, Changes: []contract.PriceRefreshChange{},
		}}
		if err != nil {
			o.result.Gaps = append(o.result.Gaps, "Official source unavailable: "+err.Error())
		} else {
			o.rates, o.result.Gaps = source.parse(page, at)
		}
		observations = append(observations, o)
	}

	compare := func(q *Request) error {
		// Recheck the director's authority in the same transaction as the catalog snapshot and any writes.
		if err := authorizeOp(q, "price_refresh"); err != nil {
			return err
		}
		saved, err := q.db().PriceEntries(q.ctx())
		if err != nil {
			return err
		}
		out, err = proposePrices(observations, saved)
		if err != nil {
			return err
		}
		if b.PlanId == nil {
			return nil
		}
		if *b.PlanId != out.PlanId {
			return fail(409, "Pricing preview changed or was already applied. Get a fresh preview")
		}
		for _, p := range out.Providers {
			for _, change := range p.Changes {
				effective := p.ObservedAt
				if change.EffectiveFrom != nil {
					effective = *change.EffectiveFrom
				}
				by := actor(q, "operator")
				entry, err := q.db().PriceInsert(q.ctx(), db.PriceInsertParams{
					ID: newID(), Subject: change.Subject, Unit: change.Unit, Price: strconv.FormatFloat(change.Price, 'f', -1, 64),
					Currency: change.Currency, Source: "vendor", SourceRef: &p.SourceUrl, CreatedBy: &by,
					ObservedAt: time.UnixMilli(p.ObservedAt), EffectiveFrom: time.UnixMilli(effective),
				})
				if err != nil {
					return err
				}
				view := priceView(entry)
				extra := auditExtra{Reason: "official provider pricing refresh"}
				if change.EffectiveSourceUrl != nil {
					extra.Note = *change.EffectiveSourceUrl
				}
				if err := audit(q, "price:"+view.Id, "price", change.Previous, view, extra); err != nil {
					return err
				}
				out.Appended = append(out.Appended, view)
			}
		}
		return nil
	}

	if b.PlanId == nil {
		err := q.app.store.Read(q.ctx(), q, compare)
		return out, err
	}
	// price_add shares this lock. Fetches finish before taking it and no outside call runs inside it.
	err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, compare)
	return out, err
}

type priceKey struct{ model, unit string }

func proposePrices(observations []providerObservation, saved []db.PriceEntry) (contract.PriceRefreshReply, error) {
	out := contract.PriceRefreshReply{Providers: []contract.PriceRefreshProvider{}, Appended: []contract.PriceEntry{}}
	timeline := map[priceKey][]contract.PriceEntry{}
	for _, entry := range saved { // PriceEntries is ordered by effective_from DESC, seq DESC.
		key := priceKey{entry.Subject, entry.Unit}
		timeline[key] = append(timeline[key], priceView(entry))
	}
	for _, o := range observations {
		p := o.result
		p.Gaps = append([]string{}, p.Gaps...)
		proposed := map[priceKey]contract.PriceRefreshChange{}
		confirmedDates := map[priceKey]map[int64]bool{}
		for _, rate := range o.rates {
			key := priceKey{rate.Subject, rate.Unit}
			if confirmedDates[key] == nil {
				confirmedDates[key] = map[int64]bool{}
			}
			if rate.EffectiveFrom != nil {
				confirmedDates[key][*rate.EffectiveFrom] = true
			}
			entries := timeline[key]
			effective := p.ObservedAt
			if rate.EffectiveFrom != nil {
				effective = *rate.EffectiveFrom
			}
			for _, entry := range entries {
				if entry.EffectiveFrom <= effective {
					rate.Previous = &entry
					break
				}
			}
			if prior, ok := proposed[key]; ok && (rate.Previous == nil || rate.Previous.EffectiveFrom < p.ObservedAt) {
				// A change from this run precedes the dated rate. Compare to what will be active then.
				if sameRate(rate, prior.Price, prior.Currency) {
					p.Unchanged++
					continue
				}
			} else if rate.Previous != nil && sameRate(rate, rate.Previous.Price, rate.Previous.Currency) {
				p.Unchanged++
				continue
			}
			if rate.Previous == nil && rate.EffectiveFrom == nil {
				initialCacheReadDate(&rate, p.ObservedAt)
			}
			proposed[key] = rate
			p.Changes = append(p.Changes, rate)
		}
		for key := range confirmedDates {
			for _, entry := range timeline[key] {
				if entry.EffectiveFrom > p.ObservedAt && !confirmedDates[key][entry.EffectiveFrom] {
					p.Gaps = append(p.Gaps, key.model+" "+key.unit+": saved future entry "+entry.Id+" has no matching published date")
				}
			}
		}
		sort.Strings(p.Gaps)
		out.Providers = append(out.Providers, p)
	}
	// Bind the exact changes and gaps. Observation times are omitted so a fresh fetch can confirm the same plan.
	evidence := append([]contract.PriceRefreshProvider(nil), out.Providers...)
	for i := range evidence {
		evidence[i].ObservedAt = 0
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		return contract.PriceRefreshReply{}, err
	}
	digest := sha256.Sum256(payload)
	out.PlanId = hex.EncodeToString(digest[:])
	return out, nil
}

func sameRate(rate contract.PriceRefreshChange, price float64, currency string) bool {
	return rate.Price == price && rate.Currency == currency
}

func initialCacheReadDate(rate *contract.PriceRefreshChange, observed int64) {
	if rate.Unit != "cache_read_tokens" || rate.Price != 0.10/1e6 {
		return
	}
	var at time.Time
	switch rate.Subject {
	case "gpt-6.2-sol":
		at = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
		rate.EffectiveSourceUrl = ptr("https://openai.com/index/introducing-gpt-6-2-astra-sol/")
	case "claude-sonnet-5-5":
		at = time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
		rate.EffectiveSourceUrl = ptr("https://www.anthropic.com/claude-sonnet-5-5")
	default:
		return
	}
	effective := at.UnixMilli()
	if effective > observed {
		rate.EffectiveSourceUrl = nil
		return
	}
	rate.EffectiveFrom = &effective
}

func init() { op("price_refresh", priceRefresh) }
