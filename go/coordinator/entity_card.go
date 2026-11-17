// entity_card: the card summary of one entity for an entity reference (docs/design-system/controls/entity-card.md), by
// D17 code or by kind and id. One read for the app's card and for agents (an MCP tool like every operation). Each kind is
// one reader over the tables and caches the kind's own reads use; row-level security and the reads' own gates decide
// what a caller sees, and an unknown or forbidden entity both answer 404.
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
)

type cardReader func(q *Request, id string) (*contract.EntityCardReply, error)

var cardReaders = map[contract.EntityKind]cardReader{
	contract.EntityKindTask: cardTask, contract.EntityKindQuestion: cardQuestion, contract.EntityKindConversation: cardConversation,
	contract.EntityKindExecution: cardExecution, contract.EntityKindResult: cardResult, contract.EntityKindExperiment: cardExperiment,
	contract.EntityKindProject: cardProject, contract.EntityKindHost: cardHost, contract.EntityKindAgent: cardDefinition("agent"),
	contract.EntityKindPipeline: cardDefinition("pipeline"), contract.EntityKindPullRequest: cardPullRequest, contract.EntityKindRelease: cardRelease,
}

// codeKinds: the D17 type letters the card resolves (spec rules, S-, are not entities).
var codeKinds = map[string]contract.EntityKind{"T": contract.EntityKindTask, "Q": contract.EntityKindQuestion, "C": contract.EntityKindConversation}

func init() {
	gatedObjOp("entity_card", true, typedOp(func(q *Request, r *contract.EntityCardRequest) (contract.EntityCardReply, error) {
		kind, id := contract.EntityKind(""), strings.TrimSpace(deref(r.Id))
		if r.Kind != nil {
			kind = *r.Kind
		}
		switch {
		case r.Code != nil && (r.Kind != nil || r.Id != nil):
			return contract.EntityCardReply{}, fail(400, "Expected a code, or a kind with its id")
		case r.Code != nil:
			code := strings.ToUpper(strings.TrimSpace(*r.Code))
			if m := codePattern.FindString(code); code == "" || m != code || codeKinds[code[:1]] == "" {
				return contract.EntityCardReply{}, fail(400, "Expected a code such as T-FLT-42")
			}
			found, err := resolveCodes(q, []string{code})
			if err != nil {
				return contract.EntityCardReply{}, err
			}
			e, ok := found[code]
			if !ok {
				return contract.EntityCardReply{}, errNotFound
			}
			kind, id = codeKinds[e.Type], e.Id
		case kind == "" || id == "":
			return contract.EntityCardReply{}, fail(400, "Expected a code, or a kind with its id")
		}
		c, err := cardReaders[kind](q, id)
		if err != nil {
			return contract.EntityCardReply{}, err
		}
		if c == nil {
			return contract.EntityCardReply{}, errNotFound
		}
		c.Kind = kind
		if c.Facts == nil {
			c.Facts = []contract.EntityFact{}
		}
		return *c, nil
	}))
	agentDescriptions["entity_card"] = "A short card of one entity: code (T-FLT-42, Q-…, C-…, old codes too) or kind (task, question, conversation, execution, result, experiment, project, host, agent, pipeline, pull_request as owner/repo#n, release as its tag) with id. Returns its state, title, project, harness, last time, 1-4 facts, the app path and an outside url; unknown and forbidden are both 404"
}

func fact(label string, t contract.EntityFactType, value string) contract.EntityFact {
	return contract.EntityFact{Label: label, Type: t, Value: value}
}

// facts: the first four facts whose value is known (OR-29: no zero counts), in order.
func facts(fs ...contract.EntityFact) []contract.EntityFact {
	fs = slices.DeleteFunc(fs, func(f contract.EntityFact) bool {
		return f.Value == "" || f.Value == "0" && f.Type == contract.EntityFactTypeCount
	})
	return fs[:min(4, len(fs))]
}

// timeOf: an RFC 3339 time, zero when unreadable (compared as times, not as text: fractions differ in length).
func timeOf(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }

