package main

import (
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

var designTxCalls = map[string]bool{"runTx": true, "runTxLocks": true, "runTxLocksOnce": true, "Tx": true, "TxLocks": true, "TxLocksOnce": true, "txLocks": true, "readOnce": true, "writeOnce": true, "onEnd": true, "Begin": true, "BeginTx": true, "BeginFunc": true, "BeginTxFunc": true}

// designCallExprName returns the called identifier or pkg.Name of fun, unwrapping generic type arguments.
func designCallExprName(fun ast.Expr) string {
	switch g := fun.(type) {
	case *ast.IndexExpr:
		fun = g.X
	case *ast.IndexListExpr:
		fun = g.X
	}
	switch x := fun.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name + "." + x.Sel.Name
		}
	}
	return ""
}

// designCalleeName returns the final function or method name of fun, unwrapping generic type arguments.
func designCalleeName(fun ast.Expr) string {
	switch g := fun.(type) {
	case *ast.IndexExpr:
		fun = g.X
	case *ast.IndexListExpr:
		fun = g.X
	}
	switch x := fun.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}

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
			name := designCallExprName(x.Fun)
			if designMapCalls[name] {
				add("R-MAP", x)
			}
			if designOpCalls[name] {
				add("R-OP", x)
			}
			if sel, ok := strings.CutPrefix(name, "time."); ok && designTimeCalls[sel] {
				add("R-GO", x)
			}
			if designTxCalls[designCalleeName(x.Fun)] {
				add("R-TX", x)
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
			if designRulesOwned[file][rule] == "" {
				t.Errorf("%s: breaks %s (DESIGN_RULES.md, %d places in the file); move it onto the standard", fset.Position(pos[0]), rule, len(pos))
			}
		}
	}
	for file, rules := range designRulesOwned {
		for rule := range rules {
			if len(found[file][rule]) == 0 {
				t.Errorf("%s no longer breaks %s: remove it from designRulesOwned", file, rule)
			}
		}
	}
}

// The S5/S6 checks of the work kit and its queues (DESIGN_RULES.md). designKitAreas: the file-name prefixes of the
// areas whose item work runs on the kit; an area moving onto the kit adds its prefix.
var designKitAreas = []string{"resource_", "pipeline_work", "delegation_work", "upload_work", "access_work"}

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
		f, err := parser.ParseFile(fset, n, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if !tests && ast.IsGenerated(f) {
			continue
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
		{"task", "tasks.go", []string{"state"}},
		{"question", "question_states.go", []string{"state"}},
		{"question", "problems.go", []string{"cause"}},
		{"experiment", "experiment_states.go", []string{"state"}},
		{"host", "host_states.go", []string{"state"}},
		{"connection_key", "key_states.go", []string{"state"}},
		{"access_grant", "access_states.go", []string{"state"}},
		{"artifact_copy", "artifact_states.go", []string{"state"}},
		{"store", "artifact_states.go", []string{"state"}},
		{"bot_run", "bot_states.go", []string{"state"}},
		{"audit_log", "audit.go", []string{"target"}}, // S4: the audit is written only through audit()
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

var (
	designBackgroundHelpers    = map[string]bool{"every": true, "everyWake": true, "coalesced": true, "untilDone": true, "parallel": true, "retryCall": true}
	designBackgroundOwnedFuncs = map[string]bool{"everyWake": true, "coalesced": true, "untilDone": true, "parallel": true, "retryCall": true}
	designBackgroundStartCalls = map[string]bool{"every": true, "everyWake": true, "untilDone": true}
	designSchedulingBanned     = map[string]bool{
		"time.NewTicker": true, "time.After": true, "time.NewTimer": true,
		"time.Sleep": true, "time.Tick": true, "time.AfterFunc": true,
		"pgx.Connect": true, "go": true,
		"every": true, "everyWake": true, "coalesced": true, "untilDone": true,
	}
)

func designSchedulingHit(n ast.Node, includeRand bool) string {
	switch x := n.(type) {
	case *ast.GoStmt:
		return "go"
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			name := id.Name + "." + x.Sel.Name
			if (includeRand && id.Name == "rand") || designSchedulingBanned[name] {
				return name
			}
		}
	case *ast.CallExpr:
		if name := designCallExprName(x.Fun); designBackgroundHelpers[name] {
			return name
		}
	}
	return ""
}

func designCheckBackgroundFile(t *testing.T, f *ast.File) {
	t.Helper()
	defined := map[string]bool{}
	bgHits := map[string]int{}
	everyDelegates := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			ast.Inspect(decl, func(n ast.Node) bool {
				if name := designSchedulingHit(n, false); name != "" && !designBackgroundHelpers[name] {
					t.Errorf("background.go uses %s outside its owned helpers (DESIGN_RULES.md S6)", name)
				}
				return true
			})
			continue
		}
		fnName := fn.Name.Name
		defined[fnName] = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if fnName == "every" {
				if call, ok := n.(*ast.CallExpr); ok && designCallExprName(call.Fun) == "everyWake" {
					everyDelegates = true
				}
			}
			if name := designSchedulingHit(n, false); name != "" && !designBackgroundHelpers[name] {
				if designBackgroundOwnedFuncs[fnName] {
					bgHits[fnName]++
				} else {
					t.Errorf("background.go (%s) uses %s outside its owned helpers (DESIGN_RULES.md S6)", fnName, name)
				}
			}
			return true
		})
	}
	for name := range designBackgroundHelpers {
		if !defined[name] {
			t.Errorf("background.go must define %s (DESIGN_RULES.md S6)", name)
		}
	}
	if !everyDelegates {
		t.Error("background.go: every() must delegate to everyWake() (DESIGN_RULES.md S6)")
	}
	for name := range designBackgroundOwnedFuncs {
		if bgHits[name] == 0 {
			t.Errorf("background.go: owned helper %s has no scheduling hit (stale exemption)", name)
		}
	}
}

