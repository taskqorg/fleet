// State owners of the resource projection (docs/designs/fleet-standard.md S5, DESIGN_RULES.md): one transition function
// per state machine, the only writer of that machine's state columns, applying the transition's effects in the caller's
// transaction. Retry state (attempts, next_attempt_at, last_error) belongs to the work kit (work_kit.go).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// buildTransitions: the phases (schema.sql, resource_build.phase) a build page may move each phase to. A rebuild starts
// clearing from any phase; ready is terminal for pages. resource_build.ready is generated from phase (migration 76).
var buildTransitions = map[string][]string{
	"clearing": {"clearing", "scanning"},
	"scanning": {"scanning", "draining"},
	"draining": {"draining", "ready"},
	"ready":    nil,
}

// repairTransitions: a metadata repair enumerates its executions page by page (done=false), is then done while its
// seeded work drains, and is finally deleted.
var repairTransitions = map[string][]string{
	"enumerating": {"enumerating", "done"},
	"done":        {"done", "deleted"},
	"deleted":     nil,
}

type buildEvent string

const (
	buildPage    buildEvent = "page"
	buildRebuild buildEvent = "rebuild"
)

// buildTransition is the only writer of a build's phase, scan_done, after_execution_id and generation and of its
// summary's ready and epoch. The caller holds the project lock. A page clears the disposable projection in bounded key
// pages (source work and frozen valuations survive), seeds one page of at most 100 source executions, or, with no
// scoped work or unresolved metadata coverage left, makes the build ready and publishes. A rebuild starts a new
// generation and epoch at clearing; the epoch stays preparing until clearing, scan and drain complete.
func buildTransition(q *Request, p string, event buildEvent) error {
	if err := resourceEnsureProject(q, p); err != nil {
		return err
	}
	d, ctx := q.db(), q.ctx()
	b, err := d.ResourceBuildLock(ctx, p)
	if err != nil {
		return err
	}
	if event == buildRebuild {
		if err := d.ResourceBuildRestart(ctx, p); err != nil {
			return err
		}
		return d.ResourceSummaryNewEpoch(ctx, p)
	}
	phase, after := b.Phase, b.AfterExecutionID
	to := func(next string, after *string) error {
		if !slices.Contains(buildTransitions[phase], next) {
			return fmt.Errorf("resource build of %s cannot move from %s to %s", p, phase, next)
		}
		return d.ResourceBuildMove(ctx, db.ResourceBuildMoveParams{ProjectID: p, Phase: next, ScanDone: next == "draining" || next == "ready", AfterExecutionID: after})
	}
	switch phase {
	case "ready":
		return nil
	case "clearing":
		var removed int64
		for _, clear := range []func(context.Context, string) (int64, error){d.ResourceClearContributions, d.ResourceClearMetrics, d.ResourceClearExtremes, d.ResourceClearTools, d.ResourceClearCosts, d.ResourceClearPriceRefs} {
			n, err := clear(ctx, p)
			if err != nil {
				return err
			}
			removed += n
		}
		if removed > 0 {
			return to("clearing", after)
		}
		if err := d.ResourceSummaryClear(ctx, p); err != nil {
			return err
		}
		return to("scanning", nil)
	case "scanning":
		ids, err := d.ResourceScanPage(ctx, db.ResourceScanPageParams{ProjectID: p, After: deref(after)})
		if err != nil {
			return err
		}
		if err = resourceSeedWork(q, ids, "build", &b.Generation, nil); err != nil {
			return err
		}
		if len(ids) == 0 {
			return to("draining", after)
		}
		return to("scanning", &ids[len(ids)-1])
	}
	pending, err := d.ResourceProgress(ctx, p)
	if err != nil {
		return err
	}
	if pending != nil && *pending {
		return to("draining", after)
	}
	if err := to("ready", after); err != nil {
		return err
	}
	if err := d.ResourceSummaryReady(ctx, p); err != nil {
		return err
	}
	return resourcePublish(q, p)
}

// repairTransition is the only writer of a metadata repair's done and after_execution_id: one atomic union page over
// the normalized old/new connection names and full account IDs; the repair keeps its scopes until enumeration and all
// its seeded work drain.
func repairTransition(q *Request, repair string) error {
	d, ctx := q.db(), q.ctx()
	r, err := d.ResourceRepairLock(ctx, repair)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Done { // done -> deleted once every seeded work UUID has drained
		return d.ResourceRepairDelete(ctx, repair)
	}
	accounts, connections := []string{}, []string{}
	for _, raw := range [][]byte{r.OldClass, r.NewClass} {
		var c struct {
			AccountID      *string `json:"accountId"`
			ConnectionName *string `json:"connectionName"`
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if c.AccountID != nil {
			accounts = append(accounts, *c.AccountID)
		}
		if c.ConnectionName != nil {
			connections = append(connections, *c.ConnectionName)
		}
	}
	after := r.AfterExecutionID
	ids, err := d.ResourceRepairPage(ctx, db.ResourceRepairPageParams{Accounts: accounts, Connections: connections, After: deref(after)})
	if err != nil {
		return err
	}
	if err = resourceSeedWork(q, ids, "metadata", nil, &repair); err != nil {
		return err
	}
	if err = d.ResourceRepairScopes(ctx, repair); err != nil {
		return err
	}
	if len(ids) > 0 {
		after = &ids[len(ids)-1]
	}
	return d.ResourceRepairMove(ctx, db.ResourceRepairMoveParams{RepairID: repair, After: after, Done: len(ids) == 0})
}
