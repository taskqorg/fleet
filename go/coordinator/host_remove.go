// Host remove and purge (docs/designs/fleet-system-management.md, Hosts; Archive, delete, purge). A removed host keeps
// its record as a tombstone (state removed) so history stays attributable; its secret hash moves to removedSecretHash
// so a poll with the old secret is answered 410 host-removed and the executor stops. system_purge deletes a removed
// host's record once nothing references it.
package main

import (
	"fmt"

	"fleet/contract"
)

func init() {
	operation("hosts_remove", false, typedOp(hostsRemove))
	operation("system_purge", false, ownerFirst("Only the owner purges", typedOp(systemPurge)))
}

func hostRemoved(h doc) bool { return astr(h["state"]) == "removed" }

// hostRemoveCounts: what removing host id touches; the preview and the confirm run the same queries.
func hostRemoveCounts(q *Request, id string) (contract.HostsRemoveReply, error) {
	r := contract.HostsRemoveReply{Id: id}
	row, err := first(q, `SELECT
	(SELECT count(*) FROM execution WHERE host_id=$1 AND state IN ('preparing','running','waiting'))::int AS running,
	(SELECT count(*) FROM document WHERE kind='session' AND data->>'hostId'=$1 AND data->>'archivedAt' IS NULL)::int AS sessions,
	(SELECT count(DISTINCT c.artifact_id) FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE s.host_id=$1 AND c.state='verified'
	  AND NOT EXISTS (SELECT 1 FROM artifact_copy o JOIN store t ON t.id=o.store_id WHERE o.artifact_id=c.artifact_id AND o.state='verified' AND t.host_id<>$1))::int AS lonely`, id)
	if err != nil {
		return r, err
	}
	r.RunningExecutions, r.OpenSessions, r.UnreplicatedArtifacts = int64(num(row["running"])), int64(num(row["sessions"])), int64(num(row["lonely"]))
	return r, nil
}

func hostsRemove(q *Request, b *contract.HostsRemoveRequest) (contract.HostsRemoveReply, error) {
	var none contract.HostsRemoveReply
	h, err := first(q, "SELECT data FROM document WHERE kind='host' AND id='host:'||$1", b.Id)
	if err != nil {
		return none, err
	}
	if h == nil {
		return none, fail(404, "Unknown host")
	}
	h = mapOf(h["data"])
	if q.person == nil || (!fullAccess(q.person) && q.person.Email != h["holder"]) {
		return none, fail(403, "Only the holder or the owner removes this host")
	}
	if hostRemoved(h) {
		return none, fail(409, "Host already removed")
	}
	r, err := hostRemoveCounts(q, b.Id)
	if err != nil || b.Preview {
		return r, err
	}
	// Tombstone first: lists, dispatch and the rules stop seeing the host, and stops settle here (knownHost is false).
	h["state"], h["removedAt"], h["removedSecretHash"] = "removed", nowMs(), h["secretHash"]
	delete(h, "secretHash")
	delete(h, "harnesses")
	if err := put(q, "host", h); err != nil {
		return none, err
	}
	if err := exec(q, "DELETE FROM host_setting WHERE host_id=$1", b.Id); err != nil {
		return none, err
	}
	runs, err := queryRows(q, "SELECT id FROM execution WHERE host_id=$1 AND state IN ('preparing','running','waiting') ORDER BY seq", b.Id)
	if err != nil {
		return none, err
	}
	for _, x := range runs {
		if _, err := executionStop(q, astr(x["id"])); err != nil {
			return none, fmt.Errorf("stop %v: %w", x["id"], err)
		}
		e, err := get(q, x["id"], "execution")
		if err != nil {
			return none, err
		}
		e["stopDetail"] = "host-removed"
		if err := put(q, "execution", e); err != nil {
			return none, err
		}
	}
	// Commands the host will never fetch (teardown, readiness, stops of finished runs) are settled.
	if err := exec(q, "UPDATE document SET data=data||'{\"status\":\"stopped\",\"stopDetail\":\"host-removed\"}', updated_at=now() WHERE kind='command' AND data->>'hostId'=$1 AND data->>'status'='pending'", b.Id); err != nil {
		return none, err
	}
	// Artifacts whose only verified copy was on this host's stores are lost (data foundation tombstone rules): the
	// artifact fails with host-removed, then the copies there are deleted.
	if err := exec(q, `UPDATE artifact a SET state='failed', last_error='host-removed' WHERE a.state='stored'
	AND EXISTS (SELECT 1 FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE c.artifact_id=a.id AND c.state='verified' AND s.host_id=$1)
	AND NOT EXISTS (SELECT 1 FROM artifact_copy o JOIN store t ON t.id=o.store_id WHERE o.artifact_id=a.id AND o.state='verified' AND t.host_id<>$1)`, b.Id); err != nil {
		return none, err
	}
	if err := exec(q, `UPDATE artifact_copy c SET state='deleted', last_error='host-removed' FROM store s WHERE s.id=c.store_id AND s.host_id=$1 AND c.state<>'deleted'`, b.Id); err != nil {
		return none, err
	}
	if err := exec(q, "UPDATE store SET state='unreachable' WHERE host_id=$1", b.Id); err != nil {
		return none, err
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "host:" + b.Id, "holder": h["holder"], "field": "state", "previous": "active", "value": "removed",
		"actor": q.person.Email, "at": nowMs(), "note": js(r)}); err != nil {
		return none, err
	}
	r.Removed = true
	return r, nil
}

