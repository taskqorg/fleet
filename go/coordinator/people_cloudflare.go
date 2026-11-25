package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

const cloudflareAuditPrefix = "cloudflare-access:"

// Group updates replace the full rule list. Serialize the roster edits and their Cloudflare
// updates without blocking sign-in or file refreshes on an external request.
var cloudflarePeopleMu sync.Mutex

var (
	cloudflareAccountID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	cloudflareGroupID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type peopleCloudflare struct {
	client  *http.Client
	url     string
	groupID string
	token   string
}

func cloudflarePeopleFromEnv() (*peopleCloudflare, error) {
	account := strings.ToLower(strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID")))
	group := strings.ToLower(strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_GROUP_ID")))
	tokenFile := strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE"))
	if account == "" && group == "" && tokenFile == "" {
		return nil, nil
	}
	if !cloudflareAccountID.MatchString(account) || !cloudflareGroupID.MatchString(group) || !filepath.IsAbs(tokenFile) {
		return nil, errors.New("Cloudflare sync needs a valid account ID, fleet-people group ID and API token file in the coordinator settings")
	}
	info, err := os.Stat(tokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("Cloudflare API token file must be a private regular file on the coordinator")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, errors.New("Could not read the Cloudflare API token file")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("Cloudflare API token file must contain one token")
	}
	return &peopleCloudflare{
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		url:     "https://api.cloudflare.com/client/v4/accounts/" + account + "/access/groups/" + group,
		groupID: group,
		token:   token,
	}, nil
}

type accessGroup struct {
	ID        string            `json:"id,omitempty"`
	Name      string            `json:"name"`
	Include   []json.RawMessage `json:"include"`
	Exclude   []json.RawMessage `json:"exclude,omitempty"`
	Require   []json.RawMessage `json:"require,omitempty"`
	IsDefault *bool             `json:"is_default,omitempty"`
}

func (c *peopleCloudflare) exchange(ctx context.Context, method string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("Could not build the Cloudflare group request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	// Provider bodies and transport errors may contain sensitive request details.
	if err != nil {
		return nil, errors.New("Could not reach Cloudflare Access")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Cloudflare Access returned HTTP %d", resp.StatusCode)
	}
	var reply struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil || !reply.Success {
		return nil, errors.New("Cloudflare Access did not confirm the group request")
	}
	return reply.Result, nil
}

func (c *peopleCloudflare) readGroup(ctx context.Context) (accessGroup, error) {
	raw, err := c.exchange(ctx, http.MethodGet, nil)
	if err != nil {
		return accessGroup{}, err
	}
	var group accessGroup
	if json.Unmarshal(raw, &group) != nil || group.ID != c.groupID || group.Name != "fleet-people" || group.Include == nil {
		return accessGroup{}, errors.New("Cloudflare Access did not return the configured fleet-people group and its Include rules")
	}
	return group, nil
}

func directAccessEmail(rule json.RawMessage) string {
	var selector map[string]json.RawMessage
	if json.Unmarshal(rule, &selector) != nil || len(selector) != 1 {
		return ""
	}
	var ruleEmail struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(selector["email"], &ruleEmail) != nil {
		return ""
	}
	return strings.ToLower(ruleEmail.Email)
}

func (c *peopleCloudflare) sync(ctx context.Context, email string, admit bool) error {
	group, err := c.readGroup(ctx)
	if err != nil {
		return err
	}
	include := make([]json.RawMessage, 0, len(group.Include))
	found := false
	for _, rule := range group.Include {
		if directAccessEmail(rule) == email {
			found = true
			if !admit {
				continue
			}
		}
		include = append(include, rule)
	}
	if found == admit {
		return nil
	}
	if admit {
		include = append(include, jsonBytes(map[string]map[string]string{"email": {"email": email}}))
	}
	group.ID, group.Include = "", include // The API takes writable fields only.
	if _, err := c.exchange(ctx, http.MethodPut, jsonBytes(group)); err != nil {
		return err
	}
	observed, err := c.readGroup(ctx)
	if err != nil {
		return err
	}
	found = false
	for _, rule := range observed.Include {
		found = found || directAccessEmail(rule) == email
	}
	if found != admit {
		return errors.New("Cloudflare Access still reports a different email rule")
	}
	return nil
}

type peopleCloudflareChange struct {
	Admit bool `json:"admit"`
	contract.PeopleCloudflareState
}

func cloudflareHistory(q *Request) (map[string]peopleCloudflareChange, error) {
	rows, err := q.db().PeopleCloudflareAudit(q.ctx())
	if err != nil {
		return nil, err
	}
	history := make(map[string]peopleCloudflareChange, len(rows))
	for _, row := range rows {
		var change peopleCloudflareChange
		if err := json.Unmarshal(row.Value, &change); err != nil {
			return nil, err
		}
		history[strings.TrimPrefix(row.Field, cloudflareAuditPrefix)] = change
	}
	return history, nil
}

func cloudflareRoster(q *Request, out *contract.PeopleListReply) error {
	history, err := cloudflareHistory(q)
	if err != nil {
		return err
	}
	mode := contract.PeopleCloudflareModeManual
	unobserved := contract.PeopleCloudflareStatusManual
	emails := map[string]bool{}
	readErr := q.app.peopleCloudflareConfigError
	if q.app.peopleCloudflare != nil {
		mode = "active"
		var group accessGroup
		group, readErr = q.app.peopleCloudflare.readGroup(q.ctx())
		for _, rule := range group.Include {
			emails[directAccessEmail(rule)] = true
		}
	}
	if readErr != nil {
		unobserved = "unknown"
		out.CloudflareError = ptr(readErr.Error())
		if q.app.peopleCloudflare == nil {
			mode = "error"
		}
	}
	out.CloudflareMode = ptr(mode)
	for i := range out.Roster {
		row := &out.Roster[i]
		change := history[row.Email]
		delete(history, row.Email)
		if row.Machine {
			continue
		}
		state := contract.PeopleCloudflareState{Status: unobserved}
		if mode == "active" && readErr == nil {
			state.Status = "missing"
			if emails[row.Email] {
				state.Status = "present"
			}
		}
		if state.Status != "present" && change.Admit {
			state.Message = change.Message
		}
		row.Cloudflare = &state
	}
	for email, change := range history {
		if change.Admit {
			continue
		}
		state := change.PeopleCloudflareState
		if mode == "active" && readErr == nil {
			if !emails[email] {
				continue
			}
			state.Status = "removal_required"
		} else if state.Status != "pending" {
			continue
		}
		out.CloudflareRemovals = append(out.CloudflareRemovals, contract.PeopleCloudflareRemoval{Email: email, Cloudflare: state})
	}
	return nil
}

func cloudflareIntent(a *App, admit bool) peopleCloudflareChange {
	status := contract.PeopleCloudflareStatusPending
	if a.peopleCloudflare == nil && a.peopleCloudflareConfigError == nil {
		status = "manual"
	}
	return peopleCloudflareChange{Admit: admit, PeopleCloudflareState: contract.PeopleCloudflareState{Status: status}}
}

func auditCloudflare(q *Request, email string, previous, next any) error {
	return audit(q, "people", cloudflareAuditPrefix+email, previous, next, auditExtra{})
}

func finishCloudflareSync(q *Request, email string, intent peopleCloudflareChange) contract.PeopleCloudflareState {
	if intent.Status == "manual" {
		return intent.PeopleCloudflareState
	}
	out := intent
	err := q.app.peopleCloudflareConfigError
	if q.app.peopleCloudflare != nil {
		err = q.app.peopleCloudflare.sync(q.ctx(), email, intent.Admit)
	}
	if err != nil {
		out.Message = ptr(err.Error())
	} else if intent.Admit {
		out.Status = "present"
	} else {
		out.Status = "removed"
	}
	if err := q.app.store.Write(q.ctx(), q, nil, func(tx *Request) error {
		return auditCloudflare(tx, email, intent, out)
	}); err != nil {
		intent.Message = ptr("Fleet could not record the Cloudflare result. Refresh or retry the rule.")
		return intent.PeopleCloudflareState
	}
	return out.PeopleCloudflareState
}

func peopleAccessSync(q *Request, r *contract.PeopleAccessSyncRequest) (contract.PeopleAccessSyncReply, error) {
	out := contract.PeopleAccessSyncReply{}
	email := strings.ToLower(strings.TrimSpace(r.Email))
	if !personEmail.MatchString(email) {
		return out, fail(400, "Enter a human sign-in email")
	}
	cloudflarePeopleMu.Lock()
	defer cloudflarePeopleMu.Unlock()
	intent, err := prepareCloudflareRetry(q, email)
	if err != nil {
		return out, err
	}
	out.Cloudflare = finishCloudflareSync(q, email, intent)
	return out, nil
}

func prepareCloudflareRetry(q *Request, email string) (peopleCloudflareChange, error) {
	peopleMu.Lock()
	defer peopleMu.Unlock()
	if err := peopleManager(q, false); err != nil {
		return peopleCloudflareChange{}, err
	}
	p := q.app.peopleSnapshot().person(email)
	if p != nil && (p.Owner || p.AccessCommonName != "") {
		return peopleCloudflareChange{}, fail(403, "Owner and machine identities are managed separately")
	}
	admit := p != nil
	intent := cloudflareIntent(q.app, admit)
	err := q.app.store.Write(q.ctx(), q, nil, func(tx *Request) error {
		if !admit {
			history, err := cloudflareHistory(tx)
			if err != nil {
				return err
			}
			if change, found := history[email]; !found || change.Admit {
				return fail(404, "No recorded Fleet revocation for this email")
			}
		}
		return auditCloudflare(tx, email, nil, intent)
	})
	return intent, err
}
