package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

const (
	serviceGrantDomain   = "fleet.ufobe.com"
	serviceGrantDuration = 8760 * time.Hour
	serviceGrantLease    = 3 * time.Minute
)

var (
	serviceGrantUUID     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	serviceGrantClientID = regexp.MustCompile(`^[0-9a-f]{32}\.access(?:\.[a-z0-9.-]+)?$`)
)

type serviceGrantToken struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ClientID       string            `json:"client_id"`
	Duration       string            `json:"duration"`
	Enabled        *bool             `json:"enabled"`
	ExpiresAt      time.Time         `json:"expires_at"`
	LastSeenAt     *time.Time        `json:"last_seen_at"`
	SecretVersion  *int32            `json:"client_secret_version"`
	LeakReportedAt *time.Time        `json:"leak_reported_at"`
	Tags           map[string]string `json:"tags"`
}

type serviceGrantApp struct {
	ID           string   `json:"id"`
	Domain       string   `json:"domain"`
	Audience     string   `json:"aud"`
	Type         string   `json:"type"`
	Domains      []string `json:"self_hosted_domains"`
	Destinations []struct {
		Type string `json:"type"`
		URI  string `json:"uri"`
	} `json:"destinations"`
}

type serviceGrantRules struct {
	Include []rawObject `json:"include"`
	Require []rawObject `json:"require"`
	Exclude []rawObject `json:"exclude"`
}

type serviceGrantPolicy struct {
	serviceGrantRules
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	Decision  string `json:"decision"`
}

type serviceGrantObservation struct {
	cf       *cloudflareAccountAPI
	token    serviceGrantToken
	provider string
	binding  string
}

func serviceGrantCFProblem(err error, noun string) string {
	var denied *cfDenied
	var httpErr *cfHTTPError
	switch {
	case errors.As(err, &denied):
		return "Cloudflare refused " + noun + " (" + denied.Permission + ")"
	case errors.As(err, &httpErr):
		return fmt.Sprintf("Cloudflare %s failed (HTTP %d)", noun, httpErr.Status)
	default:
		return "Cloudflare " + noun + " lookup unavailable"
	}
}

func serviceGrantList[T any](ctx context.Context, cf *cloudflareAccountAPI, path, permission string, params url.Values) ([]T, error) {
	if params == nil {
		params = url.Values{}
	}
	const perPage = 100
	params.Set("per_page", fmt.Sprint(perPage))
	var all []T
	for page := 1; ; page++ {
		params.Set("page", fmt.Sprint(page))
		var batch []T
		var info cfPage
		if err := cf.callPage(ctx, http.MethodGet, path+"?"+params.Encode(), nil, permission, &batch, &info); err != nil {
			return nil, err
		}
		if batch == nil || (info.Page != 0 && info.Page != page) {
			return nil, errors.New("invalid Cloudflare list")
		}
		all = append(all, batch...)
		if info.TotalPages <= page && info.TotalCount <= len(all) && (info.TotalPages > 0 || len(batch) < perPage) {
			return all, nil
		}
		if len(batch) == 0 {
			return nil, errors.New("incomplete Cloudflare list")
		}
	}
}

func serviceGrantExactApp(app serviceGrantApp, want string) bool {
	if app.ID != want || app.Type != "self_hosted" || app.Audience != contract.FleetAccessAudience ||
		(app.Domain != serviceGrantDomain && app.Domain != serviceGrantDomain+"/") {
		return false
	}
	if len(app.Destinations) > 0 {
		d := app.Destinations[0]
		return len(app.Destinations) == 1 && d.Type == "public" && (d.URI == serviceGrantDomain || d.URI == serviceGrantDomain+"/")
	}
	return len(app.Domains) == 0 || (len(app.Domains) == 1 && (app.Domains[0] == serviceGrantDomain || app.Domains[0] == serviceGrantDomain+"/"))
}

func serviceGrantExactPolicy(p serviceGrantPolicy, grant db.ServiceGrant) bool {
	if p.ID != deref(grant.PolicyID) || p.Decision != "non_identity" ||
		(p.AccountID != "" && p.AccountID != deref(grant.AccountID)) ||
		len(p.Include) != 1 || len(p.Require) != 0 || len(p.Exclude) != 0 || len(p.Include[0]) != 1 {
		return false
	}
	ref := recOf(p.Include[0]["service_token"])
	return len(ref) == 1 && rawText(ref["token_id"]) == deref(grant.TokenID)
}

