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
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

// githubPublicRepo: the one repository read without a key.
const githubPublicRepo = "taskqorg/fleet"

// keyedEntry holds the per-key mutex and active holder/waiter reference count for keyedLock.
type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

// keyedLock serializes callers per key and removes idle entries once the last holder or waiter unlocks.
type keyedLock struct {
	mu      sync.Mutex
	entries map[string]*keyedEntry
}

func (k *keyedLock) lock(key string) func() {
	k.mu.Lock()
	if k.entries == nil {
		k.entries = map[string]*keyedEntry{}
	}
	e := k.entries[key]
	if e == nil {
		e = &keyedEntry{}
		k.entries[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(k.entries, key)
		}
		k.mu.Unlock()
	}
}

var (
	githubMu    sync.Mutex // guards githubCache and githubRefused, never held across a read
	githubCache = map[string]githubCached{}
	githubLocks keyedLock
	// githubRefused: when the key last failed per tool (guarded by githubMu); for 5 min that tool of the public
	// repository is read publicly, so a key without a permission does not add a failed gateway call to every read.
	githubRefused = map[string]time.Time{}
)

// githubOnce: the cached reply of key when younger than ttl, else fetch's, stored when keep says so. Reads of one key
// run one at a time (a concurrent miss waits and gets the first one's reply); different keys never wait on each other,
// so a slow screen (Runners reads jobs run by run) does not hold up Delivery's pass.
func githubOnce(key string, ttl time.Duration, fetch func() ([]byte, error), keep func(error) bool) ([]byte, error) {
	unlock := githubLocks.lock(key)
	defer unlock()
	githubMu.Lock()
	if len(githubCache) > 2000 {
		// ponytail: run_jobs adds a key per run; drop entries past every ttl (5 min).
		for k, c := range githubCache {
			if time.Since(c.at) > 10*time.Minute {
				delete(githubCache, k)
			}
		}
	}
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
func githubConnectionOf(q *Request, owner string) (name string, c *apiConnection, keyed bool, err error) {
	serves := func(n string) (*apiConnection, bool, error) {
		c, err := apiConnectionMaybe(q, "connection:"+n)
		if err != nil || c == nil || gatewayURL() == "" || c.Intake != "all" {
			return c, false, err
		}
		active, err := q.db().ConnHasActiveKey(q.ctx(), n)
		return c, active, err
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
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		connection, _, keyed, err = githubConnectionOf(q, args["owner"])
		return
	}); err != nil {
		return nil, false, fmt.Errorf("github connection of %s: %w", args["owner"], err)
	}
	path := gatewayProviders["github"].Tools[tool].Path
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
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		connection, _, keyed, err = githubConnectionOf(q, args["owner"])
		return
	}); err != nil {
		return nil, fmt.Errorf("github connection of %s: %w", args["owner"], err)
	}
	if !keyed {
		return nil, fmt.Errorf("no github key on connection %s", connection)
	}
	path := gatewayProviders["github"].Tools[tool].Path
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

// readToks: one minted token per connection and project for the coordinator's own reads (GitHub screens, the Bugsink
// resolution pass), reused for readTokenTTL and then revoked. Minting
// and revoking per call (connCallFor) took about 12 s a read on PDS18 (two write transactions, a snapshot rewrite and a
// spool ingest); the delivery history makes hundreds. The calls become tool_runs when the token is revoked, marked
// systemActor coordinator: Fleet's own activity, kept under the project whose grant allowed the read but left out of
// its analytics and execution lists by default (docs/designs/fleet-analytics.md, System activity).
type readToken struct {
	token, exec string
	until       time.Time // its end: no new read takes it after
	users       int       // reads holding it (readTokenFor to its release)
}

const readTokenTTL = 5 * time.Minute

// readTokenGrace: how long past its end a read still holding a token keeps it; then it is revoked all the same.
const readTokenGrace = 5 * time.Second