// sizeOf: a byte size, "" (no fact) when none is recorded.
func sizeOf(v any) string {
	if v == nil || v == "" {
		return ""
	}
	return count(v)
}

// count: an integer fact value (a float64 count would print 1e+06).
func count(v any) string { return strconv.FormatInt(int64(num(v)), 10) }

func itemPath(section, id string) *string { return ptr("/" + section + "/" + url.PathEscape(id)) }

func cardTask(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := first(q, `SELECT t.id,t.code,t.title,`+stateSQL+` AS state,t.project_id,t.updated_at,t.wait_reason,t.priority,
	 coalesce(d.agent,d.last_agent) AS agent,d.host,d.open_questions,d.executions AS runs
	 FROM task t LEFT JOIN task_derived d ON d.task_id=t.id WHERE t.id=$1`, id)
	if r == nil || err != nil {
		return nil, err
	}
	wait := ""
	if s := astr(r["state"]); s == "waiting" || s == "held" || s == "paused" {
		wait = astr(r["wait_reason"])
	}
	priority := astr(r["priority"])
	if priority == "normal" {
		priority = ""
	}
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Code: optStr(astr(r["code"])), Title: astr(r["title"]), State: optStr(astr(r["state"])), ProjectId: optStr(astr(r["project_id"])),
		Harness: optStr(astr(r["agent"])), At: optStr(astr(r["updated_at"])), AtLabel: ptr("changed"), Path: itemPath("tasks", id),
		Facts: facts(fact("Waiting for", contract.EntityFactTypeText, wait), fact("Runs", contract.EntityFactTypeCount, count(r["runs"])),
			fact("Open questions", contract.EntityFactTypeCount, count(r["open_questions"])), fact("Host", contract.EntityFactTypeCode, astr(r["host"])),
			fact("Priority", contract.EntityFactTypeText, priority))}, nil
}

func cardQuestion(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := first(q, `SELECT x.id,x.code,x.text,x.state::text AS state,x.project_id,x.task_id,x.session_id,x.asked_by,x.asked_at,x.answered_by,x.answer,t.code AS task_code
	 FROM question x LEFT JOIN task t ON t.id=x.task_id WHERE x.id=$1`, id)
	if r == nil || err != nil {
		return nil, err
	}
	text := []rune(astr(r["text"]))
	path := ptr("/overview")
	if t := astr(r["task_id"]); t != "" {
		path = itemPath("tasks", t)
	} else if s := astr(r["session_id"]); s != "" {
		path = itemPath("conversations", s)
	}
	answer := []rune(astr(r["answer"]))
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Code: optStr(astr(r["code"])), Title: string(text[:min(200, len(text))]), State: optStr(astr(r["state"])),
		ProjectId: optStr(astr(r["project_id"])), At: optStr(astr(r["asked_at"])), AtLabel: ptr("asked"), Path: path,
		Facts: facts(fact("Asked by", contract.EntityFactTypeText, astr(r["asked_by"])), fact("Task", contract.EntityFactTypeCode, astr(r["task_code"])),
			fact("Answer", contract.EntityFactTypeText, string(answer[:min(120, len(answer))])), fact("Answered by", contract.EntityFactTypeText, astr(r["answered_by"])))}, nil
}

