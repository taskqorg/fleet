// Host remove and purge (docs/designs/fleet-system-management.md, Hosts; Archive, delete, purge). A removed host keeps
// its record as a tombstone (state removed) so history stays attributable; its secret hash moves to removedSecretHash
// so a poll with the old secret is answered 410 host-removed and the executor stops. system_purge (cleanup.go) deletes a
// removed host's record once nothing references it.
package main

import (
	"fmt"

	"fleet/contract"

	"fleet/coordinator/db"
)

func init() {
	op("hosts_remove", hostsRemove)
}

// hostRemoveCounts: what removing host id touches; the preview and the confirm run the same queries.
func hostRemoveCounts(q *Request, id string) (contract.HostsRemoveReply, error) {
	r := contract.HostsRemoveReply{Id: id}
	c, err := q.db().HostRemoveCounts(q.ctx(), id)
	if err != nil {
		return r, err
	}
	r.RunningExecutions, r.OpenSessions, r.UnreplicatedArtifacts = c.Running, c.Sessions, c.Lonely
	return r, nil
}

func hostsRemove(q *Request, b *contract.HostsRemoveRequest) (contract.HostsRemoveReply, error) {
	var none contract.HostsRemoveReply
	h, err := hostRecordByID(q, b.Id)
	if err != nil {
		return none, err
	}
	if h == nil {
		return none, fail(404, "Unknown host")
	}
	if q.person == nil || (!fullAccess(q.person) && (h.Holder == nil || q.person.Email != *h.Holder)) {
		return none, fail(403, "Only the holder or the owner removes this host")
	}
	if h.removed() {
		return none, fail(409, "Host already removed")
	}
	r, err := hostRemoveCounts(q, b.Id)
	if err != nil || b.Preview {
		return r, err
	}
	// Tombstone first: lists, dispatch and the rules stop seeing the host, and stops settle here (knownHost is false).
	if err := q.db().HostTombstone(q.ctx(), db.HostTombstoneParams{ID: "host:" + b.Id, RemovedAt: nowMs()}); err != nil {
		return none, err
	}
	if err := hostTransition(q, "host:"+b.Id, "removed"); err != nil {
		return none, err
	}
	if err := q.db().DeleteHostSettings(q.ctx(), b.Id); err != nil {
		return none, err
	}
	runs, err := q.db().OpenExecutionsOnHost(q.ctx(), &b.Id)
	if err != nil {
		return none, err
	}
	for _, x := range runs {
		if _, err := executionStop(q, x); err != nil {
			return none, fmt.Errorf("stop %v: %w", x, err)
		}
		e, err := getExecution(q, x)
		if err != nil {
			return none, err
		}
		e.StopDetail = "host-removed"
		if err := executionPut(q, &e); err != nil {
			return none, err
		}
	}
	// A removed host polls no more: its poll-failure problem goes with it (host_poll_health.go).
	// Unscoped: a holder removing their own host cannot see the owner's question under row-level security.
	if _, err := unscoped(q, func() (struct{}, error) { return struct{}{}, problemClose(q, hostPollCause(b.Id)) }); err != nil {
		return none, err
	}
	// Commands the host will never fetch (teardown, readiness, stops of finished runs) are settled.
	removedDetail := "host-removed"
	if _, err := commandStop(q, commandStopFilter{hostID: &b.Id, statuses: []db.CommandStatus{db.CommandStatusPending}, detail: &removedDetail}); err != nil {
		return none, err
	}
	// Artifacts whose only verified copy was on this host's stores are lost (data foundation tombstone rules): the
	// artifact fails with host-removed, then the copies there are deleted.
	if err := q.db().FailArtifactsOnlyOnHost(q.ctx(), b.Id); err != nil {
		return none, err
	}
	if err := artifactCopiesDeleteOnHost(q, b.Id); err != nil {
		return none, err
	}
	if err := storesHostUnreachable(q, b.Id); err != nil {
		return none, err
	}
	if err := audit(q, "host:"+b.Id, "state", "active", "removed", auditExtra{Holder: deref(h.Holder), Note: js(r)}); err != nil {
		return none, err
	}
	afterCommit(q, func() {
		if q.app != nil {
			q.app.clearHostCache(b.Id)
			q.app.wakeDispatch()
		}
	})
	r.Removed = true
	return r, nil
}

// purgeableHosts: removed hosts and whether anything still references them. The reference list is every table and
// document field that names a host id; its stores go with it when only deleted copies name them.
func purgeableHosts(q *Request) (gone []string, kept []contract.PurgeReason, err error) {
	rows, err := q.db().PurgeableHosts(q.ctx())
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if r.Referenced != "" {
			kept = append(kept, contract.PurgeReason{Id: r.HostID, Reason: r.Referenced + " (a removed host stays while any row names it)"})
		} else {
			gone = append(gone, r.HostID)
		}
	}
	return gone, kept, nil
}

// purgeHosts: the host part of system_purge, in the caller's transaction.
func purgeHosts(q *Request, preview bool) (removed, kept contract.PurgeCount, err error) {
	gone, held, err := purgeableHosts(q)
	if err != nil {
		return removed, kept, err
	}
	removed = contract.PurgeCount{Kind: contract.PurgeKindHost, Count: int64(len(gone)), Ids: nonNilStrs(gone)}
	kept = keptCount(contract.PurgeKindHost, held)
	if preview || len(gone) == 0 {
		return removed, kept, nil
	}
	for _, id := range gone {
		// Only deleted copies (the ones hosts_remove dropped) still name these stores; they go with them.
		if err := q.db().DeleteDeletedCopiesOfHost(q.ctx(), id); err != nil {
			return removed, kept, err
		}
		if err := q.db().DeleteStoresOfHost(q.ctx(), id); err != nil {
			return removed, kept, err
		}
		if err := q.db().DeleteHost(q.ctx(), id); err != nil {
			return removed, kept, err
		}
		hid := id
		afterCommit(q, func() {
			if q.app != nil {
				q.app.clearHostCache(hid)
			}
		})
	}
	purged := struct {
		Host []string `json:"host"`
	}{gone}
	err = audit(q, "system", "purge", nil, purged, auditExtra{})
	return removed, kept, err
}

func nonNilStrs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
