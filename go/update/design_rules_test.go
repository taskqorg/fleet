package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Documented owner exemptions for updater AST rules (S10). Every entry must match an observed hit; new violations
// outside these files fail TestDesignRules rather than growing silently.
var designUpdateOwned = map[string]map[string]string{
	"httpclient.go": {
		"U-HTTP": "sole owner of http.Client definitions via httpClient",
	},
}

// Requirement: docs/designs/fleet-standard.md S10 (typed payloads instead of map[string]any, goroutines through
// errtrack.Go, HTTP clients through httpClient in httpclient.go).
// Risk: untyped map[string]any payloads, bare go statements, or ad-hoc http.Client constructions reappear in
// non-test updater code without review.
func TestDesignRules(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	fset := token.NewFileSet()
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
		hits := updaterDesignRuleHits(f)
		rules := make([]string, 0, len(hits))
		for r := range hits {
			rules = append(rules, r)
		}
		slices.Sort(rules)
		for _, rule := range rules {
			if designUpdateOwned[file][rule] != "" {
				if seenOwned[file] == nil {
					seenOwned[file] = map[string]bool{}
				}
				seenOwned[file][rule] = true
				continue
			}
			for _, pos := range hits[rule] {
				at := fset.Position(pos)
				t.Errorf("%s:%d: %s violation (see docs/designs/fleet-standard.md S10)", file, at.Line, rule)
			}
		}
	}
	for file, rules := range designUpdateOwned {
		for rule := range rules {
			if !seenOwned[file][rule] {
				t.Errorf("stale designUpdateOwned exemption %s[%s]: no violation present", file, rule)
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

func unwrapParens(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = p.X
	}
}

func updaterDesignRuleHits(f *ast.File) map[string][]token.Pos {
	hits := map[string][]token.Pos{}
	record := func(rule string, pos token.Pos) {
		hits[rule] = append(hits[rule], pos)
	}
	httpNames, httpDot := importIdents(f, "net/http")
	isPkgSym := func(names map[string]bool, dot bool, expr ast.Expr, syms ...string) bool {
		switch e := unwrapParens(expr).(type) {
		case *ast.SelectorExpr:
			id, ok := unwrapParens(e.X).(*ast.Ident)
			return ok && names[id.Name] && slices.Contains(syms, e.Sel.Name)
		case *ast.Ident:
			return dot && slices.Contains(syms, e.Name)
		}
		return false
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			record("U-GO", node.Go)
		case *ast.MapType:
			k, kok := unwrapParens(node.Key).(*ast.Ident)
			val := unwrapParens(node.Value)
			v, vok := val.(*ast.Ident)
			iface, iok := val.(*ast.InterfaceType)
			if kok && k.Name == "string" && (vok && v.Name == "any" || iok && (iface.Methods == nil || len(iface.Methods.List) == 0)) {
				record("U-MAP", node.Pos())
			}
		case *ast.CompositeLit:
			if isPkgSym(httpNames, httpDot, node.Type, "Client") {
				record("U-HTTP", node.Pos())
			}
		case *ast.SelectorExpr:
			if isPkgSym(httpNames, httpDot, node, "DefaultClient", "Get", "Post", "PostForm", "Head") {
				record("U-HTTP", node.Pos())
			}
		case *ast.CallExpr:
			if id, ok := unwrapParens(node.Fun).(*ast.Ident); ok && id.Name == "new" && len(node.Args) == 1 && isPkgSym(httpNames, httpDot, node.Args[0], "Client") {
				record("U-HTTP", node.Pos())
			}
		case *ast.ValueSpec:
			if node.Type != nil && isPkgSym(httpNames, httpDot, node.Type, "Client") {
				record("U-HTTP", node.Type.Pos())
			}
		case *ast.Field:
			if isPkgSym(httpNames, httpDot, node.Type, "Client") {
				record("U-HTTP", node.Type.Pos())
			}
		}
		return true
	})
	return hits
}
