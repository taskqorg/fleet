// The Cloudflare edge of project apps (docs/designs/fleet-project-apps.md, Edge setup): Fleet itself creates the
// service token fleet-apps and adds a policy admitting it to the site's Access application, with the coordinator's
// Cloudflare API token (the one Humans uses). apps_edge_setup asks the owner with one card; Approve queues the
// apps-edge work on the work kit, whose call talks to Cloudflare and probes the route. The token's client secret lives
// only in <FLEET_SECRET_DIR>/apps/edge.env.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("apps_edge_status", appsEdgeStatus)
	op("apps_edge_setup", appsEdgeSetup)
	kitRegister(&kitQueue{Name: "apps-edge", Period: 5 * time.Second, Limit: 1, Workers: 1, Due: appsEdgeDue, Step: appsEdgeStep,
		Retry: kitRetry{Table: "apps_edge_work", Key: "id", Guard: `apps_edge_work.attempts=$5::int`, Class: func(error) string { return "apps_edge_failed" }}})
}

const appsEdgeTokenName = "fleet-apps"

// The permissions each Cloudflare step needs, named as the token editor names them.
const (
	cfPermServiceTokens = "Access: Service Tokens Edit"
	cfPermApps          = "Access: Apps and Policies Edit"
)

// appsEdgePayload: apps_edge.payload.
type appsEdgePayload struct {
	ApplicationID      string   `json:"applicationId,omitempty"`
	ServiceTokenID     string   `json:"serviceTokenId,omitempty"`
	ExpiresAt          *int64   `json:"expiresAt,omitempty"`
	Error              string   `json:"error,omitempty"`
	MissingPermissions []string `json:"missingPermissions,omitempty"`
	CheckedAt          *int64   `json:"checkedAt,omitempty"`
}

func appsEdgeEnvFile() string {
	if secretDir() == "" {
		return ""
	}
	return filepath.Join(secretDir(), "apps", "edge.env")
}

// appsEdgeCredentials: the edge token's client id and secret, when Fleet has made it.
func appsEdgeCredentials() (string, string, bool) {
	f := appsEdgeEnvFile()
	if f == "" {
		return "", "", false
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		return "", "", false
	}
	var id, secret string
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "CF_ACCESS_CLIENT_ID":
			id = v
		case "CF_ACCESS_CLIENT_SECRET":
			secret = v
		}
	}
	return id, secret, id != "" && secret != ""
}

// cloudflareAccountAPI: the coordinator's Cloudflare account and API token (the Humans settings), nil when unset.
type cloudflareAccountAPI struct {
	client  *http.Client
	base    string
	token   string
	account string
}

func cloudflareAccountFromEnv() (*cloudflareAccountAPI, error) {
	account := strings.ToLower(strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID")))
	tokenFile := strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE"))
	if account == "" && tokenFile == "" {
		return nil, nil
	}
	if !cloudflareAccountID.MatchString(account) || !filepath.IsAbs(tokenFile) {
		return nil, errors.New("Cloudflare needs a valid account ID and API token file in the coordinator settings")
	}
	token, err := cloudflareTokenFromFile(tokenFile)
	if err != nil {
		return nil, err
	}
	base := "https://api.cloudflare.com/client/v4/accounts/" + account + "/access"
	if v := os.Getenv("FLEET_CLOUDFLARE_API_BASE"); v != "" { // tests only
		base = strings.TrimRight(v, "/") + "/accounts/" + account + "/access"
	}
	return &cloudflareAccountAPI{client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, base: base, token: token, account: account}, nil
}

// cfDenied: Cloudflare refused the token for a step: the permission it lacks.
type cfDenied struct{ Permission string }

func (e *cfDenied) Error() string {
	return "the Cloudflare API token lacks " + e.Permission
}

type cfHTTPError struct {
	Status int
	msg    string
}

func (e *cfHTTPError) Error() string { return e.msg }

type cfPage struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_count"`
}

func (c *cloudflareAccountAPI) call(ctx context.Context, method, path string, body any, perm string, out any) error {
	return c.callPage(ctx, method, path, body, perm, out, nil)
}

