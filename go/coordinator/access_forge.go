package main

import (
	"slices"
	"sort"
	"strings"

	"fleet/contract"
)

// Forge logins on System › Access: one row per forge host (gitlab.ufobe.com, github.com) that a project with
// execution.forge publishes to, with each of the project's environment hosts and whether that host's forge CLI answered
// as a user (the executor's forgeLogins in its poll, forge_login.go). A host without one cannot publish for the project;
// that reads here as "needs a login", never only as a missing capability (DAM Lab, 2026-11-25: owner-mac had no
// gitlab.ufobe.com login).

// forgeLoginsSince: the first executor release whose poll reports forgeLogins (#1109).
const forgeLoginsSince = "1.261125.237"

// accessForgeLogins: the forge login rows of the projects the person may read.
func accessForgeLogins(q *Request) ([]contract.AccessItem, error) {
	ids, err := projectIDs(q)
	if err != nil {
		return nil, err
	}
	obs, err := q.db().HostObservations(q.ctx())
	if err != nil {
		return nil, err
	}
	// reported[host][provider|forgeHost] = the host's last answer
	reported := map[string]map[string]contract.ForgeLogin{}
	for _, r := range obs {
		var o struct {
			ForgeLogins *[]contract.ForgeLogin `json:"forgeLogins"`
			Version     *string                `json:"version"`
		}
		if lenientDecode(r.Payload, &o) != nil {
			continue
		}
		// An empty report is left out of the poll (omitempty), so a missing one from an executor that reports means none.
		if o.ForgeLogins == nil && (o.Version == nil || versionLess(*o.Version, forgeLoginsSince)) {
			continue // an executor older than the report: its logins are unknown, not missing
		}
		m, logins := map[string]contract.ForgeLogin{}, []contract.ForgeLogin{}
		if o.ForgeLogins != nil {
			logins = *o.ForgeLogins
		}
		for _, l := range logins {
			m[l.Provider+"|"+strings.ToLower(l.Host)] = l
		}
		reported[r.ID] = m
	}
	type need struct {
		provider, host string
		hosts          map[string]bool
		projects       []string
	}
	needs := map[string]*need{}
	for _, id := range ids {
		if q.person != nil && !q.person.mayAccess(id) {
			continue
		}
		var s runSettings
		if lenientDecode(jsonBytes(projectSettingsOf(q, id)), &s) != nil || s.Execution == nil || !s.Execution.Forge {
			continue
		}
		for _, r := range s.Repos {
			ref, ok := contract.ParseRepoRemote(r.Remote, r.Forge, r.Host)
			if !ok {
				continue
			}
			key := ref.Provider + "|" + ref.Host
			n := needs[key]
			if n == nil {
				n = &need{provider: ref.Provider, host: ref.Host, hosts: map[string]bool{}}
				needs[key] = n
			}
			for _, e := range s.Environments {
				if e.Repo == nil || *e.Repo == r.ID {
					n.hosts[e.HostID] = true
				}
			}
			if !slices.Contains(n.projects, id) {
				n.projects = append(n.projects, id)
			}
		}
	}
	keys := make([]string, 0, len(needs))
	for k := range needs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []contract.AccessItem{}
	for _, key := range keys {
		out = append(out, forgeLoginItem(needs[key].provider, needs[key].host, needs[key].hosts, needs[key].projects, reported))
	}
	return out, nil
}

// forgeLoginItem: one forge host's row: which of the hosts that need it answered as a user.
func forgeLoginItem(provider, forgeHost string, need map[string]bool, projects []string, reported map[string]map[string]contract.ForgeLogin) contract.AccessItem {
	key := provider + "|" + forgeHost
	it := contract.AccessItem{Id: "forge:" + key, Group: contract.AccessGroupService, Name: forgeHost + " login", Provider: provider, Kind: "forge login",
		Hosts: []contract.AccessHost{}}
	hosts := make([]string, 0, len(need))
	for h := range need {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	ready, missing := []string{}, []string{}
	for _, h := range hosts {
		ah := contract.AccessHost{HostId: h, Binding: "forge", Readiness: "unavailable"}
		got, reports := reported[h]
		l, seen := got[key]
		switch {
		case !reports:
			ah.Readiness = "unknown"
			reason := "the host's executor does not report forge logins yet (update pending)"
			ah.Reason = &reason
		case seen && l.Ok:
			ah.Readiness = "ready"
			ready = append(ready, h)
		case seen:
			reason := "the forge CLI did not answer as a user"
			ah.Reason = &reason
			missing = append(missing, h)
		default:
			reason := "no " + forgeHost + " login in the host's forge store"
			ah.Reason = &reason
			missing = append(missing, h)
		}
		if seen && l.CheckedAt != nil {
			ah.ObservedAt = l.CheckedAt
		}
		it.Hosts = append(it.Hosts, ah)
	}
	if len(hosts) > 0 {
		w := strings.Join(hosts, ", ")
		it.Where = &w
	}
	cli := "gh auth login --hostname " + forgeHost
	if provider == "gitlab" {
		cli = "glab auth login --hostname " + forgeHost
	}
	login := "On the host, as the executor's user, with the executor's FLEET_FORGE_CONFIG_DIR (GLAB_CONFIG_DIR or GH_CONFIG_DIR under it): " + cli +
		". The host checks the login again within 10 minutes; Check reads its latest answer."
	it.Guide = &contract.AccessGuide{Login: &login}
	note := "Projects that publish there: " + strings.Join(projects, ", ") + ". " + login
	it.Note = &note
	switch {
	case len(missing) == 0 && len(ready) == 0:
		it.State, it.StateText = contract.AccessStateUnknown, "No host has reported its forge login yet"
	case len(missing) == 0:
		it.State, it.StateText = contract.AccessStateWorks, "Works on "+strings.Join(ready, ", ")
	default:
		it.State = contract.AccessStateNeedsLogin
		it.StateText = missing[0] + " has no " + forgeHost + " login"
		if len(missing) > 1 {
			it.StateText = strings.Join(missing, ", ") + " have no " + forgeHost + " login"
		}
		if len(ready) > 0 {
			it.StateText += "; works on " + strings.Join(ready, ", ")
		}
		it.Next = &contract.AccessNext{Action: contract.AccessActionCheck, Label: "Check again", HostId: &missing[0]}
	}
	return it
}
