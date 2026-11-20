package main

// Design rules scan (docs/designs/fleet-standard.md S10, DESIGN_RULES.md).
// Every non-test, non-generated Go file of this package is parsed and checked
// for the patterns below. designRulesLegacy names the files not yet moved onto
// the standard; it only shrinks: an entry whose file no longer breaks its rule
// fails the test until it is removed.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"fleet/contract"
)

var designSQLStarts = []string{"select", "insert", "update", "delete", "with"}

var designMapCalls = map[string]bool{"astr": true, "anys": true, "mapOf": true}

var designTimeCalls = map[string]bool{"NewTicker": true, "Tick": true, "Sleep": true, "AfterFunc": true}

var designOpCalls = map[string]bool{"gatedOp": true, "gatedObjOp": true, "shaped": true, "operation": true, "agentTool": true, "longOp": true, "projectOp": true, "threadOp": true, "checkFirst": true, "ownerFirst": true}

// designRuleHits returns rule ID -> positions of its violations in one file.
func designRuleHits(f *ast.File) map[string][]token.Pos {
	hits := map[string][]token.Pos{}
	add := func(rule string, n ast.Node) { hits[rule] = append(hits[rule], n.Pos()) }
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				break
			}
			s, err := strconv.Unquote(x.Value)
			if err != nil {
				break
			}
			// A keyword followed by more text ("select ... from"), or an upper-case keyword and a space
			// ("SELECT " + cols); a lone word such as "update" or the HTTP method "DELETE" is not SQL.
			if fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(s), "(")); len(fields) > 0 {
				for _, kw := range designSQLStarts {
					if strings.EqualFold(fields[0], kw) && (len(fields) > 1 || fields[0] == strings.ToUpper(kw) && strings.HasSuffix(s, " ")) {
						add("R-SQL", x)
					}
				}
			}
		case *ast.CallExpr:
			fun := x.Fun
			switch g := fun.(type) {
			case *ast.IndexExpr:
				fun = g.X
			case *ast.IndexListExpr:
				fun = g.X
			}
			if id, ok := fun.(*ast.Ident); ok {
				if designMapCalls[id.Name] {
					add("R-MAP", x)
				}
				if designOpCalls[id.Name] {
					add("R-OP", x)
				}
			}
			if sel, ok := fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && designTimeCalls[sel.Sel.Name] {
					add("R-GO", x)
				}
			}
		case *ast.CompositeLit:
			if id, ok := x.Type.(*ast.Ident); ok && id.Name == "doc" {
				add("R-MAP", x)
			}
		case *ast.MapType:
			k, kok := x.Key.(*ast.Ident)
			v, vok := x.Value.(*ast.Ident)
			iface, iok := x.Value.(*ast.InterfaceType)
			if kok && k.Name == "string" && (vok && v.Name == "any" || iok && len(iface.Methods.List) == 0) {
				add("R-MAP", x)
			}
		case *ast.GoStmt:
			add("R-GO", x)
		}
		return true
	})
	return hits
}

// S4: every contract operation declares who may call it (auth), how a host agent calls it (agent) and its tool
// description; op, runOp and the MCP servers read them from contract.Operations.
func TestDesignOperationsDeclared(t *testing.T) {
	auth := regexp.MustCompile(`^(person|architect|owner|system|project:[A-Za-z][A-Za-z0-9.]*|holder:[a-z]+)$`)
	agent := regexp.MustCompile(`^(no|tool|bound:[a-z]+)$`)
	for n, o := range contract.Operations {
		if !auth.MatchString(o.Auth) || !agent.MatchString(o.Agent) || strings.TrimSpace(o.Description) == "" {
			t.Errorf("%s: declare auth, agent and description in contracts/ops-*.schema.json (auth %q, agent %q)", n, o.Auth, o.Agent)
		}
	}
}

