// Project apps (docs/designs/fleet-project-apps.md): a project's own application calls the project's pipelines with
// an app key. The app is asked for with an owner approval card; Approve makes it active and mints its first key, whose
// value waits in <FLEET_SECRET_DIR>/apps/keys/<key id> until project_app_key_take hands it out once. Requests on
// /api/apps/* authenticate with the key (appCallerFromKey, main.go authorize) and run as the approver narrowed to the
// app's project, with only the operations appOps names. The Cloudflare edge in front of /api/apps is apps_edge.go.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("project_apps", projectApps)
	op("project_app_ask", projectAppAsk)
	op("project_app_key_take", projectAppKeyTake)
	op("project_app_rotate", projectAppRotate)
	op("project_app_revoke", projectAppRevoke)
	op("project_app_set", projectAppSet)
	op("apps_ping", appsPing)
}

var projectAppID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// appOps: the operations a request on /api/apps may call (fleet-project-apps.md, Calling Fleet); anything else is 403.
var appOps = map[string]bool{"pipeline_run": true, "pipeline_get": true, "pipeline_history": true, "artifact_upload_begin": true,
	"artifact_get": true, "apps_ping": true}

// projectAppPayload: project_app.payload.
type projectAppPayload struct {
	Pipelines       []string                  `json:"pipelines"`
	CallbackOrigins []string                  `json:"callbackOrigins"`
	Limits          contract.ProjectAppLimits `json:"limits"`
	Reason          string                    `json:"reason"`
	AskedByPerson   string                    `json:"askedByPerson,omitempty"` // the person an asking agent ran for
}

// appCaller: the app a request on /api/apps authenticated as.
type appCaller struct {
	ID, ProjectID, KeyID, DirectedBy string
	KeyCreated                       time.Time
	KeyUsed                          bool
}

// narrow: the approver's access cut to the app's project: an operator of that one project, never the owner.
func (c *appCaller) narrow(p *Person) *Person {
	if p == nil {
		return nil
	}
	out := copyPerson(p)
	out.Owner = false
	out.Roles = []string{"operator"}
	out.Grants = []personGrant{{Kind: "project", ID: c.ProjectID}}
	return out
}

func projectAppPayloadOf(raw []byte) projectAppPayload {
	var p projectAppPayload
	lenientDecode(raw, &p)
	if p.Pipelines == nil {
		p.Pipelines = []string{"*"}
	}
	if p.CallbackOrigins == nil {
		p.CallbackOrigins = []string{}
	}
	return p
}

// allowsPipeline: the app may run pipeline id.
func (p projectAppPayload) allowsPipeline(id string) bool {
	return slices.Contains(p.Pipelines, "*") || slices.Contains(p.Pipelines, id)
}

func appMs(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.UnixMilli()
	return &v
}

func projectAppView(r db.ProjectAppGetRow, keys []db.ProjectAppKeysRow) contract.ProjectAppView {
	p := projectAppPayloadOf(r.Payload)
	v := contract.ProjectAppView{Id: r.ID, ProjectId: r.ProjectID, Name: r.Name, State: contract.ProjectAppState(r.State), Pipelines: p.Pipelines,
		CallbackOrigins: p.CallbackOrigins, Limits: p.Limits, RequestId: r.RequestID, AskedBy: r.AskedBy, ApprovedBy: r.ApprovedBy, QuestionId: r.QuestionID,
		Reason: p.Reason, Keys: []contract.ProjectAppKeyView{}, CreatedAt: r.CreatedAt.UnixMilli(), LastUsedAt: appMs(r.LastUsedAt)}
	for _, k := range keys {
		if k.AppID == r.ID {
			v.Keys = append(v.Keys, contract.ProjectAppKeyView{Id: k.ID, CreatedAt: k.CreatedAt.UnixMilli(), LastUsedAt: appMs(k.LastUsedAt),
				TakenAt: appMs(k.TakenAt), RetiredAt: appMs(k.RetiredAt)})
		}
	}
	return v
}