var (
	readTokMu    sync.Mutex                // guards readToks, readTokEnds and every readToken's fields, never held across a mint
	readToks     = map[string]*readToken{} // the current token of a connection and project
	readTokLocks keyedLock                 // per connection and project, held across its mint
	// readTokEnds: every minted read token not yet revoked, by its execution (the current ones and those a newer token
	// replaced while a read still held them).
	readTokEnds = map[string]*readToken{}
	// readTokWake runs the read-tokens pass at once: a token's last read released it after its end.
	readTokWake = make(chan struct{}, 1)
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		// The pass is the backstop for a token whose end comes with no read holding it, or whose read outlasts its
		// grace; the release of the last read of an ended token wakes it, so that one is revoked when its read ends.
		everyWake(ctx, "read-tokens", 10*time.Second, readTokWake, false, func(ctx context.Context, _ bool) error { return readTokensEnd(ctx, a) })
	})
}

// readTokensEnd revokes the read tokens past their end that no read holds, and those past their grace, and ingests
// their calls.
func readTokensEnd(ctx context.Context, a *App) error {
	readTokMu.Lock()
	due := []string{}
	now := time.Now()
	for exec, t := range readTokEnds {
		if !now.Before(t.until) && (t.users == 0 || !now.Before(t.until.Add(readTokenGrace))) {
			due = append(due, exec)
			delete(readTokEnds, exec)
		}
	}
	for key, t := range readToks {
		if readTokEnds[t.exec] == nil {
			delete(readToks, key)
		}
	}
	readTokMu.Unlock()
	if len(due) == 0 {
		return nil
	}
	for _, exec := range due {
		if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error { return revokeTokens(q, exec) }); err != nil {
			log.Printf("revoke %s: %v", exec, err)
		}
	}
	_, err := connIngestPass(ctx, a)
	return err
}

// readTokenFor: the read token of connection for project and its release, which the caller calls once its gateway
// calls are done. A token ended (readTokenTTL) and released by its last read is revoked then (readTokWake).
func readTokenFor(ctx context.Context, a *App, connection, project string) (string, func(), error) {
	if !connName.MatchString(connection) {
		return "", nil, fmt.Errorf("%s: connection-unavailable", connection)
	}
	key := connection + " " + project
	unlock := readTokLocks.lock(key) // concurrent reads of one key share its mint; other keys never wait
	defer unlock()
	readTokMu.Lock()
	if t := readToks[key]; t != nil && time.Now().Before(t.until) {
		t.users++
		readTokMu.Unlock()
		return t.token, readTokRelease(t), nil
	} else if t != nil && t.users == 0 {
		wakeReadTokens() // ended and held by no read: revoked now, not at the next pass
	}
	readTokMu.Unlock()
	exec := connection + "-read:" + newID()
	var token string
	if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		system := "coordinator"
		m, err := mintGrantFor(q, mintRun{ExecutionID: &exec, ProjectID: &project, Step: true, SystemActor: &system}, []string{connection})
		token = deref(m.Token)
		return err
	}); err != nil {
		return "", nil, fmt.Errorf("token for %s: %w", connection, err)
	}
	if token == "" {
		return "", nil, fmt.Errorf("%s: %w (not granted to %s, or intake closed)", connection, errConnUnavailable, project)
	}
	// Revoked at its end whatever happens next (its last read's release, else the read-tokens pass; a restart revokes it
	// too: revokeSteps); its calls are ingested then. The gateway must see the token before the call; a failed snapshot
	// revokes it at once.
	// ponytail: an access or intake change reaches a cached token within readTokenTTL plus readTokenGrace, not at the
	// next read.
	t := &readToken{token: token, exec: exec, until: time.Now().Add(readTokenTTL), users: 1}
	readTokMu.Lock()
	readTokEnds[exec] = t
	readTokMu.Unlock()
	if err := writeSnapshot(context.WithoutCancel(ctx), a); err != nil {
		readTokMu.Lock()
		t.until, t.users = time.Time{}, 0
		readTokMu.Unlock()
		if err := readTokensEnd(context.WithoutCancel(ctx), a); err != nil {
			log.Printf("ingest %s: %v", exec, err)
		}
		return "", nil, err
	}
	readTokMu.Lock()
	readToks[key] = t
	readTokMu.Unlock()
	return token, readTokRelease(t), nil
}

