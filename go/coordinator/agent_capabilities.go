package main

// The generated "what you can do" section of an agent with Fleet operations (fleet-pm-agent.md §9a): built at dispatch
// from the same grants, tool list, connection grant and host observations that serve the agent's calls, so the agent
// acts with the rights it has instead of asking a person for them.

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"fleet/contract"
)

// capabilityGroups: the operations named in the section, by purpose; one is named only when the agent's tool list
// carries it. Every other operation stays reachable through tools/list.
var capabilityGroups = []struct {
	label string
	names []string
}{
	{"projects", []string{"project_list", "project_detail", "project_create", "project_update", "project_readiness", "project_archive", "focus_set", "session_transition"}},
	{"work", []string{"task_create", "task_release", "session_start", "execution_status", "search"}},
	{"access and settings", []string{"hosts_list", "connections_list", "connections_set_tools", "connections_key_request", "people_list"}},
	{"people", []string{"ask", "approval_ask"}},
}

// personOnlyOperations: the operations no agent may call (agent: no in the contracts), sorted.
var personOnlyOperations = func() []string {
	out := []string{}
	for n, o := range contract.Operations {
		if o.Agent == "no" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}()

// hostGitConnections: each live execution host's git connections (the deploy keys its poll reports), by host id.
func hostGitConnections(q *Request) (map[string][]string, error) {
	hs, err := hostsLive(q)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, h := range hs {
		if h.executes() {
			out[h.ID] = []string{}
		}
	}
	obs, err := q.db().HostObservations(q.ctx())
	if err != nil {
		return nil, err
	}
	for _, o := range obs {
		if _, ok := out[o.ID]; !ok {
			continue
		}
		var p struct {
			GitConnections json.RawMessage `json:"gitConnections"`
		}
		_ = lenientDecode(o.Payload, &p)
		out[o.ID] = jsonStrings(p.GitConnections)
	}
	return out, nil
}

// capabilityInstruction: the section for director dir, its tool list ops, the API connections of this run and the git
// connections of each host. Text only: no key, token or path leaves the coordinator.
func capabilityInstruction(dir *Person, ops []agentOperation, conns []connSpec, gitConns map[string][]string) string {
	have := map[string]bool{}
	for _, o := range ops {
		have[o.Name] = true
	}
	lines := []string{"What you can do (generated for this turn from your director's grants, your tools and the hosts):"}
	standing := "access to projects " + strings.Join(grantedProjects(dir), ", ")
	if fullAccess(dir) {
		standing = "full access: every project, setting and connection, except secrets"
	}
	lines = append(lines, "- You act for "+dir.Email+" with "+standing+". Use these rights yourself: never ask a person to do what one of your operations does.")
	groups := []string{}
	for _, g := range capabilityGroups {
		names := []string{}
		for _, n := range g.names {
			if have[n] {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			groups = append(groups, g.label+": "+strings.Join(names, ", "))
		}
	}
	if len(groups) > 0 {
		lines = append(lines, "- Fleet operations ("+strconv.Itoa(len(ops))+" in your tool list), among them "+strings.Join(groups, "; ")+".")
	}
	if len(personOnlyOperations) > 0 {
		lines = append(lines, "- Only a person may call "+strings.Join(personOnlyOperations, ", ")+" (secrets and identity). For a missing API key call connections_key_request; never take a key from chat.")
	}
	if len(conns) > 0 {
		cs := []string{}
		for _, c := range conns {
			tools := []string{}
			for _, t := range c.Tools {
				tools = append(tools, t.Name)
			}
			cs = append(cs, c.Name+" ("+strings.Join(tools, ", ")+")")
		}
		lines = append(lines, "- API connections of this turn: "+strings.Join(cs, "; ")+". Others: connections_list; a connection's newer provider tools: connections_set_tools.")
	}
	if len(gitConns) > 0 {
		hosts := []string{}
		for h := range gitConns {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		hs := []string{}
		for _, h := range hosts {
			c := slices.Clone(gitConns[h])
			sort.Strings(c)
			names := "none"
			if len(c) > 0 {
				names = strings.Join(c, ", ")
			}
			hs = append(hs, h+": "+names)
		}
		lines = append(lines, "- Git connections (deploy keys) on hosts: "+strings.Join(hs, "; ")+". A repo's connection works only on hosts that hold it, and a deploy key reads only the repositories it was added to.")
	}
	lines = append(lines,
		"- Your native shell is sandboxed and may have no network: a DNS, connection or TLS error there says nothing about GitLab, GitHub or any other service. Check a repository with project_readiness (git ls-remote on each environment host with the repo's connection key; results in execution_status) and reach APIs through connections.",
		"- Ask a person only after your own tools: find (project_list remotes, search, connection tools, files), then check (project_readiness). The question names what you tried and what each returned, and asks only what no tool of yours can do: a decision, a secret, or a grant outside Fleet such as adding a deploy key to a GitLab project.")
	return strings.Join(lines, "\n")
}

// grantedProjects: the project ids of dir's project grants, sorted.
func grantedProjects(dir *Person) []string {
	out := []string{}
	for _, g := range dir.Grants {
		if g.Kind == "project" && g.ID != "" {
			out = append(out, g.ID)
		}
	}
	sort.Strings(out)
	return out
}
