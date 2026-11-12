package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func contextFixture(t *testing.T) (M, string, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatal(e, string(b))
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	git("config", "user.name", "fixture")
	git("config", "user.email", "fixture@example.test")
	git("remote", "add", "origin", "https://github.com/taskqorg/specq.git")
	os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("engineering/core.md filter=fixture\n"), 0600)
	os.MkdirAll(filepath.Join(root, "engineering"), 0700)
	os.WriteFile(filepath.Join(root, "engineering/core.md"), []byte("required-core\n"), 0600)
	os.WriteFile(filepath.Join(root, "engineering/area.md"), []byte("linked-area\n"), 0600)
	git("add", ".")
	git("commit", "-qm", "frozen source")
	revision := git("rev-parse", "HEAD")
	source := M{"repo": "specq", "revision": revision, "core": []any{M{"path": "engineering/core.md", "sha256": sha([]byte("required-core\n"))}}, "linked": []any{M{"path": "engineering/area.md", "sha256": sha([]byte("linked-area\n"))}}}
	project := M{"repos": []any{M{"id": "main", "remote": "https://github.com/taskqorg/fleet.git"}, M{"id": "specq", "remote": "https://github.com/taskqorg/specq"}}, "environments": []any{M{"hostId": "h", "repo": "specq", "checkoutPath": root}, M{"hostId": "h", "repo": "main", "checkoutPath": "/primary"}}, "contextSources": []any{source}}
	return project, root, revision
}

// R1, R3; risk: ambient/replaced/filtered working-tree data becomes canonical instruction content or runs code.
func TestAttemptContextFrozenGitAndHardening(t *testing.T) {
	p, root, revision := contextFixture(t)
	marker := filepath.Join(t.TempDir(), "should-not-execute")
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	os.WriteFile(filepath.Join(root, "engineering/core.md"), []byte("replacement"), 0600)
	git("add", ".")
	git("commit", "-qm", "replacement")
	b, _ := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	git("replace", revision, strings.TrimSpace(string(b)))
	git("config", "filter.fixture.smudge", "touch "+marker)
	git("config", "core.fsmonitor", marker)
	git("config", "core.sshCommand", "touch "+marker)
	git("config", "credential.helper", "!touch "+marker)
	os.Remove(filepath.Join(root, "engineering/core.md"))
	os.Symlink(marker, filepath.Join(root, "engineering/core.md"))
	input := t.TempDir()
	got, err := resolveAttemptContext(p, "h", []string{root}, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Core, "required-core") || strings.Contains(got.Core, "replacement") || strings.Contains(got.Core, "linked-area") || !strings.Contains(got.Index, "engineering/area.md") {
		t.Fatal(got)
	}
	if exists(marker) {
		t.Fatal("source executed hook/filter/credential command")
	}
	if _, err = resolveAttemptContext(p, "h", []string{root}, nil, input); err != nil {
		t.Fatal("same verified staged bytes should be reusable", err)
	}
	staged := filepath.Join(input, "context-sources/specq", revision, "engineering/core.md")
	bytes, _ := os.ReadFile(staged)
	if string(bytes) != "required-core\n" {
		t.Fatal(string(bytes))
	}
	if path, ok := repoPath(M{"project": p}, "h"); !ok || path != "/primary" {
		t.Fatal("source binding became primary checkout", path)
	}
}

// R3; risk: missing/private/forged sources consume a model or escape declared source/input roots.
func TestAttemptContextRejectsInvalidSources(t *testing.T) {
	p, root, _ := contextFixture(t)
	tests := map[string]func(M){"missing-pin": func(x M) { jObj(jArr(x, "contextSources")[0])["revision"] = strings.Repeat("a", 40) }, "digest": func(x M) { jObj(jArr(jArr(x, "contextSources")[0], "core")[0])["sha256"] = strings.Repeat("a", 64) }, "escape": func(x M) { jObj(jArr(jArr(x, "contextSources")[0], "core")[0])["path"] = "../private" }, "origin": func(x M) { jObj(jArr(x, "repos")[1])["remote"] = "https://github.com/foreign/private.git" }, "missing-env": func(x M) { x["environments"] = []any{} }, "unknown-field": func(x M) { jObj(jArr(x, "contextSources")[0])["command"] = "unexpected" }}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			x := jObj(jClone(p))
			mutate(x)
			if _, err := resolveAttemptContext(x, "h", []string{root}, nil, t.TempDir()); err == nil {
				t.Fatal("accepted invalid source")
			}
		})
	}
	if _, err := resolveAttemptContext(p, "h", []string{t.TempDir()}, nil, ""); err == nil {
		t.Fatal("outside roots")
	}
	if _, err := resolveAttemptContext(p, "h", []string{root}, []string{root}, ""); err == nil {
		t.Fatal("forbidden root")
	}
	input := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(input, "context-sources"))
	if _, err := resolveAttemptContext(p, "h", []string{root}, nil, input); err == nil {
		t.Fatal("input symlink escape")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote outside input")
	}
}

