// Grants drift (docs/connections.md, Grants drift): every catalogue tool that coordinator code or an agent profile uses
// must be granted on the connection that serves it. A connection created before a catalogue tool was added never gets
// it (github lacked commit_status, file_at, pull_get, pull_files and workflow_runs_* on 2026-11-26, and the
// coordinator read the keyless public API instead, rate limited for hours), so a pass compares grants with uses and
// raises one problem per connection that lacks a needed tool; connections_drift answers the same comparison.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"fleet/contract"
	"fleet/coordinator/db"
)

// gatewayUse: catalogue tools a part of the coordinator calls. Conn names the connection; "" is every connection of
// the provider (GitHub and GitLab reads pick the connection by repository owner), ciConn the one serving the CI
// repository (githubConnectionOf(ciOwner)). TestGatewayUsesListed keeps the table in step with the code.
type gatewayUse struct {
	Provider, Conn, User string
	Tools                []string
}

const ciConn = "<ci>"

var gatewayUses = []gatewayUse{
	{"github", "", "Delivery (go/coordinator/delivery.go)", []string{"pulls_open", "pulls_closed", "runs_in_progress", "runs_queued", "releases", "runs_completed_pr", "runs_completed_mq"}},
	{"github", ciConn, "Delivery history (go/coordinator/delivery_history.go)", []string{"pulls_closed_page", "releases_page", "workflow_runs_day", "workflow_runs_sha", "run_jobs"}},
	{"github", ciConn, "Runners CI (go/coordinator/runners_ci.go)", []string{"runs_in_progress", "runs_queued", "runs_recent", "run_jobs", "org_runners"}},
	{"github", ciConn, "Pin qualification (go/coordinator/pins_qualify.go)", []string{"pull_files", "commit_status", "file_at"}},
	{"github", ciConn, "Pin proposals view (go/coordinator/pins_view.go)", []string{"file_at", "pull_get"}},
	{"gitlab", "", "Delivery (go/coordinator/delivery.go)", []string{"mrs_open", "mrs_merged", "pipelines_running", "pipelines_pending", "pipelines_failed", "releases"}},
	{"github-pins", pinsWriteConn, "Pin publishing (go/coordinator/pins_publish.go, pins_watch.go)", []string{"status_create"}},
	{"bugsink", bugsinkConn, "Bugsink resolution (go/coordinator/bugsink.go)", []string{"issue_get"}},
	{"google-ai", titleConnection, "Session titles (go/coordinator/session_titles.go)", []string{titleTool}},
	{"telegram", telegramConn, "Telegram channel (go/coordinator/telegram.go, telegram_commands.go)", []string{"get_me", "get_updates", "send_message", "send_rich_message", "send_rich_message_draft", "edit_message_text", "send_chat_action", "set_message_reaction", "answer_callback_query", "set_my_commands"}},
}

// grantsSeen: tools a coordinator call found ungranted (gateway unknown-tool on a catalogue tool) since the last pass
// that saw them granted, by connection and tool, with the calling code. They count as needed even when gatewayUses
// misses them, so a call the table does not know still raises the problem.
var grantsSeen sync.Map // grantKey -> string (the caller)

type grantKey struct{ conn, tool string }

// grantRefused records a coordinator call the gateway refused with unknown-tool when the tool is in the provider
// catalogue: the connection's grant lacks it (connections_set_tools adds it).
func grantRefused(conn, tool string, line contract.CallLine) {
	if line.Outcome == contract.CallOutcomeOk || deref(line.ErrorCode) != "unknown-tool" {
		return
	}
	if _, ok := grantsSeen.Load(grantKey{conn, tool}); ok {
		return
	}
	grantsSeen.Store(grantKey{conn, tool}, "coordinator call at "+grantCaller())
}

// grantCaller: the first caller outside the gateway helpers, as file:line.
func grantCaller() string {
	pcs := make([]uintptr, 16)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	for {
		f, more := frames.Next()
		file := f.File[strings.LastIndex(f.File, "/")+1:]
		if f.Function != "" && !strings.HasPrefix(f.Function, "runtime.") && file != "github.go" && file != "connection_gateway.go" && file != "grants.go" {
			return fmt.Sprintf("go/coordinator/%s:%d", file, f.Line)
		}
		if !more {
			return "go/coordinator"
		}
	}
}

func init() {
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "grants", envMs("FLEET_GRANTS_TICK_MS", 120_000), func(ctx context.Context) error { return grantsPass(ctx, a) })
	})
	op("connections_drift", func(q *Request, _ *contract.ConnectionsDriftRequest) (contract.ConnectionsDriftReply, error) {
		out, err := grantsDrift(q, true)
		return replyAs[contract.ConnectionsDriftReply](out, err)
	})
}

// grantsPass syncs one problem per connection lacking a needed tool (cause problem:grant:<connection>). It reads first
// and writes only when a connection lacks a tool or a grant problem is held.
func grantsPass(ctx context.Context, a *App) error {
	quiet := false
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		d, err := grantsDrift(q, false)
		if err != nil {
			return err
		}
		held, err := q.db().TaskProblemsOfSource(q.ctx(), "grants")
		quiet = d.Missing == 0 && len(held) == 0
		return err
	}); err != nil || quiet {
		return err
	}
	return a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		d, err := grantsDrift(q, false)
		if err != nil {
			return err
		}
		ps := []Problem{}
		for _, c := range d.Connections {
			if len(c.Missing) == 0 {
				continue
			}
			lines := []string{}
			names := []string{}
			for _, m := range c.Missing {
				names = append(names, m.Tool)
				lines = append(lines, m.Tool+" (used by "+strings.Join(m.Users, "; ")+")")
			}
			ps = append(ps, Problem{Cause: "problem:grant:" + c.Name, RefreshText: true,
				Text: fmt.Sprintf("Connection %s lacks %d catalogue tool(s) that Fleet uses: %s. Fix: connections_set_tools {connection: %q, tools: granted plus %s}.",
					c.Name, len(c.Missing), strings.Join(lines, ", "), c.Name, strings.Join(names, ", ")),
				Data: struct {
					Connection string   `json:"connection"`
					Tools      []string `json:"tools"`
					Fix        string   `json:"fix"`
				}{c.Name, names, "connections_set_tools"}})
		}
		return problemsSync(q, "grants", ps)
	})
}

