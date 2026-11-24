// entity_card: the card summary of one entity for an entity reference (docs/design-system/controls/entity-card.md), by
// D17 code or by kind and id. One read for the app's card and for agents (an MCP tool like every operation). Each kind is
// one reader over the tables and caches the kind's own reads use; row-level security and the reads' own gates decide
// what a caller sees, and an unknown or forbidden entity both answer 404.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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
// A bot (B) has no card kind yet: entity_resolve resolves its code.
var codeKinds = map[string]contract.EntityKind{"T": contract.EntityKindTask, "Q": contract.EntityKindQuestion, "C": contract.EntityKindConversation, "R": contract.EntityKindResult}

func init() {
	op("entity_card", func(q *Request, r *contract.EntityCardRequest) (contract.EntityCardReply, error) {
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
	})
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

// cardTime: a stored time as the card shows it (RFC 3339 in UTC); "" (no time) for none.
func cardTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// count: an integer fact value.
func count(n int64) string { return strconv.FormatInt(n, 10) }

func itemPath(section, id string) *string { return ptr("/" + section + "/" + url.PathEscape(id)) }

// cardRow: a card read's row, nil when there is none (an unknown or forbidden entity).
func cardRow[T any](r T, err error) (*T, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func cardTask(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := cardRow(q.db().EntCardTask(q.ctx(), id))
	if r == nil || err != nil {
		return nil, err
	}
	wait := ""
	if r.State == "waiting" || r.State == "held" || r.State == "paused" {
		wait = deref(r.WaitReason)
	}
	priority := r.Priority
	if priority == "normal" {
		priority = ""
	}
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Code: optStr(deref(r.Code)), Title: r.Title, State: optStr(r.State), ProjectId: optStr(deref(r.ProjectID)),
		Harness: optStr(r.Agent), At: optStr(cardTime(&r.UpdatedAt)), AtLabel: ptr("changed"), Path: itemPath("tasks", id),
		Facts: facts(fact("Waiting for", contract.EntityFactTypeText, wait), fact("Runs", contract.EntityFactTypeCount, count(r.Runs)),
			fact("Open questions", contract.EntityFactTypeCount, count(r.OpenQuestions)), fact("Host", contract.EntityFactTypeCode, deref(r.Host)),
			fact("Priority", contract.EntityFactTypeText, priority))}, nil
}

func cardQuestion(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := cardRow(q.db().EntCardQuestion(q.ctx(), id))
	if r == nil || err != nil {
		return nil, err
	}
	text := []rune(r.Text)
	path := ptr("/overview")
	if t := deref(r.TaskID); t != "" {
		path = itemPath("tasks", t)
	} else if s := deref(r.SessionID); s != "" {
		path = itemPath("conversations", s)
	}
	answer := []rune(deref(r.Answer))
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Code: optStr(deref(r.Code)), Title: string(text[:min(200, len(text))]), State: optStr(r.State),
		ProjectId: optStr(deref(r.ProjectID)), At: optStr(cardTime(&r.AskedAt)), AtLabel: ptr("asked"), Path: path,
		Facts: facts(fact("Asked by", contract.EntityFactTypeText, deref(r.AskedBy)), fact("Task", contract.EntityFactTypeCode, deref(r.TaskCode)),
			fact("Answer", contract.EntityFactTypeText, string(answer[:min(120, len(answer))])), fact("Answered by", contract.EntityFactTypeText, deref(r.AnsweredBy)))}, nil
}

// cardConversation: one session with the sessions_list columns, read for this session only (sessionBase reads all).
func cardConversation(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := cardRow(q.db().EntCardConversation(q.ctx(), id))
	if r == nil || err != nil {
		return nil, err
	}
	var at *time.Time
	if r.ActivityAt.Valid {
		at = &r.ActivityAt.Time
	}
	title := []rune(strings.Join(strings.Fields(firstString(r.FirstMessage, deref(r.Code), "Conversation")), " "))
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Code: optStr(deref(r.Code)), Title: string(title[:min(200, len(title))]), State: optStr(r.State),
		ProjectId: optStr(deref(r.ProjectID)), Harness: optStr(r.Harness), At: optStr(cardTime(at)), AtLabel: ptr("active"), Path: itemPath("conversations", id),
		Facts: facts(fact("Profile", contract.EntityFactTypeCode, r.Profile), fact("Task", contract.EntityFactTypeCode, deref(r.TaskCode)),
			fact("Runs", contract.EntityFactTypeCount, count(r.Runs)), fact("Open questions", contract.EntityFactTypeCount, count(r.OpenQuestions)))}, nil
}

