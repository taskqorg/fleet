// Bugsink, Fleet's error tracker (docs/designs/fleet-error-tracking.md). The coordinator keeps only what is Fleet's
// own: the browser tunnel POST /api/errors (a signed-in person's Sentry envelope for the configured web DSN, forwarded
// to Bugsink with the person's actor as the event user), the alert webhook POST /api/bugsink/alert/{secret} (a new or
// returning issue opens the owner's needs-you problem with fixed text), and the pass that closes a problem once Bugsink
// reports its issue resolved. Agents read issues and events through the bugsink connection (providers.go). Each part is
// off while its configuration is unset.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"fleet/contract"
)

const (
	// bugsinkBodyMax bounds a tunnelled envelope and an alert body, like every other request body (readBody).
	bugsinkBodyMax = 1 << 20
	bugsinkCause   = "problem:bugsink:"
	bugsinkConn    = "bugsink"
)

var (
	// A Bugsink project name as errors.bugsink names it (and as a problem's text may carry it).
	bugsinkProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	bugsinkIssueID     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	bugsinkClient      = &http.Client{Timeout: 15 * time.Second}
)

func init() {
	rawRoute("POST /api/errors", (*App).bugsinkTunnel)
	rawRoute("POST /api/bugsink/alert/{secret}", (*App).bugsinkAlert)
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "bugsink-resolved", envMs("FLEET_BUGSINK_RESOLVE_TICK_MS", 300_000), func(ctx context.Context) error {
			return bugsinkResolvedPass(ctx, a)
		})
	})
}

// sentryDSN: the parts of a DSN <scheme>://<key>@<host>/<path>/<project>.
type sentryDSN struct{ scheme, key, host, project string }

func parseDSN(s string) (sentryDSN, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User == nil || u.User.Username() == "" {
		return sentryDSN{}, false
	}
	path := strings.Trim(u.Path, "/")
	project := path[strings.LastIndex(path, "/")+1:]
	if project == "" || strings.Trim(project, "0123456789") != "" {
		return sentryDSN{}, false
	}
	return sentryDSN{u.Scheme, u.User.Username(), strings.ToLower(u.Host), project}, true
}

// bugsinkWebDSN: FLEET_BUGSINK_WEB_DSN, the DSN of the operator app's Bugsink project; unset turns the tunnel off.
func bugsinkWebDSN() (sentryDSN, bool) { return parseDSN(os.Getenv("FLEET_BUGSINK_WEB_DSN")) }

// bugsinkIngest: where envelopes go: FLEET_BUGSINK_INGEST (the loopback address on PDS18), else the DSN's own host.
func bugsinkIngest(d sentryDSN) string {
	if v := strings.TrimRight(os.Getenv("FLEET_BUGSINK_INGEST"), "/"); v != "" {
		return v
	}
	return d.scheme + "://" + d.host
}

// bugsinkURL: Bugsink's public address, for issue links: FLEET_BUGSINK_URL, else the provider catalogue's.
func bugsinkURL() string {
	if v := strings.TrimRight(os.Getenv("FLEET_BUGSINK_URL"), "/"); v != "" {
		return v
	}
	return astr(mapOf(providers[bugsinkConn])["baseUrl"])
}

