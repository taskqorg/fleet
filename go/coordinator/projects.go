// Trusted agent profiles and project definitions (docs/configuration.md): read from config files,
// validated; the project operations and the projects MCP server POST /api/project-tools.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/mcpserve"
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

// canonical: JSON with object keys sorted by byte order (encoding/json sorts map keys; a struct keeps its field order),
// without HTML escaping; an absent open object or list is {} or [].
func canonical(v any) string {
	if o, ok := v.(rawObject); ok { // an open object as its decoded value, as the map it replaced encoded
		if o == nil {
			return "{}"
		}
		v = norm(o)
	}
	if rv := reflect.ValueOf(v); rv.IsValid() && (rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice) && rv.IsNil() && rv.Type().Elem().Kind() == reflect.Interface {
		if rv.Kind() == reflect.Map {
			return "{}"
		}
		return "[]"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

// readTrustedProfile: an agent profile, the head of its definition in the store (fleet-definition-editors.md, Resolve).
func readTrustedProfile(q *Request, id string) (rawObject, error) {
	if !idPattern.MatchString(id) {
		return nil, fail(400, "Unknown trusted profile")
	}
	raw, ok, err := headText(q, "agent", id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(400, "Unknown trusted profile")
	}
	p, err := openObject(raw)
	if err != nil {
		return nil, fail(400, "Invalid profile")
	}
	// One validator with the executor (contracts/profile.schema.json), so a profile listed here is never refused on a host.
	if _, err := contract.ParseProfile(raw); err != nil {
		return nil, fail(400, "Invalid profile: "+err.Error())
	}
	if own, ok := p.str("id"); !ok || own != id {
		return nil, fail(400, "Invalid profile: id differs from its definition")
	}
	return p, nil
}

// listProfiles: every live agent definition that is a valid profile, in one read.
func listProfiles(q *Request) ([]rawObject, error) {
	out := []rawObject{}
	heads, err := headTexts(q, "agent")
	for _, h := range heads {
		if p, perr := openObject(h.text); perr == nil && p.text("id") == h.id && p.has("id") {
			if _, err := contract.ParseProfile(h.text); err == nil {
				out = append(out, p)
			}
		}
	}
	return out, err
}

// Project settings (docs/configuration.md): defaults for absent keys, unknown keys reported.
var settingsDefaults = func() map[string]json.RawMessage {
	var d map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"contextSources": [], "repos": [], "environments": [], "roles": {},
	"worktree": {"envMode": "worktree", "baseRef": null, "startFromOrigin": true, "branchPrefix": "fleet", "linkPaths": [], "sharedDirectories": []},
	"scripts": {"setup": null, "teardown": null}, "setupStart": "wait",
	"execution": {"tools": [], "forge": false, "profiles": []},
	"integration": {"mode": "pr", "mergeMethod": "squash", "autoMerge": true, "autoAccept": null, "syncCheckoutAfterMerge": true},
	"cleanup": {"keepWhen": ["uncommitted", "unpushed", "teardown-failed"]}, "identity": null, "subscriptions": [],
	"errors": {"bugsink": [], "ignore": []},
	"presets": []}`), &d); err != nil {
		panic(err)
	}
	// errors.bugsink: the Bugsink projects (names) holding this project's errors (docs/designs/fleet-error-tracking.md);
	// errors.ignore: title substrings of Bugsink issues that open no needs-you problem (the issue stays in Bugsink).
	// presets: what a new conversation does (presets.go); empty: the system presets.
	return d
}()

var settingsOrder = []string{"contextSources", "repos", "environments", "roles", "worktree", "scripts", "setupStart", "integration", "cleanup", "identity", "subscriptions", "execution", "errors", "presets"}
var itemKeys = map[string][]string{"repos": {"id", "remote", "forge", "host", "defaultBranch", "connection", "path"}, "environments": {"hostId", "checkoutPath", "repo"}, "identity": {"name", "email"}}
var settingEnums = map[string][]string{"worktree.envMode": {"worktree", "local"}, "setupStart": {"wait", "background"}, "integration.mode": {"pr", "direct"}, "integration.mergeMethod": {"squash", "merge", "rebase"}, "repos.forge": {"github", "gitlab"}}

// jsType: a JSON value's type as a setting's default and value compare (lists are objects).
func jsType(v json.RawMessage) string {
	switch jsonKind(v) {
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n', 0:
		return "null"
	case '{', '[':
		return "object"
	}
	return "number"
}

// projectSettings: a project definition's settings checked value by value (open JSON, walked by kind) over their
// defaults, and the unknown keys.
// p is the settings object (decoded or raw JSON).
func projectSettings(p any) (rawObject, []string, error) {
	unknown := []string{}
	bad := func(k string) error {
		if k == "execution.profiles" || k == "errors.bugsink" || k == "errors.ignore" {
			return fail(400, "Invalid project setting "+k)
		}
		return fail(500, "Invalid project setting "+k)
	}
	isObj := func(v json.RawMessage) bool { return jsonKind(v) == '{' }
	isArr := func(v json.RawMessage) bool { return jsonKind(v) == '[' }
	check := func(k string, v, d json.RawMessage) error {
		if e, ok := settingEnums[k]; ok {
			if s, isText := jsonString(v); !isText || !slices.Contains(e, s) {
				return bad(k)
			}
		}
		if (!isNull(d) && !isNull(v) && jsType(d) != jsType(v)) || isArr(d) != isArr(v) {
			return bad(k)
		}
		return nil
	}
	in := recOf(p)
	top := append([]string{"id", "name", "capability", "recommendedMethod", "contextFiles", "artifact_locations", "artifact_rules"}, settingsOrder...)
	for k := range in {
		if !slices.Contains(top, k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	out := pipelineRule{}
	for _, k := range settingsOrder {
		d := settingsDefaults[k]
		v, present := in[k]
		if !present {
			out[k] = d
			continue
		}
		switch {
		case k == "contextSources":
			if _, err := contract.ContextSources(rawAny(v)); err != nil {
				return nil, nil, fail(400, "Invalid contextSources: "+err.Error())
			}
			out[k] = v
		case k == "presets":
			if _, err := parsePresets(rawAny(v)); err != nil {
				return nil, nil, err
			}
			out[k] = v
		case k == "subscriptions":
			ok := isArr(v)
			for _, x := range jsonList(v) {
				if _, s := jsonString(x); !s {
					ok = false
				}
			}
			if !ok {
				return nil, nil, bad(k)
			}
			out[k] = v
		case k == "roles":
			o := pipelineRuleOf(v)
			ok := o != nil
			for _, x := range o {
				if _, s := jsonString(x); !s {
					ok = false
				}
			}
			if !ok {
				return nil, nil, bad(k)
			}
			out[k] = v
		case isArr(d):
			if !isArr(v) {
				return nil, nil, bad(k)
			}
			items := []json.RawMessage{}
			for i, it := range jsonList(v) {
				o := pipelineRuleOf(it)
				if o == nil {
					return nil, nil, bad(k)
				}
				for x, y := range o {
					if !slices.Contains(itemKeys[k], x) {
						unknown = append(unknown, k+"["+itoa(i)+"]."+x)
					} else if _, s := jsonString(y); !s {
						return nil, nil, bad(k + "[" + itoa(i) + "]." + x)
					}
				}
				text := func(key string) string { t, _ := jsonString(o[key]); return t }
				if k == "repos" {
					if text("remote") == "" {
						return nil, nil, bad(k + ".remote")
					}
					if err := check("repos.forge", jsonBytes(firstString(text("forge"), "github")), nil); err != nil {
						return nil, nil, err
					}
					r := pipelineRule{"forge": jsonBytes("github"), "defaultBranch": jsonBytes("main")}
					for x, y := range o {
						r[x] = y
					}
					items = append(items, jsonBytes(r))
					continue
				}
				if text("hostId") == "" || text("checkoutPath") == "" {
					return nil, nil, bad(k)
				}
				items = append(items, it)
			}
			out[k] = jsonBytes(items)
		case k == "identity":
			o := pipelineRuleOf(v)
			if o == nil {
				return nil, nil, bad(k)
			}
			for x := range o {
				if !slices.Contains(itemKeys["identity"], x) {
					unknown = append(unknown, "identity."+x)
				}
			}
			out[k] = v
		case isObj(d):
			o := pipelineRuleOf(v)
			if o == nil {
				return nil, nil, bad(k)
			}
			defaults := pipelineRuleOf(d)
			r := pipelineRule{}
			for x, y := range defaults {
				r[x] = y
			}
			for x, y := range o {
				dv, known := defaults[x]
				if !known {
					unknown = append(unknown, k+"."+x)
					continue
				}
				if err := check(k+"."+x, y, dv); err != nil {
					return nil, nil, err
				}
				if k == "execution" && x == "profiles" {
					seen := map[string]bool{}
					for _, value := range jsonList(y) {
						id, ok := jsonString(value)
						if !ok || strings.TrimSpace(id) == "" || seen[id] {
							return nil, nil, bad("execution.profiles")
						}
						seen[id] = true
					}
				}
				if k == "errors" && x == "ignore" {
					for _, value := range jsonList(y) {
						if s, ok := jsonString(value); !ok || strings.TrimSpace(s) == "" || len(s) > 200 {
							return nil, nil, bad("errors.ignore")
						}
					}
				}
				if k == "errors" && x == "bugsink" {
					for _, name := range jsonList(y) {
						if n, _ := jsonString(name); !bugsinkProjectName.MatchString(n) {
							return nil, nil, bad("errors.bugsink")
						}
					}
				}
				if k == "execution" && x == "tools" {
					for _, tool := range jsonList(y) {
						if t, _ := jsonString(tool); !slices.Contains([]string{"node", "pnpm", "browser"}, t) {
							return nil, nil, bad("execution.tools")
						}
					}
				}
				if k == "integration" && x == "autoAccept" && !isNull(y) && jsType(y) != "boolean" {
					return nil, nil, bad("integration.autoAccept")
				}
				r[x] = y
			}
			out[k] = jsonBytes(r)
		default:
			if err := check(k, v, d); err != nil {
				return nil, nil, err
			}
			out[k] = v
		}
	}
	loc, has := in["artifact_locations"]
	if has {
		ok := isArr(loc)
		xs := jsonList(loc)
		seen := map[string]bool{}
		for _, x := range xs {
			s, _ := jsonString(x)
			if s == "" || seen[s] {
				ok = false
			}
			seen[s] = true
		}
		if !ok || len(xs) == 0 {
			return nil, nil, bad("artifact_locations")
		}
	}
	out["artifact_locations"] = orNull(loc)
	rules, has := in["artifact_rules"]
	if has {
		ok := isArr(rules)
		for _, x := range jsonList(rules) {
			if !isObj(x) {
				ok = false
			}
		}
		if !ok {
			return nil, nil, bad("artifact_rules")
		}
	} else {
		rules = json.RawMessage("[]")
	}
	out["artifact_rules"] = rules
	settings, err := openObject(jsonBytes(out))
	return settings, unknown, err
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func shippedProjectIDs() []string {
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

func readJSON(path string) (rawObject, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return openObject(raw)
}

// projectContext: the definition with its settings, unknown keys and context files.
func shippedProjectContext(id string) (rawObject, error) {
	if !slices.Contains(shippedProjectIDs(), id) {
		return nil, fail(404, "Unknown project")
	}
	root := filepath.Join(configDir(), "projects", id)
	p, err := readJSON(filepath.Join(root, "project.json"))
	if err != nil {
		return nil, fail(500, "Invalid project definition")
	}
	head := agentView[struct {
		ID           json.RawMessage `json:"id"`
		ContextFiles json.RawMessage `json:"contextFiles"`
	}](p)
	files := jsonList(head.ContextFiles)
	if own, _ := jsonString(head.ID); own != id || jsonKind(head.ContextFiles) != '[' {
		return nil, fail(500, "Invalid project definition")
	}
	settings, unknown, err := projectSettings(p)
	if err != nil {
		return nil, err
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	ctx := []projectContextFile{} // seedProjectDefinitions (project_access.go) reads the context
	for _, f := range files {
		name, ok := jsonString(f)
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
		ctx = append(ctx, projectContextFile{Path: name, Text: string(text), Sha256: hex.EncodeToString(h[:])}) // projectContextFile: project_definitions.go
	}
	p["settings"], p["unknownKeys"], p["context"] = jsonBytes(settings), jsonBytes(unknown), openJSON(ctx)
	return p, nil
}

// projectSettingsView: the settings of a project context.
type projectSettingsView struct {
	Settings rawObject `json:"settings"`
}

func projectSettingsOf(q *Request, id string) rawObject {
	p, err := projectContext(q, id)
	if err != nil {
		return nil
	}
	return agentView[projectSettingsView](p).Settings
}

func catalogue(q *Request, id string) (rawObject, error) {
	c, err := catalogueBase(q, id)
	if err != nil {
		return nil, err
	}
	if err := enrichCatalogue(q, c); err != nil {
		return nil, err
	}
	return c, nil
}

func catalogueBase(q *Request, id string) (rawObject, error) {
	p, err := projectContext(q, id)
	if err != nil {
		return nil, err
	}
	c, err := catalogueHead(q)
	if err != nil {
		return nil, err
	}
	recs, err := records(q.db().ProjectRecommendation(q.ctx(), id))
	if err != nil {
		return nil, err
	}
	own := agentView[struct {
		RecommendedMethod json.RawMessage `json:"recommendedMethod"`
	}](p).RecommendedMethod
	head := agentView[struct {
		SharedDefault json.RawMessage   `json:"sharedDefault"`
		Methods       []json.RawMessage `json:"methods"`
	}](c)
	method, source := firstString(rawAny(own), rawAny(head.SharedDefault)), "shared"
	if !isNull(own) {
		source = "project"
	}
	if len(recs) > 0 {
		method, source = agentView[struct {
			MethodID string `json:"methodId"`
		}](recs[0]).MethodID, "project-selected"
	}
	methods := []any{}
	for _, x := range head.Methods {
		if sc, _ := jsonString(pipelineRuleOf(x)["scope"]); sc == "shared" || sc == id {
			methods = append(methods, decodeNumbers(string(x))) // a catalogue number keeps its text
		}
	}
	// The head's record (read for this call) becomes the project's catalogue: its capability and shared default stay.
	for k := range c {
		if k != "capability" && k != "sharedDefault" {
			delete(c, k)
		}
	}
	c["capability"], c["sharedDefault"] = orNull(c["capability"]), orNull(c["sharedDefault"])
	c["projectId"], c["recommendedMethod"], c["recommendationSource"], c["methods"] = jsonBytes(id), jsonBytes(method), jsonBytes(source), jsonBytes(methods)
	return c, nil
}

// projectOps: the operations of the projects MCP server (/api/project-tools) and of a native agent's project operations
// (host_receipts.go); each is registered with op first and listed here with addProjectOps.
var projectOps = map[string]registeredOp{}

// addProjectOps lists registered operations in projectOps; a long one carries its own handler (it opens its own
// transaction).
func addProjectOps(names ...string) {
	for _, n := range names {
		r, ok := operations[n]
		if !ok {
			panic("addProjectOps " + n + ": not registered")
		}
		if h, long := longOperations[n]; long {
			r.h = h
		}
		projectOps[n] = r
	}
}

func init() {
	op("project_detail", projectDetail)
	opGates["project_detail"] = resourceGate
	op("project_resources", projectPreparedResources)
	opGates["project_resources"] = resourceGate
	op("project_resource_items", projectPreparedItems)
	opGates["project_resource_items"] = resourceGate
	op("profiles_list", func(q *Request, _ *contract.ProfilesListRequest) (contract.ProfilesListReply, error) {
		ps, err := listProfiles(q)
		return contract.ProfilesListReply{Profiles: recsOf(ps)}, err
	})
	op("project_list", func(q *Request, _ *contract.ProjectListRequest) (contract.ProjectListReply, error) {
		out := []rawObject{}
		ids, err := projectIDs(q)
		if err != nil {
			return contract.ProjectListReply{}, err
		}
		codes, err := q.db().ProjectsCurrentCodes(q.ctx())
		if err != nil {
			return contract.ProjectListReply{}, err
		}
		currentCode := map[string]string{}
		for _, row := range codes {
			currentCode[row.ProjectID] = row.Code
		}
		for _, id := range ids {
			if q.person != nil && !mayAccessProjectID(q.person, id) {
				continue
			}
			p, err := projectContext(q, id)
			if err != nil {
				return contract.ProjectListReply{}, err
			}
			if code := currentCode[id]; code != "" {
				p["code"] = jsonBytes(code)
			}
			out = append(out, p)
		}
		return contract.ProjectListReply{Projects: recsOf(out)}, nil
	})
	op("project_context", func(q *Request, r *contract.ProjectContextRequest) (contract.ProjectContextReply, error) {
		p, err := projectContext(q, r.ProjectId)
		if err != nil {
			return contract.ProjectContextReply{}, err
		}
		res, err := projectResources(q, r.ProjectId)
		if err != nil {
			return contract.ProjectContextReply{}, err
		}
		p["resources"] = jsonBytes(res)
		return replyAs[contract.ProjectContextReply](p, nil)
	})
	op("catalogue", func(q *Request, r *contract.CatalogueRequest) (contract.CatalogueReply, error) {
		return replyAs[contract.CatalogueReply](catalogue(q, r.ProjectId))
	})
	op("recommendation_set", func(q *Request, r *contract.RecommendationSetRequest) (contract.RecommendationSetReply, error) {
		c, err := catalogueBase(q, r.ProjectId)
		if err != nil {
			return contract.RecommendationSetReply{}, err
		}
		found := false
		for _, x := range agentView[pipelineCatalogue](c).Methods {
			id, _ := jsonString(x.ID)
			found = found || id == r.MethodId
		}
		if !found {
			return contract.RecommendationSetReply{}, fail(400, "Method unavailable")
		}
		rec := contract.RecommendationSetReply{Id: "recommendation:" + r.ProjectId, ProjectId: r.ProjectId, MethodId: r.MethodId, SelectedAt: nowMs(), Actor: actor(q, "operator")}
		if err := q.db().ProjectRecommendationSet(q.ctx(), db.ProjectRecommendationSetParams{ID: rec.Id, ProjectID: r.ProjectId, MethodID: r.MethodId, Actor: rec.Actor}); err != nil {
			return contract.RecommendationSetReply{}, err
		}
		return rec, nil
	})
	op("recommendation_changes", recommendationChanges)
	op("question_ask", func(q *Request, r *contract.QuestionAskRequest) (contract.QuestionAskReply, error) {
		if _, err := projectContext(q, r.ProjectId); err != nil {
			return contract.QuestionAskReply{}, err
		}
		if r.RequestId == "" || r.Text == "" || len([]rune(r.Text)) > 2000 {
			return contract.QuestionAskReply{}, fail(400, "Expected question text and requestId")
		}
		found, err := q.db().ProjectsDecisionByRequest(q.ctx(), db.ProjectsDecisionByRequestParams{ProjectID: r.ProjectId, RequestID: r.RequestId})
		if err != nil {
			return contract.QuestionAskReply{}, err
		}
		if len(found) > 0 {
			d := projectQuestionOf(found[0])
			if d.Text != r.Text {
				return contract.QuestionAskReply{}, fail(409, "Question identity conflict")
			}
			return d, nil
		}
		v := contract.ProjectQuestion{Id: newID(), ProjectId: r.ProjectId, RequestId: r.RequestId, Text: r.Text, State: "waiting", CreatedAt: nowMs(), Actor: actor(q, "operator")}
		at := time.UnixMilli(v.CreatedAt).UTC()
		_, err = askQuestionAs(q, questionAsk{ID: v.Id, Kind: "operator_decision", Text: r.Text, ProjectID: r.ProjectId, AskedBy: &v.Actor, RequestID: &v.RequestId, AskedAt: &at})
		return v, err
	})
	op("question_answer", func(q *Request, r *contract.QuestionAnswerRequest) (contract.QuestionAnswerReply, error) {
		if r.Answer == "" || len([]rune(r.Answer)) > 4000 {
			return contract.QuestionAnswerReply{}, fail(400, "Expected answer")
		}
		pq, err := q.db().ProjectsQuestionByID(q.ctx(), r.QuestionId)
		if err != nil {
			return contract.QuestionAnswerReply{}, err
		}
		if len(pq) == 0 {
			var ids []any
			if r.ArtifactIds != nil {
				ids = []any{}
				for _, x := range r.ArtifactIds {
					ids = append(ids, x)
				}
			}
			a, err := answerQuestion(q, r.QuestionId, r.Answer, actor(q, "operator"), ids)
			if err != nil {
				return contract.QuestionAnswerReply{}, err
			}
			answered := agentView[struct {
				ID     json.RawMessage `json:"id"`
				State  json.RawMessage `json:"state"`
				Answer json.RawMessage `json:"answer"`
				TaskID string          `json:"task_id"`
			}](a)
			if x, err := findQuestion(q, answered.TaskID, r.QuestionId); x != nil || err != nil {
				return replyAs[contract.QuestionAnswerReply](x, err)
			}
			return contract.QuestionAnswerReply{"id": orNull(answered.ID), "state": orNull(answered.State), "answer": orNull(answered.Answer)}, nil
		}
		if v := projectQuestionOf(pq[0]); v.State == "answered" {
			if deref(v.Answer) != r.Answer {
				return contract.QuestionAnswerReply{}, fail(409, "Answer already recorded")
			}
			return recOf(v), nil
		}
		if _, err := answerQuestion(q, r.QuestionId, r.Answer, actor(q, "operator"), nil); err != nil {
			return contract.QuestionAnswerReply{}, err
		}
		pq, err = q.db().ProjectsQuestionByID(q.ctx(), r.QuestionId)
		if err != nil {
			return contract.QuestionAnswerReply{}, err
		}
		if len(pq) == 0 {
			return contract.QuestionAnswerReply{}, fail(404, "Unknown question")
		}
		return recOf(projectQuestionOf(pq[0])), nil
	})
	op("question_history", func(q *Request, r *contract.QuestionHistoryRequest) (contract.QuestionHistoryReply, error) {
		if _, err := projectContext(q, r.ProjectId); err != nil {
			return contract.QuestionHistoryReply{}, err
		}
		qs, err := q.db().ProjectsQuestionHistory(q.ctx(), r.ProjectId)
		out := contract.QuestionHistoryReply{Questions: []contract.ProjectQuestion{}}
		for _, x := range qs {
			out.Questions = append(out.Questions, projectQuestionOf(x))
		}
		return out, err
	})
	op("pipeline_get", func(q *Request, r *contract.PipelineGetRequest) (contract.PipelineGetReply, error) {
		if r.RunId == "" || len(r.RunId) > 200 {
			return contract.PipelineGetReply{}, fail(400, "Expected runId")
		}
		run, err := pipelineRun(q, r.RunId)
		if err != nil {
			return contract.PipelineGetReply{}, err
		}
		if p := deref(r.ProjectId); p != "" {
			if own, _ := jsonString(run["projectId"]); own != p {
				return contract.PipelineGetReply{}, fail(403, "Pipeline belongs to another project")
			}
		}
		return asT[contract.PipelineGetReply](run)
	})
	op("pipeline_history", pipelineHistory)
	addProjectOps("project_detail", "project_resources", "project_resource_items", "project_list", "project_context", "catalogue",
		"recommendation_set", "question_ask", "question_answer", "question_history", "pipeline_get", "pipeline_history",
		"pipeline_run", "direct_search", "connections_key_request", "access_grants", "access_grant_ask")
	longRoute("POST /api/project-tools", func(q *Request) (any, error) { return q.app.mcp(q, "fleet-projects", projectOps) })
}

func pipelineHistory(q *Request, r *contract.PipelineHistoryRequest) (contract.PipelineHistoryReply, error) {
	var afterSeq int64
	if r.Cursor != nil {
		var err error
		afterSeq, err = strconv.ParseInt(*r.Cursor, 10, 64)
		if err != nil || afterSeq <= 0 || strconv.FormatInt(afterSeq, 10) != *r.Cursor {
			return contract.PipelineHistoryReply{}, fail(400, "Invalid pipeline history cursor")
		}
	}
	limit := int64(25)
	if r.Limit != nil {
		limit = *r.Limit
	}
	if limit < 1 || limit > 100 {
		return contract.PipelineHistoryReply{}, fail(400, "Pipeline history limit must be between 1 and 100")
	}
	if _, err := projectContext(q, r.ProjectId); err != nil {
		return contract.PipelineHistoryReply{}, err
	}
	rows, err := q.db().ProjectsPipelineRunPage(q.ctx(), db.ProjectsPipelineRunPageParams{
		ProjectID: r.ProjectId, AfterSeq: afterSeq, PageSize: int32(limit + 1),
	})
	if err != nil {
		return contract.PipelineHistoryReply{}, err
	}

	out := contract.PipelineHistoryReply{Runs: []contract.PipelineRunReply{}}
	remaining := maxPipelineReplyBytes
	for _, row := range rows {
		if int64(len(out.Runs)) == limit {
			break
		}
		run, err := replyAs[contract.PipelineRunReply](pipelineRun(q, row.ID))
		if err != nil {
			return contract.PipelineHistoryReply{}, err
		}
		run, size, err := boundPipelineReply(run)
		if err != nil {
			return contract.PipelineHistoryReply{}, err
		}
		// Count each item's MCP envelope. The reserved frame space also covers
		// the page wrapper and cursor.
		if size > remaining {
			break
		}
		remaining -= size
		out.Runs = append(out.Runs, run)
		afterSeq = row.Seq
	}
	if len(out.Runs) < len(rows) {
		out.NextCursor = ptr(strconv.FormatInt(afterSeq, 10))
	}
	return out, nil
}

// pipelineRun: a pipeline run as its readers get it (planRead).
func pipelineRun(q *Request, id string) (recordRest, error) {
	run, err := getRun(q, id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fail(404, "Unknown pipeline run")
	}
	return run.planRead(q)
}

// mcp: an MCP tools server (mcpserve) over an operation table: tools/list publishes its advertised operations, tools/call
// runs one through runOp.
func (a *App) mcp(q *Request, server string, ops map[string]registeredOp) (any, error) {
	names := []string{}
	for n := range ops {
		if resourceOperationAdvertised(n) {
			names = append(names, n)
		}
	}
	out, err := mcpserve.Server{Name: server, Tools: names, Call: func(name string, args json.RawMessage) (any, error) {
		if isResourceRead(name) && !resourceReadsEnabled {
			return nil, fail(503, "Prepared project reads disabled")
		}
		if _, ok := ops[name]; !ok {
			return nil, fail(400, "Unknown tool")
		}
		return a.runOp(q, name, args, nil)
	}}.Handle(q.body)
	if errors.Is(err, mcpserve.ErrUnknownMethod) {
		return nil, fail(400, "Unknown tool")
	}
	return out, err
}

// artifactLocations: a project's artifact_locations; the default list when it names none.
func artifactLocations(a *App, projectID *string) []string {
	q := backgroundRequest(context.Background(), a, nil)
	if projectID != nil {
		if l := jsonStrings(projectSettingsOf(q, *projectID)["artifact_locations"]); len(l) > 0 {
			return l
		}
	}
	return defaultLocations
}

// Small project and agent lookups shared across domains.

func projectSetting(q *Request, projectID any, key string) any {
	p, ok := projectID.(string)
	if !ok || p == "" {
		return nil
	}
	st, err := projectSettingsByID(q, p)
	if err != nil {
		return nil
	}
	return rawAny(st[key])
}

// agentReady: agent.ready(command) in the poll's ready filter (a steer of a finished execution waits forever).
func agentReady(q *Request, kind, executionID string) (bool, error) {
	if kind != "steer" {
		return true, nil
	}
	e, err := getExecution(q, executionID)
	return err == nil && !e.terminal(), err
}

// takeTaskFrom: from is a record's value (text, else none).
func takeTaskFrom(q *Request, taskID, taker string, from any) error {
	f, _ := from.(string)
	return takeTask(q, taskID, taker, f)
}

// projectSettingsByID: project_context(projectId).settings; error when unknown.
func projectSettingsByID(q *Request, id string) (rawObject, error) {
	p, err := projectContext(q, id)
	if err != nil {
		return nil, err
	}
	return agentView[projectSettingsView](p).Settings, nil
}

// gitNetworkOf: whether the session's agent may use the network for git (its profile, else a project role with repos).
func gitNetworkOf(q *Request, s any) bool {
	sv := agentView[struct {
		AgentConfig     json.RawMessage `json:"agentConfig"`
		ProjectID       json.RawMessage `json:"projectId"`
		Role            json.RawMessage `json:"role"`
		ProjectSettings json.RawMessage `json:"projectSettings"`
	}](s)
	if v, ok := jsonBool(pipelineRuleOf(sv.AgentConfig)["gitNetwork"]); ok {
		return v
	}
	p, _ := jsonString(sv.ProjectID)
	role, _ := jsonString(sv.Role)
	if p == "" || (role != "pm" && role != "reviewer" && role != "worker") {
		return false
	}
	if st := pipelineRuleOf(sv.ProjectSettings); st != nil {
		return len(jsonList(st["repos"])) > 0
	}
	st, err := projectSettingsByID(q, p)
	return err == nil && len(jsonList(st["repos"])) > 0
}

// catalogueHead: the catalogue definition's head (one definition, config/catalogue.json as its seed).
func catalogueHead(q *Request) (rawObject, error) {
	raw, ok, err := headText(q, "catalogue", "catalogue")
	if err != nil || !ok {
		if err == nil {
			err = fail(404, "No catalogue")
		}
		return nil, err
	}
	// Numbers keep their text (json.Number); keys sorted at every level, as openObject.
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var c rawObject
	if dec.Decode(&v) != nil || json.Unmarshal(jsonBytes(v), &c) != nil || c == nil {
		return nil, fail(500, "Invalid catalogue definition")
	}
	return c, nil
}

// projectQuestionOf: a project question (queries/projects.sql) as the contract writes it: open is waiting; the answer,
// its time and its author only once answered.
func projectQuestionOf(x db.Question) contract.ProjectQuestion {
	ms := func(t time.Time) int64 { return int64(math.Round(float64(t.UnixMicro()) / 1000)) }
	v := contract.ProjectQuestion{Id: x.ID, ProjectId: deref(x.ProjectID), RequestId: deref(x.RequestID), Text: x.Text, State: string(x.State),
		CreatedAt: ms(x.AskedAt), Actor: deref(x.AskedBy)}
	if x.State == db.QuestionStateOpen {
		v.State = "waiting"
	}
	if x.State == db.QuestionStateAnswered {
		v.Answer, v.AnsweredBy = x.Answer, x.AnsweredBy
		if x.AnsweredAt != nil {
			v.AnsweredAt = ptr(ms(*x.AnsweredAt))
		}
	}
	return v
}

// Startup exposure is independent of processor scheduling; source capture has no flag.
var resourceReadsEnabled = os.Getenv("FLEET_RESOURCE_READS") == "on"

func isResourceRead(name string) bool {
	return name == "project_detail" || name == "project_resources" || name == "project_resource_items"
}
func resourceOperationAdvertised(name string) bool {
	return !isResourceRead(name) || resourceReadsEnabled
}

// resourceGate: the prepared project reads' gate, before the body is decoded (opGates): reads off 503, no projectId 400,
// an unknown or retired project 404. The grant is the declared project:projectId rule.
func resourceGate(q *Request) error {
	if !resourceReadsEnabled {
		return fail(503, "Prepared project reads disabled")
	}
	id := jsonPathString(q.body, "projectId")
	if strings.TrimSpace(id) == "" {
		return fail(400, "Expected projectId")
	}
	exists, err := q.db().ProjectsLiveDefinition(q.ctx(), id)
	if err != nil {
		return err
	}
	if !exists {
		return fail(404, "Unknown project")
	}
	return nil
}

func projectDetail(q *Request, r *contract.ProjectDetailRequest) (contract.ProjectDetailReply, error) {
	p, err := projectContext(q, r.ProjectId)
	if err != nil {
		return contract.ProjectDetailReply{}, err
	}
	// Only this explicit shape reaches the new reader; legacy flattened settings stay legacy.
	rec, out := recOf(p), pipelineRule{}
	for _, k := range []string{"id", "name", "content", "settings", "unknownKeys", "context", "revision", "contentSha", "repoSha", "sourceState", "initialAccess"} {
		if v, ok := rec[k]; ok {
			out[k] = v
		}
	}
	return asT[contract.ProjectDetailReply](out)
}
func projectPreparedResources(q *Request, r *contract.ProjectResourcesRequest) (contract.ProjectResourcesReply, error) {
	s, err := resourceReadPrepared(q, r.ProjectId)
	if err != nil {
		return nil, err
	}
	progress := resourcePublicProgress(s)
	if !s.Ready {
		resourceTraceResponse(q, r.ProjectId, "", "preparing")
		return jsonBytes(contract.ResourcePreparingReply{Kind: "preparing", Version: json.RawMessage("null"), ProjectId: r.ProjectId, Progress: progress}), nil
	}
	version := s.Epoch + ":" + s.Revision
	if r.KnownVersion != nil && *r.KnownVersion == version {
		resourceTraceResponse(q, r.ProjectId, version, "unchanged")
		return jsonBytes(contract.ResourceUnchangedReply{Kind: "unchanged", ProjectId: r.ProjectId, Version: version, Progress: progress}), nil
	}
	resourceTraceResponse(q, r.ProjectId, version, "ready")
	return jsonBytes(contract.ResourceReadyReply{Kind: "ready", ProjectId: r.ProjectId, Version: version, Header: s.Header, Progress: progress}), nil
}
func projectPreparedItems(q *Request, r *contract.ProjectResourceItemsRequest) (contract.ProjectResourceItemsReply, error) {
	if err := hostKeys(q.body, reflect.TypeOf(*r)); err != nil {
		return nil, err
	}
	return resourceReadItems(q, r)
}

// recommendationChanges: the recommendations projects selected in the last days (default 7, at most 90), newest first,
// with the method's name and the Lab experiment it came from as the catalogue head names them (Library idle, J3).
func recommendationChanges(q *Request, r *contract.RecommendationChangesRequest) (contract.RecommendationChangesReply, error) {
	days := int64(7)
	if r.Days != nil {
		days = *r.Days
	}
	if days < 1 || days > 90 {
		return contract.RecommendationChangesReply{}, fail(400, "days must be 1 to 90")
	}
	rows, err := q.db().RecommendationChanges(q.ctx(), int32(days))
	if err != nil {
		return contract.RecommendationChangesReply{}, err
	}
	type method struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		SourceExperiment string `json:"sourceExperiment"`
	}
	methods := map[string]method{}
	if len(rows) > 0 {
		c, err := catalogueHead(q)
		if err != nil {
			return contract.RecommendationChangesReply{}, err
		}
		for _, m := range agentView[struct {
			Methods []method `json:"methods"`
		}](c).Methods {
			methods[m.ID] = m
		}
	}
	out := contract.RecommendationChangesReply{Changes: []contract.RecommendationChange{}}
	for _, x := range rows {
		if q.person != nil && !mayAccessProjectID(q.person, x.ProjectID) {
			continue
		}
		c := contract.RecommendationChange{ProjectId: x.ProjectID, MethodId: x.MethodID, SelectedAt: x.SelectedAtMs, Actor: x.Actor}
		if m, ok := methods[x.MethodID]; ok {
			c.MethodName = &m.Name
			if m.SourceExperiment != "" {
				c.SourceExperiment = &m.SourceExperiment
			}
		}
		out.Changes = append(out.Changes, c)
	}
	return out, nil
}