// An attached Service Auth policy may admit the token directly, through a rule group, or through a broad selector.
// Treat a possible match as ineligible even if another rule could exclude it.
func serviceGrantRuleMatches(ctx context.Context, cf *cloudflareAccountAPI, rules []rawObject, token serviceGrantToken, groups map[string]bool, visiting map[string]bool) (bool, error) {
	for _, rule := range rules {
		if rule["any_valid_service_token"] != nil || rule["everyone"] != nil ||
			jsonPathString(rule["service_token"], "token_id") == token.ID {
			return true, nil
		}
		if rule["service_token_tag"] != nil && len(token.Tags) != 0 {
			return true, nil
		}
		id := jsonPathString(rule["group"], "id")
		if id == "" {
			if rule["group"] != nil {
				return false, errors.New("invalid Cloudflare rule group")
			}
			continue
		}
		if matched, ok := groups[id]; ok {
			if matched {
				return true, nil
			}
			continue
		}
		if visiting[id] || !serviceGrantUUID.MatchString(id) {
			return false, errors.New("invalid Cloudflare rule group")
		}
		var group struct {
			serviceGrantRules
			ID string `json:"id"`
		}
		if err := cf.call(ctx, http.MethodGet, "/groups/"+id, nil, "Access: Groups Read", &group); err != nil {
			return false, err
		}
		if group.ID != id {
			return false, errors.New("invalid Cloudflare rule group")
		}
		visiting[id] = true
		matched, err := serviceGrantRuleMatches(ctx, cf, group.Include, token, groups, visiting)
		delete(visiting, id)
		if err != nil || matched {
			return matched, err
		}
		groups[id] = false
	}
	return false, nil
}

func serviceGrantPolicyScope(ctx context.Context, cf *cloudflareAccountAPI, grant db.ServiceGrant, token serviceGrantToken) string {
	appID := deref(grant.ApplicationID)
	var app serviceGrantApp
	if err := cf.call(ctx, http.MethodGet, "/apps/"+appID, nil, cfPermApps, &app); err != nil {
		return serviceGrantCFProblem(err, "application")
	}
	if !serviceGrantExactApp(app, appID) {
		return "Cloudflare application domain, type or audience differs from Fleet"
	}
	apps, err := serviceGrantList[serviceGrantApp](ctx, cf, "/apps", cfPermApps, nil)
	if err != nil {
		return serviceGrantCFProblem(err, "applications")
	}
	groups := map[string]bool{}
	seenApps := map[string]bool{}
	found := false
	for _, entry := range apps {
		if !serviceGrantUUID.MatchString(entry.ID) || seenApps[entry.ID] {
			return "Cloudflare did not return a complete application listing"
		}
		seenApps[entry.ID] = true
		policies, err := serviceGrantList[serviceGrantPolicy](ctx, cf, "/apps/"+entry.ID+"/policies", cfPermApps, nil)
		if err != nil {
			return serviceGrantCFProblem(err, "application policies")
		}
		seenPolicies := map[string]bool{}
		for _, policy := range policies {
			if !serviceGrantUUID.MatchString(policy.ID) || seenPolicies[policy.ID] || policy.Decision == "" {
				return "Cloudflare did not return a complete policy listing"
			}
			seenPolicies[policy.ID] = true
			if policy.ID == deref(grant.PolicyID) && entry.ID == appID {
				if found || !serviceGrantExactPolicy(policy, grant) {
					return "Cloudflare policy is not the exact Service Auth binding"
				}
				found = true
				continue
			}
			if policy.ID == deref(grant.PolicyID) {
				return "Cloudflare grant policy also appears under another binding"
			}
			if policy.Decision != "non_identity" {
				continue
			}
			rules := slices.Concat(policy.Include, policy.Require)
			match, err := serviceGrantRuleMatches(ctx, cf, rules, token, groups, map[string]bool{})
			if err != nil {
				return serviceGrantCFProblem(err, "policy rule group")
			}
			if match {
				return "Another Cloudflare Service Auth policy could admit this token"
			}
		}
	}
	if !found {
		return "The exact Cloudflare Service Auth policy is absent from the application"
	}
	return ""
}

