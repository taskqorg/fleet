// Trusted agent profiles (packages/coordinator/profiles.ts) and project definitions (projects.ts): read from config files,
// validated; routes /api/profiles, /api/projects*, /api/project-operations/{name}, /api/project-tools.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

func repoRoot() string {
	if exe, err := os.Executable(); err == nil {
		if r := filepath.Join(filepath.Dir(exe), "../.."); dirExists(filepath.Join(r, "config")) {
			return r
		}
	}
	return "."
}

func dirExists(p string) bool { _, err := os.Stat(p); return err == nil }

func envDir(k, rel string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return filepath.Join(repoRoot(), rel)
}

func profilesDir() string { return envDir("FLEET_PROFILES_DIRECTORY", "config/agents") }
func configDir() string   { return envDir("FLEET_PROJECT_CONFIG_DIRECTORY", "config") }

// canonical: JSON with object keys sorted by byte order (profiles.ts canonical).
func canonical(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b bytes.Buffer
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			b.WriteString(canonical(x[k]))
		}
		b.WriteByte('}')
		return b.String()
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = canonical(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

func profileDigest(v any) string {
	h := sha256.Sum256([]byte(canonical(v)))
	return hex.EncodeToString(h[:])
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

func validAntigravitySettings(v any) bool {
	if v == nil {
		return true
	}
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for k, x := range o {
		switch k {
		case "sandbox":
			if _, ok := x.(bool); !ok {
				return false
			}
		case "includeDefaultCustomizations":
			if x != false {
				return false
			}
		case "mode":
			if x != "plan" && x != "accept-edits" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validNativeProfile(p doc) bool {
	h := firstString(p["harness"], "codex")
	for _, k := range []string{"fleetTools", "collaborationTools", "gitNetwork"} {
		if v, ok := p[k]; ok {
			if _, isBool := v.(bool); !isBool {
				return false
			}
		}
	}
	model, _ := p["model"].(string)
	_, hasSettings := p["settings"]
	_, hasTools := p["tools"]
	if h == "codex" {
		effort, _ := p["effort"].(string)
		role := p["instructionRole"]
		return model != "" && !strings.HasPrefix(model, "-") && effort != "" && (role == nil || role == "" || role == false || role == "developer") && (p["tools"] == nil || p["tools"] == false) && !hasSettings
	}
	if !slices.Contains([]string{"claude", "antigravity", "acp"}, h) || model == "" || strings.HasPrefix(model, "-") {
		return false
	}
	if e, ok := p["effort"]; !ok || e != nil {
		return false
	}
	want := "user"
	if h == "claude" {
		want = "system"
	}
	if p["instructionRole"] != want {
		return false
	}
	if h == "claude" {
		if hasSettings {
			return false
		}
		tools, ok := p["tools"].([]any)
		if !ok {
			return false
		}
		for _, t := range tools {
			if !slices.Contains([]any{"Read", "Edit", "Write", "Bash", "Glob", "Grep"}, t) {
				return false
			}
		}
		return true
	}
	return validAntigravitySettings(p["settings"]) && !hasTools
}

var profileOptional = []string{"delegationProfiles", "harness", "instructionRole", "tools", "settings", "fleetTools", "collaborationTools", "gitNetwork", "skills", "connections"}

func readTrustedProfile(id string) (doc, error) {
	if !idPattern.MatchString(id) {
		return nil, fail(400, "Unknown trusted profile")
	}
	raw, err := os.ReadFile(filepath.Join(profilesDir(), id+".json"))
	if err != nil {
		return nil, fail(400, "Unknown trusted profile")
	}
	var p doc
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fail(400, "Invalid repository profile")
	}
	req := []string{}
	for k := range p {
		if !slices.Contains(profileOptional, k) {
			req = append(req, k)
		}
	}
	sort.Strings(req)
	instr, _ := p["instructions"].(string)
	mcp, mcpOK := p["mcpServers"].([]any)
	if strings.Join(req, ",") != "effort,id,instructions,mcpServers,model" || p["id"] != id || !validNativeProfile(p) || p["instructions"] == nil || instr != p["instructions"] || len(instr) > 16384 || !mcpOK || len(mcp) > 1 {
		return nil, fail(400, "Invalid repository profile")
	}
	for _, c := range []struct {
		k   string
		max int
		msg string
	}{{"delegationProfiles", 8, "Invalid delegation grant"}, {"skills", 32, "Invalid profile skills"}, {"connections", 32, "Invalid profile connections"}} {
		v, ok := p[c.k]
		if !ok {
			continue
		}
		xs, isList := v.([]any)
		seen := map[string]bool{}
		bad := !isList || len(xs) > c.max
		for _, x := range xs {
			s, _ := x.(string)
			if !idPattern.MatchString(s) || seen[s] {
				bad = true
			}
			seen[s] = true
		}
		if bad {
			return nil, fail(400, c.msg)
		}
	}
	for _, x := range mcp {
		o, _ := x.(map[string]any)
		d, isObj := o["data"].(map[string]any)
		if len(o) != 2 || o["id"] != "design-rules" || !isObj || len(canonical(d)) > 16384 {
			return nil, fail(400, "Invalid repository grant")
		}
	}
	return p, nil
}

func listProfiles() []doc {
	out := []doc{}
	es, _ := os.ReadDir(profilesDir())
	for _, e := range es {
		n := e.Name()
		if !strings.HasSuffix(n, ".json") || !idPattern.MatchString(strings.TrimSuffix(n, ".json")) {
			continue
		}
		if p, err := readTrustedProfile(strings.TrimSuffix(n, ".json")); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Project settings (projects.ts projectSettings): defaults for absent keys, unknown keys reported.
var settingsDefaults = map[string]any{"repos": []any{}, "environments": []any{}, "roles": doc{},
	"worktree": doc{"envMode": "worktree", "baseRef": nil, "startFromOrigin": true, "branchPrefix": "fleet", "linkPaths": []any{}, "sharedDirectories": []any{}},
	"scripts":  doc{"setup": nil, "teardown": nil}, "setupStart": "wait",
	"integration": doc{"mode": "pr", "mergeMethod": "squash", "autoMerge": true, "syncCheckoutAfterMerge": true},
	"cleanup":     doc{"keepWhen": []any{"uncommitted", "teardown-failed"}}, "identity": nil, "subscriptions": []any{}}

var settingsOrder = []string{"repos", "environments", "roles", "worktree", "scripts", "setupStart", "integration", "cleanup", "identity", "subscriptions"}
var itemKeys = map[string][]string{"repos": {"id", "remote", "forge", "defaultBranch", "connection", "path"}, "environments": {"hostId", "checkoutPath", "repo"}, "identity": {"name", "email"}}
var settingEnums = map[string][]any{"worktree.envMode": {"worktree", "local"}, "setupStart": {"wait", "background"}, "integration.mode": {"pr", "direct"}, "integration.mergeMethod": {"squash", "merge", "rebase"}, "repos.forge": {"github", "gitlab"}}

func clone(v any) any { var out any; json.Unmarshal([]byte(js(v)), &out); return out }

func jsType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case nil:
		return "null"
	}
	return "object"
}

func projectSettings(p doc) (doc, []string, error) {
	unknown := []string{}
	bad := func(k string) error { return fail(500, "Invalid project setting "+k) }
	isObj := func(v any) bool { _, ok := v.(map[string]any); return ok }
	isArr := func(v any) bool { _, ok := v.([]any); return ok }
	check := func(k string, v, d any) error {
		if e, ok := settingEnums[k]; ok && !slices.Contains(e, v) {
			return bad(k)
		}
		if (d != nil && v != nil && jsType(d) != jsType(v)) || isArr(d) != isArr(v) {
			return bad(k)
		}
		return nil
	}
	top := append([]string{"id", "name", "capability", "recommendedMethod", "contextFiles", "artifact_locations", "artifact_rules"}, settingsOrder...)
	for k := range p {
		if !slices.Contains(top, k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	out := doc{}
	for _, k := range settingsOrder {
		d := settingsDefaults[k]
		v, present := p[k]
		if !present {
			out[k] = clone(d)
			continue
		}
		switch {
		case k == "subscriptions":
			xs, ok := v.([]any)
			for _, x := range xs {
				if _, s := x.(string); !s {
					ok = false
				}
			}
			if !ok {
				return nil, nil, bad(k)
			}
			out[k] = v
		case k == "roles":
			o, ok := v.(map[string]any)
			for _, x := range o {
				if _, s := x.(string); !s {
					ok = false
				}
			}
			if !ok {
				return nil, nil, bad(k)
			}
			out[k] = v
		case isArr(d):
			xs, ok := v.([]any)
			if !ok {
				return nil, nil, bad(k)
			}
			items := []any{}
			for i, it := range xs {
				o, ok := it.(map[string]any)
				if !ok {
					return nil, nil, bad(k)
				}
				for x, y := range o {
					if !slices.Contains(itemKeys[k], x) {
						unknown = append(unknown, k+"["+itoa(i)+"]."+x)
					} else if _, s := y.(string); !s {
						return nil, nil, bad(k + "[" + itoa(i) + "]." + x)
					}
				}
				if k == "repos" {
					if gs(o, "remote") == "" {
						return nil, nil, bad(k + ".remote")
					}
					if err := check("repos.forge", firstString(o["forge"], "github"), nil); err != nil {
						return nil, nil, err
					}
					r := doc{"forge": "github", "defaultBranch": "main"}
					for x, y := range o {
						r[x] = y
					}
					items = append(items, r)
					continue
				}
				if gs(o, "hostId") == "" || gs(o, "checkoutPath") == "" {
					return nil, nil, bad(k)
				}
				items = append(items, o)
			}
			out[k] = items
		case k == "identity":
			o, ok := v.(map[string]any)
			if !ok {
				return nil, nil, bad(k)
			}
			for x := range o {
				if !slices.Contains(itemKeys["identity"], x) {
					unknown = append(unknown, "identity."+x)
				}
			}
			out[k] = v
		case isObj(d):
			o, ok := v.(map[string]any)
			if !ok {
				return nil, nil, bad(k)
			}
			r := clone(d).(map[string]any)
			for x, y := range o {
				dv, known := d.(doc)[x]
				if !known {
					unknown = append(unknown, k+"."+x)
					continue
				}
				if err := check(k+"."+x, y, dv); err != nil {
					return nil, nil, err
				}
				r[x] = y
			}
			out[k] = r
		default:
			if err := check(k, v, d); err != nil {
				return nil, nil, err
			}
			out[k] = v
		}
	}
	loc, has := p["artifact_locations"]
	if has {
		xs, ok := loc.([]any)
		seen := map[string]bool{}
		for _, x := range xs {
			s, _ := x.(string)
			if s == "" || seen[s] {
				ok = false
			}
			seen[s] = true
		}
		if !ok || len(xs) == 0 {
			return nil, nil, bad("artifact_locations")
		}
	}
	out["artifact_locations"] = loc
	rules, has := p["artifact_rules"]
	if has {
		xs, ok := rules.([]any)
		for _, x := range xs {
			if !isObj(x) {
				ok = false
			}
		}
		if !ok {
			return nil, nil, bad("artifact_rules")
		}
	} else {
		rules = []any{}
	}
	out["artifact_rules"] = rules
	return out, unknown, nil
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func projectIDs() []string {
	root := filepath.Join(configDir(), "projects")
	es, _ := os.ReadDir(root)
	ids := []string{}
	for _, e := range es {
		if _, err := os.Stat(filepath.Join(root, e.Name(), "project.json")); err == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

func readJSON(path string) (doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d doc
	return d, json.Unmarshal(raw, &d)
}

// projectContext: the definition with its settings, unknown keys and context files (projects.ts project()).
func projectContext(id string) (doc, error) {
	if !slices.Contains(projectIDs(), id) {
		return nil, fail(404, "Unknown project")
	}
	root := filepath.Join(configDir(), "projects", id)
	p, err := readJSON(filepath.Join(root, "project.json"))
	if err != nil {
		return nil, fail(500, "Invalid project definition")
	}
	files, ok := p["contextFiles"].([]any)
	if p["id"] != id || !ok {
		return nil, fail(500, "Invalid project definition")
	}
	settings, unknown, err := projectSettings(p)
	if err != nil {
		return nil, err
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	ctx := []doc{}
	for _, f := range files {
		name, ok := f.(string)
		if !ok || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			return nil, fail(500, "Invalid project context path")
		}
		path, err := filepath.EvalSymlinks(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(path, realRoot+string(filepath.Separator)) {
			return nil, fail(500, "Project context escapes repository")
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256([]byte(js(string(text))))
		ctx = append(ctx, doc{"path": name, "text": string(text), "sha256": hex.EncodeToString(h[:])})
	}
	out := doc{}
	for k, v := range p {
		out[k] = v
	}
	out["settings"], out["unknownKeys"], out["context"] = settings, unknown, ctx
	return out, nil
}

func projectSettingsOf(id string) doc {
	p, err := projectContext(id)
	if err != nil {
		return nil
	}
	return gm(p, "settings")
}

func catalogue(q *Request, id string) (doc, error) {
	p, err := projectContext(id)
	if err != nil {
		return nil, err
	}
	c, err := readJSON(filepath.Join(configDir(), "catalogue.json"))
	if err != nil {
		return nil, err
	}
	recs, err := all(q, "project-recommendation", doc{"projectId": id})
	if err != nil {
		return nil, err
	}
	method, source := firstString(p["recommendedMethod"], c["sharedDefault"]), "shared"
	if p["recommendedMethod"] != nil {
		source = "project"
	}
	if len(recs) > 0 {
		method, source = gs(recs[0], "methodId"), "project-selected"
	}
	methods := []any{}
	ms, _ := c["methods"].([]any)
	for _, x := range ms {
		if sc := gs(x.(map[string]any), "scope"); sc == "shared" || sc == id {
			methods = append(methods, x)
		}
	}
	return doc{"projectId": id, "capability": c["capability"], "sharedDefault": c["sharedDefault"], "recommendedMethod": method, "recommendationSource": source, "methods": methods}, nil
}

// projectOps: the project operation table of projects.ts without the pipeline runs (pipelines area).
var projectOps = map[string]registeredOp{}

func projectOp(name string, read bool, h handler) {
	projectOps[name] = registeredOp{read, h}
	bop(name, read, h)
}

func init() {
	broute("GET /api/profiles", func(q *Request) (any, error) { return doc{"profiles": listProfiles()}, nil })
	bop("profiles_list", true, func(q *Request) (any, error) { return doc{"profiles": listProfiles()}, nil })
	projectOp("project_list", true, func(q *Request) (any, error) {
		out := []doc{}
		for _, id := range projectIDs() {
			if q.person != nil && !mayAccessProjectID(q.person, id) {
				continue
			}
			p, err := projectContext(id)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return doc{"projects": out}, nil
	})
	projectOp("project_context", true, func(q *Request) (any, error) { return projectContext(gs(body(q), "projectId")) })
	projectOp("catalogue", true, func(q *Request) (any, error) { return catalogue(q, gs(body(q), "projectId")) })
	projectOp("recommendation_set", false, func(q *Request) (any, error) {
		b := body(q)
		c, err := catalogue(q, gs(b, "projectId"))
		if err != nil {
			return nil, err
		}
		found := false
		for _, x := range c["methods"].([]any) {
			found = found || gs(x.(map[string]any), "id") == gs(b, "methodId")
		}
		if !found {
			return nil, fail(400, "Method unavailable")
		}
		r := doc{"id": "recommendation:" + gs(b, "projectId"), "projectId": b["projectId"], "methodId": b["methodId"], "selectedAt": now(), "actor": actor(q, "operator")}
		return r, putDoc(q, "project-recommendation", r)
	})
	projectOp("question_ask", false, func(q *Request) (any, error) {
		b := body(q)
		if _, err := projectContext(gs(b, "projectId")); err != nil {
			return nil, err
		}
		rid, text := gs(b, "requestId"), gs(b, "text")
		if rid == "" || text == "" || len([]rune(text)) > 2000 {
			return nil, fail(400, "Expected question text and requestId")
		}
		old, err := first(q, "SELECT data FROM question WHERE legacy_kind='project-question' AND data->>'projectId'=$1 AND data->>'requestId'=$2", b["projectId"], rid)
		if err != nil {
			return nil, err
		}
		if old != nil {
			d := gm(old, "data")
			if gs(d, "text") != text {
				return nil, fail(409, "Question identity conflict")
			}
			return d, nil
		}
		v := doc{"id": uuid(), "projectId": b["projectId"], "requestId": rid, "text": text, "state": "waiting", "createdAt": now(), "actor": actor(q, "operator")}
		_, err = askQuestion(q, doc{"id": v["id"], "kind": "operator_decision", "text": text, "projectId": b["projectId"], "askedBy": v["actor"], "legacyKind": "project-question", "data": v, "askedAt": v["createdAt"]})
		return v, err
	})
	projectOp("question_answer", false, func(q *Request) (any, error) {
		b := body(q)
		id := gs(b, "questionId")
		answer, err := strArg(b, "answer", "answer", 4000)
		if err != nil {
			return nil, err
		}
		legacy, err := first(q, "SELECT data FROM question WHERE id=$1 AND legacy_kind='project-question'", id)
		if err != nil {
			return nil, err
		}
		if legacy == nil {
			ids, _ := b["artifactIds"].([]any)
			a, err := answerQuestion(q, id, answer, actor(q, "operator"), ids)
			if err != nil {
				return nil, err
			}
			if x, err := findQuestion(q, gs(a, "task_id"), id); x != nil || err != nil {
				return x, err
			}
			return doc{"id": a["id"], "state": a["state"], "answer": a["answer"]}, nil
		}
		v := gm(legacy, "data")
		if gs(v, "state") == "answered" {
			if gs(v, "answer") != answer {
				return nil, fail(409, "Answer already recorded")
			}
			return v, nil
		}
		v["state"], v["answer"], v["answeredAt"], v["answeredBy"] = "answered", answer, now(), actor(q, "operator")
		if _, err := answerQuestion(q, id, answer, gs(v, "answeredBy"), nil); err != nil {
			return nil, err
		}
		return v, exec(q, "UPDATE question SET data=$2 WHERE id=$1", id, js(v))
	})
	projectOp("question_history", true, func(q *Request) (any, error) {
		b := body(q)
		if _, err := projectContext(gs(b, "projectId")); err != nil {
			return nil, err
		}
		qs, err := rows(q, "SELECT data FROM question WHERE legacy_kind='project-question' AND data->>'projectId'=$1 ORDER BY seq", b["projectId"])
		return doc{"questions": qs}, err
	})
	projectOp("pipeline_get", true, func(q *Request) (any, error) {
		b := body(q)
		id := gs(b, "runId")
		if id == "" || len(id) > 200 {
			return nil, fail(400, "Expected runId")
		}
		r, err := pipelineRun(q, id)
		if err != nil {
			return nil, err
		}
		if p := gs(b, "projectId"); p != "" && gs(r, "projectId") != p {
			return nil, fail(403, "Pipeline belongs to another project")
		}
		return r, nil
	})
	projectOp("pipeline_history", true, func(q *Request) (any, error) {
		b := body(q)
		if _, err := projectContext(gs(b, "projectId")); err != nil {
			return nil, err
		}
		ids, err := rowsOf(q, "SELECT id FROM execution WHERE legacy_kind='pipeline-run' AND data->>'projectId'=$1 ORDER BY seq", b["projectId"])
		if err != nil {
			return nil, err
		}
		runs := []doc{}
		for _, x := range ids {
			r, err := pipelineRun(q, gs(x, "id"))
			if err != nil {
				return nil, err
			}
			runs = append(runs, r)
		}
		return doc{"runs": runs}, nil
	})
	broute("GET /api/projects", func(q *Request) (any, error) { return projectOps["project_list"].h(q) })
	projectRoute := func(path, op string) {
		broute("GET "+path, func(q *Request) (any, error) {
			q.body = json.RawMessage(js(doc{"projectId": q.r.PathValue("id")}))
			return projectOps[op].h(q)
		})
	}
	projectRoute("/api/projects/{id}", "project_context")
	projectRoute("/api/projects/{id}/catalogue", "catalogue")
	projectRoute("/api/projects/{id}/runs", "pipeline_history")
	broute("GET /api/pipeline-runs/{id}", func(q *Request) (any, error) {
		q.body = json.RawMessage(js(doc{"runId": q.r.PathValue("id")}))
		return projectOps["pipeline_get"].h(q)
	})
	blongRoute("POST /api/project-operations/{name}", func(q *Request) (any, error) {
		if _, ok := projectOps[q.r.PathValue("name")]; !ok {
			return nil, fail(404, "Unknown operation")
		}
		return q.app.runOp(q, q.r.PathValue("name"), q.body)
	})
	blongRoute("POST /api/project-tools", func(q *Request) (any, error) { return q.app.mcp(q, "fleet-projects", projectOps, nil) })
}

// pipelineRun: a pipeline-run record with its steps (executions.ts withSteps).
func pipelineRun(q *Request, id string) (doc, error) {
	r, err := one(q, "SELECT data FROM execution WHERE id=$1 AND legacy_kind='pipeline-run'", id)
	if err != nil {
		return nil, err
	}
	steps, err := rows(q, "SELECT data - 'index' FROM execution WHERE legacy_kind='pipeline-step' AND parent_execution_id=$1 ORDER BY (data->>'index')::int", id)
	r["steps"] = steps
	return r, err
}

// mcp: a JSON-RPC tools server over an operation table; each call runs in its own transaction.
func (a *App) mcp(q *Request, server string, ops map[string]registeredOp, descriptions map[string]string) (any, error) {
	var rpc struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	json.Unmarshal(q.body, &rpc)
	switch rpc.Method {
	case "initialize":
		return doc{"jsonrpc": "2.0", "id": rpc.ID, "result": doc{"protocolVersion": "2025-06-18", "capabilities": doc{"tools": doc{}}, "serverInfo": doc{"name": server, "version": "1"}}}, nil
	case "tools/list":
		names := make([]string, 0, len(ops))
		for n := range ops {
			names = append(names, n)
		}
		sort.Strings(names)
		tools := []doc{}
		for _, n := range names {
			t := doc{"name": n, "inputSchema": doc{"type": "object"}}
			if d, ok := descriptions[n]; ok {
				t["description"] = d
			} else if server == "fleet-projects" {
				t["description"] = "Fleet shared " + n
			}
			tools = append(tools, t)
		}
		return doc{"jsonrpc": "2.0", "id": rpc.ID, "result": doc{"tools": tools}}, nil
	}
	if _, ok := ops[rpc.Params.Name]; rpc.Method != "tools/call" || !ok {
		return nil, fail(400, "Unknown tool")
	}
	out, err := a.runOp(q, rpc.Params.Name, rpc.Params.Arguments)
	if err != nil {
		return nil, err
	}
	return doc{"jsonrpc": "2.0", "id": rpc.ID, "result": doc{"content": []doc{{"type": "text", "text": js(out)}}}}, nil
}

// A project's artifact_locations for the artifacts area; the default list when it names none.
func init() {
	artifactLocations = func(a *App, projectID *string) []string {
		if projectID != nil {
			if l := strList(projectSettingsOf(*projectID)["artifact_locations"]); len(l) > 0 {
				return l
			}
		}
		return defaultLocations
	}
}