// projectAppRead: one app with its keys, 404 when the caller cannot see it.
func projectAppRead(q *Request, id string) (contract.ProjectAppView, error) {
	r, err := q.db().ProjectAppGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.ProjectAppView{}, fail(404, "Unknown app "+id)
	}
	if err != nil {
		return contract.ProjectAppView{}, err
	}
	keys, err := q.db().ProjectAppKeys(q.ctx(), []string{id})
	// A write's reply leaves use30d out: project_apps, which the change stream rereads, answers it.
	return projectAppView(db.ProjectAppGetRow(r), keys), err
}

// appUseDays: the window of an app's and a pipeline's use (fleet-project-apps.md, Screens).
const appUseDays = 30

func appUseSince() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().Add(-appUseDays * 24 * time.Hour), Valid: true}
}

// projectAppUse: each app's runs, failed runs and leaf cost over the last 30 days, by app id (absent: no runs).
func projectAppUse(q *Request, ids []string) (map[string]contract.ProjectAppUse, error) {
	out := map[string]contract.ProjectAppUse{}
	if len(ids) == 0 {
		return out, nil
	}
	rs, err := q.db().ProjectAppUse(q.ctx(), db.ProjectAppUseParams{AppIds: ids, Since: appUseSince()})
	for _, r := range rs {
		out[r.Caller] = contract.ProjectAppUse{Runs: r.Runs, Failed: r.Failed, Cost: callGroupCost(r.Costs)}
	}
	return out, err
}

func projectApps(q *Request, r *contract.ProjectAppsRequest) (contract.ProjectAppsReply, error) {
	rs, err := q.db().ProjectAppsOfProject(q.ctx(), r.ProjectId)
	if err != nil {
		return contract.ProjectAppsReply{}, err
	}
	ids := make([]string, 0, len(rs))
	for _, a := range rs {
		ids = append(ids, a.ID)
	}
	keys, err := q.db().ProjectAppKeys(q.ctx(), ids)
	if err != nil {
		return contract.ProjectAppsReply{}, err
	}
	use, err := projectAppUse(q, ids)
	out := contract.ProjectAppsReply{Apps: []contract.ProjectAppView{}}
	for _, a := range rs {
		v := projectAppView(db.ProjectAppGetRow(a), keys)
		u, ok := use[a.ID]
		if !ok {
			u = contract.ProjectAppUse{Cost: []contract.CostFigure{}}
		}
		v.Use30d = &u
		out.Apps = append(out.Apps, v)
	}
	return out, err
}

// projectAppSettings: the checked pipelines, callback origins and limits of an ask or a set.
func projectAppSettings(pipelines, origins []string, limits *contract.ProjectAppLimits, base projectAppPayload) (projectAppPayload, error) {
	out := base
	if pipelines != nil {
		if len(pipelines) == 0 {
			return out, fail(400, "pipelines: name at least one pipeline, or [\"*\"] for every pipeline of the project")
		}
		for _, p := range pipelines {
			if p != "*" && !accessServerID.MatchString(p) {
				return out, fail(400, "pipelines: "+p+" is not a pipeline id")
			}
		}
		out.Pipelines = slices.Clone(pipelines)
	}
	if origins != nil {
		clean := []string{}
		for _, o := range origins {
			u, err := url.Parse(o)
			if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
				return out, fail(400, "callbackOrigins: "+o+" is not an https origin such as https://example.com")
			}
			clean = append(clean, "https://"+strings.ToLower(u.Host))
		}
		out.CallbackOrigins = clean
	}
	if limits != nil {
		if s := limits.SpendPerMonth; s != nil && (s.Amount <= 0 || strings.TrimSpace(s.Currency) == "") {
			return out, fail(400, "limits.spendPerMonth: a positive amount and a currency")
		}
		out.Limits = *limits
	}
	return out, nil
}

