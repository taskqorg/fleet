// Gateway snapshot publishing, secret directory sync, periodic reconciliation, and in-process gateway calls.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
			if err := revokeSteps(q); err != nil {
				return err
			}
			return connSync(q)
		}); err != nil {
			log.Printf("connections sync: %v", err)
		}
		if err := writeSnapshot(ctx, a); err != nil {
			log.Printf("snapshot: %v", err)
		}
		// Spool ingest and token revocation (FLEET_CONNECTION_TICK_MS: test seam, 0 turns it off); usage reconcile every 10 min.
		every(ctx, "connections", envMs("FLEET_CONNECTION_TICK_MS", 3000), func(ctx context.Context) error {
			err := connTickPass(ctx, a)
			if err != nil {
				log.Printf("connections tick: %v", err)
			}
			return err
		})
		every(ctx, "reconcile", envMs("FLEET_RECONCILE_MS", 600000), func(ctx context.Context) error { connReconcile(ctx, a); return nil })
	})
}

// Snapshot (§3). Built in its own read transaction, so it shows only committed rows.
func connSnapshot(q *Request) (contract.GatewaySnapshot, error) {
	snap := contract.GatewaySnapshot{Schema: 2, GeneratedMs: nowMs(), Providers: snapshotProviders(), Connections: map[string]contract.GatewayConnection{},
		Tokens: []contract.GatewayRunToken{}, UsageTokenSha256: sha256hex([]byte(usageToken))}
	blocked, err := blockedConnections(q)
	if err != nil {
		return snap, err
	}
	connections, err := apiConnectionAll(q)
	if err != nil {
		return snap, err
	}
	for _, c := range connections {
		ks, err := connKeys(q, c.Name)
		if err != nil {
			return snap, err
		}
		keys := []contract.GatewayKey{}
		for _, k := range ks {
			if k.State == "retired" {
				continue
			}
			e := contract.GatewayKey{Id: strings.TrimPrefix(k.ID, "key:"), State: k.State}
			if k.EnabledAt != nil {
				ms := int64(*k.EnabledAt)
				e.EnabledAt = &ms
			}
			if k.Reconcile != nil && k.Reconcile.Recorded != nil && *k.Reconcile.Recorded >= 0 {
				u := int64(*k.Reconcile.Recorded)
				e.Usage = &u
			}
			keys = append(keys, e)
		}
		bl := []string{}
		if blocked[c.Name] {
			bl = append(bl, "connection")
		}
		tools := c.Tools
		if tools == nil {
			tools = []string{}
		}
		if c.Provider == telegramConn {
			// The Telegram channel's own connection offers every tool of this build: a release that adds one (rich
			// messages, the command menu) must not wait for an owner's grant, or the chat's outbox stops behind it.
			tools = toolNames(telegramConn)
		}
		gc := contract.GatewayConnection{Provider: c.Provider, Tools: tools, Intake: c.Intake, Keys: keys, Blocked: bl}
		// A value that does not decode leaves this connection at its defaults; it never blocks every other one.
		if limits, err := c.limits(); err != nil {
			log.Printf("connection %s: limits not published: %v", c.Name, err)
		} else if limits != (contract.GatewayLimits{}) {
			gc.Limits = &limits
		}
		if policy, err := c.keyPolicy(); err != nil {
			log.Printf("connection %s: keyPolicy not published: %v", c.Name, err)
		} else if !keyPolicyEmpty(policy) {
			gc.KeyPolicy = &policy
		}
		if gc.Provider == "youtrack" {
			// Keep issue_get URLs for connections and existing tokens without the replacement download tool.
			const legacyProvider = "youtrack-legacy-attachments"
			if _, ok := snap.Providers[legacyProvider]; !ok {
				p := snap.Providers[gc.Provider]
				p.Tools = maps.Clone(p.Tools)
				issue := p.Tools["issue_get"]
				issue.Path = strings.Replace(issue.Path, "attachments(id,name,size,mimeType,comment(id))", "attachments(name,url,size)", 1)
				p.Tools["issue_get"] = issue
				snap.Providers = maps.Clone(snap.Providers)
				snap.Providers[legacyProvider] = p
			}
			if !slices.Contains(gc.Tools, "attachment_get") {
				gc.Provider = legacyProvider
			}
		}
		snap.Connections[c.Name] = gc
	}
	toks, err := runTokensAll(q)
	if err != nil {
		return snap, err
	}
	for _, t := range toks {
		if t.revoked() {
			continue
		}
		conns, downloads := t.Connections, t.FileDownloads
		if conns == nil {
			conns = []string{}
		}
		if downloads == nil {
			downloads = []string{}
		}
		snap.Tokens = append(snap.Tokens, contract.GatewayRunToken{Sha256: t.Sha256, ExecutionId: deref(t.ExecutionID), DirectedBy: deref(t.DirectedBy),
			ProjectId: deref(t.ProjectID), ExperimentId: deref(t.ExperimentID), Connections: conns, FileDownloads: downloads})
	}
	return snap, nil
}