// R1, R3; risk: a receipt hashes a different instruction than Codex CLI/protocol or Claude/ACP actually receive.
func TestAttemptInstructionAssemblyAcrossHarnessShapes(t *testing.T) {
	core := strings.Repeat("core-body\n", 2000)
	profile := M{"id": "baseline", "model": "gpt-6.1-sol", "effort": "low", "instructions": "profile-body", "mcpServers": []any{}}
	if err := profileValidate(profile); err != nil {
		t.Fatal(err)
	}
	bad := jObj(jClone(profile))
	bad["instructions"] = core
	if profileValidate(bad) == nil {
		t.Fatal("profile cap weakened")
	}
	for _, name := range []string{"codex", "claude", "antigravity"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("not-executed")
			if name == "claude" {
				cmd.Args = append(cmd.Args, "--system-prompt", "old")
			}
			p := M{"agentConfig": profile, "model": "gpt-6.1-sol", "effort": "low", "text": "task", "skills": []any{M{"id": "required", "text": "skill-body"}}}
			runtime, receipt := prepareInstructions(cmd, name, p, "project-body", attemptContext{Core: core, Index: "linked-index", Sources: []any{}}, nil)
			text := jStr(runtime, "agentConfig", "instructions")
			if !strings.Contains(text, "skill-body") || !strings.Contains(text, "project-body") || !strings.Contains(text, "linked-index") || receipt["instructionsSha256"] != sha([]byte(text)) || jStr(receipt, "applied", "instructions") != text {
				t.Fatal(receipt)
			}
			if jStr(profile, "instructions") != "profile-body" {
				t.Fatal("mutated definition")
			}
			switch name {
			case "codex":
				if !strings.Contains(strings.Join(cmd.Args, "\x00"), "developer_instructions="+enc(text)) {
					t.Fatal("CLI mismatch")
				}
				s := &codexSession{profile: runtime, root: "/work", stage: 0}
				frames, _, _, err := s.Receive(M{"id": 1, "result": M{"userAgent": "fixture"}})
				if err != nil {
					t.Fatal(err)
				}
				if jStr(frames[1], "params", "developerInstructions") != text {
					t.Fatal("protocol mismatch")
				}
				for _, native := range []string{"resume", "fork"} {
					r := &codexSession{profile: runtime, root: "/work"}
					if native == "resume" {
						r.resume = "thread"
					} else {
						r.fork = "thread"
					}
					r.Receive(M{"id": 1, "result": M{}})
					frames, _, _, err := r.Receive(M{"id": 2, "result": M{"thread": M{"id": "thread"}}})
					if err != nil || len(frames) != 1 || frames[0]["method"] != "thread/inject_items" || jStr(jArr(jAt(jArr(jAt(frames[0], "params", "items"))[0], "content"))[0], "text") != text {
						t.Fatal("resolved core missing from continuation", native, frames, err)
					}
				}
			case "claude":
				if cmd.Args[len(cmd.Args)-1] != text {
					t.Fatal("Claude prompt mismatch")
				}
			default:
				s := newACPSession("/work", "", runtime, M{})
				request := s.prompt()
				if !strings.Contains(enc(request), strings.Trim(enc(text), "\"")) {
					t.Fatal("ACP prompt mismatch")
				}
			}
			raw, _ := json.Marshal(runtime)
			if len(raw) == 0 {
				t.Fatal("missing runtime")
			}
		})
	}
}