// purgeableHosts: removed hosts and whether anything still references them. The reference list is every table and
// document field that names a host id; its stores go with it when no artifact copy names them.
func purgeableHosts(q *Request) (gone, kept []string, err error) {
	rows, err := queryRows(q, `SELECT d.data->>'hostId' AS id, (
	EXISTS (SELECT 1 FROM execution e WHERE e.host_id=d.data->>'hostId')
	OR EXISTS (SELECT 1 FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE s.host_id=d.data->>'hostId')
	OR EXISTS (SELECT 1 FROM route_decision r WHERE r.host_id=d.data->>'hostId')
	OR EXISTS (SELECT 1 FROM document x WHERE x.kind IN ('session','command') AND x.data->>'hostId'=d.data->>'hostId')) AS referenced
	FROM document d WHERE d.kind='host' AND d.data->>'state'='removed' ORDER BY d.seq`)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if r["referenced"] == true {
			kept = append(kept, astr(r["id"]))
		} else {
			gone = append(gone, astr(r["id"]))
		}
	}
	return gone, kept, nil
}

func systemPurge(q *Request, b *contract.SystemPurgeRequest) (contract.SystemPurgeReply, error) {
	out := contract.SystemPurgeReply{Removed: []contract.PurgeCount{}, Kept: []contract.PurgeCount{}}
	for _, k := range b.Kinds {
		if k != contract.PurgeKindHost {
			return out, fail(400, "kinds: host")
		}
	}
	gone, kept, err := purgeableHosts(q)
	if err != nil {
		return out, err
	}
	out.Removed = append(out.Removed, contract.PurgeCount{Kind: contract.PurgeKindHost, Count: int64(len(gone)), Ids: nonNilStrs(gone)})
	out.Kept = append(out.Kept, contract.PurgeCount{Kind: contract.PurgeKindHost, Count: int64(len(kept)), Ids: nonNilStrs(kept)})
	if b.Preview || len(gone) == 0 {
		return out, nil
	}
	for _, id := range gone {
		if err := exec(q, "DELETE FROM store WHERE host_id=$1", id); err != nil { // no copy names these stores
			return out, err
		}
		if err := exec(q, "DELETE FROM document WHERE (kind='host' AND id='host:'||$1) OR (kind='host-observation' AND id=$1)", id); err != nil {
			return out, err
		}
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "system", "field": "purge", "value": doc{"host": gone}, "actor": q.person.Email, "at": nowMs()}); err != nil {
		return out, err
	}
	out.Purged = true
	return out, nil
}

func nonNilStrs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