func projectAppAsk(q *Request, r *contract.ProjectAppAskRequest) (contract.ProjectAppAskReply, error) {
	var no contract.ProjectAppAskReply
	reason, name := strings.TrimSpace(r.Reason), strings.TrimSpace(r.Name)
	if r.RequestId == "" || len(r.RequestId) > 200 || reason == "" || len([]rune(reason)) > 1000 || name == "" || len([]rune(name)) > 80 {
		return no, fail(400, "Expected requestId, a name of at most 80 and a reason of at most 1000 characters")
	}
	if !projectAppID.MatchString(r.Id) {
		return no, fail(400, "id: lower-case letters, digits and dashes, such as dam-site")
	}
	if q.caller != nil {
		return no, fail(403, "An app may only run its pipelines")
	}
	if _, err := projectContext(q, r.ProjectId); err != nil {
		return no, err
	}
	payload, err := projectAppSettings(r.Pipelines, r.CallbackOrigins, r.Limits, projectAppPayload{Pipelines: []string{"*"}, CallbackOrigins: []string{}})
	if err != nil {
		return no, err
	}
	payload.Reason = reason
	if q.person != nil {
		payload.AskedByPerson = q.person.Email
	}
	prev, err := q.db().ProjectAppByRequest(q.ctx(), db.ProjectAppByRequestParams{ProjectID: r.ProjectId, RequestID: r.RequestId})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return no, err
	}
	if err == nil {
		old := db.ProjectAppGetRow(prev)
		if old.ID != r.Id {
			return no, fail(409, "Request "+r.RequestId+" already names app "+old.ID+": use a new requestId")
		}
		if string(jsonBytes(projectAppPayloadOf(old.Payload))) == string(jsonBytes(payload)) && old.Name == name {
			return projectAppRead(q, old.ID)
		}
		if old.State != "asked" {
			return no, fail(409, "App "+old.ID+" is "+old.State+": change it with project_app_set")
		}
		// A changed request withdraws the old card and asks again: an approval covers exactly what it named.
		if old.QuestionID != nil {
			if err := questionsWithdrawKeep(q, *old.QuestionID); err != nil {
				return no, err
			}
		}
		return projectAppCard(q, old.ID, r.ProjectId, name, old.AskedBy, payload)
	}
	taken, err := q.db().ProjectAppIDTaken(q.ctx(), r.Id)
	if err != nil {
		return no, err
	}
	if taken {
		return no, fail(409, "App id "+r.Id+" is taken")
	}
	asked := actor(q, "operator")
	if err := q.db().ProjectAppInsert(q.ctx(), db.ProjectAppInsertParams{ID: r.Id, ProjectID: r.ProjectId, Name: name, RequestID: r.RequestId,
		AskedBy: asked, Payload: jsonBytes(payload)}); err != nil {
		return no, err
	}
	return projectAppCard(q, r.Id, r.ProjectId, name, asked, payload)
}

// projectAppCard: the owner's approval card for an asked app (action app, owner only), and the app read back.
func projectAppCard(q *Request, id, projectID, name, askedBy string, p projectAppPayload) (contract.ProjectAppView, error) {
	pipes := strings.Join(p.Pipelines, ", ")
	if slices.Contains(p.Pipelines, "*") {
		pipes = "every pipeline of " + projectID
	}
	callbacks, limits := "none", "no limits"
	if len(p.CallbackOrigins) > 0 {
		callbacks = strings.Join(p.CallbackOrigins, ", ")
	}
	if l := projectAppLimitsText(p.Limits); l != "" {
		limits = l
	}
	what := fmt.Sprintf("%s app %s (%s) asks to run %s: %s", projectID, name, id, pipes, p.Reason)
	scope := approvalScope{RequestID: "app:" + id, Action: "app", Risk: "radical", What: what, Where: projectID}
	text := approvalText(scope, approverNames(approvers(q, projectID, "app"))) + "\n\nCallbacks to: " + callbacks + "\nLimits: " + limits +
		"\nThe app calls Fleet with its own key and may only run these pipelines of " + projectID + "."
	ask := questionAsk{Kind: "operator_decision", Text: text, ProjectID: projectID, AskedBy: new(askedBy), RequestID: new("app:" + id + ":" + newID()),
		Options: jsonBytes([]string{approvalYes, approvalNo}), Data: jsonBytes(struct {
			Approval approvalScope `json:"approval"`
			App      struct {
				ID string `json:"id"`
			} `json:"app"`
		}{Approval: scope, App: struct {
			ID string `json:"id"`
		}{id}})}
	if eid, ok := strings.CutPrefix(q.agent, "execution:"); ok {
		sid, err := q.db().ConnExecutionSession(q.ctx(), eid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return contract.ProjectAppView{}, err
		}
		ask.Kind, ask.ExecutionID = "agent_question", &eid
		if err == nil && sid != nil && *sid != "" {
			ask.SessionID = sid
		}
	}
	qid, err := askQuestionAs(q, ask)
	if err != nil {
		return contract.ProjectAppView{}, err
	}
	if err := q.db().ProjectAppSetQuestion(q.ctx(), db.ProjectAppSetQuestionParams{ID: id, QuestionID: &qid, Payload: jsonBytes(p), Name: name}); err != nil {
		return contract.ProjectAppView{}, err
	}
	return projectAppRead(q, id)
}