// cardConversation: one session with the sessions_list columns, read for this session only (sessionBase reads all).
func cardConversation(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := first(q, `SELECT d.id,d.code,d.project_id,d.data->'agentConfig'->>'id' AS profile,t.code AS task_code,
	 coalesce(last.state,'idle') AS state,coalesce(last.harness,d.data->'agentConfig'->>'harness') AS harness,
	 (SELECT m.data->>'text' FROM document m WHERE m.session_ref=d.id AND m.kind='message' ORDER BY m.seq LIMIT 1) AS first_message,
	 greatest(d.updated_at,(SELECT max(a.updated_at) FROM document a WHERE a.session_ref=d.id AND a.kind IN ('message','event')),ex.last_at) AS activity_at,
	 ex.n AS runs,(SELECT count(*) FROM question x WHERE x.session_id=d.id AND x.state='open') AS open_questions
	 FROM document d LEFT JOIN task t ON t.id=d.data->>'taskId'
	 LEFT JOIN LATERAL (SELECT e.state::text AS state,e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) last ON true
	 LEFT JOIN LATERAL (SELECT count(*) AS n,max(greatest(e.started_at,e.ended_at)) AS last_at FROM execution e WHERE e.session_id=d.id) ex ON true
	 WHERE d.kind='session' AND d.id=$1`, id)
	if r == nil || err != nil {
		return nil, err
	}
	title := []rune(strings.Join(strings.Fields(firstString(astr(r["first_message"]), astr(r["code"]), "Conversation")), " "))
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Code: optStr(astr(r["code"])), Title: string(title[:min(200, len(title))]), State: optStr(astr(r["state"])),
		ProjectId: optStr(astr(r["project_id"])), Harness: optStr(astr(r["harness"])), At: optStr(astr(r["activity_at"])), AtLabel: ptr("active"), Path: itemPath("conversations", id),
		Facts: facts(fact("Profile", contract.EntityFactTypeCode, astr(r["profile"])), fact("Task", contract.EntityFactTypeCode, astr(r["task_code"])),
			fact("Runs", contract.EntityFactTypeCount, count(r["runs"])), fact("Open questions", contract.EntityFactTypeCount, count(r["open_questions"])))}, nil
}

var executionKindLabel = map[string]string{"agent_run": "Agent run", "pipeline_run": "Pipeline run", "pipeline_step": "Pipeline step", "child_agent": "Child agent", "tool_run": "Tool run", "host_job": "Host job"}

func cardExecution(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := first(q, `SELECT e.id,e.kind::text AS kind,e.state::text AS state,e.project_id,e.task_id,e.session_id,e.experiment_id,e.host_id,e.harness,e.model,e.problem,
	 e.started_at,e.ended_at,e.updated_at,extract(epoch FROM e.ended_at-e.started_at) AS secs,t.code AS task_code,t.title AS task_title,s.code AS session_code
	 FROM execution e LEFT JOIN task t ON t.id=e.task_id LEFT JOIN document s ON s.kind='session' AND s.id=e.session_id WHERE e.id=$1`, id)
	if r == nil || err != nil || astr(r["experiment_id"]) != "" && !architect(q) { // a Lab trial is the architect's, as its experiment
		return nil, err
	}
	path := ptr("/runners")
	switch {
	case astr(r["session_id"]) != "":
		path = itemPath("conversations", astr(r["session_id"]))
	case astr(r["task_id"]) != "":
		path = itemPath("tasks", astr(r["task_id"]))
	case astr(r["experiment_id"]) != "":
		path = itemPath("lab", astr(r["experiment_id"]))
	}
	secs := ""
	if r["secs"] != nil {
		secs = strconv.FormatFloat(num(r["secs"]), 'f', 0, 64)
	}
	at, label := firstString(astr(r["ended_at"]), astr(r["started_at"]), astr(r["updated_at"])), "changed"
	if astr(r["ended_at"]) != "" {
		label = "ended"
	} else if astr(r["started_at"]) != "" {
		label = "started"
	}
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Title: firstString(astr(r["task_title"]), executionKindLabel[astr(r["kind"])], "Run"), State: optStr(astr(r["state"])),
		ProjectId: optStr(astr(r["project_id"])), Harness: optStr(astr(r["harness"])), At: optStr(at), AtLabel: ptr(label), Path: path,
		Facts: facts(fact("Problem", contract.EntityFactTypeText, astr(r["problem"])), fact("Task", contract.EntityFactTypeCode, astr(r["task_code"])),
			fact("Duration", contract.EntityFactTypeDuration, secs), fact("Host", contract.EntityFactTypeCode, astr(r["host_id"])),
			fact("Model", contract.EntityFactTypeCode, astr(r["model"])), fact("Conversation", contract.EntityFactTypeCode, astr(r["session_code"])))}, nil
}

