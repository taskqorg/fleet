// Host update history (docs/designs/fleet-host-updates.md, "What is recorded"): every change of the update result a
// host reports on its poll becomes a host_update row with the component versions that release pins for the host, and a
// switch to a new release is journalled (audit). host_update_history reads it newest first, one page per request.
package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/harnessrelease"
)

const (
	hostUpdateLimit    = 30
	hostUpdateMaxLimit = 100
)

func init() {
	op("host_update_history", hostUpdateHistory)
}

// hostUpdatePayload: the payload column of a host_update row.
type hostUpdatePayload struct {
	Components map[string]string `json:"components,omitempty"` // kind/name to version, as the release pins it
	Brought    map[string]string `json:"brought,omitempty"`    // those that differ from the host's last switched release
}

// releaseComponents: the component versions (cli/codex, tool/node) this coordinator's release pins for a target, enabled
// kinds only; nil for an unknown target.
func releaseComponents(target string) map[string]string {
	if target == "" {
		return nil
	}
	out := map[string]string{}
	for _, k := range harnessrelease.Kinds {
		enabled, versions, err := k.Pinned(releaseDir())
		if err != nil || !enabled {
			continue
		}
		for name, v := range versions[target] {
			out[k.Name+"/"+name] = v
		}
	}
	return out
}

// hostUpdateComponents: what the reported release pins for the host. The coordinator knows its own release's pins (the
// release hosts install after PDS18); a host running the reported release states its CLI pins itself (harnessVersions
// expected). Otherwise unknown (nil).
func hostUpdateComponents(r contract.UpdateResult, platform string, running *string, harnesses []contract.HarnessVersion) map[string]string {
	if r.Version != "" && r.Version == ownRelease().Version {
		if c := releaseComponents(pinsTargetOf(platform)); len(c) > 0 {
			return c
		}
	}
	if running != nil && *running == r.Version && len(harnesses) > 0 {
		out := map[string]string{}
		for _, h := range harnesses {
			if h.Expected != "" {
				out["cli/"+h.Harness] = h.Expected
			}
		}
		return out
	}
	return nil
}

// componentsBrought: the components whose version differs from base (every one when there is no base).
func componentsBrought(components, base map[string]string) map[string]string {
	out := map[string]string{}
	for c, v := range components {
		if base == nil || base[c] != v {
			out[c] = v
		}
	}
	return out
}

// hostUpdateRecord writes the reported update result as a row when it differs from the host's newest one (version, state
// and reason; a repeated result writes nothing, so a row keeps the time it was first reported), and journals a switch to
// a new release.
func hostUpdateRecord(q *Request, host, holder, platform string, running *string, harnesses []contract.HarnessVersion, r contract.UpdateResult) error {
	if host == "" || r.Version == "" {
		return nil
	}
	reason := deref(r.Error)
	last, err := q.db().HostUpdateLast(q.ctx(), host)
	if err == nil && last.Version == r.Version && last.UpdateState == string(r.State) && last.Reason == reason {
		return nil
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var prev *string
	if sw, err := q.db().HostUpdateLastSwitch(q.ctx(), host); err == nil {
		prev = &sw
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// The base is the newest switch whose components are known: a switch with unknown components says nothing changed.
	var base map[string]string
	if b, err := q.db().HostUpdateLastPins(q.ctx(), host); err == nil {
		var p hostUpdatePayload
		if err := lenientDecode(b, &p); err != nil {
			return err
		}
		base = p.Components
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	p := hostUpdatePayload{Components: hostUpdateComponents(r, platform, running, harnesses)}
	if len(p.Components) > 0 {
		p.Brought = componentsBrought(p.Components, base)
	}
	at, err := time.Parse(time.RFC3339, r.At)
	if err != nil {
		at = time.Now()
	}
	if err := q.db().HostUpdateInsert(q.ctx(), db.HostUpdateInsertParams{HostID: host, Holder: optStr(holder), Version: r.Version,
		UpdateState: string(r.State), Reason: reason, At: at, Payload: jsonBytes(p)}); err != nil {
		return err
	}
	// The first row starts the history; only a later ok result for another release is a switch.
	if r.State != contract.UpdateStateOk || prev == nil || *prev == r.Version {
		return nil
	}
	return audit(q, "host:"+host, "release", *prev, r.Version, auditExtra{Holder: holder, Note: componentsText(p.Brought)})
}

// componentsSorted: a component map as the contract's list, by component.
func componentsSorted(m map[string]string) []contract.ComponentVersion {
	out := []contract.ComponentVersion{}
	for c, v := range m {
		out = append(out, contract.ComponentVersion{Component: c, Version: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Component < out[j].Component })
	return out
}

// componentsText: "cli/claude 2.1.331, cli/codex 0.169.0" for the journal.
func componentsText(m map[string]string) string {
	parts := []string{}
	for _, c := range componentsSorted(m) {
		parts = append(parts, c.Component+" "+c.Version)
	}
	return strings.Join(parts, ", ")
}

func hostUpdateHistory(q *Request, b *contract.HostUpdateHistoryRequest) (contract.HostUpdateHistoryReply, error) {
	out := contract.HostUpdateHistoryReply{Entries: []contract.HostUpdateEntry{}}
	if strings.TrimSpace(b.HostId) == "" {
		return out, fail(400, "hostId is required")
	}
	limit := hostUpdateLimit
	if b.Limit != nil {
		limit = min(max(int(*b.Limit), 1), hostUpdateMaxLimit)
	}
	var before int64
	if b.Before != nil {
		before = *b.Before
	}
	rows, err := q.db().HostUpdatePage(q.ctx(), db.HostUpdatePageParams{HostID: b.HostId, Before: before, Lim: int32(limit + 1)})
	if err != nil {
		return out, err
	}
	for i, r := range rows {
		if i == limit {
			out.NextCursor = &rows[limit-1].Seq
			break
		}
		var p hostUpdatePayload
		if err := lenientDecode(r.Payload, &p); err != nil {
			return out, err
		}
		e := contract.HostUpdateEntry{Seq: r.Seq, Version: r.Version, State: contract.UpdateState(r.UpdateState),
			At: r.At.UTC().Format(time.RFC3339), Components: componentsSorted(p.Components), Brought: componentsSorted(p.Brought)}
		if r.Reason != "" {
			e.Reason = &r.Reason
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}
