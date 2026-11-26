package main

// Host inventory (docs/designs/fleet-host-cleanup.md sections 10 and 11): once an hour each host sends the session
// directories that still hold a worktree, with its disk reading. One read classifies the whole batch; one write queues
// the teardowns of settled sessions and keeps the summary for hosts_list (disk) and the leftovers rule. A session the
// coordinator has no live row for, or whose work moved to another host, is answered unknown: the host tears it down
// itself.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	hostRoute(contract.RouteInventory, hostInventory)
}

// inventorySummary: the host's latest inventory as hosts_list shows it (HostDiskReading) plus the ids the leftovers
// rule compares between two inventories.
type inventorySummary struct {
	contract.HostDiskReading
	Leftovers []string         `json:"leftovers,omitempty"` // teardown or unknown in this inventory
	Repeated  int64            `json:"repeated"`            // of them, also leftovers in the previous inventory
	Kept      int64            `json:"kept"`                // settled sessions whose teardown kept the worktree (keepWhen)
	Personal  map[string]int64 `json:"personal,omitempty"`  // personal projects: worktrees still holding output/
}

// inventoryVerdict: what the coordinator answers for one session of the batch.
type inventoryVerdict int

const (
	inventoryKeep inventoryVerdict = iota
	inventoryTeardown
	inventoryUnknown
	inventoryKept // settled, but an earlier teardown kept it (keepWhen): left alone, counted
)

// classifyInventory: the verdict for one session row (InventorySessions).
func classifyInventory(r db.InventorySessionsRow) inventoryVerdict {
	switch {
	case !r.Known:
		return inventoryUnknown
	case r.Live || r.Busy || r.Asking || r.Uploading:
		return inventoryKeep
	case !(r.Archived || r.Pipeline || r.Accepted):
		return inventoryKeep
	case r.Torn:
		return inventoryKept
	}
	return inventoryTeardown
}

func hostInventory(q *Request, b *contract.HostInventoryRequest) (contract.HostInventoryReply, error) {
	host := hostOf(q)
	out := contract.HostInventoryReply{Teardown: []string{}, Unknown: []string{}}
	ids := make([]string, 0, len(b.Sessions))
	bytes := map[string]float64{}
	output := map[string]bool{}
	for _, s := range b.Sessions {
		if s.Id == "" || len(ids) >= 5000 {
			continue
		}
		ids = append(ids, s.Id)
		bytes[s.Id] = s.Bytes
		output[s.Id] = s.Output != nil && *s.Output
	}
	var rows []db.InventorySessionsRow
	if err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		var err error
		rows, err = q.db().InventorySessions(q.ctx(), ids)
		return err
	}); err != nil {
		return out, err
	}
	summary := inventorySummary{HostDiskReading: contract.HostDiskReading{At: float64(nowMs()), Projects: []contract.HostDiskProject{}}}
	if b.Disk != nil {
		summary.Disk = *b.Disk
	}
	merged := mergedSessionIDs()
	for i := range rows {
		rows[i].Accepted = rows[i].Accepted || merged[rows[i].ID] // delivered: its task accepted or its pull request merged
	}
	projects := map[string]*contract.HostDiskProject{}
	var queue []db.InventorySessionsRow
	for _, r := range rows {
		p := projects[r.ProjectID]
		if p == nil {
			p = &contract.HostDiskProject{ProjectId: r.ProjectID}
			projects[r.ProjectID] = p
		}
		p.Bytes += bytes[r.ID]
		p.Worktrees++
		switch classifyInventory(r) {
		case inventoryUnknown:
			out.Unknown = append(out.Unknown, r.ID)
		case inventoryTeardown:
			queue = append(queue, r)
		case inventoryKept:
			summary.Kept++
		default:
			out.Keep++
		}
	}
	err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
		var prev inventorySummary
		if r, err := q.db().HostObservationOf(q.ctx(), host); err == nil {
			var p struct {
				Inventory inventorySummary `json:"inventory"`
			}
			json.Unmarshal(r.Payload, &p)
			prev = p.Inventory
		}
		for _, r := range queue {
			queued, err := inventoryTeardownOf(q, host, r)
			if err != nil {
				return err
			}
			if queued {
				out.Teardown = append(out.Teardown, r.ID)
			} else {
				out.Unknown = append(out.Unknown, r.ID) // its last run was elsewhere: no command reaches this copy
			}
		}
		summary.Keep, summary.Teardown, summary.Unknown = out.Keep, int64(len(out.Teardown)), int64(len(out.Unknown))
		for _, id := range append(slices.Clone(out.Teardown), out.Unknown...) {
			summary.Leftovers = append(summary.Leftovers, id)
			if summary.LeftoverBytes == nil {
				summary.LeftoverBytes = new(float64)
			}
			*summary.LeftoverBytes += bytes[id]
			if slices.Contains(prev.Leftovers, id) {
				summary.Repeated++
			}
		}
		personal := map[string]bool{}
		for id := range projects {
			if id != "" {
				personal[id] = projectPersonal(q, id)
			}
		}
		for _, r := range rows {
			if output[r.ID] && personal[r.ProjectID] {
				if summary.Personal == nil {
					summary.Personal = map[string]int64{}
				}
				summary.Personal[r.ProjectID]++
			}
		}
		for _, p := range projects {
			summary.Projects = append(summary.Projects, *p)
		}
		slices.SortFunc(summary.Projects, func(a, b contract.HostDiskProject) int {
			switch {
			case a.Bytes > b.Bytes:
				return -1
			case a.Bytes < b.Bytes:
				return 1
			}
			return 0
		})
		return q.db().HostInventorySet(q.ctx(), db.HostInventorySetParams{ID: host, Inventory: jsonBytes(summary)})
	})
	return out, err
}