func serviceGrantObserveProvider(ctx context.Context, cf *cloudflareAccountAPI, grant db.ServiceGrant) serviceGrantObservation {
	out := serviceGrantObservation{cf: cf}
	if cf == nil || cf.account != deref(grant.AccountID) {
		out.provider = "The configured Cloudflare account authority is missing or differs from enrollment"
		return out
	}
	tokenPath := "/service_tokens/" + deref(grant.TokenID)
	var token serviceGrantToken
	if err := cf.call(ctx, http.MethodGet, tokenPath, nil, cfPermServiceTokens, &token); err != nil {
		out.provider = serviceGrantCFProblem(err, "service token")
		return out
	}
	token.ExpiresAt = token.ExpiresAt.Truncate(time.Microsecond)
	duration, _ := time.ParseDuration(token.Duration)
	if token.ID != deref(grant.TokenID) || token.Name != grant.Name || !serviceGrantClientID.MatchString(token.ClientID) ||
		(grant.ClientID != nil && token.ClientID != *grant.ClientID) || duration != serviceGrantDuration ||
		token.ExpiresAt.IsZero() || (token.Enabled != nil && !*token.Enabled) || token.LeakReportedAt != nil {
		out.provider = "Cloudflare token identity, one-year duration or enabled state differs from the grant"
		return out
	}
	tokens, err := serviceGrantList[serviceGrantToken](ctx, cf, "/service_tokens", cfPermServiceTokens, url.Values{"name": {grant.Name}})
	if err != nil {
		out.provider = serviceGrantCFProblem(err, "named tokens")
		return out
	}
	matches := 0
	for _, t := range tokens {
		if t.Name == grant.Name {
			if t.ID != token.ID || t.ClientID != token.ClientID || !t.ExpiresAt.Truncate(time.Microsecond).Equal(token.ExpiresAt) {
				out.provider = "Cloudflare named token lookup is inconsistent"
				return out
			}
			matches++
		}
	}
	if matches != 1 || (grant.ExpiresAt != nil && token.ExpiresAt.Before(*grant.ExpiresAt)) {
		out.provider = "Cloudflare token lookup is missing, duplicated or stale"
		return out
	}
	out.provider = serviceGrantPolicyScope(ctx, cf, grant, token)
	if out.provider == "" {
		out.token = token
	}
	return out
}

type serviceGrantRefresh struct {
	expires *time.Time
	refused bool
	detail  string
}

func serviceGrantRefreshToken(ctx context.Context, cf *cloudflareAccountAPI, token serviceGrantToken) serviceGrantRefresh {
	var reply serviceGrantToken
	err := cf.call(ctx, http.MethodPost, "/service_tokens/"+token.ID+"/refresh", nil, cfPermServiceTokens, &reply)
	if err != nil {
		var denied *cfDenied
		var httpErr *cfHTTPError
		refused := errors.As(err, &denied) || (errors.As(err, &httpErr) &&
			slices.Contains([]int{400, 404, 409, 422, 429}, httpErr.Status))
		return serviceGrantRefresh{refused: refused, detail: serviceGrantCFProblem(err, "service token refresh")}
	}
	duration, _ := time.ParseDuration(reply.Duration)
	reply.ExpiresAt = reply.ExpiresAt.Truncate(time.Microsecond)
	if reply.ID != token.ID || reply.Name != token.Name || reply.ClientID != token.ClientID ||
		duration != serviceGrantDuration || !reply.ExpiresAt.After(token.ExpiresAt) ||
		!serviceGrantSameSecretVersion(token.SecretVersion, reply.SecretVersion) {
		return serviceGrantRefresh{detail: "Cloudflare refresh response did not confirm the original identity and extended expiry"}
	}
	return serviceGrantRefresh{expires: &reply.ExpiresAt}
}

func serviceGrantSameSecretVersion(before, after *int32) bool {
	// Cloudflare's read schema may omit the version. The refresh endpoint preserves the secret.
	return before == nil || after == nil || *before == *after
}

func serviceGrantRenewalResult(a db.ServiceGrantRenewal, token serviceGrantToken) (result, detail string) {
	if a.DispatchedAt == nil || a.BaselineExpiresAt == nil {
		return "", "Cloudflare refresh dispatch has no saved baseline"
	}
	if a.BaselineSecretVersion != nil && token.SecretVersion == nil {
		return "", "Cloudflare omitted the previously observed token secret version"
	}
	if !serviceGrantSameSecretVersion(a.BaselineSecretVersion, token.SecretVersion) {
		return "", "Cloudflare token secret version changed during refresh"
	}
	if !token.ExpiresAt.After(*a.BaselineExpiresAt) {
		return "", "Cloudflare expiry readback has yet to advance"
	}
	refreshTime := token.ExpiresAt.Add(-serviceGrantDuration)
	if refreshTime.Before(a.DispatchedAt.Add(-time.Minute)) || refreshTime.After(a.DispatchedAt.Add(serviceGrantLease)) {
		return "", "Cloudflare expiry is outside the recorded refresh interval"
	}
	if a.AcknowledgedExpiry != nil {
		if !token.ExpiresAt.Equal(*a.AcknowledgedExpiry) {
			return "", "Cloudflare expiry differs from the refresh acknowledgement"
		}
		return "verified", ""
	}
	return "verified_by_readback", ""
}
