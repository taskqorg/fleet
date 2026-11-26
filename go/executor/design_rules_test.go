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

// Documented owner exemptions for executor AST rules (S7/S10). Every entry must match an observed hit; new violations
// outside these files fail TestDesignRules rather than growing silently.
var designExecutorOwned = map[string]map[string]string{
	"cleanup.go": {
		"E-PROC": "listing and removing SysV shared memory of killed run processes via /usr/bin/ipcs and ipcrm",
	},
	"local_agent.go": {
		"E-PROC": "running the person's own harness CLI (claude mcp, codex mcp) to register Fleet's servers in its settings",
	},
	"coding_tools.go": {
		"E-PROC": "probing installed coding tool binaries (--version)",
	},
	"executor_launch.go": {
		"E-PROC": "harness --version/tools capability probes and git symbolic-ref for forge branch context",
	},
	"project_env.go": {
		"E-PROC": "running bounded podman commands of the project dev environment (context deadlines; the pod is the owned lifecycle)",
	},
	"forge.go": {
		"E-PROC": "running git/gh forge CLI helpers and reading gh auth token",
	},
	"harness_codex.go": {
		"E-PROC": "creating the Codex app-server exec.Cmd launched through supervise.Spawn",
	},
	"harness_command.go": {
		"E-PROC": "constructing the per-execution harness exec.Cmd launched through supervise.Spawn",
	},
	"host_readings_darwin.go": {
		"E-PROC": "sampling macOS memory pressure via /usr/bin/vm_stat",
	},
	"host_consumers.go": {
		"E-PROC": "sampling the top memory consumers via /bin/ps",
	},
	"httpclient.go": {
		"E-HTTP": "sole owner of http.Client definitions (httpClient, streamClient) with NoRedirect and sharedTransport",
	},
	"mcp_code.go": {
		"E-PROC": "creating the Node code-mode runtime exec.Cmd launched through supervise.Spawn and bounded node --version/probe calls",
	},
	"mcp_code_host.go": {
		"E-PROC": "invoking bounded code-shell subprocesses and running the guardian's direct shell command",
	},
	"mcp_stdio.go": {
		"E-PROC": "creating external MCP server exec.Cmds launched through supervise.Spawn",
	},
	"native.go": {
		"E-PROC": "sampling running host processes via /bin/ps",
	},
	"preview.go": {
		"E-PROC": "running ffmpeg/unshare for preview availability checks and bounded transcoding",
	},
	"project_browser.go": {
		"E-PROC": "creating the Playwright browser MCP exec.Cmd launched through supervise.Spawn",
	},
	"release_harnesses.go": {
		"E-PROC": "probing installed harness binary versions with CommandContext",
	},
	"worktree.go": {
		"E-PROC": "running synchronous git worktree commands and bounded setup/teardown project scripts",
	},
}

// Requirement: docs/designs/fleet-standard.md S7 (typed boundaries instead of untyped map helpers), S10 (child processes
// through supervise.Spawn, goroutines through errtrack.Go, HTTP clients through httpclient.go).
// Risk: untyped map accessors (jStr/jObj/jBoolOr), bare go statements, unowned exec.Command calls, or ad-hoc
// http.Client literals reappear in non-test executor code without review.
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
		hits := executorDesignRuleHits(f)
		rules := make([]string, 0, len(hits))
		for r := range hits {
			rules = append(rules, r)
		}
		slices.Sort(rules)
		for _, rule := range rules {
			if designExecutorOwned[file][rule] != "" {
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
	for file, rules := range designExecutorOwned {
		for rule := range rules {
			if !seenOwned[file][rule] {
				t.Errorf("stale designExecutorOwned exemption %s[%s]: no violation present", file, rule)
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

func executorDesignRuleHits(f *ast.File) map[string][]token.Pos {
	hits := map[string][]token.Pos{}
	record := func(rule string, pos token.Pos) {
		hits[rule] = append(hits[rule], pos)
	}
	execNames, execDot := importIdents(f, "os/exec")
	httpNames, httpDot := importIdents(f, "net/http")
	isPkgSym := func(names map[string]bool, dot bool, expr ast.Expr, syms ...string) bool {
		switch e := expr.(type) {
		case *ast.SelectorExpr:
			id, ok := e.X.(*ast.Ident)
			return ok && names[id.Name] && slices.Contains(syms, e.Sel.Name)
		case *ast.Ident:
			return dot && slices.Contains(syms, e.Name)
		}
		return false
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			record("E-GO", node.Go)
		case *ast.CompositeLit:
			if isPkgSym(httpNames, httpDot, node.Type, "Client") {
				record("E-HTTP", node.Pos())
			}
		case *ast.SelectorExpr:
			if isPkgSym(httpNames, httpDot, node, "DefaultClient", "Get", "Post", "PostForm", "Head") {
				record("E-HTTP", node.Pos())
			}
		case *ast.CallExpr:
			if id, ok := node.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "jStr", "jObj", "jBoolOr":
					record("E-MAP", node.Pos())
				}
			}
			if isPkgSym(execNames, execDot, node.Fun, "Command", "CommandContext") {
				record("E-PROC", node.Pos())
			}
		}
		return true
	})
	return hits
}