// designStartupFuncs returns the package-level functions registered via onStart(...) or called solely from main()
// or an onStart(...) callback, excluding general helpers used elsewhere and callbacks passed into background helpers.
func designStartupFuncs(sources []designFile) map[string]bool {
	startup := map[string]bool{"main": true}
	calledInStartup := map[string]bool{}
	usedOutside := map[string]bool{}
	var walkStartup func(n ast.Node)
	var walkOther func(n ast.Node, inInit bool)
	handleOnStart := func(call *ast.CallExpr, inInit bool) bool {
		if !inInit || designCallExprName(call.Fun) != "onStart" || len(call.Args) != 1 {
			return false
		}
		switch a := call.Args[0].(type) {
		case *ast.Ident:
			startup[a.Name] = true
		case *ast.FuncLit:
			walkStartup(a.Body)
		default:
			walkOther(a, false)
		}
		return true
	}
	walkStartup = func(n ast.Node) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(cur ast.Node) bool {
			switch x := cur.(type) {
			case *ast.GoStmt:
				walkOther(x, false)
				return false
			case *ast.FuncLit:
				walkOther(x.Body, false)
				return false
			case *ast.CallExpr:
				name := designCallExprName(x.Fun)
				if name != "" && !strings.Contains(name, ".") && !designBackgroundHelpers[name] {
					calledInStartup[name] = true
				} else if name == "" || strings.Contains(name, ".") {
					walkOther(x.Fun, false)
				}
				for _, arg := range x.Args {
					if designBackgroundHelpers[name] {
						if fl, ok := arg.(*ast.FuncLit); ok {
							walkOther(fl.Body, false)
							continue
						}
					}
					walkStartup(arg)
				}
				return false
			}
			return true
		})
	}
	walkOther = func(n ast.Node, inInit bool) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(cur ast.Node) bool {
			switch x := cur.(type) {
			case *ast.FuncLit:
				walkOther(x.Body, false)
				return false
			case *ast.CallExpr:
				if handleOnStart(x, inInit) {
					return false
				}
			case *ast.Ident:
				usedOutside[x.Name] = true
			case *ast.SelectorExpr:
				walkOther(x.X, false)
				return false
			}
			return true
		})
	}
	for _, src := range sources {
		for _, decl := range src.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				walkOther(decl, false)
				continue
			}
			if fn.Recv == nil && fn.Name.Name == "main" {
				walkStartup(fn.Body)
				continue
			}
			walkOther(fn.Body, fn.Recv == nil && fn.Name.Name == "init")
		}
	}
	for name := range calledInStartup {
		if !usedOutside[name] {
			startup[name] = true
		}
	}
	return startup
}

