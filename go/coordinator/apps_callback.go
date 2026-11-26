// Project app callbacks (docs/designs/fleet-project-apps.md, Calling Fleet): a pipeline_run an app starts with notify
// gets an app_callback row that waits (next_attempt_at 'infinity') until the run ends; pipelineEnded makes it due, and
// the work kit's app-callback queue posts {runId, requestId, state, finishedAt} to the URL with X-Fleet-App and
// X-Fleet-Signature (hex HMAC-SHA256 of the body with the app's callback secret). A 2xx ends the item; anything else is
// retried with the kit's backoff until the run's record goes (the row goes with it). The body carries no result.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	kitRegister(&kitQueue{Name: "app-callback", Period: 5 * time.Second, Limit: 16, Workers: 4, Due: appCallbackDue, Step: appCallbackStep,
		Retry: kitRetry{Table: "app_callback", Key: "run_id", Guard: `app_callback.attempts=$5::int`, Class: func(error) string { return "app_callback_failed" }}})
}

// appCallbackBody: what a callback says; the app reads the result with pipeline_get.
type appCallbackBody struct {
	RunID      string `json:"runId"`
	RequestID  string `json:"requestId"`
	State      string `json:"state"`
	FinishedAt *int64 `json:"finishedAt"`
}

// appCallbackSign: the X-Fleet-Signature value of body under secret.
func appCallbackSign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// appNotifyURL: a pipeline_run's checked callback URL: only for an app caller, on one of its callback origins.
func appNotifyURL(p projectAppPayload, n *contract.PipelineRunNotify) (string, error) {
	u, err := url.Parse(n.Url)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", fail(400, "notify.url: an https URL on one of the app's callbackOrigins")
	}
	origin := "https://" + strings.TrimSuffix(strings.ToLower(u.Host), ":443")
	if !slices.Contains(p.CallbackOrigins, origin) && !slices.Contains(p.CallbackOrigins, origin+":443") {
		return "", fail(400, "notify.url: "+origin+" is not one of the app's callbackOrigins (project_app_set)")
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String(), nil
}

func appCallbackSecretFile(appID string) string {
	if secretDir() == "" {
		return ""
	}
	return filepath.Join(secretDir(), "apps", "callback", appID)
}

// appCallbackSecretEnsure: app's callback secret, made with its first key (at approval) and kept across key rotations,
// so callbacks verify while the app still uses its previous key. As the key file, it is written in the minting
// transaction and goes again if that transaction rolls back.
func appCallbackSecretEnsure(q *Request, appID string) error {
	file := appCallbackSecretFile(appID)
	if file == "" {
		return fail(409, "FLEET_SECRET_DIR is not set on the coordinator")
	}
	if _, err := os.Stat(file); err == nil {
		return nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil // made meanwhile by another mint
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString("fcs_" + base64.RawURLEncoding.EncodeToString(raw))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(file)
		return err
	}
	afterRollback(q, func() { _ = os.Remove(file) })
	return nil
}

// appCallbackSecret: app's callback secret, "" when it has none.
func appCallbackSecret(appID string) string {
	file := appCallbackSecretFile(appID)
	if file == "" {
		return ""
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func appCallbackDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().AppCallbackDue(q.ctx(), db.AppCallbackDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.RunID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

// appCallbackResult: one post's outcome ("" delivered).
type appCallbackResult struct{ Err string }

func appCallbackStep(q *Request, now time.Time, c *kitClaim) error {
	cb, err := q.db().AppCallbackGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // delivered, or gone with its run
	}
	if err != nil {
		return err
	}
	if c.Result != nil {
		if res, _ := c.Result.(appCallbackResult); res.Err != "" {
			return errors.New(res.Err)
		}
		return q.db().AppCallbackDelivered(q.ctx(), c.Key)
	}
	run, err := getRun(q, c.Key)
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}
	if run.State != "succeeded" && run.State != "failed" && run.State != "cancelled" {
		return errors.New("the run has not ended")
	}
	secret := appCallbackSecret(cb.AppID)
	if secret == "" {
		return errors.New("app " + cb.AppID + " has no callback secret: the owner rotates its key (project_app_rotate)")
	}
	body := jsonBytes(appCallbackBody{RunID: run.ID, RequestID: run.RequestID, State: run.State, FinishedAt: run.FinishedAt})
	sig, target, app := appCallbackSign(secret, body), cb.Url, cb.AppID
	c.Call = func(ctx context.Context) any { return appCallbackPost(ctx, target, app, sig, body) }
	return nil
}

// appCallbackClient: one post at a time per call, never following a redirect (the origin was approved, its targets not).
var appCallbackClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func appCallbackPost(ctx context.Context, target, app, sig string, body []byte) appCallbackResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return appCallbackResult{Err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fleet-App", app)
	req.Header.Set("X-Fleet-Signature", sig)
	res, err := appCallbackClient.Do(req)
	if err != nil {
		return appCallbackResult{Err: err.Error()}
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return appCallbackResult{Err: fmt.Sprintf("callback answered %d", res.StatusCode)}
	}
	return appCallbackResult{}
}
