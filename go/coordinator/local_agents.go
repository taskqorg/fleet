// Local agents (docs/designs/fleet-connections.md#13-local-agents): a person's own Claude Code, Codex or Antigravity on
// one machine, served by the personal token <machine>-<harness> that names every connection the person may use.
// local_agent_connect mints it (or refreshes its connections) and answers the command to run on that machine;
// local_agents is the per-machine report: each local agent's connections with their last call, and its drift.
package main

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"fleet/contract"
)

func init() {
	op("local_agent_connect", localAgentConnect)
	op("local_agent_disconnect", localAgentDisconnect)
	op("local_agents", localAgents)
}

var localAgentHarnesses = []string{"claude", "codex", "antigravity"}

var localAgentName = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)-(claude|codex|antigravity)$`)

// localAgentToken: the token name of a machine's harness; the machine is a Fleet host id.
func localAgentToken(machine, harness string) string { return machine + "-" + harness }

// localAgentWanted: every connection the person may use, as a personal token of theirs reaches it.
func localAgentWanted(q *Request, name string) ([]string, error) {
	all, err := apiConnectionAll(q)
	if err != nil {
		return nil, err
	}
	director := directorOf(q)
	run := mintRun{DirectedBy: &director, Personal: &name}
	out := []string{}
	for _, c := range all {
		if c.Intake == "all" && (connAllowedFor(c, run) || accessTokenAllowed(q, c, run)) && coordinatorOnlyAllowed(c.Name, run) {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func localAgentCheck(q *Request, machine, harness string) error {
	if !slices.Contains(localAgentHarnesses, harness) {
		return fail(400, "harness is claude, codex or antigravity")
	}
	hosts, err := hostRecords(q)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h.ID == machine && h.State != "removed" {
			return nil
		}
	}
	return fail(404, "Unknown host "+machine+": a local agent runs on a Fleet host (it has fleet-executor)")
}

func localAgentLive(q *Request, name string) (*runToken, error) {
	toks, err := runTokensAll(q)
	if err != nil {
		return nil, err
	}
	for _, t := range toks {
		if deref(t.Personal) == name && !t.revoked() {
			return &t, nil
		}
	}
	return nil, nil
}

func localAgentCommand(harness, name string, token *string) string {
	cmd := "~/releases/current/fleet-executor local-agent " + harness + " " + name
	if token != nil {
		return "printf '%s\\n' '" + *token + "' | " + cmd
	}
	return cmd
}

func localAgentConnect(q *Request, r *contract.LocalAgentConnectRequest) (contract.LocalAgentConnectReply, error) {
	var no contract.LocalAgentConnectReply
	if err := localAgentCheck(q, r.Machine, r.Harness); err != nil {
		return no, err
	}
	name := localAgentToken(r.Machine, r.Harness)
	if !connName.MatchString(name) {
		return no, fail(400, "Machine name "+r.Machine+" makes no token name")
	}
	wanted, err := localAgentWanted(q, name)
	if err != nil {
		return no, err
	}
	live, err := localAgentLive(q, name)
	if err != nil {
		return no, err
	}
	if live != nil && r.Reissue != nil && *r.Reissue {
		if _, err := connectionsTokenRevoke(q, &contract.ConnectionsTokenRevokeRequest{Name: name}); err != nil {
			return no, err
		}
		live = nil
	}
	out := contract.LocalAgentConnectReply{Name: name, Machine: r.Machine, Harness: r.Harness}
	if live != nil {
		set, err := connectionsTokenSet(q, &contract.ConnectionsTokenSetRequest{Name: name, Connections: wanted})
		if err != nil {
			return no, err
		}
		out.Command = localAgentCommand(r.Harness, name, nil)
		for _, c := range set.Connections {
			out.Connections = append(out.Connections, fmt.Sprint(c["name"]))
		}
		return out, nil
	}
	exec, director := "token:"+name, directorOf(q)
	g, err := mintGrantFor(q, mintRun{ExecutionID: &exec, DirectedBy: &director, Personal: &name}, wanted)
	if err != nil {
		return no, err
	}
	if g.Token == nil {
		return no, fail(409, "No connection you may use")
	}
	out.Command = localAgentCommand(r.Harness, name, g.Token)
	out.NewToken = new(true)
	for _, c := range g.Connections {
		out.Connections = append(out.Connections, c.Name)
	}
	return out, nil
}

func localAgentDisconnect(q *Request, r *contract.LocalAgentDisconnectRequest) (contract.LocalAgentDisconnectReply, error) {
	if err := localAgentCheck(q, r.Machine, r.Harness); err != nil {
		return contract.LocalAgentDisconnectReply{}, err
	}
	name := localAgentToken(r.Machine, r.Harness)
	if _, err := connectionsTokenRevoke(q, &contract.ConnectionsTokenRevokeRequest{Name: name}); err != nil {
		return contract.LocalAgentDisconnectReply{}, err
	}
	return contract.LocalAgentDisconnectReply{Name: name, Revoked: true}, nil
}

// localAgents: per machine and harness, the newest token of that name: state, connections with their last calls,
// and drift against what the person may use now.
func localAgents(q *Request, _ *contract.LocalAgentsRequest) (contract.LocalAgentsReply, error) {
	out := contract.LocalAgentsReply{Agents: []contract.LocalAgent{}}
	toks, err := runTokensAll(q)
	if err != nil {
		return out, err
	}
	newest := map[string]runToken{}
	for _, t := range toks {
		name := deref(t.Personal)
		if !localAgentName.MatchString(name) {
			continue
		}
		cur, seen := newest[name]
		if !seen || cur.revoked() && !t.revoked() || cur.revoked() == t.revoked() && createdMs(t) > createdMs(cur) {
			newest[name] = t
		}
	}
	names := make([]string, 0, len(newest))
	for n := range newest {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t := newest[name]
		m := localAgentName.FindStringSubmatch(name)
		a := contract.LocalAgent{Name: name, Machine: m[1], Harness: m[2], Connections: []contract.LocalAgentConnection{}, Missing: []string{}}
		if c := createdMs(t); c > 0 {
			a.ConnectedAt = new(c)
		}
		last := int64(0)
		for _, c := range t.Connections {
			lc := contract.LocalAgentConnection{Name: c}
			if k, ok := t.Calls[c]; ok {
				if k.OkAt > 0 {
					lc.OkAt, lc.OkTool = new(k.OkAt), new(k.OkTool)
					last = max(last, k.OkAt)
				}
				if k.FailAt > 0 {
					lc.FailAt, lc.FailTool, lc.FailCode = new(k.FailAt), new(k.FailTool), new(k.FailCode)
				}
			}
			a.Connections = append(a.Connections, lc)
		}
		if last > 0 {
			a.LastOkAt = new(last)
		}
		switch {
		case t.revoked():
			a.State = contract.LocalAgentStateDisconnected
		default:
			wanted, err := localAgentWanted(q, name)
			if err != nil {
				return out, err
			}
			for _, w := range wanted {
				if !slices.Contains(t.Connections, w) {
					a.Missing = append(a.Missing, w)
				}
			}
			switch {
			case len(a.Missing) > 0:
				a.State = contract.LocalAgentStateRefresh
			case last == 0:
				a.State = contract.LocalAgentStateUnused
			default:
				a.State = contract.LocalAgentStateConnected
			}
		}
		out.Agents = append(out.Agents, a)
	}
	return out, nil
}

func createdMs(t runToken) int64 {
	if c := rawMs(t.CreatedAt); c != nil {
		return c.UnixMilli()
	}
	return 0
}

// tokenCallNote keeps a personal token's last successful and last failed call per connection (connRecord).
func tokenCallNote(q *Request, id string, l contract.CallLine) error {
	if l.Phase != nil && *l.Phase == contract.CallPhaseStarted {
		return nil
	}
	if err := acquireTxLock(q, "token-calls:"+id); err != nil {
		return err
	}
	t, err := runTokenMaybe(q, id)
	if err != nil || t == nil {
		return err
	}
	if t.Calls == nil {
		t.Calls = map[string]tokenCalls{}
	}
	k := t.Calls[l.Connection]
	at := l.StartedAt
	if at == 0 {
		at = nowMs()
	}
	if l.Outcome == contract.CallOutcomeOk {
		if at < k.OkAt {
			return nil
		}
		k.OkAt, k.OkTool = at, l.Tool
	} else {
		if at < k.FailAt {
			return nil
		}
		k.FailAt, k.FailTool, k.FailCode = at, l.Tool, strings.TrimSpace(deref(l.ErrorCode))
	}
	t.Calls[l.Connection] = k
	return runTokenPut(q, *t)
}