func projectAppLimitsText(l contract.ProjectAppLimits) string {
	var parts []string
	if l.RunsPerDay != nil {
		parts = append(parts, fmt.Sprintf("%d runs a day", *l.RunsPerDay))
	}
	if l.ConcurrentRuns != nil {
		parts = append(parts, fmt.Sprintf("%d runs at once", *l.ConcurrentRuns))
	}
	if s := l.SpendPerMonth; s != nil {
		parts = append(parts, fmt.Sprintf("%g %s a month", s.Amount, s.Currency))
	}
	return strings.Join(parts, ", ")
}

// appQuestionEnded: questionTransition's hook for app cards (and the edge card, apps_edge.go). Approve activates the
// app and mints its first key; Not now declines it; a withdrawal (but a changed request, which asks again) withdraws
// the ask, leaving the app declined.
func appQuestionEnded(q *Request, questionID string, ev questionEvent) error {
	if ev.To != "answered" && ev.To != "withdrawn" {
		return nil
	}
	if err := appsEdgeQuestionEnded(q, questionID, ev); err != nil {
		return err
	}
	id, err := q.db().ProjectAppByQuestion(q.ctx(), &questionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	app, err := q.db().ProjectAppLock(q.ctx(), id)
	if err != nil || app.State != "asked" {
		return err
	}
	switch {
	case ev.To == "answered" && deref(ev.Answer) == approvalYes:
		by := deref(ev.AnsweredBy)
		if by == "" {
			by = actor(q, "")
		}
		if by == "" {
			return fail(409, "An app approval needs the approving person")
		}
		if err := q.db().ProjectAppSetState(q.ctx(), db.ProjectAppSetStateParams{ID: id, State: "active", ApprovedBy: &by}); err != nil {
			return err
		}
		_, err := projectAppMint(q, db.ProjectAppGetRow(app), by)
		return err
	case ev.To == "answered":
		return q.db().ProjectAppSetState(q.ctx(), db.ProjectAppSetStateParams{ID: id, State: "declined"})
	case strings.Contains(string(ev.Withdrawal), "scope changed"):
		return nil
	default:
		return q.db().ProjectAppSetState(q.ctx(), db.ProjectAppSetStateParams{ID: id, State: "declined"})
	}
}

func appKeyDir() (string, error) {
	if secretDir() == "" {
		return "", fail(409, "FLEET_SECRET_DIR is not set on the coordinator")
	}
	return filepath.Join(secretDir(), "apps", "keys"), nil
}

// projectAppMint: a new key for app, its value kept in the secret directory until it is taken.
func projectAppMint(q *Request, app db.ProjectAppGetRow, directedBy string) (string, error) {
	dir, err := appKeyDir()
	if err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := "fla_" + base64.RawURLEncoding.EncodeToString(raw)
	keyID := "appkey:" + newID()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// The file comes first: a key row whose value is missing could never be taken, a value without a row is never used.
	file := filepath.Join(dir, strings.TrimPrefix(keyID, "appkey:"))
	if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
		return "", err
	}
	afterRollback(q, func() { _ = os.Remove(file) })
	if err := q.db().ProjectAppKeyInsert(q.ctx(), db.ProjectAppKeyInsertParams{ID: keyID, AppID: app.ID, ProjectID: app.ProjectID, DirectedBy: directedBy,
		Sha256: secretHash(value)}); err != nil {
		return "", err
	}
	if err := appCallbackSecretEnsure(q, app.ID); err != nil {
		return "", err
	}
	if err := audit(q, "project-app:"+app.ID, "key", nil, keyID, auditExtra{}); err != nil {
		return "", err
	}
	return keyID, nil
}