func cardResult(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := first(q, `SELECT a.id,a.name,a.state,a.project_id,a.media_type,a.size,a.content_hash,a.created_at,t.code AS task_code
	 FROM artifact a LEFT JOIN task t ON t.id=coalesce(a.task_id,(SELECT e.task_id FROM execution e WHERE e.id=a.execution_id)) WHERE a.id=$1 OR a.legacy_id=$1`, id)
	if r == nil && err == nil { // a legacy result kept as a document (task_view's latest_result reads both)
		r, err = first(q, `SELECT d.id,coalesce(d.data->>'path',d.data->>'name') AS name,'stored' AS state,coalesce(d.project_id,d.data->>'projectId') AS project_id,
		 d.data->>'mediaType' AS media_type,d.data->>'size' AS size,d.data->>'sha256' AS content_hash,d.updated_at AS created_at,t.code AS task_code,t.id AS legacy_task
		 FROM document d LEFT JOIN execution e ON e.id=d.data->>'executionId' LEFT JOIN task t ON t.id=e.task_id WHERE d.kind='artifact' AND d.id=$1`, id)
	}
	if r == nil || err != nil {
		return nil, err
	}
	id = astr(r["id"])
	path := itemPath("results", id)
	if _, legacy := r["legacy_task"]; legacy { // Results reads the artifact table only: a legacy result opens its task
		path = nil
		if t := astr(r["legacy_task"]); t != "" {
			path = itemPath("tasks", t)
		}
	}
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Hash: optStr(astr(r["content_hash"])), Title: firstString(astr(r["name"]), "Result"), State: optStr(astr(r["state"])),
		ProjectId: optStr(astr(r["project_id"])), At: optStr(astr(r["created_at"])), AtLabel: ptr("stored"), Path: path,
		Facts: facts(fact("Task", contract.EntityFactTypeCode, astr(r["task_code"])), fact("Type", contract.EntityFactTypeCode, astr(r["media_type"])),
			fact("Size", contract.EntityFactTypeBytes, sizeOf(r["size"])))}, nil
}

// architect: definitions and Lab are the architect's to read (people.go architectOps); an agent without a person passes.
func architect(q *Request) bool {
	return fullAccess(q.person) || hasRole(q.person, "architect")
}

func cardExperiment(q *Request, id string) (*contract.EntityCardReply, error) {
	if !architect(q) {
		return nil, nil
	}
	r, err := first(q, `SELECT x.id,x.kind,x.state,x.project_id,x.pipeline_id,x.goal,x.decision,x.created_at,coalesce(x.launched_at,x.created_at) AS at,
	 jsonb_array_length(x.variants) AS variants,(SELECT count(*) FROM execution e WHERE e.experiment_id=x.id AND e.kind='pipeline_run') AS trials
	 FROM experiment x WHERE x.id=$1`, id)
	if r == nil || err != nil {
		return nil, err
	}
	label := "created"
	if astr(r["at"]) != astr(r["created_at"]) {
		label = "launched"
	}
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Title: firstString(astr(r["goal"]), astr(r["pipeline_id"])), State: optStr(astr(r["state"])),
		ProjectId: optStr(astr(r["project_id"])), At: optStr(astr(r["at"])), AtLabel: ptr(label), Path: itemPath("lab", id),
		Facts: facts(fact("Pipeline", contract.EntityFactTypeCode, astr(r["pipeline_id"])), fact("Trials", contract.EntityFactTypeCount, count(r["trials"])),
			fact("Variants", contract.EntityFactTypeCount, count(r["variants"])), fact("Decision", contract.EntityFactTypeText, astr(r["decision"])))}, nil
}