func TestDesignRules(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string]map[string][]token.Pos{} // file -> rule -> positions
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if ast.IsGenerated(f) {
			continue
		}
		if hits := designRuleHits(f); len(hits) > 0 {
			found[p] = hits
		}
	}
	for file, rules := range found {
		for rule, pos := range rules {
			if !designRulesLegacy[file][rule] && designRulesOwned[file][rule] == "" {
				t.Errorf("%s: breaks %s (DESIGN_RULES.md, %d places in the file); move it onto the standard", fset.Position(pos[0]), rule, len(pos))
			}
		}
	}
	for file, rules := range designRulesLegacy {
		for rule := range rules {
			if len(found[file][rule]) == 0 {
				t.Errorf("%s no longer breaks %s: remove it from designRulesLegacy", file, rule)
			}
		}
	}
	if t.Failed() {
		t.Logf("the current tree as designRulesLegacy (only remove entries):\n%s", designRulesAllowlist(found))
	}
}

func designRulesAllowlist(found map[string]map[string][]token.Pos) string {
	files := make([]string, 0, len(found))
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	var b strings.Builder
	for _, f := range files {
		rules := make([]string, 0, len(found[f]))
		for r := range found[f] {
			rules = append(rules, fmt.Sprintf("%q: true", r))
		}
		sort.Strings(rules)
		fmt.Fprintf(&b, "\t%q: {%s},\n", f, strings.Join(rules, ", "))
	}
	return b.String()
}

// The S5/S6 checks of the work kit and its queues (DESIGN_RULES.md). designKitAreas: the file-name prefixes of the
// areas whose item work runs on the kit; an area moving onto the kit adds its prefix.
var designKitAreas = []string{"resource_"}

func designKitArea(name string) bool {
	for _, p := range designKitAreas {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

type designFile struct {
	name string
	ast  *ast.File
}

// designSources: the package's Go files, parsed; tests true gives the test files instead.
func designSources(t *testing.T, tests bool) []designFile {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	var out []designFile
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") != tests {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, designFile{n, f})
	}
	return out
}

// designLiterals: every string expression of f, constant-folded: literal parts concatenated, any other operand "?".
func designLiterals(f *ast.File) []string {
	var fold func(e ast.Expr) (string, bool)
	fold = func(e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				s, _ := strconv.Unquote(x.Value)
				return s, true
			}
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				a, okA := fold(x.X)
				b, okB := fold(x.Y)
				return a + b, okA || okB
			}
		case *ast.ParenExpr:
			return fold(x.X)
		}
		return "?", false
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if s, ok := fold(x); ok && x.Op == token.ADD {
				out = append(out, s)
				return false
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				s, _ := strconv.Unquote(x.Value)
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

var (
	designAssign    = regexp.MustCompile(`(?:^|,)\s*([a-z_]+)\s*=`)
	designClauseEnd = regexp.MustCompile(`(?i)\b(WHERE|FROM|RETURNING)\b`)
	designDoUpdate  = regexp.MustCompile(`(?is)\bDO\s+UPDATE\s+SET\s+(.*)`)
	designTableRes  sync.Map // table -> [3]*regexp.Regexp
)

// designWrites: the columns of table that sql writes (UPDATE ... SET, INSERT column lists, ON CONFLICT DO UPDATE SET),
// and "DELETE" when it deletes rows of table.
func designWrites(sql, table string) []string {
	if !strings.Contains(sql, table) {
		return nil
	}
	v, ok := designTableRes.Load(table)
	if !ok {
		t := regexp.QuoteMeta(table)
		v, _ = designTableRes.LoadOrStore(table, [3]*regexp.Regexp{
			regexp.MustCompile(`(?is)\bUPDATE\s+` + t + `\b(?:\s+[a-z]+)?\s+SET\s+(.*)`),
			regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+` + t + `\s*\(([^)]*)\)(.*)`),
			regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+` + t + `\b`)})
	}
	res := v.([3]*regexp.Regexp)
	var cols []string
	set := func(clause string) {
		if i := designClauseEnd.FindStringIndex(clause); i != nil {
			clause = clause[:i[0]]
		}
		for _, m := range designAssign.FindAllStringSubmatch(clause, -1) {
			cols = append(cols, m[1])
		}
	}
	for _, m := range res[0].FindAllStringSubmatch(sql, -1) {
		set(m[1])
	}
	for _, m := range res[1].FindAllStringSubmatch(sql, -1) {
		for _, c := range strings.Split(m[1], ",") {
			cols = append(cols, strings.TrimSpace(c))
		}
		if u := designDoUpdate.FindStringSubmatch(m[2]); u != nil {
			set(u[1])
		}
	}
	if res[2].MatchString(sql) {
		cols = append(cols, "DELETE")
	}
	return cols
}

