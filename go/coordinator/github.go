// GitHub reads for the coordinator's own screens (Delivery, Runners): one tool of the github provider (providers.go),
// cached here so browsers never call GitHub. With the github connection the read goes through fleet-gateway with a
// token minted for it (revoked after, recorded as a tool_run like any call); without it the coordinator reads the
// public fleet repository unauthenticated, 60 requests an hour per address, so its cache lives longer.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

// githubPublicRepo: the one repository read without a key.
const githubPublicRepo = "taskqorg/fleet"

var (
	githubMu    sync.Mutex // guards githubCache and githubLocks, never held across a read
	githubCache = map[string]githubCached{}
	githubLocks = map[string]*sync.Mutex{}
	// githubRefused: when the key last failed per tool (guarded by githubMu); for 5 min that tool of the public
	// repository is read publicly, so a key without a permission does not add a failed gateway call to every read.
	githubRefused = map[string]time.Time{}
)

// githubOnce: the cached reply of key when younger than ttl, else fetch's, stored when keep says so. Reads of one key
// run one at a time (a concurrent miss waits and gets the first one's reply); different keys never wait on each other,
// so a slow screen (Runners reads jobs run by run) does not hold up Delivery's pass.
func githubOnce(key string, ttl time.Duration, fetch func() ([]byte, error), keep func(error) bool) ([]byte, error) {
	githubMu.Lock()
	if len(githubCache) > 2000 {
		// ponytail: run_jobs adds a key per run; drop entries past every ttl (5 min) with their locks. A lock dropped
		// while held lets one concurrent read through twice, nothing worse.
		for k, c := range githubCache {
			if time.Since(c.at) > 10*time.Minute {
				delete(githubCache, k)
				delete(githubLocks, k)
			}
		}
	}
	l := githubLocks[key]
	if l == nil {
		l = &sync.Mutex{}
		githubLocks[key] = l
	}
	githubMu.Unlock()
	l.Lock()
	defer l.Unlock()
	githubMu.Lock()
	c, ok := githubCache[key]
	githubMu.Unlock()
	if ok && time.Since(c.at) < ttl {
		return c.body, c.err
	}
	body, err := fetch()
	if keep(err) {
		githubMu.Lock()
		githubCache[key] = githubCached{time.Now(), body, err}
		githubMu.Unlock()
	}
	return body, err
}

type githubCached struct {
	at   time.Time
	body []byte
	err  error
}

// githubConnectionOf: the connection holding the key for a repository owner, its row and whether the gateway can serve
// it (configured, intake open, an active key). A fine-grained token has one resource owner, so another owner's
// repositories use connection github-<owner> (lower case) when it exists, else github.
// An owner connection that cannot serve (no active key yet, intake closed) falls back to github.
func githubConnectionOf(q *Request, owner string) (name string, c doc, keyed bool, err error) {
	serves := func(n string) (doc, bool, error) {
		c, err := maybe(q, "connection:"+n, "api-connection")
		if err != nil || c == nil || gatewayURL() == "" || c["intake"] != "all" {
			return c, false, err
		}
		ks, err := connKeys(q, n)
		for _, k := range ks {
			if k["state"] == "active" {
				return c, true, err
			}
		}
		return c, false, err
	}
	if n := "github-" + strings.ToLower(owner); connName.MatchString(n) {
		if c, keyed, err = serves(n); err != nil || keyed {
			return n, c, keyed, err
		}
	}
	c, keyed, err = serves("github")
	return "github", c, keyed, err
}

// githubRead: the reply body of tool with args (the path fields, e.g. owner and repo) for project's access check. A
// reply younger than 30 s (keyed) or 5 min (public) is served from the cache. keyed says which way it went.
func githubRead(ctx context.Context, a *App, project, tool string, args map[string]string) (body []byte, keyed bool, err error) {
	var connection string
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
		connection, _, keyed, err = githubConnectionOf(q, args["owner"])
		return
	}); err != nil {
		return nil, false, fmt.Errorf("github connection of %s: %w", args["owner"], err)
	}
	path := astr(mapOf(providerTools("github")[tool])["path"])
	if path == "" {
		return nil, keyed, fmt.Errorf("github has no tool %s", tool)
	}
	pairs := []string{}
	for k, v := range args {
		pairs = append(pairs, "{"+k+"}", v)
	}
	path = strings.NewReplacer(pairs...).Replace(path)
	if strings.Contains(path, "{") {
		return nil, keyed, fmt.Errorf("github %s: a path field is missing", tool)
	}
	ttl := 30 * time.Second
	if !keyed {
		ttl = 5 * time.Minute
		if args["owner"]+"/"+args["repo"] != githubPublicRepo {
			return nil, false, fmt.Errorf("no github key: only %s is read", githubPublicRepo)
		}
	}
	// A keyed reply is served only to the project its token was minted for; the public reply is the same for all.
	cacheKey := "public " + path
	if keyed {
		cacheKey = "key " + connection + " " + project + " " + path
	}
	public := func() ([]byte, error) {
		// A failed public read is kept too (a rate-limited address must not ask again before the cache expires), but
		// not a caller that left: its cancelled read says nothing about GitHub.
		return githubOnce("public "+path, 5*time.Minute, func() ([]byte, error) { return githubPublic(ctx, path) }, func(error) bool { return ctx.Err() == nil })
	}
	githubMu.Lock()
	refused := time.Since(githubRefused[tool]) < 5*time.Minute
	githubMu.Unlock()
	isPublic := strings.EqualFold(args["owner"]+"/"+args["repo"], githubPublicRepo)
	if !keyed || refused && isPublic {
		body, err = public()
		return body, false, err
	}
	body, err = githubOnce(cacheKey, ttl, func() ([]byte, error) { return githubGateway(ctx, a, connection, project, tool, args) }, func(err error) bool { return err == nil })
	if err != nil && isPublic && ctx.Err() == nil {
		// A refused key (expired, wrong lifetime for the org) must not blank the public repository.
		log.Printf("github %s keyed: %v; read public", tool, err)
		githubMu.Lock()
		githubRefused[tool] = time.Now()
		githubMu.Unlock()
		body, err = public()
		return body, false, err
	}
	return body, true, err
}

