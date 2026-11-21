// Artifact copy and store state machines (docs/designs/fleet-standard.md S5, DESIGN_RULES.md):
// artifact_states.go is the only writer of artifact_copy.state and store.state.
package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// artifactCopyTransitions: the states (schema.sql, artifact_copy.state) an artifact copy may move each state to.
// A deleted copy can be started again if the store returns to a still-stored artifact's locations.
var artifactCopyTransitions = map[string][]string{
	"pending":       {"verified", "failed", "purge_pending", "deleted"},
	"copying":       {"verified", "failed", "purge_pending", "deleted"},
	"verified":      {"purge_pending", "deleted"},
	"failed":        {"copying", "purge_pending", "deleted"},
	"purge_pending": {"deleted"},
	"deleted":       {"copying"},
}

// storeTransitions: the states (schema.sql, store.state) a store may move each state to. An unreachable store
// returns to reachable or full when its host reports again or re-registers it.
var storeTransitions = map[string][]string{
	"reachable":   {"reachable", "full", "unreachable"},
	"full":        {"full", "reachable", "unreachable"},
	"unreachable": {"unreachable", "reachable", "full"},
}

type artifactCopyEvent struct {
	To            string
	Why           *string
	RetryWindowMs float64
}

func artifactCopyInsertPending(ctx context.Context, d *db.Queries, p db.ArtCopyInsertPendingParams) error {
	return d.ArtCopyInsertPending(ctx, p)
}

func artifactCopyInsertVerified(ctx context.Context, d *db.Queries, artifactID, storeID string, projectID *string) error {
	return d.ArtCopyInsertVerified(ctx, db.ArtCopyInsertVerifiedParams{ArtifactID: artifactID, StoreID: storeID, ProjectID: projectID})
}

func artifactCopyStart(ctx context.Context, d *db.Queries, p db.DispCopyStartParams) error {
	cur, err := d.ArtCopyStateForUpdate(ctx, db.ArtCopyStateForUpdateParams{ArtifactID: p.ArtifactID, StoreID: p.StoreID})
	if err == nil {
		if !slices.Contains(artifactCopyTransitions[cur], "copying") {
			return fail(409, fmt.Sprintf("Artifact copy %s cannot become copying", cur))
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return d.DispCopyStart(ctx, p)
}

func artifactCopyTransition(ctx context.Context, d *db.Queries, artifactID, storeID string, ev artifactCopyEvent) error {
	cur, err := d.ArtCopyStateForUpdate(ctx, db.ArtCopyStateForUpdateParams{ArtifactID: artifactID, StoreID: storeID})
	if errors.Is(err, pgx.ErrNoRows) {
		if ev.To == "deleted" || ev.To == "purge_pending" {
			return nil
		}
		return errNotFound
	}
	if err != nil {
		return err
	}
	if cur == ev.To {
		return nil
	}
	if !slices.Contains(artifactCopyTransitions[cur], ev.To) {
		return fail(409, fmt.Sprintf("Artifact copy %s cannot become %s", cur, ev.To))
	}
	switch ev.To {
	case "verified":
		return d.ArtCopyVerified(ctx, db.ArtCopyVerifiedParams{ArtifactID: artifactID, StoreID: storeID})
	case "failed":
		return d.ArtCopyReject(ctx, db.ArtCopyRejectParams{ArtifactID: artifactID, StoreID: storeID, Why: ev.Why, RetryWindowMs: ev.RetryWindowMs})
	case "purge_pending", "deleted":
		return d.ArtCopySetState(ctx, db.ArtCopySetStateParams{ArtifactID: artifactID, StoreID: storeID, State: ev.To})
	default:
		return fail(400, "Unknown artifact copy state "+ev.To)
	}
}

func artifactCopiesDeleteOnHost(q *Request, hostID string) error {
	return q.db().DeleteCopiesOnHost(q.ctx(), hostID)
}

type storeEvent struct {
	To           string
	SetWarning   bool
	WarningBytes *int64
}

func storeUpsert(ctx context.Context, d *db.Queries, p db.ArtStoreUpsertParams) error {
	return d.ArtStoreUpsert(ctx, p)
}

func storeObserve(ctx context.Context, d *db.Queries, p db.ArtStoreObserveParams) error {
	return d.ArtStoreObserve(ctx, p)
}

func storeTransition(q *Request, id string, ev storeEvent) (string, error) {
	cur, err := q.db().ArtStoreLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fail(404, "Unknown store "+id)
	}
	if err != nil {
		return "", err
	}
	to := ev.To
	if ev.SetWarning {
		to = "reachable"
		if cur.State == "unreachable" {
			to = "unreachable"
		} else if ev.WarningBytes != nil && cur.CapacityBytes != nil && *cur.CapacityBytes < *ev.WarningBytes {
			to = "full"
		}
	}
	if !slices.Contains(storeTransitions[cur.State], to) {
		return "", fail(409, fmt.Sprintf("Store %s cannot move from %s to %s", id, cur.State, to))
	}
	if err := q.db().ArtStoreMove(q.ctx(), db.ArtStoreMoveParams{ID: id, SetWarning: ev.SetWarning, Bytes: ev.WarningBytes, State: to}); err != nil {
		return "", err
	}
	return to, nil
}

func storesSweepUnreachable(ctx context.Context, d *db.Queries, unseenMS int64) error {
	return d.ArtStoresSweep(ctx, unseenMS)
}

func storesHostUnreachable(q *Request, hostID string) error {
	return q.db().StoresOfHostUnreachable(q.ctx(), hostID)
}