type designOwner struct {
	table, owner string
	columns      []string
}

// designStateOwners: each state column of a machine and the one file that may write it (S5). The retry columns of
// every registered kit queue's table belong to the kit (S6).
func designStateOwners() []designOwner {
	out := []designOwner{
		{"resource_build", "resource_states.go", []string{"phase", "scan_done", "after_execution_id", "generation", "DELETE"}},
		{"resource_project_summary", "resource_states.go", []string{"ready", "epoch"}},
		{"resource_metadata_repair", "resource_states.go", []string{"done", "after_execution_id", "DELETE"}},
		{"command", "commands.go", []string{"status", "stop_detail"}},
		{"execution", "execution_states.go", []string{"state"}},
		{"question", "question_states.go", []string{"state"}},
		{"question", "problems.go", []string{"cause"}},
		{"experiment", "experiment_states.go", []string{"state"}},
	}
	for _, q := range kitQueues {
		out = append(out, designOwner{q.Retry.Table, "work_kit.go", []string{"attempts", "next_attempt_at", "last_error"}})
	}
	return out
}

// S5: a machine's state columns and a kit queue's retry columns are written only by their owner file.
func TestDesignStateWrites(t *testing.T) {
	if got := strings.Join(designWrites(`INSERT INTO resource_build(project_id,phase) VALUES($1,'x') ON CONFLICT(project_id) DO UPDATE SET attempts=1 WHERE resource_build.phase='y'`, "resource_build"), ","); got != "project_id,phase,attempts" {
		t.Fatal("the write scanner is broken:", got)
	}
	if len(kitQueues) == 0 {
		t.Fatal("no work kit queue is registered")
	}
	// Typed queries (queries/*.sql) that write an owned column may be called only from its owner file.
	owned := map[string]designOwner{} // query name -> owner
	sqlFiles, err := filepath.Glob("queries/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sqlFiles {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range strings.Split(string(raw), "-- name: ")[1:] {
			name, body, _ := strings.Cut(part, "\n")
			name = strings.Fields(name)[0]
			for _, o := range designStateOwners() {
				for _, c := range designWrites(body, o.table) {
					if slices.Contains(o.columns, c) {
						owned[name] = o
					}
				}
			}
		}
	}
	if len(owned) == 0 {
		t.Fatal("the query scanner found no owned write in queries/*.sql")
	}
	for _, f := range designSources(t, false) {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if o, ok := owned[sel.Sel.Name]; ok && f.name != o.owner {
					t.Errorf("%s uses query %s, which writes %s state only %s may write (DESIGN_RULES.md S5)", f.name, sel.Sel.Name, o.table, o.owner)
				}
			}
			return true
		})
		for _, sql := range designLiterals(f.ast) {
			for _, o := range designStateOwners() {
				if f.name == o.owner {
					continue
				}
				for _, c := range designWrites(sql, o.table) {
					if slices.Contains(o.columns, c) {
						t.Errorf("%s writes %s.%s, which only %s may write (DESIGN_RULES.md S5): %.120s", f.name, o.table, c, o.owner, strings.Join(strings.Fields(sql), " "))
					}
				}
			}
		}
	}
}

// S6: in the kit's areas only the kit schedules: tickers, timers, sleeps, its own connections, randomness, every() and
// the statement clock (time reaches their SQL only as the kit's $now).
func TestDesignScheduling(t *testing.T) {
	banned := map[string]bool{"time.NewTicker": true, "time.After": true, "time.NewTimer": true, "time.Sleep": true, "time.Tick": true, "time.AfterFunc": true, "pgx.Connect": true, "every": true}
	seen := 0
	for _, f := range designSources(t, false) {
		if !designKitArea(f.name) && f.name != "work_kit.go" {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			name := ""
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok {
					name = id.Name + "." + x.Sel.Name
					if id.Name == "rand" {
						banned[name] = true
					}
				}
			case *ast.GoStmt:
				name = "go"
				banned[name] = true
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok {
					name = id.Name
				}
			}
			if banned[name] {
				if f.name == "work_kit.go" {
					seen++
				} else {
					t.Errorf("%s uses %s; only the work kit schedules (DESIGN_RULES.md S6)", f.name, name)
				}
			}
			return true
		})
		for _, sql := range designLiterals(f.ast) {
			if strings.Contains(sql, "statement_timestamp()") && f.name != "work_kit.go" {
				t.Errorf("%s reads the statement clock; queue SQL gets the kit's $now (DESIGN_RULES.md S6): %.80s", f.name, sql)
			}
		}
	}
	if seen == 0 {
		t.Fatal("the scheduling scanner found nothing in work_kit.go")
	}
}

