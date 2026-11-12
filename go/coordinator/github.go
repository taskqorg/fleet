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

var (
	githubMu    sync.Mutex
	githubCache = map[string]githubCached{}
)

type githubCached struct {
	at   time.Time
	body []byte
}

// githubKeyed: the github connection exists (its key is the owner's to add; a call without one fails with no-usable-key).
func githubKeyed(ctx context.Context, a *App) bool {
	var keyed bool
	runTx(ctx, a, nil, false, "", func(q *Request) error {
		c, err := maybe(q, "connection:github", "api-connection")
		keyed = c != nil
		return err
	})
	return keyed
}

// githubRead: the reply body of tool with args (the path fields, e.g. owner and repo) for project's access check. A
// reply younger than 30 s (keyed) or 5 min (public) is served from the cache. keyed says which way it went.
func githubRead(ctx context.Context, a *App, project, tool string, args map[string]string) (body []byte, keyed bool, err error) {
	keyed = githubKeyed(ctx, a)
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
	cacheKey := fmt.Sprint(keyed, " ", project, " ", path) // a keyed reply is served only to the project its token was minted for
	githubMu.Lock()
	c, ok := githubCache[cacheKey]
	githubMu.Unlock()
	if ok && time.Since(c.at) < ttl {
		return c.body, keyed, nil
	}
	if keyed {
		body, err = githubGateway(ctx, a, project, tool, args)
	} else {
		body, err = githubPublic(ctx, path)
	}
	if err == nil {
		githubMu.Lock()
		githubCache[cacheKey] = githubCached{time.Now(), body}
		githubMu.Unlock()
	}
	return body, keyed, err
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
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err == nil && res.StatusCode != 200 {
		err = fmt.Errorf("GitHub %d on %s", res.StatusCode, strings.SplitN(path, "?", 2)[0])
	}
	return b, err
}

func githubGateway(ctx context.Context, a *App, project, tool string, args map[string]string) ([]byte, error) {
	id := "github-read:" + newID()
	var token string
	if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		m, err := mint(q, doc{"executionId": id, "projectId": project, "personal": "github-read", "step": true}, []string{"github"})
		token, _ = m["token"].(string)
		return err
	}); err != nil || token == "" {
		return nil, fmt.Errorf("github connection refused for project %s", project)
	}
	writeSnapshot(ctx, a) // the gateway must see the token before the call
	line := gatewayCall(ctx, token, "github", tool, args)
	if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		if err := revokeTokens(q, id); err != nil {
			return err
		}
		_, err := connIngest(q)
		return err
	}); err != nil {
		log.Printf("github read revoke %s: %v", id, err)
	}
	if line.Outcome != contract.CallOutcomeOk {
		return nil, fmt.Errorf("github %s: %s %d", tool, deref(line.ErrorCode), line.Status)
	}
	return line.Output, nil
}