// githubReadKeyed: through the connection only, never the public fallback: the delivery history makes hundreds of
// reads, which must not spend the shared keyless budget. A tool the connection does not grant fails with the
// gateway's error (unknown-tool: connections_set_tools).
func githubReadKeyed(ctx context.Context, a *App, project, tool string, args map[string]string) ([]byte, error) {
	var connection string
	var keyed bool
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
		connection, _, keyed, err = githubConnectionOf(q, args["owner"])
		return
	}); err != nil {
		return nil, fmt.Errorf("github connection of %s: %w", args["owner"], err)
	}
	if !keyed {
		return nil, fmt.Errorf("no github key on connection %s", connection)
	}
	path := astr(mapOf(providerTools("github")[tool])["path"])
	if path == "" {
		return nil, fmt.Errorf("github has no tool %s", tool)
	}
	pairs := []string{}
	for k, v := range args {
		pairs = append(pairs, "{"+k+"}", v)
	}
	path = strings.NewReplacer(pairs...).Replace(path)
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("github %s: a path field is missing", tool)
	}
	return githubOnce("key "+connection+" "+project+" "+path, 30*time.Second, func() ([]byte, error) { return githubGateway(ctx, a, connection, project, tool, args) }, func(err error) bool { return err == nil })
}

func githubPublic(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "fleet-coordinator")
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20+1))
	if err == nil && len(b) > 16<<20 {
		err = fmt.Errorf("GitHub reply over 16 MiB on %s", strings.SplitN(path, "?", 2)[0])
	}
	if err == nil && res.StatusCode != 200 {
		err = fmt.Errorf("GitHub %d on %s", res.StatusCode, strings.SplitN(path, "?", 2)[0])
	}
	return b, err
}

// githubTokens: one minted token per connection and project, reused for githubTokenTTL and then revoked. Minting
// and revoking per call (connCallFor) took about 12 s a read on PDS18 (two write transactions, a snapshot rewrite and a
// spool ingest); the delivery history makes hundreds. The calls become tool_runs when the token is revoked.
type githubToken struct {
	token string
	until time.Time
}

const githubTokenTTL = 5 * time.Minute

var (
	githubTokMu    sync.Mutex // guards the two maps, never held across a mint
	githubToks     = map[string]githubToken{}
	githubTokLocks = map[string]*sync.Mutex{} // per connection and project, held across its mint
)

func githubTokenFor(ctx context.Context, a *App, connection, project string) (string, error) {
	if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(connection) {
		return "", fmt.Errorf("github %s: connection-unavailable", connection)
	}
	key := connection + " " + project
	githubTokMu.Lock()
	l := githubTokLocks[key]
	if l == nil {
		l = &sync.Mutex{}
		githubTokLocks[key] = l
	}
	githubTokMu.Unlock()
	l.Lock() // concurrent reads of one key share its mint; other keys never wait
	defer l.Unlock()
	githubTokMu.Lock()
	t, ok := githubToks[key]
	githubTokMu.Unlock()
	if ok && time.Now().Before(t.until) {
		return t.token, nil
	}
	exec := "github-read:" + newID()
	var token string
	if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		m, err := mint(q, doc{"executionId": exec, "projectId": project, "step": true}, []string{connection})
		if err == nil {
			token, _ = m["token"].(string)
		}
		return err
	}); err != nil {
		return "", fmt.Errorf("github token for %s: %w", connection, err)
	}
	if token == "" {
		return "", fmt.Errorf("github %s: connection-unavailable (not granted to %s, or intake closed)", connection, project)
	}
	revoke := func() {
		if err := runTx(context.Background(), a, nil, true, "", func(q *Request) error {
			if err := revokeTokens(q, exec); err != nil {
				return err
			}
			_, err := connIngest(q)
			return err
		}); err != nil {
			log.Printf("revoke %s: %v", exec, err)
		}
	}
	// The gateway must see the token before the call; a failed snapshot revokes it at once.
	if err := writeSnapshot(context.WithoutCancel(ctx), a); err != nil {
		revoke()
		return "", err
	}
	githubTokMu.Lock()
	githubToks[key] = githubToken{token, time.Now().Add(githubTokenTTL)}
	githubTokMu.Unlock()
	// Revoked at its end whatever happens next (a restart revokes it too: revokeSteps); its calls are ingested then.
	// ponytail: an access or intake change reaches a cached token within githubTokenTTL, not at the next read.
	time.AfterFunc(githubTokenTTL+5*time.Second, revoke)
	return token, nil
}

// githubGateway: one call through the gateway with the connection's shared read token, recorded as a tool_run.
func githubGateway(ctx context.Context, a *App, connection, project, tool string, args map[string]string) ([]byte, error) {
	if gatewayURL() == "" {
		return nil, fmt.Errorf("github %s: connection-unavailable", tool)
	}
	token, err := githubTokenFor(ctx, a, connection, project)
	if err != nil {
		return nil, err
	}
	line := gatewayCall(ctx, token, connection, tool, args)
	if line.Outcome != contract.CallOutcomeOk {
		return nil, fmt.Errorf("github %s: %s %d", tool, deref(line.ErrorCode), line.Status)
	}
	return line.Output, nil
}