// S5: the transition tables (resource build and repair, execution, question, experiment) cover the schema's states: every
// state has a row or is declared terminal, every target is a state, every state is reachable and reaches a terminal one,
// and buildTransition moves only to declared phases.
func TestDesignTransitions(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`phase text NOT NULL DEFAULT '(\w+)' CHECK\(phase IN \(([^)]*)\)\)`).FindSubmatch(schema)
	if m == nil {
		t.Fatal("resource_build.phase check not found in schema.sql")
	}
	var phases []string
	for _, p := range strings.Split(string(m[2]), ",") {
		phases = append(phases, strings.Trim(strings.TrimSpace(p), "'"))
	}
	if !strings.Contains(string(schema), `ADD COLUMN ready boolean GENERATED ALWAYS AS (phase = 'ready') STORED`) {
		t.Error("resource_build.ready must be generated from phase (migration 76)")
	}
	check := func(name string, table map[string][]string, states []string, ends func(string) bool, starts ...string) {
		if ends == nil {
			ends = func(s string) bool { return len(table[s]) == 0 }
		}
		for _, s := range states {
			if _, ok := table[s]; !ok {
				t.Errorf("%s: state %s has no transition row (declare it terminal with nil)", name, s)
			}
		}
		for s, targets := range table {
			if !slices.Contains(states, s) {
				t.Errorf("%s: row %s is no state", name, s)
			}
			for _, to := range targets {
				if !slices.Contains(states, to) {
					t.Errorf("%s: %s -> %s is no state", name, s, to)
				}
			}
		}
		reach := map[string]bool{}
		var walk func(string)
		walk = func(s string) {
			if !reach[s] {
				reach[s] = true
				for _, to := range table[s] {
					walk(to)
				}
			}
		}
		for _, s := range starts {
			walk(s)
		}
		for _, s := range states {
			if !reach[s] {
				t.Errorf("%s: state %s is unreachable", name, s)
			}
			seen := map[string]bool{}
			var down func(string) bool
			down = func(x string) bool {
				if ends(x) {
					return true
				}
				if seen[x] {
					return false
				}
				seen[x] = true
				for _, to := range table[x] {
					if to != x && down(to) {
						return true
					}
				}
				return false
			}
			if !down(s) {
				t.Errorf("%s: state %s never reaches a terminal state", name, s)
			}
		}
	}
	check("build", buildTransitions, phases, nil, string(m[1]), "clearing")
	check("repair", repairTransitions, []string{"enumerating", "done", "deleted"}, nil, "enumerating")
	cm := regexp.MustCompile(`CREATE TYPE command_status AS ENUM \(([^)]*)\)`).FindSubmatch(schema)
	if cm == nil {
		t.Fatal("command_status enum not found in schema.sql")
	}
	var statuses []string
	for _, p := range strings.Split(string(cm[1]), ",") {
		statuses = append(statuses, strings.Trim(strings.TrimSpace(p), "'"))
	}
	check("command", commandTransitions, statuses, nil, "pending")
	enum := func(re string) []string {
		m := regexp.MustCompile(re).FindSubmatch(schema)
		if m == nil {
			t.Fatalf("%s not found in schema.sql", re)
		}
		var out []string
		for _, v := range strings.Split(string(m[1]), ",") {
			out = append(out, strings.Trim(strings.TrimSpace(v), "'"))
		}
		return out
	}
	executionStates := enum(`CREATE TYPE execution_state AS ENUM \(([^)]*)\)`)
	check("execution", executionTransitions, executionStates, nil, "preparing")
	check("pipeline step", pipelineStepTransitions, executionStates, executionEnds, "preparing")
	check("question", questionTransitions, enum(`CREATE TYPE question_state AS ENUM \(([^)]*)\)`), nil, "open")
	check("experiment", experimentTransitions, enum(`state text NOT NULL DEFAULT 'draft' CHECK \(state IN \(([^)]*)\)\)`), nil, "draft")
	src, err := os.ReadFile("resource_states.go")
	if err != nil {
		t.Fatal(err)
	}
	targets := regexp.MustCompile(`\bto\("(\w+)"`).FindAllSubmatch(src, -1)
	if len(targets) == 0 {
		t.Fatal("no buildTransition targets found")
	}
	for _, x := range targets {
		if !slices.Contains(phases, string(x[1])) {
			t.Errorf("buildTransition moves to %s, which is no phase", x[1])
		}
	}
}

