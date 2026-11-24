package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"fleet/contract"
)

const accessGroupName = "fleet-people"

var (
	accessAccountID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	accessGroupID   = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)
)

type peopleAccess struct {
	endpoint, groupID, token string
	client                   *http.Client
	configError              error
}

func peopleAccessFromEnv() *peopleAccess {
	account := strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID"))
	group := strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_GROUP_ID"))
	token := strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_API_TOKEN"))
	if account == "" && group == "" && token == "" {
		return nil
	}
	if !accessAccountID.MatchString(account) || !accessGroupID.MatchString(group) || token == "" {
		return &peopleAccess{configError: errors.New("Set the Cloudflare account ID, fleet-people group UUID and API token on the coordinator")}
	}
	return &peopleAccess{
		endpoint: "https://api.cloudflare.com/client/v4/accounts/" + account + "/access/groups/" + group,
		groupID:  group,
		token:    token,
		client: &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Group updates replace the mutable fields. Keep rules opaque so selectors Fleet
// does not manage round-trip through the API without losing their fields.
type accessGroupRules struct {
	Name      string            `json:"name"`
	Include   []json.RawMessage `json:"include"`
	Exclude   json.RawMessage   `json:"exclude,omitempty"`
	Require   json.RawMessage   `json:"require,omitempty"`
	IsDefault json.RawMessage   `json:"is_default,omitempty"`
}

func (a *peopleAccess) group(ctx context.Context, method string, rules *accessGroupRules) (accessGroupRules, error) {
	if a.configError != nil {
		return accessGroupRules{}, a.configError
	}
	var body *bytes.Reader
	if rules != nil {
		body = bytes.NewReader(jsonBytes(rules))
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.endpoint, body)
	if err != nil {
		return accessGroupRules{}, errors.New("Could not prepare the Cloudflare Access request")
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if rules != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return accessGroupRules{}, errors.New("Could not reach Cloudflare Access")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return accessGroupRules{}, fmt.Errorf("Cloudflare Access returned HTTP %d", res.StatusCode)
	}

	// Only Fleet-authored error strings leave this boundary. Provider payloads can contain private details.
	var reply struct {
		Success bool `json:"success"`
		Result  struct {
			accessGroupRules
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil || !reply.Success {
		return accessGroupRules{}, errors.New("Cloudflare Access did not confirm the group request")
	}
	if !strings.EqualFold(reply.Result.ID, a.groupID) || reply.Result.Name != accessGroupName || reply.Result.Include == nil {
		return accessGroupRules{}, errors.New("Cloudflare Access did not return the configured fleet-people group and its Include rules")
	}
	return reply.Result.accessGroupRules, nil
}

func accessRuleEmail(rule json.RawMessage) string {
	var entry struct {
		Email struct {
			Email string `json:"email"`
		} `json:"email"`
	}
	if json.Unmarshal(rule, &entry) != nil {
		return ""
	}
	email := strings.ToLower(strings.TrimSpace(entry.Email.Email))
	if !personEmail.MatchString(email) {
		return ""
	}
	return email
}

func (g accessGroupRules) includes(email string) bool {
	return slices.ContainsFunc(g.Include, func(rule json.RawMessage) bool { return accessRuleEmail(rule) == email })
}

func (a *peopleAccess) sync(ctx context.Context, email string, admit bool) error {
	group, err := a.group(ctx, http.MethodGet, nil)
	if err != nil || group.includes(email) == admit {
		return err
	}
	if admit {
		group.Include = append(group.Include, jsonBytes(map[string]any{"email": map[string]string{"email": email}}))
	} else {
		group.Include = slices.DeleteFunc(group.Include, func(rule json.RawMessage) bool { return accessRuleEmail(rule) == email })
	}
	if _, err := a.group(ctx, http.MethodPut, &group); err != nil {
		return err
	}
	confirmed, err := a.group(ctx, http.MethodGet, nil)
	if err == nil && confirmed.includes(email) != admit {
		return errors.New("Cloudflare Access still differs from the Fleet roster")
	}
	return err
}

func init() {
	op("people_access_status", peopleAccessStatus)
	op("people_access_sync", peopleAccessSync)
}

func peopleAccessStatus(q *Request, _ *contract.PeopleAccessStatusRequest) (contract.PeopleAccessStatusReply, error) {
	if err := peopleOwner(q); err != nil {
		return contract.PeopleAccessStatusReply{}, err
	}
	access := q.app.peopleAccess
	if access == nil {
		return contract.PeopleAccessStatusReply{State: "manual"}, nil
	}
	group, err := access.group(q.ctx(), http.MethodGet, nil)
	if err != nil {
		return contract.PeopleAccessStatusReply{State: "error", Error: ptr(err.Error())}, nil
	}
	admitted := []string{}
	for _, rule := range group.Include {
		if email := accessRuleEmail(rule); email != "" {
			admitted = append(admitted, email)
		}
	}
	sort.Strings(admitted)
	return contract.PeopleAccessStatusReply{State: "active", Admitted: slices.Compact(admitted)}, nil
}

func peopleAccessSync(q *Request, r *contract.PeopleAccessSyncRequest) (contract.PeopleAccessSyncReply, error) {
	email := strings.ToLower(strings.TrimSpace(r.Email))
	if !personEmail.MatchString(email) {
		return contract.PeopleAccessSyncReply{}, fail(400, "Enter a human sign-in email")
	}
	q.app.peopleAccessMu.Lock()
	defer q.app.peopleAccessMu.Unlock()
	if err := peopleOwner(q); err != nil {
		return contract.PeopleAccessSyncReply{}, err
	}
	current := q.app.peopleSnapshot().person(email)
	version := ""
	if current != nil {
		if current.Owner || current.AccessCommonName != "" {
			return contract.PeopleAccessSyncReply{}, fail(403, "Owner and machine identities are managed separately")
		}
		version = peopleRowVersion(current)
	}
	if r.BaseVersion != version {
		return contract.PeopleAccessSyncReply{}, fail(409, "Access changed. Reopen the person before syncing")
	}
	return syncPeopleAccess(q, email, current != nil), nil
}

func syncPeopleAccess(q *Request, email string, admit bool) contract.PeopleAccessSyncReply {
	access := q.app.peopleAccess
	if access == nil {
		return contract.PeopleAccessSyncReply{State: "manual"}
	}
	writeAudit := func(state, reason string) error {
		return q.app.store.Write(q.ctx(), q, nil, func(req *Request) error {
			value := map[string]any{"email": email, "admit": admit, "state": state}
			return audit(req, "people", "cloudflare-access", nil, value, auditExtra{Reason: reason})
		})
	}
	if err := writeAudit("requested", ""); err != nil {
		return contract.PeopleAccessSyncReply{State: "error", Error: ptr("Fleet could not record the Cloudflare sync attempt")}
	}
	out := contract.PeopleAccessSyncReply{State: "synced"}
	if err := access.sync(q.ctx(), email, admit); err != nil {
		out.State, out.Error = "error", ptr(err.Error())
	}
	if err := writeAudit(string(out.State), deref(out.Error)); err != nil {
		return contract.PeopleAccessSyncReply{State: "error", Error: ptr("Fleet could not record the Cloudflare sync outcome. Recheck admission")}
	}
	return out
}