// R1, R3; risk: a missing declared rule source reaches native consumption instead of an actionable preparation failure.
func TestAttemptMissingSourceStopsActualStart(t *testing.T) {
	p, source, _ := contextFixture(t)
	jObj(jArr(p, "contextSources")[0])["revision"] = strings.Repeat("f", 40)
	jObj(jArr(p, "environments")[1])["checkoutPath"] = source
	assertAttemptSourceStartBlocked(t, p, source)
}

func assertAttemptSourceStartBlocked(t *testing.T, p M, source string) {
	t.Helper()
	p["worktree"] = M{"envMode": "checkout", "startFromOrigin": false}
	e, _ := scriptExecutor(t)
	e.host = "h"
	marker := filepath.Join(t.TempDir(), "native-started")
	bin := filepath.Join(t.TempDir(), "native")
	os.WriteFile(bin, []byte("#!/bin/sh\nprintf native > '"+marker+"'\n"), 0700)
	home := t.TempDir()
	os.Chmod(home, 0700)
	accountHome := t.TempDir()
	os.Chmod(accountHome, 0700)
	registry := filepath.Join(t.TempDir(), "registry.json")
	os.WriteFile(registry, []byte(enc(M{"fixture": M{"harness": "codex", "home": home, "nativeHome": accountHome, "executable": bin}})), 0600)
	t.Setenv("FLEET_NATIVE_ACCOUNTS", registry)
	t.Setenv("FLEET_PROJECT_ROOTS", source)
	payload := M{"agentConfig": M{"id": "baseline", "model": "gpt-6.1-sol", "effort": "low", "instructions": "profile", "mcpServers": []any{}, "fleetTools": false, "collaborationTools": false}, "model": "gpt-6.1-sol", "effort": "low", "account": "fixture", "text": "read only", "project": p}
	c := M{"id": "source-negative", "executionId": "source-negative-execution", "sessionId": "source-negative-session", "kind": "start", "payload": payload}
	if err := e.start(c, "source-negative", "source-negative-execution", "source-negative-session", payload); err != nil {
		t.Fatal(err)
	}
	if exists(marker) || len(e.live) != 0 || lastReceipt(e, "source-negative") != "failed" {
		t.Fatal("native started or failure not retained")
	}
	rows, err := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/events'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		if strings.Contains(raw, "instruction-source-unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing actionable source failure")
	}
	ready := readiness(p, "h", registry, e.forbidden())
	found = false
	for _, check := range ready.Checks {
		if check.Check == "instruction-sources" && check.Reason != nil && strings.Contains(*check.Reason, "instruction-source-unavailable") {
			found = true
		}
	}
	if ready.Ok || !found {
		t.Fatal("readiness/start disagree", ready)
	}
}