func projectAppKeyTake(q *Request, r *contract.ProjectAppKeyTakeRequest) (contract.ProjectAppKeyTakeReply, error) {
	var no contract.ProjectAppKeyTakeReply
	if q.caller != nil {
		return no, fail(403, "An app may only run its pipelines")
	}
	app, err := q.db().ProjectAppLock(q.ctx(), r.AppId)
	if errors.Is(err, pgx.ErrNoRows) {
		return no, fail(404, "Unknown app "+r.AppId)
	}
	if err != nil {
		return no, err
	}
	asker := projectAppPayloadOf(app.Payload).AskedByPerson
	if !fullAccess(q.person) && !q.person.Owner && actor(q, "") != app.AskedBy && (asker == "" || q.person.Email != asker) {
		return no, fail(403, "Only the asker or the owner takes an app key")
	}
	if app.State != "active" {
		return no, fail(409, "App "+app.ID+" is "+app.State)
	}
	keyID, err := q.db().ProjectAppKeyNewestUntaken(q.ctx(), app.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		at, _ := q.db().ProjectAppKeyTaken(q.ctx(), app.ID)
		when := ""
		if at != nil {
			when = " at " + at.UTC().Format(time.RFC3339)
		}
		return no, fail(409, "The key was taken"+when+": the owner rotates it with project_app_rotate")
	}
	if err != nil {
		return no, err
	}
	dir, err := appKeyDir()
	if err != nil {
		return no, err
	}
	file := filepath.Join(dir, strings.TrimPrefix(keyID, "appkey:"))
	value, err := os.ReadFile(file)
	if err != nil {
		return no, fail(409, "The key's value is missing on the coordinator: rotate the key")
	}
	if err := q.db().ProjectAppKeyMarkTaken(q.ctx(), keyID); err != nil {
		return no, err
	}
	afterCommit(q, func() { _ = os.Remove(file) })
	out := contract.ProjectAppKeyTakeReply{AppId: app.ID, KeyId: keyID, Key: string(value), Url: new(appsBaseURL())}
	if id, secret, ok := appsEdgeCredentials(); ok {
		out.EdgeClientId, out.EdgeClientSecret = &id, &secret
	}
	if secret := appCallbackSecret(app.ID); secret != "" {
		out.CallbackSecret = &secret
	}
	if err := audit(q, "project-app:"+app.ID, "key-taken", nil, keyID, auditExtra{}); err != nil {
		return no, err
	}
	return out, nil
}

// appsBaseURL: the app route's base on the deployed site.
func appsBaseURL() string {
	o := browserOrigin
	if o == "" {
		o = "https://fleet.ufobe.com"
	}
	return strings.TrimRight(o, "/") + "/api/apps"
}

func projectAppRotate(q *Request, r *contract.ProjectAppRotateRequest) (contract.ProjectAppRotateReply, error) {
	app, err := q.db().ProjectAppLock(q.ctx(), r.AppId)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.ProjectAppRotateReply{}, fail(404, "Unknown app "+r.AppId)
	}
	if err != nil {
		return contract.ProjectAppRotateReply{}, err
	}
	if app.State != "active" {
		return contract.ProjectAppRotateReply{}, fail(409, "App "+app.ID+" is "+app.State)
	}
	if _, err := projectAppMint(q, db.ProjectAppGetRow(app), firstString(deref(app.ApprovedBy), actor(q, ""))); err != nil {
		return contract.ProjectAppRotateReply{}, err
	}
	return projectAppRead(q, app.ID)
}