// S6: code of the kit's areas never opens a transaction; the kit gives each step, selection, failure and restart one.
func TestDesignTransactions(t *testing.T) {
	opens := map[string]bool{"runTx": true, "runTxLocks": true, "runTxLocksOnce": true, "Tx": true, "TxLocks": true, "TxLocksOnce": true, "Read": true, "Write": true, "Begin": true, "BeginTx": true, "BeginFunc": true, "BeginTxFunc": true}
	for _, f := range designSources(t, false) {
		if !designKitArea(f.name) {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch x := call.Fun.(type) {
			case *ast.Ident:
				name = x.Name
			case *ast.SelectorExpr:
				name = x.Sel.Name
				if r, ok := x.X.(*ast.SelectorExpr); (name == "Read" || name == "Write") && (!ok || r.Sel.Name != "store") {
					name = "" // an io.Writer, not store.Read/Write
				}
			}
			if opens[name] {
				t.Errorf("%s: %s opens a transaction; work runs in the kit's (DESIGN_RULES.md S6)", f.name, name)
			}
			return true
		})
	}
}

// S10: DESIGN_RULES.md names every TestDesign* and TestLaw* check, and every check it names exists.
func TestDesignRulesList(t *testing.T) {
	raw, err := os.ReadFile("DESIGN_RULES.md")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	rows := 0
	for _, line := range strings.Split(string(raw), "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 5 || !regexp.MustCompile(`^\s*S\d+\s*$`).MatchString(cells[1]) {
			continue
		}
		rows++
		for _, m := range regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(cells[3], -1) {
			listed[m[1]] = true
		}
	}
	if rows != 10 {
		t.Fatalf("DESIGN_RULES.md has %d rule rows, want S1-S10", rows)
	}
	tests := map[string]bool{}
	for _, f := range designSources(t, true) {
		for _, d := range f.ast.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && strings.HasPrefix(fn.Name.Name, "Test") {
				tests[fn.Name.Name] = true
			}
		}
	}
	for name := range listed {
		if !tests[name] {
			t.Errorf("DESIGN_RULES.md lists %s, which does not exist", name)
		}
	}
	for name := range tests {
		if (strings.HasPrefix(name, "TestDesign") || strings.HasPrefix(name, "TestLaw")) && !listed[name] {
			t.Errorf("%s is not listed in DESIGN_RULES.md", name)
		}
	}
}

// designRulesOwned: file -> rule -> why the file owns what the rule keeps out of every other file. Not legacy: these
// entries stay.
var designRulesOwned = map[string]map[string]string{
	"work_kit.go": {
		"R-GO":  "S6: the kit's loop and its fleet_work listener are the scheduling the rule keeps out of the queues",
		"R-SQL": "S6: the kit alone writes attempts, next_attempt_at and last_error of any queue's table, named by the queue; sqlc cannot name a table per call",
	},
}