// inventoryTeardownOf queues the teardown of a settled session on this host; false when its last run was on another
// host (or none), so no command reaches this copy.
func inventoryTeardownOf(q *Request, host string, r db.InventorySessionsRow) (bool, error) {
	s, err := sessionByID(q, r.ID)
	if err != nil {
		return false, err
	}
	if pending, err := teardownPending(q, r.ID); err != nil || len(pending) > 0 {
		return err == nil, err
	}
	es, err := agentExecutions(q, r.ID)
	if err != nil {
		return false, err
	}
	last := lastOnHost(es)
	if last == nil || deref(last.HostID) != host {
		return false, nil
	}
	t, err := teardownCommandWith(q, s, *last, teardownFlags{deleteBranch: r.Accepted || r.Pipeline, outputsStored: r.OutputsStored != nil && *r.OutputsStored, step: r.Pipeline})
	_, queued := t.(agentCommand)
	return queued, err
}

// projectPersonal: the project's settings say data: personal (fleet-project-access.md#personal-data).
func projectPersonal(q *Request, id string) bool {
	set, err := projectSettingsByID(q, id)
	return err == nil && set.text("data") == "personal"
}

// The Idle archive (docs/designs/fleet-host-cleanup.md#10-worktree-lifecycle-for-every-run-kind): every hour, project
// chats without a task that saw no turn, message or run for FLEET_SESSIONS_IDLE_ARCHIVE_DAYS (7) are archived by Fleet,
// as session_archive does it, so their worktrees go (keepWhen still keeps unpushed work); unarchive brings them back.
func init() {
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "idle-archive", envMs("FLEET_IDLE_ARCHIVE_TICK_MS", 3_600_000), func(ctx context.Context) error {
			return idleArchivePass(ctx, a)
		})
	})
}

func idleArchivePass(ctx context.Context, a *App) error {
	days, err := strconv.Atoi(envOr("FLEET_SESSIONS_IDLE_ARCHIVE_DAYS", "7"))
	if err != nil || days < 1 {
		days = 7
	}
	before := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var ids []string
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		var err error
		ids, err = q.db().IdleSessionsToArchive(q.ctx(), db.IdleSessionsToArchiveParams{Before: &before, Lim: 100})
		return err
	}); err != nil {
		return err
	}
	for _, id := range ids {
		err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
			_, err := sessionArchive(q, id)
			return err
		})
		if he := (*HTTPError)(nil); errors.As(err, &he) {
			continue // became active in between (409): left for the next pass
		} else if err != nil {
			return err
		}
	}
	return nil
}