func designCheckNonKitFile(t *testing.T, f designFile, startup map[string]bool) {
	t.Helper()
	for _, decl := range f.ast.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if name := designCallExprName(call.Fun); designBackgroundStartCalls[name] {
						t.Errorf("%s calls %s in a package variable initializer instead of onStart/main (DESIGN_RULES.md S6)", f.name, name)
					}
				}
				return true
			})
			continue
		}
		inInit := fn.Recv == nil && fn.Name.Name == "init"
		inStartup := fn.Recv == nil && startup[fn.Name.Name]
		var walk func(n ast.Node, okStartup, allowOnStart bool)
		walk = func(n ast.Node, okStartup, allowOnStart bool) {
			if n == nil {
				return
			}
			ast.Inspect(n, func(cur ast.Node) bool {
				switch x := cur.(type) {
				case *ast.FuncLit:
					walk(x.Body, false, false)
					return false
				case *ast.CallExpr:
					name := designCallExprName(x.Fun)
					if allowOnStart && name == "onStart" && len(x.Args) == 1 {
						if fl, ok := x.Args[0].(*ast.FuncLit); ok {
							walk(fl.Body, true, false)
							return false
						}
					}
					if designBackgroundStartCalls[name] && !okStartup {
						t.Errorf("%s (%s) calls %s outside startup (onStart/main) (DESIGN_RULES.md S6)", f.name, fn.Name.Name, name)
					}
					if name == "coalesced" && !okStartup {
						t.Errorf("%s (%s) calls coalesced outside package init/startup (DESIGN_RULES.md S6)", f.name, fn.Name.Name)
					}
				}
				return true
			})
		}
		walk(fn.Body, inStartup, inInit)
	}
}