// readTokRelease: the release of one read of t (once); the last read of an ended token wakes the pass that revokes it.
func readTokRelease(t *readToken) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			readTokMu.Lock()
			t.users--
			ended := t.users == 0 && !time.Now().Before(t.until)
			readTokMu.Unlock()
			if ended {
				wakeReadTokens()
			}
		})
	}
}

func wakeReadTokens() {
	select {
	case readTokWake <- struct{}{}:
	default:
	}
}

// githubGateway: one call through the gateway with the connection's shared read token, recorded as a tool_run.
func githubGateway(ctx context.Context, a *App, connection, project, tool string, args map[string]string) ([]byte, error) {
	if gatewayURL() == "" {
		return nil, fmt.Errorf("github %s: connection-unavailable", tool)
	}
	token, release, err := readTokenFor(ctx, a, connection, project)
	if err != nil {
		return nil, err
	}
	defer release()
	line := gatewayCall(ctx, token, connection, tool, args, gatewayProviders["github"].Tools[tool].Idempotent)
	if line.Outcome != contract.CallOutcomeOk {
		return nil, fmt.Errorf("github %s: %s %d", tool, deref(line.ErrorCode), line.Status)
	}
	return line.Output, nil
}

// gitlabConnectionOf finds an active gateway API connection for a GitLab host/owner (gitlab-<host>, gitlab-<owner>, or gitlab).
func gitlabConnectionOf(q *Request, host, owner, repoConn string) (name string, c *apiConnection, keyed bool, err error) {
	serves := func(n string) (*apiConnection, bool, error) {
		c, err := apiConnectionMaybe(q, "connection:"+n)
		if err != nil || c == nil || c.Provider != "gitlab" || gatewayURL() == "" || c.Intake != "all" {
			return c, false, err
		}
		active, err := q.db().ConnHasActiveKey(q.ctx(), n)
		return c, active, err
	}
	var candidates []string
	if repoConn != "" {
		candidates = append(candidates, repoConn)
	}
	if host != "" {
		h := strings.NewReplacer(".", "-", ":", "-").Replace(strings.ToLower(host))
		candidates = append(candidates, "gitlab-"+h)
	}
	if owner != "" {
		o := strings.NewReplacer("/", "-", ".", "-").Replace(strings.ToLower(owner))
		candidates = append(candidates, "gitlab-"+o)
	}
	candidates = append(candidates, "gitlab")
	for _, n := range candidates {
		if !connName.MatchString(n) {
			continue
		}
		if c, keyed, err = serves(n); err != nil || keyed {
			return n, c, keyed, err
		}
	}
	return "gitlab", nil, false, nil
}

func gitlabRead(ctx context.Context, a *App, project, host, owner, repoConn, tool string, args map[string]string) ([]byte, error) {
	var connection string
	var keyed bool
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		connection, _, keyed, err = gitlabConnectionOf(q, host, owner, repoConn)
		return
	}); err != nil {
		return nil, fmt.Errorf("gitlab connection of %s: %w", host, err)
	}
	if !keyed {
		return nil, fmt.Errorf("no gitlab key on connection %s", connection)
	}
	path := gatewayProviders["gitlab"].Tools[tool].Path
	if path == "" {
		return nil, fmt.Errorf("gitlab has no tool %s", tool)
	}
	pairs := []string{}
	for k, v := range args {
		pairs = append(pairs, "{"+k+"}", v)
	}
	path = strings.NewReplacer(pairs...).Replace(path)
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("gitlab %s: a path field is missing", tool)
	}
	return githubOnce("key "+connection+" "+project+" "+host+" "+path, 30*time.Second, func() ([]byte, error) {
		if gatewayURL() == "" {
			return nil, fmt.Errorf("gitlab %s: connection-unavailable", tool)
		}
		token, release, err := readTokenFor(ctx, a, connection, project)
		if err != nil {
			return nil, err
		}
		defer release()
		line := gatewayCall(ctx, token, connection, tool, args, gatewayProviders["gitlab"].Tools[tool].Idempotent)
		if line.Outcome != contract.CallOutcomeOk {
			return nil, fmt.Errorf("gitlab %s: %s %d", tool, deref(line.ErrorCode), line.Status)
		}
		return line.Output, nil
	}, func(err error) bool { return err == nil })
}
