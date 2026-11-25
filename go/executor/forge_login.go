package main

import (
	"os"
	"strings"
	"sync"
	"time"

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
	return strings.TrimSpace(v) != ""
}
