package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: a connection lacking a catalogue tool that Fleet uses shows as missing in connections_drift, naming the
// tool and its users, and raises problem problem:grant:<connection>; granting it again clears both. A coordinator call the
// gateway refuses with unknown-tool counts as a use even when the table does not list it.
func TestGrantsDrift(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	var ignore map[string]any
	e.opInto(t, "connections_create", map[string]any{"name": "github", "provider": "github"}, h, &ignore)
	e.opInto(t, "connections_create", map[string]any{"name": "openai", "provider": "openai"}, h, &ignore)
	drift := func() contract.ConnectionsDriftReply {
		t.Helper()
		var d contract.ConnectionsDriftReply
		e.opInto(t, "connections_drift", map[string]any{}, h, &d)
		return d
	}
	of := func(d contract.ConnectionsDriftReply, name string) contract.GrantDrift {
		for _, c := range d.Connections {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("connections_drift has no %s", name)
		return contract.GrantDrift{}
	}
	held := func(cause string) bool {
		t.Helper()
		var n int
		if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT count(*) FROM question WHERE cause = $1 AND state = 'open'`, cause).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	pass := func() {
		t.Helper()
		if err := grantsPass(e.ctx, e.app); err != nil {
			t.Fatal(err)
		}
	}
	if d := drift(); d.Missing != 0 {
		t.Fatalf("fresh connections drift: %+v", d)
	}
	gh := of(drift(), "github")
	if len(gh.Needed) == 0 || gh.LastChange != nil && gh.LastChange.Op == "" {
		t.Fatalf("github needs nothing: %+v", gh)
	}

	// A removed grant: missing, with its users and the fix, and a problem.
	without := slices.DeleteFunc(slices.Clone(gh.Granted), func(s string) bool { return s == "commit_status" })
	e.opInto(t, "connections_set_tools", map[string]any{"connection": "github", "tools": without}, h, &ignore)
	gh = of(drift(), "github")
	if len(gh.Missing) != 1 || gh.Missing[0].Tool != "commit_status" {
		t.Fatalf("missing after removing commit_status: %+v", gh.Missing)
	}
	users := strings.Join(gh.Missing[0].Users, "; ")
	if !strings.Contains(users, "pins_qualify.go") || !strings.Contains(users, "profile pm") {
		t.Fatalf("commit_status users: %s", users)
	}
	if gh.LastChange == nil || gh.LastChange.Field != "tools" || gh.LastChange.Op != "connections_set_tools" || gh.LastChange.Actor != testOwnerEmail {
		t.Fatalf("last change: %+v", gh.LastChange)
	}
	pass()
	if !held("problem:grant:github") {
		t.Fatal("no problem problem:grant:github")
	}
	e.opInto(t, "connections_set_tools", map[string]any{"connection": "github", "tools": append(without, "commit_status")}, h, &ignore)
	pass()
	pass()
	if held("problem:grant:github") {
		t.Fatal("problem problem:grant:github stays after the grant came back")
	}

	// A refused call of a tool no table lists.
	oa := of(drift(), "openai")
	if len(oa.Needed) != 0 {
		t.Fatalf("openai needs %+v", oa.Needed)
	}
	tool := oa.Granted[0]
	e.opInto(t, "connections_set_tools", map[string]any{"connection": "openai", "tools": oa.Granted[1:]}, h, &ignore)
	grantRefused("openai", tool, contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: new("unknown-tool")})
	oa = of(drift(), "openai")
	if len(oa.Missing) != 1 || oa.Missing[0].Tool != tool || !strings.Contains(oa.Missing[0].Users[0], "grants_test.go") {
		t.Fatalf("refused call: %+v", oa.Missing)
	}
	e.opInto(t, "connections_set_tools", map[string]any{"connection": "openai", "tools": oa.Catalogue}, h, &ignore)
	if oa = of(drift(), "openai"); len(oa.Missing) != 0 || len(oa.Needed) != 0 {
		t.Fatalf("after the grant: %+v", oa)
	}
}

// TestGatewayUsesListed: every catalogue tool name written in a file that reads a provider through the coordinator's
// readers is in gatewayUses for that provider, so the drift check knows every tool the code calls.
func TestGatewayUsesListed(t *testing.T) {
	readers := map[string]string{"githubRead": "github", "githubReadKeyed": "github", "gitlabRead": "gitlab", "telegramCall": "telegram", "pinsGatewayCall": "github-pins"}
	listed := map[string][]string{}
	for _, u := range gatewayUses {
		listed[u.Provider] = append(listed[u.Provider], u.Tools...)
	}
	files, _ := os.ReadDir(".")
	fs := token.NewFileSet()
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "github.go" || name == "grants.go" {
			continue
		}
		file, err := parser.ParseFile(fs, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		providers := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && readers[id.Name] != "" {
					providers[readers[id.Name]] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			l, ok := n.(*ast.BasicLit)
			if !ok || l.Kind != token.STRING {
				return true
			}
			s, _ := strconv.Unquote(l.Value)
			for p := range providers {
				if _, tool := gatewayProviders[p].Tools[s]; tool && !slices.Contains(listed[p], s) {
					t.Errorf("%s: %s tool %q is not in gatewayUses (grants.go)", fs.Position(l.Pos()), p, s)
				}
			}
			return true
		})
	}
}

// Requirement: connections_token_set changes a live personal token's connections without a new value: the token's row
// names the new list, the reply carries the specs a dev session's MCP servers need, and a connection the token may not
// reach is refused, not added.
func TestConnectionsTokenSet(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	var ignore map[string]any
	e.opInto(t, "connections_create", map[string]any{"name": "github", "provider": "github"}, h, &ignore)
	e.opInto(t, "connections_create", map[string]any{"name": "openai", "provider": "openai"}, h, &ignore)
	var created contract.ConnectionsTokenCreateReply
	e.opInto(t, "connections_token_create", map[string]any{"name": "dev-mac", "connections": []string{"github"}}, h, &created)
	if created.Token == nil || *created.Token == "" {
		t.Fatalf("create: %+v", created)
	}
	var set contract.ConnectionsTokenSetReply
	e.opInto(t, "connections_token_set", map[string]any{"name": "dev-mac", "connections": []string{"github", "openai", "missing"}}, h, &set)
	if len(set.Connections) != 2 || !slices.Contains(set.Refused, "missing") {
		t.Fatalf("set: %+v", set)
	}
	var list contract.ConnectionsTokenListReply
	e.opInto(t, "connections_token_list", map[string]any{}, h, &list)
	n := 0
	for _, tok := range list.Tokens {
		if tok.Name == "dev-mac" {
			n++
			if !strings.Contains(string(tok.Connections), "openai") {
				t.Fatalf("token connections %s", tok.Connections)
			}
		}
	}
	if n != 1 {
		t.Fatalf("set made %d tokens named dev-mac, want the one", n)
	}
	var sha string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT sha256 FROM run_token WHERE personal = 'dev-mac'`).Scan(&sha); err != nil || sha != sha256hex([]byte(*created.Token)) {
		t.Fatalf("the token's value changed: %v", err)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/connections_token_set", map[string]any{"name": "nobody", "connections": []string{"github"}}, h); code != 404 {
		t.Fatalf("unknown token: %d", code)
	}
}
