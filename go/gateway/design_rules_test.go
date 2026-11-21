package main

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Documented owner exemptions for gateway AST rules (S10). Every entry must match an observed hit; new violations
// outside these files fail TestDesignRules rather than growing silently.
var designGatewayOwned = map[string]map[string]string{}

// Requirement: docs/designs/fleet-standard.md S10 (typed JSON boundaries without map[string]any, goroutines through
// errtrack.Go, and no child processes in the key-holding gateway).
// Risk: untyped map[string]any/interface{} objects, bare unrecovered go statements, or exec.Command subprocesses
// reappear in non-test gateway code without review.
func TestDesignRules(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	fset := gotoken.NewFileSet()
	seenOwned := map[string]map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if ast.IsGenerated(f) {
			continue
		}
		hits := gatewayDesignRuleHits(f)
		rules := make([]string, 0, len(hits))
		for r := range hits {
			rules = append(rules, r)
		}
		slices.Sort(rules)
		for _, rule := range rules {
			if designGatewayOwned[file][rule] != "" {
				if seenOwned[file] == nil {
					seenOwned[file] = map[string]bool{}
				}
				seenOwned[file][rule] = true
				continue
			}
			for _, pos := range hits[rule] {
				at := fset.Position(pos)
				t.Errorf("%s:%d: %s violation (see docs/designs/fleet-standard.md)", file, at.Line, rule)
			}
		}
	}
	for file, rules := range designGatewayOwned {
		for rule := range rules {
			if !seenOwned[file][rule] {
				t.Errorf("stale designGatewayOwned exemption %s[%s]: no violation present", file, rule)
			}
		}
	}
}

func importIdents(f *ast.File, pkgPath string) (map[string]bool, bool) {
	names := map[string]bool{}
	dot := false
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != pkgPath {
			continue
		}
		if imp.Name == nil {
			names[path.Base(pkgPath)] = true
			continue
		}
		switch imp.Name.Name {
		case "_":
		case ".":
			dot = true
		default:
			names[imp.Name.Name] = true
		}
	}
	return names, dot
}

func unwrapParen(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = p.X
	}
}

func gatewayDesignRuleHits(f *ast.File) map[string][]gotoken.Pos {
	hits := map[string][]gotoken.Pos{}
	record := func(rule string, pos gotoken.Pos) {
		hits[rule] = append(hits[rule], pos)
	}
	execNames, execDot := importIdents(f, "os/exec")
	isPkgSym := func(names map[string]bool, expr ast.Expr, syms ...string) bool {
		switch e := unwrapParen(expr).(type) {
		case *ast.SelectorExpr:
			id, ok := unwrapParen(e.X).(*ast.Ident)
			return ok && names[id.Name] && slices.Contains(syms, e.Sel.Name)
		}
		return false
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.MapType:
			k, kok := unwrapParen(node.Key).(*ast.Ident)
			val := unwrapParen(node.Value)
			v, vok := val.(*ast.Ident)
			iface, iok := val.(*ast.InterfaceType)
			if kok && k.Name == "string" && (vok && v.Name == "any" || iok && (iface.Methods == nil || len(iface.Methods.List) == 0)) {
				record("G-MAP", node.Pos())
			}
		case *ast.GoStmt:
			record("G-GO", node.Go)
		case *ast.SelectorExpr:
			if isPkgSym(execNames, node, "Command", "CommandContext") {
				record("G-PROC", node.Pos())
			}
		case *ast.CallExpr:
			if execDot {
				if id, ok := unwrapParen(node.Fun).(*ast.Ident); ok && slices.Contains([]string{"Command", "CommandContext"}, id.Name) {
					record("G-PROC", node.Pos())
				}
			}
		}
		return true
	})
	return hits
}