// grantsDrift: every connection with its grants, the tools Fleet uses on it and who uses each, missing and stale
// grants, key policy and pool, and with history the last change of its settings (the pass does not need it).
func grantsDrift(q *Request, history bool) (contract.ConnectionsDriftReply, error) {
	conns, err := apiConnectionAll(q)
	if err != nil {
		return contract.ConnectionsDriftReply{}, err
	}
	profiles, err := accessProfiles(q)
	if err != nil {
		return contract.ConnectionsDriftReply{}, err
	}
	keys, err := connKeysList(q, nil)
	if err != nil {
		return contract.ConnectionsDriftReply{}, err
	}
	var audits []db.AuditOfKindRow
	if history {
		if audits, err = q.db().AuditOfKind(q.ctx(), "connection"); err != nil {
			return contract.ConnectionsDriftReply{}, err
		}
	}
	last := map[string]contract.GrantChange{}
	for _, r := range audits { // oldest first: the last one stays
		var v struct {
			Field      string  `json:"field"`
			Actor      string  `json:"actor"`
			OnBehalfOf *string `json:"onBehalfOf"`
			At         int64   `json:"at"`
		}
		if json.Unmarshal(r.Data, &v) == nil {
			last[strings.TrimPrefix(r.Target, "connection:")] = contract.GrantChange{Field: v.Field, Actor: v.Actor, OnBehalfOf: v.OnBehalfOf, At: float64(v.At), Op: "connections_set_" + grantOpOf(v.Field)}
		}
	}
	ci := "github"
	names := map[string]apiConnection{}
	for _, c := range conns {
		names[c.Name] = c
	}
	if c, ok := names["github-"+strings.ToLower(ciOwner)]; ok && c.Provider == "github" {
		ci = c.Name
	}
	out := contract.ConnectionsDriftReply{Connections: []contract.GrantDrift{}, CheckedAt: float64(nowMs())}
	for _, c := range conns {
		catalogue := toolNames(c.Provider)
		users := map[string][]string{}
		need := func(tool, user string) {
			if !slices.Contains(users[tool], user) {
				users[tool] = append(users[tool], user)
			}
		}
		for _, u := range gatewayUses {
			if u.Provider != c.Provider || u.Conn != "" && u.Conn != c.Name && !(u.Conn == ciConn && c.Name == ci) {
				continue
			}
			for _, t := range u.Tools {
				need(t, u.User)
			}
		}
		for _, p := range profiles {
			if slices.Contains(p.Connections, c.Name) {
				for _, t := range catalogue { // an agent may call any tool of a connection its profile lists
					need(t, "profile "+p.ID)
				}
			}
		}
		if isAccessProvider(c.Provider) { // runConnections adds project-access to every agent run
			for _, t := range catalogue {
				need(t, "every agent run of its project (project-access)")
			}
		}
		grantsSeen.Range(func(k, v any) bool {
			g := k.(grantKey)
			if _, live := names[g.conn]; !live {
				grantsSeen.Delete(k) // a deleted connection
			} else if g.conn == c.Name && slices.Contains(catalogue, g.tool) {
				if slices.Contains(c.Tools, g.tool) {
					grantsSeen.Delete(k)
				} else {
					need(g.tool, v.(string))
				}
			}
			return true
		})
		d := contract.GrantDrift{Name: c.Name, Provider: c.Provider, Holder: c.Holder, Intake: c.Intake,
			Granted: nonNilStrings(c.Tools), Catalogue: catalogue, Needed: []contract.GrantUse{}, Missing: []contract.GrantUse{}, Stale: []string{}}
		for _, t := range slices.Sorted(maps.Keys(users)) {
			u := contract.GrantUse{Tool: t, Users: users[t]}
			sort.Strings(u.Users)
			d.Needed = append(d.Needed, u)
			if !slices.Contains(c.Tools, t) {
				d.Missing = append(d.Missing, u)
			}
		}
		for _, t := range c.Tools {
			if !slices.Contains(catalogue, t) {
				d.Stale = append(d.Stale, t)
			}
		}
		if p, err := c.keyPolicy(); err == nil && !keyPolicyEmpty(p) {
			d.KeyPolicy = &p
		}
		for _, k := range keys {
			if k.ConnectionID == c.Name {
				d.Keys.Total++
				if k.State == "active" {
					d.Keys.Active++
				}
			}
		}
		if l, ok := last[c.Name]; ok {
			d.LastChange = &l
		}
		out.Missing += int64(len(d.Missing))
		out.Connections = append(out.Connections, d)
	}
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].Name < out.Connections[j].Name })
	return out, nil
}

// grantOpOf: the connections_set_<x> operation that writes an audited connection field.
func grantOpOf(field string) string {
	if field == "keyPolicy" {
		return "key_policy"
	}
	return field
}

// errNotGranted: the gateway refused a catalogue tool the connection does not grant (unknown-tool).
var errNotGranted = errors.New("not granted")