var executionKindLabel = map[string]string{"agent_run": "Agent run", "pipeline_run": "Pipeline run", "pipeline_step": "Pipeline step", "child_agent": "Child agent", "tool_run": "Tool run", "host_job": "Host job"}

func cardExecution(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := cardRow(q.db().EntCardExecution(q.ctx(), id))
	if r == nil || err != nil || deref(r.ExperimentID) != "" && !architect(q) { // a Lab trial is the architect's, as its experiment
		return nil, err
	}
	path := ptr("/runners")
	switch {
	case deref(r.SessionID) != "":
		path = itemPath("conversations", *r.SessionID)
	case deref(r.TaskID) != "":
		path = itemPath("tasks", *r.TaskID)
	case deref(r.ExperimentID) != "":
		path = itemPath("lab", *r.ExperimentID)
	}
	secs := ""
	if r.StartedAt != nil && r.EndedAt != nil {
		secs = strconv.FormatFloat(r.EndedAt.Sub(*r.StartedAt).Seconds(), 'f', 0, 64)
	}
	at, label := firstString(cardTime(r.EndedAt), cardTime(r.StartedAt), cardTime(&r.UpdatedAt)), "changed"
	if r.EndedAt != nil {
		label = "ended"
	} else if r.StartedAt != nil {
		label = "started"
	}
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Title: firstString(deref(r.TaskTitle), executionKindLabel[r.Kind], "Run"), State: optStr(r.State),
		ProjectId: optStr(deref(r.ProjectID)), Harness: optStr(deref(r.Harness)), At: optStr(at), AtLabel: ptr(label), Path: path,
		Facts: facts(fact("Problem", contract.EntityFactTypeText, deref(r.Problem)), fact("Task", contract.EntityFactTypeCode, deref(r.TaskCode)),
			fact("Duration", contract.EntityFactTypeDuration, secs), fact("Host", contract.EntityFactTypeCode, deref(r.HostID)),
			fact("Model", contract.EntityFactTypeCode, deref(r.Model)), fact("Conversation", contract.EntityFactTypeCode, deref(r.SessionCode)))}, nil
}

// cardResult: a result (artifact) by its id or its legacy id. Results kept as documents (kind artifact) are no longer
// written by any path; the artifact table holds every result.
func cardResult(q *Request, id string) (*contract.EntityCardReply, error) {
	r, err := cardRow(q.db().EntCardResult(q.ctx(), id))
	if r == nil || err != nil {
		return nil, err
	}
	return &contract.EntityCardReply{Id: r.ID, Uuid: ptr(r.ID), Code: optStr(deref(r.Code)), Hash: optStr(r.ContentHash), Title: firstString(r.Name, "Result"), State: optStr(r.State),
		ProjectId: optStr(deref(r.ProjectID)), At: optStr(cardTime(&r.CreatedAt)), AtLabel: ptr("stored"), Path: itemPath("results", r.ID),
		Facts: facts(fact("Task", contract.EntityFactTypeCode, deref(r.TaskCode)), fact("Type", contract.EntityFactTypeCode, r.MediaType),
			fact("Size", contract.EntityFactTypeBytes, count(r.Size)))}, nil
}

// architect: definitions and Lab are the architect's to read (people.go architectOps); an agent without a person passes.
func architect(q *Request) bool {
	return fullAccess(q.person) || hasRole(q.person, "architect")
}

func cardExperiment(q *Request, id string) (*contract.EntityCardReply, error) {
	if !architect(q) {
		return nil, nil
	}
	r, err := cardRow(q.db().EntCardExperiment(q.ctx(), id))
	if r == nil || err != nil {
		return nil, err
	}
	at, label := r.CreatedAt, "created"
	if r.At.Valid && !r.At.Time.Equal(r.CreatedAt) {
		at, label = r.At.Time, "launched"
	}
	return &contract.EntityCardReply{Id: id, Uuid: ptr(id), Title: firstString(deref(r.Goal), r.PipelineID), State: optStr(r.State),
		ProjectId: optStr(r.ProjectID), At: optStr(cardTime(&at)), AtLabel: ptr(label), Path: itemPath("lab", id),
		Facts: facts(fact("Pipeline", contract.EntityFactTypeCode, r.PipelineID), fact("Trials", contract.EntityFactTypeCount, count(r.Trials)),
			fact("Variants", contract.EntityFactTypeCount, count(r.Variants)), fact("Decision", contract.EntityFactTypeText, deref(r.Decision)))}, nil
}