// designRulesLegacy: file -> rules it may still break, generated from the tree
// at stage 0. Entries are only removed.
var designRulesLegacy = map[string]map[string]bool{
	"accounts.go":            {"R-MAP": true, "R-SQL": true},
	"agent.go":               {"R-MAP": true, "R-SQL": true},
	"agent_receipts.go":      {"R-SQL": true},
	"agents.go":              {"R-MAP": true, "R-SQL": true},
	"analytics.go":           {"R-SQL": true},
	"artifact_routes.go":     {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"artifacts.go":           {"R-MAP": true, "R-SQL": true},
	"attachments.go":         {"R-MAP": true, "R-SQL": true},
	"browser_checks.go":      {"R-MAP": true, "R-SQL": true},
	"bugsink.go":             {"R-MAP": true, "R-SQL": true},
	"catalogue_evidence.go":  {"R-MAP": true, "R-SQL": true},
	"cleanup.go":             {"R-MAP": true, "R-SQL": true},
	"connections.go":         {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"definitions.go":         {"R-MAP": true, "R-SQL": true},
	"definitions_usage.go":   {"R-MAP": true, "R-SQL": true},
	"definitions_write.go":   {"R-MAP": true, "R-SQL": true},
	"delegation.go":          {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"delivery.go":            {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"delivery_history.go":    {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"dispatch.go":            {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"entity_card.go":         {"R-MAP": true, "R-SQL": true},
	"entity_codes.go":        {"R-MAP": true, "R-SQL": true},
	"event_batch.go":         {"R-MAP": true, "R-SQL": true},
	"execution_trace.go":     {"R-MAP": true, "R-SQL": true},
	"executions.go":          {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"github.go":              {"R-GO": true, "R-MAP": true},
	"health.go":              {"R-MAP": true},
	"host_events.go":         {"R-MAP": true, "R-SQL": true},
	"host_poll.go":           {"R-MAP": true, "R-SQL": true},
	"host_poll_health.go":    {"R-MAP": true, "R-SQL": true},
	"host_receipts.go":       {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"host_remove.go":         {"R-MAP": true, "R-SQL": true},
	"hosts.go":               {"R-MAP": true, "R-SQL": true},
	"json.go":                {"R-MAP": true},
	"lab.go":                 {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"lab_assessment.go":      {"R-MAP": true, "R-SQL": true},
	"lab_compare.go":         {"R-MAP": true, "R-SQL": true},
	"lab_loop.go":            {"R-MAP": true, "R-SQL": true},
	"lab_pairwise.go":        {"R-MAP": true},
	"lab_retry.go":           {"R-MAP": true, "R-SQL": true},
	"lab_variant.go":         {"R-MAP": true},
	"limits.go":              {"R-MAP": true, "R-SQL": true},
	"limits_refresh.go":      {"R-MAP": true, "R-SQL": true},
	"main.go":                {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"nav_counts.go":          {"R-MAP": true, "R-SQL": true},
	"people.go":              {"R-MAP": true, "R-SQL": true},
	"person_settings.go":     {"R-MAP": true},
	"pipeline_condition.go":  {"R-MAP": true},
	"pipeline_plan.go":       {"R-MAP": true, "R-SQL": true},
	"pipelines.go":           {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"placement.go":           {"R-MAP": true, "R-SQL": true},
	"presets.go":             {"R-MAP": true},
	"problems.go":            {"R-MAP": true, "R-SQL": true},
	"project_access.go":      {"R-MAP": true, "R-SQL": true},
	"project_bundle.go":      {"R-MAP": true},
	"project_definitions.go": {"R-MAP": true, "R-SQL": true},
	"projects.go":            {"R-MAP": true, "R-SQL": true},
	"providers.go":           {"R-MAP": true},
	"records.go":             {"R-MAP": true, "R-SQL": true},
	"registry.go":            {"R-MAP": true},
	"release.go":             {"R-MAP": true, "R-SQL": true},
	"resource_apply.go":      {"R-SQL": true},
	"resource_projection.go": {"R-SQL": true},
	"rules.go":               {"R-MAP": true, "R-SQL": true},
	"runners.go":             {"R-MAP": true, "R-SQL": true},
	"runners_ci.go":          {"R-GO": true, "R-MAP": true},
	"s3.go":                  {"R-GO": true},
	"search.go":              {"R-MAP": true, "R-SQL": true},
	"sessions.go":            {"R-MAP": true, "R-SQL": true},
	"store.go":               {"R-MAP": true, "R-SQL": true},
	"stream.go":              {"R-GO": true, "R-SQL": true},
	"stream_shared.go":       {"R-GO": true, "R-SQL": true},
	"system_activity.go":     {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"tasks.go":               {"R-MAP": true, "R-SQL": true},
	"telemetry.go":           {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"threads.go":             {"R-MAP": true, "R-SQL": true},
	"uploads.go":             {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"versions.go":            {"R-GO": true, "R-MAP": true},
	"view_link.go":           {"R-MAP": true},
	"wake_outbox.go":         {"R-GO": true, "R-SQL": true},
	"watch.go":               {"R-MAP": true, "R-SQL": true},
}