func projectAppRevoke(q *Request, r *contract.ProjectAppRevokeRequest) (contract.ProjectAppRevokeReply, error) {
	app, err := q.db().ProjectAppLock(q.ctx(), r.AppId)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.ProjectAppRevokeReply{}, fail(404, "Unknown app "+r.AppId)
	}
	if err != nil {
		return contract.ProjectAppRevokeReply{}, err
	}
	if app.State == "asked" && app.QuestionID != nil {
		if err := questionsWithdraw(q, []string{*app.QuestionID}); err != nil {
			return contract.ProjectAppRevokeReply{}, err
		}
	}
	if err := q.db().ProjectAppKeysRetire(q.ctx(), app.ID); err != nil {
		return contract.ProjectAppRevokeReply{}, err
	}
	if app.State != "revoked" {
		if err := q.db().ProjectAppSetState(q.ctx(), db.ProjectAppSetStateParams{ID: app.ID, State: "revoked"}); err != nil {
			return contract.ProjectAppRevokeReply{}, err
		}
		if err := audit(q, "project-app:"+app.ID, "state", app.State, "revoked", auditExtra{}); err != nil {
			return contract.ProjectAppRevokeReply{}, err
		}
	}
	return projectAppRead(q, app.ID)
}

func projectAppSet(q *Request, r *contract.ProjectAppSetRequest) (contract.ProjectAppSetReply, error) {
	app, err := q.db().ProjectAppLock(q.ctx(), r.AppId)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.ProjectAppSetReply{}, fail(404, "Unknown app "+r.AppId)
	}
	if err != nil {
		return contract.ProjectAppSetReply{}, err
	}
	if app.State != "active" {
		return contract.ProjectAppSetReply{}, fail(409, "App "+app.ID+" is "+app.State)
	}
	old := projectAppPayloadOf(app.Payload)
	next, err := projectAppSettings(r.Pipelines, r.CallbackOrigins, r.Limits, old)
	if err != nil {
		return contract.ProjectAppSetReply{}, err
	}
	if err := q.db().ProjectAppSetPayload(q.ctx(), db.ProjectAppSetPayloadParams{ID: app.ID, Payload: jsonBytes(next)}); err != nil {
		return contract.ProjectAppSetReply{}, err
	}
	if err := audit(q, "project-app:"+app.ID, "settings", old, next, auditExtra{}); err != nil {
		return contract.ProjectAppSetReply{}, err
	}
	return projectAppRead(q, app.ID)
}

// appRunLimit: the 429 of an app's next run over one of its limits (fleet-project-apps.md, Limits), nil within all of
// them; an empty limit never refuses. Days and months are UTC.
func appRunLimit(q *Request, appID string, l contract.ProjectAppLimits, now time.Time) error {
	now = now.UTC()
	if l.RunsPerDay != nil {
		n, err := q.db().AppRunsSince(q.ctx(), db.AppRunsSinceParams{AppID: &appID, Since: new(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))})
		if err != nil {
			return err
		}
		if n >= *l.RunsPerDay {
			return fail(429, "limit:runs-per-day")
		}
	}
	if l.ConcurrentRuns != nil {
		n, err := q.db().AppRunsInFlight(q.ctx(), &appID)
		if err != nil {
			return err
		}
		if n >= *l.ConcurrentRuns {
			return fail(429, "limit:concurrent-runs")
		}
	}
	if s := l.SpendPerMonth; s != nil {
		spent, err := q.db().AppSpendSince(q.ctx(), db.AppSpendSinceParams{AppID: &appID, Since: new(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)),
			Currency: s.Currency})
		if err != nil {
			return err
		}
		if spent >= s.Amount {
			return fail(429, "limit:spend-per-month")
		}
	}
	return nil
}

