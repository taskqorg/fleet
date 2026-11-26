package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

const deliverySiteInterval = time.Minute

// Allow a deployment restart between samples. A missed cadence starts a new
// coverage interval, independent of how long any individual release takes.
const deliverySiteCoverageStep = 2 * deliverySiteInterval

func startDeliverySiteObservations(ctx context.Context, a *App) {
	everyWake(ctx, "delivery-site", deliverySiteInterval, nil, true, func(ctx context.Context, _ bool) error {
		return recordDeliverySite(ctx, a, version, uiServed(), time.Now().UTC())
	})
}

// A committed observation at listener readiness also proves database access,
// which is the local coordinator health check. This pass makes no forge calls.
func recordDeliverySite(ctx context.Context, a *App, coordinator, ui string, at time.Time) error {
	if !releaseVersionRe.MatchString(coordinator) || !releaseVersionRe.MatchString(ui) {
		return nil
	}
	at = at.UTC().Truncate(time.Microsecond)
	return a.store.Write(ctx, nil, []string{"project:" + ciProject}, func(q *Request) error {
		d := q.db()
		last, err := d.DeliverySiteLatest(q.ctx())
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !at.After(last.LastSeen) {
			return nil
		}
		coveredFrom := at
		if last.CoveredFrom != nil && at.Sub(last.LastSeen) <= deliverySiteCoverageStep {
			coveredFrom = *last.CoveredFrom
			if coordinator == last.CoordinatorVersion && ui == last.UiVersion {
				return d.DeliverySiteExtend(q.ctx(), db.DeliverySiteExtendParams{FirstSeen: last.FirstSeen, LastSeen: at})
			}
		}
		return d.DeliverySiteStart(q.ctx(), db.DeliverySiteStartParams{
			FirstSeen: at, CoveredFrom: &coveredFrom, CoordinatorVersion: coordinator, UiVersion: ui, ProjectID: ciProject,
		})
	})
}

type deliverySiteEvidence struct {
	observations []db.DeliverySiteObservation
	gaps         []db.DeliverySiteObservation
	firstUI      map[string]time.Time
	legacyUI     map[string]string
}

func readDeliverySiteEvidence(q *Request) (deliverySiteEvidence, error) {
	rows, err := q.db().DeliverySiteObservations(q.ctx())
	if err != nil {
		return deliverySiteEvidence{}, err
	}
	ui, err := q.db().DeliveryUISeen(q.ctx())
	if err != nil {
		return deliverySiteEvidence{}, err
	}
	legacyUI := make(map[string]string, len(ui))
	for _, sighting := range ui {
		legacyUI[sighting.Version] = sighting.SeenAt
	}
	return deliverySiteEvidenceOf(rows, legacyUI), nil
}

func deliverySiteEvidenceOf(rows []db.DeliverySiteObservation, legacyUI map[string]string) deliverySiteEvidence {
	e := deliverySiteEvidence{legacyUI: legacyUI, firstUI: map[string]time.Time{}}
	for _, row := range rows {
		if row.CoveredFrom == nil {
			e.gaps = append(e.gaps, row)
			continue
		}
		e.observations = append(e.observations, row)
		if e.firstUI[row.UiVersion].IsZero() {
			e.firstUI[row.UiVersion] = row.FirstSeen
		}
	}
	return e
}

// Only add a pending wait when sightings cover its publication and still
// cover the present. A wait that started before a gap has an unknown duration.
func (e deliverySiteEvidence) pendingFrom(now time.Time, installsFrom string) string {
	if len(e.observations) == 0 {
		return ""
	}
	last := e.observations[len(e.observations)-1]
	if now.Sub(last.LastSeen) > deliverySiteCoverageStep {
		return ""
	}
	return max(installsFrom, last.CoveredFrom.UTC().Format(time.RFC3339))
}