// projectName: the name in a project definition's settings, "" when none.
func projectName(content []byte) string {
	var b struct {
		Settings struct {
			Name string `json:"name"`
		} `json:"settings"`
	}
	_ = json.Unmarshal(content, &b)
	return b.Settings.Name
}

func cardProject(q *Request, id string) (*contract.EntityCardReply, error) {
	if q.person != nil && !mayAccessProjectID(q.person, id) {
		return nil, nil
	}
	xs, err := definitionRows(q, "d.kind='project' AND d.id=$1 AND d.deleted_at IS NULL", 0, id)
	if err != nil || len(xs) == 0 {
		return nil, err
	}
	r, err := q.db().EntCardProjectCounts(q.ctx(), id)
	if err != nil {
		return nil, err
	}
	return &contract.EntityCardReply{Id: id, Code: optStr(r.Code), Title: firstString(projectName(xs[0].Content), id),
		At: ptr(cardTime(&xs[0].At)), AtLabel: ptr("changed"), Path: itemPath("projects", id),
		Facts: facts(fact("Open tasks", contract.EntityFactTypeCount, count(r.Open)), fact("Running", contract.EntityFactTypeCount, count(r.Running)),
			fact("Open questions", contract.EntityFactTypeCount, count(r.Questions)))}, nil
}

// cardHost: one host as hosts_list shows it: online while it polled within hostStaleMs, failing while its polls fail.
func cardHost(q *Request, id string) (*contract.EntityCardReply, error) {
	list, err := hostsListReply(q, false)
	if err != nil {
		return nil, err
	}
	for _, h := range list.Hosts {
		if h.Id != id {
			continue
		}
		state, at := "offline", (*string)(nil)
		if h.LastSeenAt != nil {
			seen := int64(*h.LastSeenAt)
			if nowMs()-seen < hostStaleMs { // polling, as hostsList and the Hosts page decide it
				state = "online"
				if h.PollError != nil {
					state = "failing"
				}
			}
			at = ptr(time.UnixMilli(seen).UTC().Format(time.RFC3339Nano))
		}
		return &contract.EntityCardReply{Id: id, Title: id, State: ptr(state), At: at, AtLabel: ptr("seen"), Path: itemPath("hosts", id),
			Facts: facts(fact("Version", contract.EntityFactTypeCode, deref(h.Version)), fact("Running", contract.EntityFactTypeCount, count(h.Running)),
				fact("Queued", contract.EntityFactTypeCount, count(h.Queued)), fact("Platform", contract.EntityFactTypeText, deref(h.Platform)))}, nil
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
		var v struct {
			Model  string            `json:"model"`
			Effort string            `json:"effort"`
			Steps  []json.RawMessage `json:"steps"`
		}
		_ = json.Unmarshal(x.Content, &v)
		c := &contract.EntityCardReply{Id: id, Title: definitionName(x), At: ptr(cardTime(&x.At)), AtLabel: ptr("changed"), Path: itemPath(kind+"s", id)}
		if c.Title != id {
			c.Code = ptr(id)
		}
		if kind == "agent" {
			c.Harness = ptr(profileHarness(x.Content))
			c.Facts = facts(fact("Model", contract.EntityFactTypeCode, v.Model), fact("Effort", contract.EntityFactTypeText, v.Effort), fact("Revision", contract.EntityFactTypeCount, fmt.Sprint(x.Revision)))
		} else {
			c.Facts = facts(fact("Steps", contract.EntityFactTypeCount, fmt.Sprint(len(v.Steps))), fact("Revision", contract.EntityFactTypeCount, fmt.Sprint(x.Revision)))
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
	d, err := cardRow(q.db().EntDeliveryPr(q.ctx(), number))
	if d == nil || err != nil {
		return nil, err
	}
	var x contract.DeliveryPr
	if err := json.Unmarshal(*d, &x); err != nil {
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
	rec, err := cardRow(q.db().EntDeliveryRelease(q.ctx(), tag))
	if err != nil {
		return nil, err
	}
	var history contract.DeliveryRelease
	if rec != nil {
		_ = json.Unmarshal(*rec, &history)
	}
	version, known := releaseVersionOf(tag), prs > 0 || rec != nil
	path := "/delivery?release=" + url.QueryEscape(tag)
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
