package main

import (
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/errtrack"
)

// Forge login checks behind the publishing capabilities (capabilities): a host advertises publishing:<provider>:<host>:*
// only after the forge CLI answered as a user from Fleet's forge store, not because a config file exists (DAM Lab,
// 2026-11-25: owner-mac advertised gitlab.ufobe.com with no working glab login, and its runs failed
// forge-actor-auth-unready). A check runs in the background, so a poll never waits on the forge; until the first
// answer the capability stays off.
var forgeLogins = struct {
	sync.Mutex
	ok, busy map[string]bool
	at       map[string]time.Time
}{ok: map[string]bool{}, busy: map[string]bool{}, at: map[string]time.Time{}}

// forgeLoginRecheck: how old a forge login answer may be before it is checked again.
const forgeLoginRecheck = 10 * time.Minute

// forgeLoginVerified: the last answer for provider and host; a missing or stale one starts a background check.
func forgeLoginVerified(provider, host string) bool {
	key := provider + "|" + strings.ToLower(host)
	forgeLogins.Lock()
	defer forgeLogins.Unlock()
	if at, seen := forgeLogins.at[key]; (!seen || time.Since(at) > forgeLoginRecheck) && !forgeLogins.busy[key] {
		forgeLogins.busy[key] = true
		errtrack.Go("forge-login", func() {
			ok := forgeLoginCheck(provider, host)
			forgeLogins.Lock()
			forgeLogins.ok[key], forgeLogins.at[key], forgeLogins.busy[key] = ok, time.Now(), false
			forgeLogins.Unlock()
		})
	}
	return forgeLogins.ok[key]
}

// forgeLoginCheck: the forge CLI of provider answers its user endpoint on host from the forge store.
func forgeLoginCheck(provider, host string) bool {
	store := os.Getenv("FLEET_FORGE_CONFIG_DIR")
	ctx := forgeContext{Provider: provider, Host: host, Store: store, GlabStore: resolveGlabStore(store), Home: os.TempDir(),
		CredentialHome: os.Getenv("HOME"), Workdir: os.TempDir()}
	if !ctx.hasCredentialStore(provider) {
		return false
	}
	name, field := "gh", "login"
	if provider == "gitlab" {
		name, field = "glab", "username"
	}
	out, err := boundedOutput(forgeCommand(ctx, name, "api", "--hostname", host, "user"), probeWait)
	if err != nil {
		return false
	}
	user := typedRaw[map[string]any](out)
	v, _ := user[field].(string)
	if strings.TrimSpace(v) == "" {
		return false
	}
	if provider == "gitlab" {
		if patOut, patErr := boundedOutput(forgeCommand(ctx, "glab", "api", "--hostname", host, "personal_access_tokens/self"), probeWait); patErr == nil {
			pat := typedRaw[struct {
				Active *bool    `json:"active"`
				Scopes []string `json:"scopes"`
			}](patOut)
			if pat.Active != nil && !*pat.Active {
				return false
			}
			if len(pat.Scopes) > 0 {
				hasAPI := false
				for _, s := range pat.Scopes {
					if s == "api" {
						hasAPI = true
						break
					}
				}
				if !hasAPI {
					return false
				}
			}
		}
	}
	return true
}

// forgeLoginReport: every answered forge login check, for the poll (System › Access shows a missing login).
func forgeLoginReport() []contract.ForgeLogin {
	forgeLogins.Lock()
	defer forgeLogins.Unlock()
	out := []contract.ForgeLogin{}
	for key, at := range forgeLogins.at {
		provider, host, _ := strings.Cut(key, "|")
		ms := at.UnixMilli()
		out = append(out, contract.ForgeLogin{Provider: provider, Host: host, Ok: forgeLogins.ok[key], CheckedAt: &ms})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider+out[i].Host < out[j].Provider+out[j].Host })
	return out
}