// reconcile dates each release from the ordered observations.
// The historical gap rows mark intervals where the prior collector lost local
// evidence. A later sighting after such a gap cannot date first availability.
func (e deliverySiteEvidence) reconcile(r *contract.DeliveryRelease) {
	published, err := time.Parse(time.RFC3339, r.Published)
	if err != nil || !deliveryReleaseTag(r.Tag) {
		return
	}
	v := releaseVersionOf(r.Tag)
	const coordinator = "coordinator"
	exact := r.Installed[coordinator]
	if r.InstalledVersions[coordinator] != v || msBetween(&r.Published, &exact) == nil {
		exact = ""
	}
	available, availableVersion := r.Available[coordinator], r.AvailabilityVersions[coordinator]
	if availableVersion == "" || versionLess(availableVersion, v) || msBetween(&r.Published, &available) == nil {
		available, availableVersion = "", ""
	}
	uiAt := deref(r.UiOnSite)
	for _, o := range e.gaps {
		if !published.Before(o.LastSeen) {
			continue
		}
		from := o.FirstSeen.UTC().Format(time.RFC3339)
		if available >= from {
			available, availableVersion = "", ""
		}
		if uiAt >= from {
			uiAt = ""
		}
	}
	// A recorded exact install is independent of the gap in same-or-newer evidence.
	if exact != "" && (available == "" || exact < available) {
		available, availableVersion = exact, v
	}
	// The old durable UI table supplies first sightings of that exact UI
	// even for releases absent from the history during the forge outage.
	uiOnly := uiOnlyRelease(r.Tag)
	if at := e.legacyUI[v]; at != "" {
		if at < r.Published && (uiOnly || at >= published.Add(-time.Minute).UTC().Format(time.RFC3339)) {
			at = r.Published // the UI's own identity can predate publication only by clock skew
		}
		if at >= r.Published && (uiAt == "" || at < uiAt) {
			uiAt = at
		}
	}
	// A UI-only version is unique to its publication. An exact sighting
	// before it proves the site already served the release at publication.
	if firstUI := e.firstUI[v]; uiOnly && !firstUI.IsZero() && firstUI.Before(published) {
		uiAt = r.Published
	}
	skew := published.Add(-time.Minute)
	start := sort.Search(len(e.observations), func(i int) bool {
		return !e.observations[i].LastSeen.Before(skew)
	})
	for _, o := range e.observations[start:] {
		if o.CoveredFrom.UTC().Truncate(time.Second).After(published) {
			break
		}
		if o.LastSeen.UTC().Truncate(time.Second).Before(published) {
			if !o.FirstSeen.Before(skew) && !versionLess(o.UiVersion, v) {
				uiAt = r.Published // retain the delivery UI clock-skew allowance
			}
			continue
		}
		at := max(r.Published, o.FirstSeen.UTC().Format(time.RFC3339))
		if !versionLess(o.UiVersion, v) && (uiAt == "" || at < uiAt) {
			uiAt = at
		}
		if uiOnly && uiAt != "" {
			break
		}
		if uiOnly || versionLess(o.CoordinatorVersion, v) {
			continue
		}
		if available == "" || at < available {
			available, availableVersion = at, o.CoordinatorVersion
		}
		if o.CoordinatorVersion == v && r.Installed[coordinator] == "" {
			if r.Installed == nil {
				r.Installed = map[string]string{}
			}
			if r.InstalledVersions == nil {
				r.InstalledVersions = map[string]string{}
			}
			r.Installed[coordinator], r.InstalledVersions[coordinator] = at, v
		}
		if uiAt != "" && r.Installed[coordinator] != "" {
			break
		}
	}
	r.UiOnSite = optStr(uiAt)
	if available != "" && !uiOnly {
		if r.Available == nil {
			r.Available = map[string]string{}
		}
		if r.AvailabilityVersions == nil {
			r.AvailabilityVersions = map[string]string{}
		}
		r.Available[coordinator], r.AvailabilityVersions[coordinator] = available, availableVersion
	} else {
		delete(r.Available, coordinator)
		delete(r.AvailabilityVersions, coordinator)
	}
}