// bugsinkTunnel: Sentry's tunnel (docs.sentry.io, "Dealing with Ad-Blockers"): the browser SDK posts its envelope to
// Fleet's own origin; only a signed-in person's envelope for the configured web DSN goes on.
func (a *App) bugsinkTunnel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := &Request{r: r, app: a}
	if err := a.authorize(q); err != nil {
		writeError(w, err)
		return
	}
	if q.person == nil {
		writeError(w, fail(403, "Sign in to report browser errors"))
		return
	}
	dsn, ok := bugsinkWebDSN()
	if !ok {
		writeError(w, fail(404, "Error tracking is not configured"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bugsinkBodyMax))
	if err != nil {
		writeError(w, &HTTPError{Code: 413, Msg: "An envelope is at most 1 MiB"})
		return
	}
	out, err := tunnelEnvelope(body, dsn, actor(q, ""))
	if err != nil {
		writeError(w, err)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", bugsinkIngest(dsn)+"/api/"+dsn.project+"/envelope/", bytes.NewReader(out))
	if err != nil {
		writeError(w, err)
		return
	}
	req.Header.Set("Content-Type", "application/x-sentry-envelope")
	req.Header.Set("X-Sentry-Auth", "Sentry sentry_version=7, sentry_client=fleet-tunnel/1, sentry_key="+dsn.key)
	res, err := bugsinkClient.Do(req)
	if err != nil {
		writeError(w, fail(502, "Bugsink is unreachable"))
		return
	}
	defer res.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 500 {
		writeError(w, fail(502, "Bugsink answered "+itoa(res.StatusCode)))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(res.StatusCode)
	w.Write(reply)
}

// tunnelEnvelope checks the envelope header's DSN (host and project id) and rewrites each event item's user to the
// person's actor (id only; whatever the browser sent as user is dropped) with tag fleet.actor. Other items pass
// unchanged. A malformed envelope is 400, another DSN 403.
func tunnelEnvelope(body []byte, want sentryDSN, actorID string) ([]byte, error) {
	header, rest, _ := bytes.Cut(body, []byte("\n"))
	var h struct {
		DSN string `json:"dsn"`
	}
	if json.Unmarshal(header, &h) != nil {
		return nil, fail(400, "Invalid envelope header")
	}
	got, ok := parseDSN(h.DSN)
	if !ok || got.host != want.host || got.project != want.project {
		return nil, fail(403, "Envelope for another DSN")
	}
	var out bytes.Buffer
	out.Write(header)
	out.WriteByte('\n')
	for len(rest) > 0 {
		line, after, _ := bytes.Cut(rest, []byte("\n"))
		if len(bytes.TrimSpace(line)) == 0 {
			rest = after
			continue
		}
		var item map[string]json.RawMessage
		if json.Unmarshal(line, &item) != nil || item == nil {
			return nil, fail(400, "Invalid envelope item header")
		}
		var payload []byte
		if raw, ok := item["length"]; ok {
			var n int
			if json.Unmarshal(raw, &n) != nil || n < 0 || n > len(after) {
				return nil, fail(400, "Invalid envelope item length")
			}
			payload, rest = after[:n], after[n:]
			rest = bytes.TrimPrefix(rest, []byte("\n"))
		} else {
			payload, rest, _ = bytes.Cut(after, []byte("\n"))
		}
		var kind string
		json.Unmarshal(item["type"], &kind)
		if kind == "event" {
			p, err := eventWithActor(payload, actorID)
			if err != nil {
				return nil, err
			}
			payload = p
			item["length"] = json.RawMessage(itoa(len(payload)))
			line, _ = json.Marshal(item)
		}
		out.Write(line)
		out.WriteByte('\n')
		out.Write(payload)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func eventWithActor(payload []byte, actorID string) ([]byte, error) {
	var ev map[string]json.RawMessage
	if json.Unmarshal(payload, &ev) != nil || ev == nil {
		return nil, fail(400, "Invalid event item")
	}
	ev["user"], _ = json.Marshal(map[string]string{"id": actorID})
	tags := map[string]any{}
	if raw, ok := ev["tags"]; ok && json.Unmarshal(raw, &tags) != nil {
		tags = nil // the pair-list form: kept as sent
	}
	if tags != nil {
		tags["fleet.actor"] = actorID
		ev["tags"], _ = json.Marshal(tags)
	}
	return json.Marshal(ev)
}

// bugsinkAlertSecret: the webhook secret, the content of FLEET_BUGSINK_ALERT_SECRET_FILE (read per alert, so a
// rotation needs no restart); "" turns the webhook off.
func bugsinkAlertSecret() string {
	p := os.Getenv("FLEET_BUGSINK_ALERT_SECRET_FILE")
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		log.Printf("bugsink alert secret: %v", err)
		return ""
	}
	return strings.TrimSpace(string(b))
}

// bugsinkAlert: Bugsink's custom webhook (bugsink.com/docs/alerts-custom-webhook): the issue's canonical API fields plus
// project_name and alert_reason NEW, REGRESSED, UNMUTED or TEST. Bugsink signs nothing, so the secret is the last path
// segment; the route is reached on PDS18's loopback only (Caddy sends no unauthenticated request to it).
func (a *App) bugsinkAlert(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	secret := bugsinkAlertSecret()
	if secret == "" {
		writeError(w, fail(404, "Bugsink alerts are not configured"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PathValue("secret")), []byte(secret)) != 1 {
		writeError(w, &HTTPError{Code: 401, Msg: "Unauthorized", Refused: true})
		return
	}
	var p struct {
		ID          string `json:"id"`
		ProjectName string `json:"project_name"`
		AlertReason string `json:"alert_reason"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bugsinkBodyMax))
	if err != nil || json.Unmarshal(body, &p) != nil {
		writeError(w, fail(400, "Invalid alert body"))
		return
	}
	reply := func(v doc) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(v)
	}
	switch p.AlertReason {
	case "TEST":
		reply(doc{"ok": true, "test": true})
		return
	case "NEW", "REGRESSED", "UNMUTED":
	default:
		writeError(w, fail(400, "Unknown alert_reason"))
		return
	}
	if !bugsinkIssueID.MatchString(p.ID) {
		writeError(w, fail(400, "Invalid issue id"))
		return
	}
	name := p.ProjectName
	if !bugsinkProjectName.MatchString(name) {
		name = ""
	}
	var asked bool
	if err := runTx(r.Context(), a, nil, true, "", func(q *Request) (err error) {
		asked, err = bugsinkProblem(q, p.ID, name, p.AlertReason)
		return err
	}); err != nil {
		writeError(w, err)
		return
	}
	reply(doc{"ok": true, "asked": asked})
}

// bugsinkProblem opens the owner's needs-you problem for an issue: a new issue asks once while its problem holds the
// cause (Dismiss keeps it quiet); a regression or an unmute is the issue coming back, so an earlier problem is let go
// and the issue asks again. The problem belongs to the Fleet project whose errors.bugsink names the Bugsink project.
func bugsinkProblem(q *Request, issue, project, reason string) (bool, error) {
	cause := bugsinkCause + issue
	if reason == "NEW" {
		if held, err := problemHeld(q, cause); err != nil || held != nil {
			return false, err
		}
	} else if err := problemClose(q, cause); err != nil {
		return false, err
	}
	fleetProject, err := bugsinkFleetProject(q, project)
	if err != nil {
		return false, err
	}
	data := doc{"issueId": issue, "alertReason": reason}
	if project != "" {
		data["bugsinkProject"] = project
	}
	return true, problemAsk(q, "direct", Problem{Cause: cause, Text: bugsinkText(issue, project, reason), ProjectID: fleetProject, Data: data})
}

// bugsinkText: fixed wording, the project name (a checked Bugsink project name), the issue id and its link only. The
// issue's title and every event field are reported by browsers or servers, untrusted: they never enter a question that
// people and agents read as Fleet's own words.
func bugsinkText(issue, project, reason string) string {
	what := "A new error issue"
	switch reason {
	case "REGRESSED":
		what = "A resolved error issue came back"
	case "UNMUTED":
		what = "A muted error issue passed its mute condition"
	}
	where := ""
	if project != "" {
		where = " in Bugsink project " + project
	}
	return what + where + ": " + bugsinkURL() + "/issues/issue/" + issue + "/ (bugsink issue_get {\"issue\": \"" + issue + "\"})."
}

// bugsinkFleetProject: the live Fleet project whose settings errors.bugsink list the Bugsink project; "" for none.
func bugsinkFleetProject(q *Request, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	heads, err := headTexts(q, "project")
	for _, h := range heads {
		var p struct {
			Errors struct {
				Bugsink []string `json:"bugsink"`
			} `json:"errors"`
		}
		if json.Unmarshal(h.text, &p) == nil {
			for _, n := range p.Errors.Bugsink {
				if n == name {
					return h.id, err
				}
			}
		}
	}
	return "", err
}

// bugsinkResolvedPass closes the problem of every issue Bugsink now reports resolved, or no longer has. Without the
// bugsink connection (a gateway, intake, an active key) it does nothing: problems then close through their answers.
func bugsinkResolvedPass(ctx context.Context, a *App) error {
	var held []doc
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
		held, err = queryRows(q, "SELECT cause,coalesce(project_id,'') AS project FROM question WHERE cause LIKE $1 AND state IN ('open','answered') ORDER BY seq", bugsinkCause+"%")
		return err
	}); err != nil || len(held) == 0 {
		return err
	}
	if ok, err := bugsinkKeyed(ctx, a); err != nil || !ok {
		return err
	}
	var failed error
	for _, h := range held {
		cause := astr(h["cause"])
		closed, err := bugsinkIssueClosed(ctx, a, astr(h["project"]), strings.TrimPrefix(cause, bugsinkCause))
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		if closed {
			if err := runTx(ctx, a, nil, true, "", func(q *Request) error { return problemClose(q, cause) }); err != nil {
				return err
			}
		}
	}
	return failed
}

// bugsinkIssueClosed: whether Bugsink reports the issue resolved or no longer has it (HTTP 404 from Bugsink itself,
// not a gateway refusal), read with the bugsink connection's issue_get as project. A variable so tests answer without
// a gateway.
var bugsinkIssueClosed = func(ctx context.Context, a *App, project, issue string) (bool, error) {
	token, err := readTokenFor(ctx, a, bugsinkConn, project)
	if err != nil {
		return false, err
	}
	line := gatewayCall(ctx, token, bugsinkConn, "issue_get", map[string]string{"issue": issue})
	if line.Outcome != contract.CallOutcomeOk {
		code := deref(line.ErrorCode)
		if code == "http-404" {
			return true, nil
		}
		return false, errors.New("bugsink issue_get " + issue + ": " + code)
	}
	var is struct {
		IsResolved bool `json:"is_resolved"`
	}
	if err := json.Unmarshal(line.Output, &is); err != nil {
		return false, errors.New("bugsink issue_get " + issue + ": " + err.Error())
	}
	return is.IsResolved, nil
}

// bugsinkKeyed: the gateway is configured and the bugsink connection takes calls with an active key. A variable so
// tests answer without a gateway.
var bugsinkKeyed = func(ctx context.Context, a *App) (bool, error) {
	if gatewayURL() == "" {
		return false, nil
	}
	ok := false
	err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		c, err := maybe(q, "connection:"+bugsinkConn, "api-connection")
		if err != nil || c == nil || c["intake"] != "all" {
			return err
		}
		ks, err := connKeys(q, bugsinkConn)
		for _, k := range ks {
			ok = ok || k["state"] == "active"
		}
		return err
	})
	return ok, err
}