func cardProject(q *Request, id string) (*contract.EntityCardReply, error) {
	if q.person != nil && !mayAccessProjectID(q.person, id) {
		return nil, nil
	}
	xs, err := definitionRows(q, "d.kind='project' AND d.id=$1 AND d.deleted_at IS NULL", 0, id)
	if err != nil || len(xs) == 0 {
		return nil, err
	}
	var b doc
	_ = json.Unmarshal(xs[0].Content, &b)
	r, err := first(q, `SELECT (SELECT code FROM project_code WHERE project_id=$1 AND current) AS code,
	 (SELECT count(*) FROM task WHERE project_id=$1 AND state IN ('queued','in_progress','waiting','paused')) AS open,
	 (SELECT count(*) FROM execution WHERE project_id=$1 AND state IN ('preparing','running')) AS running,
	 (SELECT count(*) FROM question WHERE project_id=$1 AND state='open') AS questions`, id)
	if err != nil {
		return nil, err
	}
	return &contract.EntityCardReply{Id: id, Code: optStr(astr(r["code"])), Title: firstString(astr(mapOf(b["settings"])["name"]), id),
		At: ptr(xs[0].At.UTC().Format(time.RFC3339Nano)), AtLabel: ptr("changed"), Path: itemPath("projects", id),
		Facts: facts(fact("Open tasks", contract.EntityFactTypeCount, count(r["open"])), fact("Running", contract.EntityFactTypeCount, count(r["running"])),
			fact("Open questions", contract.EntityFactTypeCount, count(r["questions"])))}, nil
}

// cardHost: one host as hosts_list shows it: online while it polled within hostStaleMs, failing while its polls fail.
func cardHost(q *Request, id string) (*contract.EntityCardReply, error) {
	list, err := hostsList(q, false)
	if err != nil {
		return nil, err
	}
	hosts, _ := list.(doc)["hosts"].([]doc)
	for _, h := range hosts {
		if astr(h["id"]) != id {
			continue
		}
		state, seen := "offline", int64(num(h["lastSeenAt"]))
		if h["lastSeenAt"] != nil && nowMs()-seen < hostStaleMs { // polling, as hostsList and the Hosts page decide it
			state = "online"
			if h["pollError"] != nil {
				state = "failing"
			}
		}
		var at *string
		if h["lastSeenAt"] != nil {
			at = ptr(time.UnixMilli(seen).UTC().Format(time.RFC3339Nano))
		}
		return &contract.EntityCardReply{Id: id, Title: id, State: ptr(state), At: at, AtLabel: ptr("seen"), Path: itemPath("hosts", id),
			Facts: facts(fact("Version", contract.EntityFactTypeCode, astr(h["version"])), fact("Running", contract.EntityFactTypeCount, count(h["running"])),
				fact("Queued", contract.EntityFactTypeCount, count(h["queued"])), fact("Platform", contract.EntityFactTypeText, astr(h["platform"])))}, nil
	}
	return nil, nil
}

func cardDefinition(kind string) cardReader {
	return func(q *Request, id string) (*contract.EntityCardReply, error) {
		if !architect(q) {
			return nil, nil
		}
		xs, err := definitionRows(q, "d.kind=$1 AND d.id=$2 AND d.deleted_at IS NULL", 0, kind, id) // retired ones still open, as definition_get
		if err != nil || len(xs) == 0 {
			return nil, err
		}
		x := xs[0]
		var v doc
		_ = json.Unmarshal(x.Content, &v)
		c := &contract.EntityCardReply{Id: id, Title: definitionName(x), At: ptr(x.At.UTC().Format(time.RFC3339Nano)), AtLabel: ptr("changed"), Path: itemPath(kind+"s", id)}
		if c.Title != id {
			c.Code = ptr(id)
		}
		if kind == "agent" {
			c.Harness = ptr(profileHarness(x.Content))
			c.Facts = facts(fact("Model", contract.EntityFactTypeCode, astr(v["model"])), fact("Effort", contract.EntityFactTypeText, astr(v["effort"])), fact("Revision", contract.EntityFactTypeCount, fmt.Sprint(x.Revision)))
		} else {
			steps, _ := v["steps"].([]any)
			c.Facts = facts(fact("Steps", contract.EntityFactTypeCount, fmt.Sprint(len(steps))), fact("Revision", contract.EntityFactTypeCount, fmt.Sprint(x.Revision)))
		}
		return c, nil
	}
}