func (c *cloudflareAccountAPI) callPage(ctx context.Context, method, path string, body any, perm string, out any, page *cfPage) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(jsonBytes(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return errors.New("Could not build the Cloudflare request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method == http.MethodGet {
		req.Header.Set("Cache-Control", "no-cache")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.New("Could not reach Cloudflare")
	}
	defer resp.Body.Close()
	var reply struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Page    json.RawMessage `json:"result_info"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&reply)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return &cfDenied{Permission: perm}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !reply.Success {
		msg := ""
		if len(reply.Errors) > 0 {
			msg = fmt.Sprintf(": %d %s", reply.Errors[0].Code, reply.Errors[0].Message)
		}
		return &cfHTTPError{resp.StatusCode, fmt.Sprintf("Cloudflare %s %s answered HTTP %d%s", method, path, resp.StatusCode, msg)}
	}
	if page != nil && len(reply.Page) != 0 {
		if err := json.Unmarshal(reply.Page, page); err != nil {
			return errors.New("Invalid Cloudflare pagination metadata")
		}
	}
	if out != nil {
		return json.Unmarshal(reply.Result, out)
	}
	return nil
}

type cfServiceToken struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	ExpiresAt    string `json:"expires_at"`
}

type cfTokenCreate struct {
	Name     string `json:"name"`
	Duration string `json:"duration"`
}

type cfTokenRef struct {
	TokenID string `json:"token_id"`
}

type cfInclude struct {
	ServiceToken cfTokenRef `json:"service_token"`
}

type cfPolicyCreate struct {
	Name       string      `json:"name"`
	Decision   string      `json:"decision"`
	Precedence int         `json:"precedence"`
	Include    []cfInclude `json:"include"`
}

type cfApp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

type cfPolicy struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Precedence int    `json:"precedence"`
}

// appsEdgeOutcome: what one apps-edge call found.
type appsEdgeOutcome struct {
	Payload appsEdgePayload
	Ready   bool
}

// appsEdgeApply: the Cloudflare work, each step idempotent: the service token (made, or rotated, or reused), the Access
// application and its one policy, then the probe of the route.
func appsEdgeApply(rotate bool, prev appsEdgePayload) func(ctx context.Context) any {
	return func(ctx context.Context) any {
		out := appsEdgeOutcome{Payload: prev}
		now := time.Now().UnixMilli()
		out.Payload.CheckedAt, out.Payload.Error, out.Payload.MissingPermissions = &now, "", nil
		fail := func(err error) any {
			var d *cfDenied
			if errors.As(err, &d) {
				out.Payload.MissingPermissions = []string{d.Permission}
			}
			out.Payload.Error = err.Error()
			return out
		}
		cf, err := cloudflareAccountFromEnv()
		if err != nil {
			return fail(err)
		}
		if cf == nil {
			return fail(errors.New("the coordinator has no Cloudflare API settings (FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID, FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE)"))
		}
		envFile := appsEdgeEnvFile()
		if envFile == "" {
			return fail(errors.New("FLEET_SECRET_DIR is not set on the coordinator"))
		}
		// 1. The service token.
		var tokens []cfServiceToken
		if err := cf.call(ctx, "GET", "/service_tokens?name="+url.QueryEscape(appsEdgeTokenName), nil, cfPermServiceTokens, &tokens); err != nil {
			return fail(err)
		}
		var tok *cfServiceToken
		for i := range tokens {
			if tokens[i].Name == appsEdgeTokenName {
				tok = &tokens[i]
			}
		}
		_, _, haveSecret := appsEdgeCredentials()
		switch {
		case tok == nil:
			var made cfServiceToken
			if err := cf.call(ctx, "POST", "/service_tokens", cfTokenCreate{Name: appsEdgeTokenName, Duration: "8760h"}, cfPermServiceTokens, &made); err != nil {
				return fail(err)
			}
			tok = &made
		case rotate || !haveSecret:
			// A token whose secret Fleet does not hold (made elsewhere, or the file lost) gets a new secret.
			var rot cfServiceToken
			if err := cf.call(ctx, "POST", "/service_tokens/"+tok.ID+"/rotate", nil, cfPermServiceTokens, &rot); err != nil {
				return fail(err)
			}
			rot.ID, rot.Name = tok.ID, tok.Name
			if rot.ClientID == "" {
				rot.ClientID = tok.ClientID
			}
			tok = &rot
		}
		if tok.ClientSecret != "" {
			if err := os.MkdirAll(filepath.Dir(envFile), 0o700); err != nil {
				return fail(err)
			}
			body := "CF_ACCESS_CLIENT_ID=" + tok.ClientID + "\nCF_ACCESS_CLIENT_SECRET=" + tok.ClientSecret + "\n"
			if err := writeFileAtomic(envFile, []byte(body)); err != nil {
				return fail(err)
			}
		}
		out.Payload.ServiceTokenID = tok.ID
		if t, err := time.Parse(time.RFC3339, tok.ExpiresAt); err == nil {
			ms := t.UnixMilli()
			out.Payload.ExpiresAt = &ms
		}
		// 2. The site's own Access application gets one more policy admitting only that token (the guard then names the
		// token's client id, which authorizeApp checks; its audience stays the site's, which the guard verifies).
		site := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(appsBaseURL(), "/api/apps"), "https://"), "http://")
		var apps []cfApp
		if err := cf.call(ctx, "GET", "/apps?per_page=1000", nil, cfPermApps, &apps); err != nil {
			return fail(err)
		}
		var app *cfApp
		for i := range apps {
			if strings.EqualFold(strings.TrimRight(apps[i].Domain, "/"), site) {
				app = &apps[i]
			}
		}
		if app == nil {
			return fail(fmt.Errorf("no Cloudflare Access application covers %s", site))
		}
		out.Payload.ApplicationID = app.ID
		var policies []cfPolicy
		if err := cf.call(ctx, "GET", "/apps/"+app.ID+"/policies", nil, cfPermApps, &policies); err != nil {
			return fail(err)
		}
		hasPolicy, top := false, 0
		for _, p := range policies {
			hasPolicy = hasPolicy || p.Name == appsEdgeTokenName
			top = max(top, p.Precedence)
		}
		if !hasPolicy {
			if err := cf.call(ctx, "POST", "/apps/"+app.ID+"/policies", cfPolicyCreate{Name: appsEdgeTokenName, Decision: "non_identity", Precedence: top + 1,
				Include: []cfInclude{{ServiceToken: cfTokenRef{TokenID: tok.ID}}}}, cfPermApps, nil); err != nil {
				return fail(err)
			}
		}
		// 3. The probe: with the edge token the request reaches Fleet (its 401 for a missing app key); without it, not.
		if err := appsEdgeProbe(ctx); err != nil {
			return fail(err)
		}
		out.Ready = true
		return out
	}
}

// appsEdgeProbe: Cloudflare lets the edge token through to Fleet and stops a request without it.
func appsEdgeProbe(ctx context.Context) error {
	id, secret, ok := appsEdgeCredentials()
	if !ok {
		return errors.New("the edge token's secret is missing on the coordinator")
	}
	probe := func(withToken bool) (int, bool, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", appsBaseURL()+"/ops/apps_ping", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if withToken {
			req.Header.Set("CF-Access-Client-Id", id)
			req.Header.Set("CF-Access-Client-Secret", secret)
		}
		c := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Do(req)
		if err != nil {
			return 0, false, err
		}
		defer resp.Body.Close()
		var b errorBody
		fleet := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&b) == nil && b.StatusCode == resp.StatusCode
		return resp.StatusCode, fleet, nil
	}
	// Cloudflare takes a while to apply a new policy everywhere; the work kit retries a failed probe.
	code, fleet, err := probe(true)
	if err != nil {
		return fmt.Errorf("probe with the edge token: %v", err)
	}
	if code != 401 || !fleet {
		return fmt.Errorf("probe with the edge token: Fleet's 401 expected, got HTTP %d", code)
	}
	code, fleet, err = probe(false)
	if err != nil {
		return fmt.Errorf("probe without the edge token: %v", err)
	}
	if fleet {
		return fmt.Errorf("probe without the edge token reached Fleet (HTTP %d): Cloudflare Access does not cover %s", code, appsBaseURL())
	}
	return nil
}

func appsEdgeRow(q *Request) (db.AppsEdgeGetRow, bool, error) {
	r, err := q.db().AppsEdgeGet(q.ctx())
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func appsEdgeView(r db.AppsEdgeGetRow, found bool) contract.AppsEdgeStatusReply {
	cf, err := cloudflareAccountFromEnv()
	out := contract.AppsEdgeStatusReply{State: "missing", Configured: cf != nil && err == nil}
	if !found {
		return out
	}
	var p appsEdgePayload
	lenientDecode(r.Payload, &p)
	out.State = contract.AppsEdgeState(r.State)
	out.ApplicationId, out.ServiceTokenId, out.QuestionId = strNil(p.ApplicationID), strNil(p.ServiceTokenID), r.QuestionID
	out.ExpiresAt, out.CheckedAt = p.ExpiresAt, p.CheckedAt
	if e := firstString(p.Error, deref(r.LastError)); e != "" {
		out.Error = &e
	}
	if len(p.MissingPermissions) > 0 {
		out.MissingPermissions = p.MissingPermissions
	}
	return out
}

func appsEdgeStatus(q *Request, _ *contract.AppsEdgeStatusRequest) (contract.AppsEdgeStatusReply, error) {
	r, found, err := appsEdgeRow(q)
	return appsEdgeView(r, found), err
}

func appsEdgeSetup(q *Request, r *contract.AppsEdgeSetupRequest) (contract.AppsEdgeSetupReply, error) {
	var no contract.AppsEdgeSetupReply
	if q.caller != nil {
		return no, fail(403, "An app may only run its pipelines")
	}
	if r.RequestId == "" || len(r.RequestId) > 200 {
		return no, fail(400, "Expected requestId")
	}
	if cf, err := cloudflareAccountFromEnv(); err != nil || cf == nil {
		return no, fail(409, "The coordinator has no Cloudflare API settings: the owner sets FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID and FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE (docs/designs/fleet-people-access.md)")
	}
	cur, found, err := appsEdgeRow(q)
	if err != nil {
		return no, err
	}
	rotate := r.Rotate != nil && *r.Rotate
	if found && deref(cur.RequestID) == r.RequestId {
		return appsEdgeView(cur, true), nil
	}
	if found && (cur.State == "asked" || cur.State == "working") {
		return no, fail(409, "The edge is already "+cur.State+": read apps_edge_status")
	}
	what := "Fleet creates the Cloudflare service token fleet-apps and adds a policy admitting it to the Access application of " +
		strings.TrimPrefix(strings.TrimSuffix(appsBaseURL(), "/api/apps"), "https://") + ", through the Cloudflare API, then probes " +
		strings.TrimPrefix(appsBaseURL(), "https://") + ". Project apps send that token with their own Fleet key; Fleet admits the token on /api/apps only."
	if rotate {
		what = "Fleet gives the Cloudflare service token fleet-apps a new secret; apps get it with their next key."
	}
	scope := approvalScope{RequestID: "apps-edge:" + r.RequestId, Action: "app", Risk: "radical", What: what, Where: "Cloudflare Access"}
	text := approvalText(scope, approverNames(approvers(q, "", "app"))) + "\n\nThe Cloudflare API token needs " + cfPermServiceTokens + " and " + cfPermApps + "."
	qid, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", Text: text, AskedBy: new(actor(q, "operator")), RequestID: new("apps-edge:" + r.RequestId),
		Options: jsonBytes([]string{approvalYes, approvalNo}), Data: jsonBytes(struct {
			Approval approvalScope `json:"approval"`
		}{scope})})
	if err != nil {
		return no, err
	}
	payload := cur.Payload
	if !found {
		payload = []byte("{}")
	}
	if err := q.db().AppsEdgeUpsert(q.ctx(), db.AppsEdgeUpsertParams{State: "asked", RequestID: &r.RequestId, QuestionID: &qid, Rotate: rotate, Payload: payload}); err != nil {
		return no, err
	}
	cur, found, err = appsEdgeRow(q)
	return appsEdgeView(cur, found), err
}

// appsEdgeQuestionEnded: Approve on the edge card queues the Cloudflare work; Not now or a withdrawal leaves the edge
// as it was before the ask (missing, or ready).
func appsEdgeQuestionEnded(q *Request, questionID string, ev questionEvent) error {
	if _, err := q.db().AppsEdgeByQuestion(q.ctx(), &questionID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	cur, err := q.db().AppsEdgeLock(q.ctx())
	if err != nil || cur.State != "asked" {
		return err
	}
	if ev.To == "answered" && deref(ev.Answer) == approvalYes {
		if err := q.db().AppsEdgeQueue(q.ctx()); err != nil {
			return err
		}
		if err := q.db().AppsEdgeWorkPut(q.ctx()); err != nil {
			return err
		}
		if err := kitSchedule(q, "apps-edge", "edge", time.Now()); err != nil {
			return err
		}
		return kitNotify(q, "apps-edge")
	}
	var p appsEdgePayload
	lenientDecode(cur.Payload, &p)
	state := "missing"
	if p.ApplicationID != "" && p.CheckedAt != nil && p.Error == "" {
		state = "ready"
	}
	return q.db().AppsEdgeSet(q.ctx(), db.AppsEdgeSetParams{State: state, Payload: cur.Payload, Rotate: false})
}

func appsEdgeDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().AppsEdgeWorkDue(q.ctx(), db.AppsEdgeWorkDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

var cfProbeRetry = regexp.MustCompile(`^probe `)

func appsEdgeStep(q *Request, now time.Time, c *kitClaim) error {
	cur, err := q.db().AppsEdgeLock(q.ctx())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var prev appsEdgePayload
	lenientDecode(cur.Payload, &prev)
	if c.Result == nil {
		if cur.State != "working" {
			return q.db().AppsEdgeWorkDelete(q.ctx())
		}
		c.Call = appsEdgeApply(cur.Rotate, prev)
		return nil
	}
	out, _ := c.Result.(appsEdgeOutcome)
	if !out.Ready && cfProbeRetry.MatchString(out.Payload.Error) {
		// A new Access application takes a while to apply: the kit retries the whole (idempotent) work with its backoff.
		return errors.New(out.Payload.Error)
	}
	state := "failed"
	if out.Ready {
		state = "ready"
	}
	if err := q.db().AppsEdgeSet(q.ctx(), db.AppsEdgeSetParams{State: state, Payload: jsonBytes(out.Payload), Rotate: false}); err != nil {
		return err
	}
	if err := audit(q, "apps-edge", "state", cur.State, state, auditExtra{}); err != nil {
		return err
	}
	return q.db().AppsEdgeWorkDelete(q.ctx())
}