// R3; risk: a partial clone silently fetches missing objects through repository SSH/credential configuration.
func TestAttemptContextPartialCloneNeverFetches(t *testing.T) {
	p, root, rev := contextFixture(t)
	marker := filepath.Join(t.TempDir(), "ssh-invoked")
	hook := filepath.Join(t.TempDir(), "ssh")
	os.WriteFile(hook, []byte("#!/bin/sh\necho invoked > '"+marker+"'\nexit 1\n"), 0700)
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatal(e, string(b))
		}
		return strings.TrimSpace(string(b))
	}
	origin := "git@offline.invalid:specq"
	git("config", "remote.origin.url", origin)
	jObj(jArr(p, "repos")[1])["remote"] = origin
	blob := git("rev-parse", rev+":engineering/core.md")
	if err := os.Remove(filepath.Join(root, ".git/objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	git("config", "extensions.partialClone", "origin")
	git("config", "remote.origin.promisor", "true")
	git("config", "remote.origin.partialclonefilter", "blob:none")
	git("config", "core.sshCommand", hook)
	if _, err := resolveAttemptContext(p, "h", []string{root}, nil, ""); err == nil {
		t.Fatal("missing blob accepted")
	}
	if exists(marker) {
		t.Fatal("offline source resolution launched SSH")
	}
}

// R1; risk: delivering a whole source tree or dropping one declared core file changes the attempt contract.
func TestAttemptContextDeclaredTenCoreAndLinkedSelection(t *testing.T) {
	p, root, _ := contextFixture(t)
	paths := []string{"engineering/principles.md", "engineering/spec-first.md", "engineering/ponytail.md", "engineering/agent-workflow.md", "engineering/use-cases.md", "engineering/verification.md", "engineering/security.md", "caveman/rules/core.md", "caveman/rules/documentation.md", "caveman/rules/changes.md"}
	core := []any{}
	for _, path := range paths {
		body := []byte("selected core: " + path + "\n")
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
			t.Fatal(err)
		}
		core = append(core, M{"path": path, "sha256": sha(body)})
	}
	os.WriteFile(filepath.Join(root, "unselected.md"), []byte("not selected"), 0600)
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "ten declared core files"}} {
		if b, e := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	pin, e := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if e != nil {
		t.Fatal(e)
	}
	source := jObj(jArr(p, "contextSources")[0])
	source["revision"] = strings.TrimSpace(string(pin))
	source["core"] = core
	input := t.TempDir()
	got, e := resolveAttemptContext(p, "h", []string{root}, nil, input)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Sources) != 11 || strings.Contains(got.Core, "linked-area") || strings.Contains(got.Core, "not selected") {
		t.Fatal(got)
	}
	for _, path := range paths {
		if strings.Count(got.Core, "selected core: "+path+"\n") != 1 {
			t.Fatal("core not delivered exactly once", path)
		}
	}
	if exists(filepath.Join(input, "context-sources/specq", source["revision"].(string), "unselected.md")) {
		t.Fatal("unselected source staged")
	}
}

// R3; risk: Git storage or ambient Git variables redirect immutable blob reads outside source authority.
func TestAttemptContextGitStorageAuthority(t *testing.T) {
	for _, layout := range []string{"symlink", "alternates"} {
		t.Run(layout, func(t *testing.T) {
			p, root, _ := contextFixture(t)
			external, _ := filepath.EvalSymlinks(t.TempDir())
			outside := filepath.Join(external, "objects")
			objects := filepath.Join(root, ".git", "objects")
			if err := os.Rename(objects, outside); err != nil {
				t.Fatal(err)
			}
			if layout == "symlink" {
				if err := os.Symlink(outside, objects); err != nil {
					t.Fatal(err)
				}
			} else {
				os.MkdirAll(filepath.Join(objects, "info"), 0700)
				os.WriteFile(filepath.Join(objects, "info", "alternates"), []byte(outside+"\n"), 0600)
			}
			input := t.TempDir()
			if _, err := resolveAttemptContext(p, "h", []string{root}, nil, input); err == nil {
				t.Fatal("foreign object storage accepted")
			}
			entries, _ := os.ReadDir(input)
			if len(entries) != 0 {
				t.Fatal("external content staged before rejection")
			}
			jObj(jArr(p, "environments")[1])["checkoutPath"] = root
			assertAttemptSourceStartBlocked(t, p, root)
		})
	}
	t.Run("ambient-object-config-ignored", func(t *testing.T) {
		p, root, _ := contextFixture(t)
		t.Setenv("GIT_OBJECT_DIRECTORY", t.TempDir())
		t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", t.TempDir())
		t.Setenv("GIT_DIR", t.TempDir())
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
		t.Setenv("GIT_CONFIG_VALUE_0", "https://foreign.invalid/repo")
		got, err := resolveAttemptContext(p, "h", []string{root}, nil, t.TempDir())
		if err != nil || !strings.Contains(got.Core, "required-core") {
			t.Fatal(got, err)
		}
	})
	t.Run("ordinary-linked-worktree", func(t *testing.T) {
		p, root, _ := contextFixture(t)
		parent, _ := filepath.EvalSymlinks(t.TempDir())
		work := filepath.Join(parent, "linked")
		if b, e := exec.Command("git", "-C", root, "worktree", "add", "--detach", work, "HEAD").CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
		jObj(jArr(p, "environments")[0])["checkoutPath"] = work
		if _, e := resolveAttemptContext(p, "h", []string{root, parent}, nil, t.TempDir()); e != nil {
			t.Fatal(e)
		}
	})
}