// deliveryVisible: the last delivery pass as delivery_status shows it to the caller (every project the caller sees).
func deliveryVisible(q *Request) contract.DeliveryStatusReply {
	r, _ := deliveryStatus(q, &contract.DeliveryStatusRequest{}) // fails only on its own excludeProjectIds, none here
	return r
}

// cardPullRequest: a pull request of the delivery pass by owner/repo#number (the Delivery list's owner~repo~number too).
func cardPullRequest(q *Request, id string) (*contract.EntityCardReply, error) {
	repo, n, ok := strings.Cut(strings.ReplaceAll(id, "~", "/"), "#")
	if !ok { // owner/repo/number
		if i := strings.LastIndex(repo, "/"); i > 0 {
			repo, n = repo[:i], repo[i+1:]
		}
	}
	number, err := strconv.ParseInt(n, 10, 64)
	if err != nil || strings.Count(repo, "/") != 1 {
		return nil, fail(400, "Expected a pull request as owner/repo#number")
	}
	n = strconv.FormatInt(number, 10)
	for _, it := range deliveryVisible(q).Items {
		if !strings.EqualFold(it.Repo, repo) || it.Number != number {
			continue
		}
		return &contract.EntityCardReply{Id: it.Repo + "#" + n, Code: ptr("#" + n), Title: it.Title, State: ptr(string(it.Stage)), ProjectId: optStr(it.ProjectId),
			At: ptr(it.Since), AtLabel: ptr("since"), Url: ptr(it.Url), Path: itemPath("delivery", strings.ReplaceAll(it.Repo, "/", "~")+"~"+n),
			Facts: facts(fact("Repository", contract.EntityFactTypeCode, it.Repo), fact("Release", contract.EntityFactTypeCode, deref(it.Release)),
				fact("Opened", contract.EntityFactTypeTime, deref(it.Opened)), fact("Merged", contract.EntityFactTypeTime, deref(it.Merged)))}, nil
	}
	return cardPullRequestHistory(q, repo, number)
}

// cardPullRequestHistory: a merged pull request of the fleet repository the current pass no longer holds, from the
// delivery history (delivery_history.go, the Delivery list's picked days), under delivery_history's own gate.
func cardPullRequestHistory(q *Request, repo string, number int64) (*contract.EntityCardReply, error) {
	if !strings.EqualFold(repo, ciOwner+"/"+ciRepo) || deliveryMay(q) != nil {
		return nil, nil
	}
	d, err := first(q, "SELECT data FROM document WHERE id=$1 AND kind='delivery-pr'", "delivery-pr:"+strconv.FormatInt(number, 10))
	if d == nil || err != nil {
		return nil, err
	}
	var x contract.DeliveryPr
	if err := json.Unmarshal(jsonBytes(d["data"]), &x); err != nil {
		return nil, err
	}
	// The stage as Delivery's fromHistory puts it: delivered (on the coordinator, or the UI on site for a UI-only release),
	// else merged when it was released before installs were recorded, else installing once released, else releasing.
	st, err := dhStateOf(q)
	if err != nil {
		return nil, err
	}
	n, done := strconv.FormatInt(number, 10), x.Installed["coordinator"]
	if uiOnlyRelease(deref(x.Release)) {
		done = deref(x.UiOnSite)
	}
	stage, at := "releasing", x.Merged
	switch {
	case done != "":
		stage, at = "installed", done
	case x.Released != nil && st.InstallsFrom != "" && timeOf(*x.Released).Before(timeOf(st.InstallsFrom)):
		stage, at = "merged", *x.Released
	case x.Released != nil:
		stage, at = "installing", *x.Released
	}
	return &contract.EntityCardReply{Id: ciOwner + "/" + ciRepo + "#" + n, Code: ptr("#" + n), Title: x.Title, State: ptr(stage), ProjectId: ptr(ciProject),
		At: optStr(at), AtLabel: ptr("since"), Url: optStr(x.Url), Path: itemPath("delivery", ciOwner+"~"+ciRepo+"~"+n),
		Facts: facts(fact("Author", contract.EntityFactTypeText, deref(x.Author)), fact("Release", contract.EntityFactTypeCode, deref(x.Release)),
			fact("Opened", contract.EntityFactTypeTime, x.Opened), fact("Merged", contract.EntityFactTypeTime, x.Merged))}, nil
}