// S6: background.go owns periodic passes (every/everyWake) and background helpers in its named functions; non-kit files
// schedule periodic passes only at startup; in the kit's areas only the kit schedules (no goroutines, tickers, timers,
// sleeps, own connections, randomness, background helpers or statement clock).
func TestDesignScheduling(t *testing.T) {
	sources := designSources(t, false)
	startup := designStartupFuncs(sources)
	seenKit := 0
	seenBg := false
	for _, f := range sources {
		if f.name == "background.go" {
			seenBg = true
			designCheckBackgroundFile(t, f.ast)
			continue
		}
		for _, decl := range f.ast.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && designBackgroundHelpers[fn.Name.Name] {
				t.Errorf("%s defines %s; background helpers belong only in background.go (DESIGN_RULES.md S6)", f.name, fn.Name.Name)
			}
		}
		if !designKitArea(f.name) && f.name != "work_kit.go" {
			designCheckNonKitFile(t, f, startup)
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			if name := designSchedulingHit(n, true); name != "" {
				if f.name == "work_kit.go" {
					seenKit++
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
	if !seenBg {
		t.Fatal("background.go was not scanned")
	}
	if seenKit == 0 {
		t.Fatal("the scheduling scanner found nothing in work_kit.go")
	}
}

// S5: the transition tables (resource build and repair, execution, question, experiment) cover the schema's states: every
// state has a row or is declared terminal, every target is a state, every state is reachable and reaches a terminal one,
// and buildTransition moves only to declared phases.
func TestDesignTransitions(t *testing.T) {
	schema := []byte(migrationsText())
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
	check("task", taskTransitions, enum(`CREATE TYPE task_state AS ENUM \(([^)]*)\)`), nil, "queued", "paused")
	check("question", questionTransitions, enum(`CREATE TYPE question_state AS ENUM \(([^)]*)\)`), nil, "open")
	check("experiment", experimentTransitions, enum(`state text NOT NULL DEFAULT 'draft' CHECK \(state IN \(([^)]*)\)\)`), nil, "draft")
	check("host", hostTransitions, enum(`state text NOT NULL DEFAULT 'active' CHECK \(state IN \(('active', 'removed')\)\)`), func(s string) bool { return s == "removed" }, "active")
	check("connection key", connectionKeyTransitions, enum(`state text NOT NULL DEFAULT 'active' CHECK \(state IN \(('active', 'disabled', 'retired')\)\)`), nil, "active")
	check("artifact copy", artifactCopyTransitions, enum(`CREATE TABLE artifact_copy\([\s\S]*?CHECK \(state IN \(([^)]*)\)\)`), func(s string) bool { return s == "deleted" }, "pending", "copying", "verified")
	check("store", storeTransitions, enum(`CREATE TABLE store\([\s\S]*?CHECK \(state IN \(([^)]*)\)\)`), func(s string) bool { return s == "unreachable" }, "reachable", "unreachable")
	check("access grant", accessGrantTransitions, enum(`CREATE TABLE access_grant \([\s\S]*?CHECK \(state IN \(([^)]*)\)\)`), nil, "asked")
	check("bot run", botRunTransitions, enum(`CREATE TABLE bot_run \([\s\S]*?CHECK \(state IN \(([^)]*)\)\)`), nil, "queued")
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
	opens := map[string]bool{"runTx": true, "runTxLocks": true, "runTxLocksOnce": true, "Tx": true, "TxLocks": true, "TxLocksOnce": true, "txLocks": true, "readOnce": true, "writeOnce": true, "Read": true, "Write": true, "Begin": true, "BeginTx": true, "BeginFunc": true, "BeginTxFunc": true}
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

// designRulesOwned: file -> rule -> why the file owns what the rule keeps out of every other file. Not legacy: these
// entries stay.
var designRulesOwned = map[string]map[string]string{
	"background.go": {
		"R-GO": "S6: every()/everyWake() is the one periodic-pass loop (everyWake also runs on a wake channel and at start), coalesced() the one runner of a coalesced after-commit effect (the gateway snapshot), untilDone() the one retried start-up step (the span schema), parallel() the one bounded fan-out inside a call (Delivery's side-by-side gateway reads) and retryCall() the one bounded retry of a remote call (an object store request)",
	},
	"main.go": {
		"R-GO": "S6: HTTP plumbing: the signal handler that stops the long polls and the stream and drains the server",
	},
	"store.go": {
		"R-TX": "S3: the database boundary itself, opening pgx transactions with advisory locks, scope, retry and afterCommit inside txLocks and migrateLocked",
	},
	"tx.go": {
		"R-TX": "S3: implements Store.Read, Store.Write, afterCommit, afterRollback and the package-private single-attempt readOnce/writeOnce entry points over Store.txLocks",
	},
	"wake_outbox.go": {
		"R-GO": "S6: the wake dispatcher, the one sender of wake_outbox's NOTIFYs: it claims the next batch at once while a full one was claimed, so it cannot be an every() pass",
		"R-TX": "S3: the wake dispatcher runs pgx.BeginTxFunc every 100 ms directly so empty recovery checks never touch Store's auth-cache invalidation",
	},
	"stream.go": {
		"R-GO": "S6: HTTP/stream plumbing: the LISTEN fleet_change connection (reconnected after a second), the feed loop that reads behind it, and the ping ticker of each open /api/stream",
	},
	"telemetry.go": {
		"R-SQL": "S2 covers PostgreSQL: these are the ClickHouse span-store queries (telemetry_summary, telemetry_trace, the purge's span deletes), which sqlc cannot generate",
	},
	"work_kit.go": {
		"R-GO":  "S6: the kit's loop and its fleet_work listener are the scheduling the rule keeps out of the queues",
		"R-SQL": "S6: the kit alone writes attempts, next_attempt_at and last_error of any queue's table, named by the queue; sqlc cannot name a table per call",
		"R-TX":  "S6: uses single-attempt store.readOnce/store.writeOnce so durable queue steps report serialization/deadlock errors directly to the kit's retry accounting and cohort bisection",
	},
	"rules.go": {
		"R-SQL": "S2 covers PostgreSQL: these are the ClickHouse span-store queries of the span rules (absence-host, absence-pass, error-spike, host-saturation), which sqlc cannot generate",
	},
	"sql_builder.go": {
		"R-SQL": "S2: the one place SQL is assembled at run time (resource item pages, analytics_query grouping and filters, list pages and their counts, delete and purge cascades), from allowlisted identifiers with bind parameters; sqlc cannot vary a statement's table, comparison, grouping or conditions",
	},
}
