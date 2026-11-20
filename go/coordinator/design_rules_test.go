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
	"sort"
	"strconv"
	"strings"
	"testing"
)

var designSQLStarts = []string{"select", "insert", "update", "delete", "with"}

var designMapCalls = map[string]bool{"astr": true, "anys": true, "mapOf": true}

var designTimeCalls = map[string]bool{"NewTicker": true, "Tick": true, "Sleep": true, "AfterFunc": true}

var designOpCalls = map[string]bool{"gatedOp": true, "gatedObjOp": true, "shaped": true, "operation": true, "agentTool": true}

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
			if !designRulesLegacy[file][rule] {
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

// designRulesLegacy: file -> rules it may still break, generated from the tree
// at stage 0. Entries are only removed.
var designRulesLegacy = map[string]map[string]bool{
	"accounts.go":            {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"agent.go":               {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"agent_receipts.go":      {"R-SQL": true},
	"agents.go":              {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"analytics.go":           {"R-OP": true, "R-SQL": true},
	"artifact_routes.go":     {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"artifacts.go":           {"R-MAP": true, "R-SQL": true},
	"attachments.go":         {"R-MAP": true, "R-SQL": true},
	"browser_checks.go":      {"R-MAP": true, "R-SQL": true},
	"budgets.go":             {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"bugsink.go":             {"R-MAP": true, "R-SQL": true},
	"catalogue_evidence.go":  {"R-MAP": true, "R-SQL": true},
	"cleanup.go":             {"R-MAP": true, "R-SQL": true},
	"connections.go":         {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"definitions.go":         {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"definitions_usage.go":   {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"definitions_write.go":   {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"delegation.go":          {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"delivery.go":            {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"delivery_history.go":    {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"dispatch.go":            {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"entity_card.go":         {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"entity_codes.go":        {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"event_batch.go":         {"R-MAP": true, "R-SQL": true},
	"execution_trace.go":     {"R-MAP": true, "R-SQL": true},
	"executions.go":          {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"github.go":              {"R-GO": true, "R-MAP": true},
	"health.go":              {"R-MAP": true},
	"host_events.go":         {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"host_poll.go":           {"R-MAP": true, "R-SQL": true},
	"host_poll_health.go":    {"R-MAP": true, "R-SQL": true},
	"host_receipts.go":       {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"host_remove.go":         {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"hosts.go":               {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"json.go":                {"R-MAP": true},
	"lab.go":                 {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"lab_assessment.go":      {"R-MAP": true, "R-SQL": true},
	"lab_compare.go":         {"R-MAP": true, "R-SQL": true},
	"lab_loop.go":            {"R-MAP": true, "R-SQL": true},
	"lab_pairwise.go":        {"R-MAP": true},
	"lab_retry.go":           {"R-MAP": true, "R-SQL": true},
	"lab_variant.go":         {"R-MAP": true},
	"limits.go":              {"R-MAP": true, "R-SQL": true},
	"limits_refresh.go":      {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"main.go":                {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"nav_counts.go":          {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"ops.go":                 {"R-OP": true},
	"people.go":              {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"person_settings.go":     {"R-MAP": true, "R-OP": true},
	"pipeline_condition.go":  {"R-MAP": true},
	"pipeline_plan.go":       {"R-MAP": true, "R-SQL": true},
	"pipelines.go":           {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"placement.go":           {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"presets.go":             {"R-MAP": true},
	"prices.go":              {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"problems.go":            {"R-MAP": true, "R-SQL": true},
	"project_access.go":      {"R-MAP": true, "R-SQL": true},
	"project_bundle.go":      {"R-MAP": true},
	"project_definitions.go": {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"projects.go":            {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"providers.go":           {"R-MAP": true},
	"records.go":             {"R-MAP": true, "R-SQL": true},
	"registry.go":            {"R-MAP": true},
	"release.go":             {"R-MAP": true, "R-SQL": true},
	"resource_apply.go":      {"R-SQL": true},
	"resource_projection.go": {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"rules.go":               {"R-MAP": true, "R-SQL": true},
	"runners.go":             {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"runners_ci.go":          {"R-GO": true, "R-MAP": true, "R-OP": true},
	"s3.go":                  {"R-GO": true},
	"search.go":              {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"session_titles.go":      {"R-MAP": true, "R-SQL": true},
	"sessions.go":            {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"store.go":               {"R-MAP": true, "R-SQL": true},
	"stream.go":              {"R-GO": true, "R-SQL": true},
	"stream_shared.go":       {"R-GO": true, "R-SQL": true},
	"system_activity.go":     {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"tasks.go":               {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"telemetry.go":           {"R-GO": true, "R-MAP": true, "R-SQL": true},
	"threads.go":             {"R-MAP": true, "R-OP": true, "R-SQL": true},
	"uploads.go":             {"R-GO": true, "R-MAP": true, "R-OP": true, "R-SQL": true},
	"versions.go":            {"R-GO": true, "R-MAP": true, "R-OP": true},
	"view_link.go":           {"R-MAP": true},
	"wake_outbox.go":         {"R-GO": true, "R-SQL": true},
	"watch.go":               {"R-MAP": true, "R-SQL": true},
}