// cardRelease: a release tag the delivery pass or history knows, or that a host or the site runs; installed as the
// delivery pass decides it (delivery.go): every host seen within the hour that reports a version runs it or newer; a
// UI-only release (ui-<backend>-v<UI>) installs on no host and is installed once the site serves that UI or newer.
func cardRelease(q *Request, id string) (*contract.EntityCardReply, error) {
	tag := strings.TrimSpace(id)
	if !deliveryReleaseTag(tag) || tag == "v" {
		return nil, fail(400, "Expected a release tag such as v1.2.3")
	}
	prs, installing := 0, false
	for _, it := range deliveryVisible(q).Items {
		if deref(it.Release) == tag {
			prs++
			installing = installing || it.Stage != contract.DeliveryStageInstalled
		}
	}
	rec, err := first(q, "SELECT data FROM document WHERE id=$1 AND kind='delivery-release'", "delivery-release:"+tag)
	if err != nil {
		return nil, err
	}
	var history contract.DeliveryRelease
	if rec != nil {
		_ = json.Unmarshal(jsonBytes(rec["data"]), &history)
	}
	version, known := releaseVersionOf(tag), prs > 0 || rec != nil
	// Open lists its pull requests while the pass holds them; an older release opens Delivery, whose history has it.
	path := "/delivery"
	if prs > 0 {
		path += "?q=" + url.QueryEscape("release="+tag+".") // Delivery's search text ends each release with a dot, so v1.2.3 is not v1.2.31
	}
	c := &contract.EntityCardReply{Id: tag, Code: ptr(tag), State: ptr("installing"), Path: ptr(path)}
	if uiOnlyRelease(tag) {
		on := uiServed()
		if !known && (on != version || !fullAccess(q.person)) {
			return nil, nil
		}
		c.Title, c.Facts = "UI release "+version, facts(fact("Hosts", contract.EntityFactTypeText, "install nothing: the web app only"), fact("Site serves", contract.EntityFactTypeCode, on),
			fact("Pull requests", contract.EntityFactTypeCount, fmt.Sprint(prs)))
		if on != "" && !versionLess(on, version) {
			c.State = ptr("installed")
		}
		return c, nil
	}
	c.Title = "Release " + version
	if !fullAccess(q.person) {
		// A person sees only the hosts they hold: the state is the one the delivery pass gave this release's pull requests,
		// else the delivery history's record of its install on the coordinator.
		if !known {
			return nil, nil
		}
		if prs > 0 && !installing || prs == 0 && history.Installed["coordinator"] != "" {
			c.State = ptr("installed")
		}
		c.Facts = facts(fact("Pull requests", contract.EntityFactTypeCount, fmt.Sprint(prs)))
		return c, nil
	}
	versions, err := liveHostVersions(q)
	if err != nil {
		return nil, err
	}
	if !known && !slices.Contains(versions, version) {
		return nil, nil
	}
	on := 0
	for _, v := range versions {
		if !versionLess(v, version) {
			on++
		}
	}
	c.Facts = facts(fact("Hosts", contract.EntityFactTypeText, fmt.Sprintf("%d of %d", on, len(versions))), fact("Pull requests", contract.EntityFactTypeCount, fmt.Sprint(prs)))
	if installed(version, versions) {
		c.State = ptr("installed")
	}
	return c, nil
}