var snapshotMu sync.Mutex

func writeSnapshot(ctx context.Context, a *App) error {
	if gatewayDir() == "" {
		return nil
	}
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	var s contract.GatewaySnapshot
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) { s, err = connSnapshot(q); return }); err != nil {
		return err
	}
	if err := os.MkdirAll(gatewayDir(), 0o750); err != nil {
		return err
	}
	file := filepath.Join(gatewayDir(), "snapshot.json")
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, jsonBytes(s), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// publish: the snapshot is rewritten after the caller's transaction commits (coalesced). A rewrite scheduled before the
// commit would read the old rows, and the tick does not republish on every change.
func publish(q *Request) {
	if q.tx == nil {
		schedulePublish()
		return
	}
	afterCommit(q, schedulePublish)
}

// schedulePublish rewrites the snapshot once per burst of calls (background.go); before the app runs there is none to
// write.
func schedulePublish() {
	if a := runningApp; a != nil {
		publishSnapshot(a)
	}
}

var publishSnapshot = coalesced("snapshot", writeSnapshot)

// connSync: key files without a row are registered; a directory without a connection becomes one (owner-held, every project).
func connSync(q *Request) error {
	if secretDir() == "" {
		return nil
	}
	root := filepath.Join(secretDir(), "connections")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	owner := ownerEmailOf(q.app)
	for _, ent := range entries {
		name := ent.Name()
		if !connName.MatchString(name) || !ent.IsDir() {
			continue
		}
		c, err := apiConnectionMaybe(q, "connection:"+name)
		if err != nil {
			return err
		}
		if c != nil && c.Holder != owner {
			log.Printf("secret directory %s: connection is held by %v, files not registered", name, c.Holder)
			continue
		}
		if c == nil {
			if _, known := gatewayProviders[name]; !known {
				continue
			}
			nc := apiConnection{ID: "connection:" + name, Name: name, Provider: name, Holder: owner, Access: connAccess{Projects: []string{"*"}}.stored(),
				Intake: "all", Tools: toolNames(name), Comment: "", CreatedAt: jsonBytes(nowMs())}
			if err := apiConnectionPut(q, nc); err != nil {
				return err
			}
		}
		files, _ := os.ReadDir(filepath.Join(root, name))
		for _, f := range files {
			if m := stagedKey.FindStringSubmatch(f.Name()); m != nil {
				// An add a crash interrupted: its bytes count only when the row committed.
				k, err := connKeyMaybe(q, "key:"+m[1])
				if err != nil {
					return err
				}
				staged := filepath.Join(root, name, f.Name())
				if k != nil && k.State != "retired" && k.ConnectionID == name {
					os.Rename(staged, filepath.Join(root, name, m[1]))
				} else {
					os.Remove(staged)
				}
				continue
			}
			if !keyFile.MatchString(f.Name()) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, name, f.Name()))
			if err != nil {
				log.Printf("secret file %s %s: %v", name, f.Name(), err)
				continue
			}
			if fingerprint(strings.TrimSpace(string(b))) != f.Name() {
				continue // a renamed or edited file never registers under a wrong fingerprint
			}
			if k, err := connKeyMaybe(q, "key:"+f.Name()); err != nil {
				return err
			} else if k != nil && k.State == "retired" {
				os.Remove(filepath.Join(root, name, f.Name())) // a retirement committed before a crash removed the value
			} else if k == nil {
				by := "import"
				if err := connKeyPut(q, connKey{ID: "key:" + f.Name(), ConnectionID: name, Fingerprint: f.Name(), State: "active", AddedAt: float64(nowMs()), AddedBy: &by}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var (
	lastBlockedMu sync.Mutex
	lastBlocked   string
)

// One pass: ingest without GLOBAL, republish when the blocked set changed, revoke tokens of ended or unknown runs.
func connTickPass(ctx context.Context, a *App) error {
	if _, err := connIngestPass(ctx, a); err != nil {
		return err
	}
	var blockedJSON string
	var toRevoke []string
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		b, err := blockedConnections(q)
		if err != nil {
			return err
		}
		names := []string{}
		for k, v := range b {
			if v {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		blockedJSON = string(jsonBytes(names))
		open, err := q.db().OpenRunTokenExecutions(q.ctx())
		if err != nil {
			return err
		}
		for _, id := range open {
			state, err := q.db().ConnRunState(q.ctx(), id)
			if err != nil {
				return err
			}
			if state == "" || executionEnds(state) {
				toRevoke = append(toRevoke, id)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if len(toRevoke) > 0 {
		if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
			for _, id := range toRevoke {
				if err := revokeTokens(q, id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	lastBlockedMu.Lock()
	changed := blockedJSON != lastBlocked
	if changed {
		lastBlocked = blockedJSON
	}
	lastBlockedMu.Unlock()
	if changed {
		schedulePublish()
	}
	return nil
}

// usageReport: the gateway's per-key provider usage (GET /v1/usage).
type usageReport struct {
	At   json.RawMessage `json:"at"`
	Keys []struct {
		Key     string          `json:"key"`
		Usage   json.RawMessage `json:"usage"`
		Unknown json.RawMessage `json:"unknown"`
	} `json:"keys"`
}

// Reconcile (§2): the provider's per-key usage since the first observation against the credits recorded through the gateway.
func connReconcile(ctx context.Context, a *App) {
	if gatewayURL() == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, "GET", gatewayURL()+"/v1/usage", nil)
	if err != nil {
		connReconcileMarkUnknown(ctx, a, "usage request: invalid gateway URL")
		return
	}
	req.Header.Set("Authorization", "Bearer "+usageToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		reason := fmt.Sprintf("usage request: %v", err)
		if usageToken != "" {
			reason = strings.ReplaceAll(reason, usageToken, "[redacted]")
		}
		connReconcileMarkUnknown(ctx, a, reason)
		return
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		connReconcileMarkUnknown(ctx, a, fmt.Sprintf("usage HTTP %d", res.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil {
		connReconcileMarkUnknown(ctx, a, "usage reply read error")
		return
	}
	if len(body) > 1<<20 {
		connReconcileMarkUnknown(ctx, a, "usage reply too large")
		return
	}
	var report usageReport
	if err := json.Unmarshal(body, &report); err != nil || report.Keys == nil {
		connReconcileMarkUnknown(ctx, a, "usage reply invalid JSON")
		return
	}
	if !isNull(report.At) {
		if _, ok := jsonAs[float64](report.At); !ok {
			connReconcileMarkUnknown(ctx, a, "usage reply invalid JSON")
			return
		}
	}
	at := orNull(report.At)
	if isNull(at) {
		at = jsonBytes(nowMs())
	}
	err = a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		for _, row := range report.Keys {
			k, err := connKeyMaybe(q, "key:"+row.Key)
			if err != nil {
				return err
			}
			if k == nil {
				continue
			}
			if usage, ok := jsonAs[float64](row.Usage); !ok {
				rc := keyReconcile{}
				if k.Reconcile != nil {
					rc = *k.Reconcile
				}
				unknown := "no usage reported"
				if s, ok := jsonAs[string](row.Unknown); ok {
					unknown = s
				} else if !isNull(row.Unknown) {
					unknown = string(row.Unknown)
				}
				rc.At, rc.Unknown = at, &unknown
				k.Reconcile = &rc
			} else {
				if k.UsageBaseline == nil {
					k.UsageBaseline = &usageBaseline{Usage: usage, At: at}
				}
				since, _ := jsonAs[float64](k.UsageBaseline.At)
				recorded, err := q.db().ConnKeyRecordedSince(q.ctx(), db.ConnKeyRecordedSinceParams{Fingerprint: k.Fingerprint, Since: int64(since)})
				if err != nil {
					return err
				}
				delta := usage - k.UsageBaseline.Usage
				difference := delta - recorded
				k.Reconcile = &keyReconcile{At: at, ProviderDelta: &delta, Recorded: &recorded, Difference: &difference}
			}
			if err := connKeyPut(q, *k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("usage reconcile: %v", err)
	}
}

// connReconcileMarkUnknown records a top-level /v1/usage failure on each reconcilable non-retired key while preserving
// any prior measurement fields.
func connReconcileMarkUnknown(ctx context.Context, a *App, reason string) {
	if ctx.Err() != nil {
		return
	}
	at := jsonBytes(nowMs())
	err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		connections, err := apiConnectionAll(q)
		if err != nil {
			return err
		}
		reconcilable := make(map[string]bool, len(connections))
		for _, c := range connections {
			p := gatewayProviders[c.Provider]
			if p.Usage != nil && p.Usage.Path != "" && p.Usage.Field != "" {
				reconcilable[c.Name] = true
			}
		}
		keys, err := connKeysList(q, nil)
		if err != nil {
			return err
		}
		for _, k := range keys {
			if k.State == "retired" || (!reconcilable[k.ConnectionID] && k.Reconcile == nil) {
				continue
			}
			rc := keyReconcile{}
			if k.Reconcile != nil {
				rc = *k.Reconcile
			}
			r := reason
			rc.At, rc.Unknown = at, &r
			k.Reconcile = &rc
			if err := connKeyPut(q, k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("usage reconcile: %v", err)
	}
}

// gatewayCall: one in-process call through fleet-gateway (contract.GatewayCall). idempotent is the catalogue tool's
// flag: an edge 5xx (no gateway call id) on a tool that may not be repeated reads unknown.
func gatewayCall(ctx context.Context, token, connection, tool string, args any, idempotent bool) contract.CallLine {
	t := contract.GatewayTarget{URL: gatewayURL(), Token: token, Idempotent: idempotent}
	if strings.HasPrefix(t.URL, "https://") {
		t.Header = cfAccessHeaders()
	}
	return contract.GatewayCall(ctx, t, connection, tool, jsonBytes(args))
}

func cfAccessHeaders() map[string]string {
	p := os.Getenv("FLEET_CF_ACCESS_FILE")
	if p == "" {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, ".config/fleet/cf-access-machine.env")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	vals := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(l, "export "))
		if k, v, ok := strings.Cut(l, "="); ok {
			vals[k] = strings.Trim(v, `'"`)
		}
	}
	out := map[string]string{}
	if v, ok := vals["CF_ACCESS_CLIENT_ID"]; ok {
		out["CF-Access-Client-Id"] = v
	}
	if v, ok := vals["CF_ACCESS_CLIENT_SECRET"]; ok {
		out["CF-Access-Client-Secret"] = v
	}
	return out
}
