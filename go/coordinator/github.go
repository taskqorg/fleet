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
	err  error
}

// githubKeyed: the gateway can serve github: it is configured, the connection takes calls and holds an active key.
func githubKeyed(ctx context.Context, a *App) bool {
	if gatewayURL() == "" {
		return false
	}
	var keyed bool
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		c, err := maybe(q, "connection:github", "api-connection")
		if err != nil || c == nil || c["intake"] != "all" {
			return err
		}
		ks, err := connKeys(q, "github")
		for _, k := range ks {
			keyed = keyed || k["state"] == "active"
		}
		return err
	}); err != nil {
		log.Printf("github keyed: %v", err)
	}
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
	// A keyed reply is served only to the project its token was minted for; the public reply is the same for all.
	cacheKey := "public " + path
	if keyed {
		cacheKey = "key " + project + " " + path
	}
	githubMu.Lock()
	defer githubMu.Unlock() // ponytail: one read at a time (concurrent misses never both reach GitHub); per-path locks if reads queue
	c, ok := githubCache[cacheKey]
	if ok && time.Since(c.at) < ttl {
		return c.body, keyed, c.err
	}
	if keyed {
		body, err = githubGateway(ctx, a, project, tool, args)
		if err != nil && strings.EqualFold(args["owner"]+"/"+args["repo"], githubPublicRepo) {
			// A refused key (expired, wrong lifetime for the org) must not blank the public repository.
			log.Printf("github %s keyed: %v; read public", tool, err)
			keyed = false
			if pc, ok := githubCache["public "+path]; ok && time.Since(pc.at) < 5*time.Minute {
				return pc.body, false, pc.err
			}
			body, err = githubPublic(ctx, path)
			githubCache["public "+path] = githubCached{time.Now(), body, err}
			return body, false, err
		}
	} else {
		body, err = githubPublic(ctx, path)
	}
	if err == nil || !keyed {
		// A failed public read is kept too: a rate-limited address must not ask again before the cache expires.
		githubCache[cacheKey] = githubCached{time.Now(), body, err}
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
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20+1))
	if err == nil && len(b) > 16<<20 {
		err = fmt.Errorf("GitHub reply over 16 MiB on %s", strings.SplitN(path, "?", 2)[0])
	}
	if err == nil && res.StatusCode != 200 {
		err = fmt.Errorf("GitHub %d on %s", res.StatusCode, strings.SplitN(path, "?", 2)[0])
	}
	return b, err
}

// githubGateway: one call with a token minted for it and revoked after (connCallFor), recorded as a tool_run.
func githubGateway(ctx context.Context, a *App, project, tool string, args map[string]string) ([]byte, error) {
	line := connCallFor(ctx, a, doc{"projectId": project}, "github", "github-read:"+newID(), tool, args)
	if line.Outcome != contract.CallOutcomeOk {
		return nil, fmt.Errorf("github %s: %s %d", tool, deref(line.ErrorCode), line.Status)
	}
	return line.Output, nil
}