// appRunCheck: before an app's pipeline_run reads any input, its notify URL (checked against the app's callback
// origins) and, for a request that starts a new run (fresh), its limits. Other callers may not ask for a callback.
func appRunCheck(q *Request, fresh bool, notify *contract.PipelineRunNotify) (string, error) {
	if q.caller == nil {
		if notify != nil {
			return "", fail(400, "notify: only a project app's run calls back")
		}
		return "", nil
	}
	target := ""
	err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		raw, err := q.db().ProjectAppPayload(q.ctx(), q.caller.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(403, "The app is not active")
		}
		if err != nil {
			return err
		}
		p := projectAppPayloadOf(raw)
		if notify != nil {
			if target, err = appNotifyURL(p, notify); err != nil {
				return err
			}
		}
		if !fresh {
			return nil // a retried request answers its run whatever the limits
		}
		return appRunLimit(q, q.caller.ID, p.Limits, time.Now())
	})
	return target, err
}

func appsPing(q *Request, _ *contract.AppsPingRequest) (contract.AppsPingReply, error) {
	if q.caller == nil {
		return contract.AppsPingReply{}, fail(400, "apps_ping answers on /api/apps only")
	}
	return contract.AppsPingReply{App: q.caller.ID, ProjectId: q.caller.ProjectID}, nil
}

// appCallerFromKey: the app whose live key is secret, nil when none (read before any scope, like a host secret). The
// first use of a rotated key retires the app's older keys.
func (a *App) appCallerFromKey(q *Request, secret string) (*appCaller, error) {
	if !strings.HasPrefix(secret, "fla_") {
		return nil, nil
	}
	var c *appCaller
	err := a.store.Write(q.ctx(), nil, []string{NOLOCK}, func(t *Request) error {
		k, err := t.db().ProjectAppKeyBySecret(t.ctx(), secretHash(secret))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		c = &appCaller{ID: k.AppID, ProjectID: k.ProjectID, KeyID: k.ID, DirectedBy: k.DirectedBy, KeyCreated: k.CreatedAt, KeyUsed: k.LastUsedAt != nil}
		if k.LastUsedAt == nil || time.Since(*k.LastUsedAt) > time.Minute {
			if err := t.db().ProjectAppKeyUsed(t.ctx(), k.ID); err != nil {
				return err
			}
			if err := t.db().ProjectAppUsed(t.ctx(), k.AppID); err != nil {
				return err
			}
		}
		if k.LastUsedAt == nil {
			return t.db().ProjectAppKeysRetireBefore(t.ctx(), db.ProjectAppKeysRetireBeforeParams{AppID: k.AppID, CreatedAt: k.CreatedAt})
		}
		return nil
	})
	return c, err
}

func strNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// authorizeApp: a request on /api/apps passed Cloudflare Access with the fleet-apps service token (the guard names its
// client id as X-Fleet-Person, apps_edge.go) and carries the app key in X-Fleet-App-Key. It runs as the app's approver
// narrowed to the app's project (appCaller.narrow), and only the operations appOps names (ops.go authorizeOp).
func (a *App) authorizeApp(q *Request, edgeName, key string) error {
	id, _, ok := appsEdgeCredentials()
	if !ok || edgeName == "" || !strings.EqualFold(edgeName, id) {
		return fail(403, "App requests pass Cloudflare with the fleet-apps token (apps_edge_status)")
	}
	if key == "" {
		return fail(401, "App key required (X-Fleet-App-Key)")
	}
	c, err := a.appCallerFromKey(q, key)
	if err != nil {
		return err
	}
	if c == nil {
		return fail(401, "Unauthorized")
	}
	p, err := a.resolveAccess(q.ctx(), c.DirectedBy)
	if err != nil {
		return err
	}
	if p == nil {
		return fail(401, "The app's approver no longer has access")
	}
	q.caller, q.person = c, c.narrow(p)
	return nil
}
