// API key connections (docs/designs/fleet-connections.md). Key values live only in
// <FLEET_SECRET_DIR>/connections/<connection>/<fingerprint> (0600); rows hold the fingerprint and state. Every change
// republishes <FLEET_GATEWAY_DIR>/snapshot.json (no key values) once the change committed.
package main

import (
	"fleet/coordinator/db"

	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
)

var (
	connName       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	keyValue       = regexp.MustCompile(`^\S+$`) // with 8..4096 bytes, checked beside it
	keyFile        = regexp.MustCompile(`^[0-9a-f]{12}$`)
	stagedKey      = regexp.MustCompile(`^\.([0-9a-f]{12})\.add$`)
	spoolFile      = regexp.MustCompile(`^\d{4}-\d\d-\d\d\.jsonl$`)
	usageToken     = randomToken()
	processStarted = nowMs()
)

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func fingerprint(v string) string { return sha256hex([]byte(v))[:12] }

func secretDir() string  { return os.Getenv("FLEET_SECRET_DIR") }
func gatewayDir() string { return os.Getenv("FLEET_GATEWAY_DIR") }
func gatewayURL() string { return strings.TrimRight(os.Getenv("FLEET_GATEWAY_URL"), "/") }

func connDir(name string) (string, error) {
	if secretDir() == "" {
		return "", fail(409, "FLEET_SECRET_DIR is not set on the coordinator")
	}
	return filepath.Join(secretDir(), "connections", name), nil
}

// apiConnection: a connection as all/get/put serve it (api_connection: the columns and the payload fields).
type apiConnection struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Provider  string          `json:"provider"`
	Holder    string          `json:"holder,omitempty"`
	ProjectID *string         `json:"projectId,omitempty"`
	Access    connAccess      `json:"access"`
	Intake    string          `json:"intake"`
	Tools     []string        `json:"tools"`
	Comment   string          `json:"comment"`
	CreatedAt json.RawMessage `json:"createdAt,omitempty"`
	UpdatedAt *int64          `json:"updatedAt,omitempty"`
	Limits    json.RawMessage `json:"limits,omitempty"`
}

// limits: the stored file tool limits (§9); none is the zero value.
func (c apiConnection) limits() (contract.GatewayLimits, error) {
	var l contract.GatewayLimits
	if isNull(c.Limits) {
		return l, nil
	}
	return l, json.Unmarshal(c.Limits, &l)
}

// connAccess: who a connection is granted to; all three empty means its holder only.
type connAccess struct {
	Experiments []string `json:"experiments"`
	People      []string `json:"people"`
	Projects    []string `json:"projects"`
}

// stored: the lists as written (an absent list is empty).
func (a connAccess) stored() connAccess {
	for _, l := range []*[]string{&a.Experiments, &a.People, &a.Projects} {
		if *l == nil {
			*l = []string{}
		}
	}
	return a
}

// connKey: a connection key as all/get/put serve it (connection_key; its state moves through key_states.go).
type connKey struct {
	ID             string         `json:"id"`
	ConnectionID   string         `json:"connectionId"`
	Fingerprint    string         `json:"fingerprint"`
	State          string         `json:"state"`
	ProjectID      *string        `json:"projectId,omitempty"`
	AddedAt        float64        `json:"addedAt"`
	AddedBy        *string        `json:"addedBy,omitempty"`
	UpdatedAt      *int64         `json:"updatedAt,omitempty"`
	EnabledAt      *float64       `json:"enabledAt,omitempty"`
	DisabledReason *string        `json:"disabledReason,omitempty"`
	DisabledAt     *int64         `json:"disabledAt,omitempty"`
	Problem        *keyProblem    `json:"problem,omitempty"`
	Reconcile      *keyReconcile  `json:"reconcile,omitempty"`
	UsageBaseline  *usageBaseline `json:"usageBaseline,omitempty"`
}

// keyProblem: the provider's last complaint about a key that did not disable it.
type keyProblem struct {
	Status  int64  `json:"status"`
	Message string `json:"message"`
	At      int64  `json:"at"`
}

// keyReconcile: the last usage reconcile of a key (§2).
type keyReconcile struct {
	At            json.RawMessage `json:"at,omitempty"`
	Unknown       *string         `json:"unknown,omitempty"`
	ProviderDelta *float64        `json:"providerDelta,omitempty"`
	Recorded      *float64        `json:"recorded,omitempty"`
	Difference    *float64        `json:"difference,omitempty"`
}

// usageBaseline: the provider's usage of a key when Fleet first observed it.
type usageBaseline struct {
	Usage float64         `json:"usage"`
	At    json.RawMessage `json:"at"`
}

// runToken: a run token as all/get/put serve it (run_token).
type runToken struct {
	ID                      string          `json:"id"`
	Sha256                  string          `json:"sha256"`
	ExecutionID             *string         `json:"executionId"`
	DirectedBy              *string         `json:"directedBy"`
	ProjectID               *string         `json:"projectId"`
	ExperimentID            *string         `json:"experimentId"`
	PipelineRunID           *string         `json:"pipelineRunId"`
	Connections             []string        `json:"connections"`
	CreatedAt               json.RawMessage `json:"createdAt,omitempty"`
	FileDownloads           []string        `json:"fileDownloads,omitempty"`
	Step                    bool            `json:"step,omitempty"`
	Personal                *string         `json:"personal,omitempty"`
	SystemActor             *string         `json:"systemActor,omitempty"`
	RevokedAt               json.RawMessage `json:"revokedAt,omitempty"`
	DirectorAccessRevokedAt json.RawMessage `json:"directorAccessRevokedAt,omitempty"`
}

func (t runToken) revoked() bool { return !isNull(t.RevokedAt) }

// connNamed: the connection called name; 400 for a name that is not one, 404 for an unknown one.
func connNamed(q *Request, name string) (apiConnection, error) {
	if !connName.MatchString(name) {
		return apiConnection{}, fail(400, "Expected connection name")
	}
	c, err := kindMaybe[apiConnection](q, "connection:"+name, "api-connection")
	if err == nil && c == nil {
		err = fail(404, "Unknown API key "+name)
	}
	if err != nil {
		return apiConnection{}, err
	}
	return *c, nil
}

// connByName: connNamed for the pipeline files, which read the connection as a map (provider, tools).
func connByName(q *Request, name any) (doc, error) {
	n, _ := name.(string)
	c, err := connNamed(q, n)
	if err != nil {
		return nil, err
	}
	return docOf(c), nil
}

func connChange(q *Request, c apiConnection, field string, previous, value any) error {
	return audit(q, "connection:"+c.Name, field, previous, value, auditExtra{Holder: c.Holder})
}

type connOnly struct {
	ConnectionID string `json:"connectionId,omitempty"`
}

func connKeys(q *Request, name string) ([]connKey, error) {
	return kindAll[connKey](q, "connection-key", connOnly{ConnectionID: name})
}

// Snapshot (§3). Built in its own read transaction, so it shows only committed rows.
func connSnapshot(q *Request) (contract.GatewaySnapshot, error) {
	snap := contract.GatewaySnapshot{Schema: 2, GeneratedMs: nowMs(), Providers: snapshotProviders(), Connections: map[string]contract.GatewayConnection{},
		Tokens: []contract.GatewayRunToken{}, UsageTokenSha256: sha256hex([]byte(usageToken))}
	blocked, err := blockedConnections(q)
	if err != nil {
		return snap, err
	}
	connections, err := kindAll[apiConnection](q, "api-connection", nil)
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
		gc := contract.GatewayConnection{Provider: c.Provider, Tools: tools, Intake: c.Intake, Keys: keys, Blocked: bl}
		// A value that does not decode leaves this connection at its defaults; it never blocks every other one.
		if limits, err := c.limits(); err != nil {
			log.Printf("connection %s: limits not published: %v", c.Name, err)
		} else if limits != (contract.GatewayLimits{}) {
			gc.Limits = &limits
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
	toks, err := kindAll[runToken](q, "run-token", nil)
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
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) { s, err = connSnapshot(q); return }); err != nil {
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
		c, err := kindMaybe[apiConnection](q, "connection:"+name, "api-connection")
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
			if err := kindPut(q, "api-connection", nc); err != nil {
				return err
			}
		}
		files, _ := os.ReadDir(filepath.Join(root, name))
		for _, f := range files {
			if m := stagedKey.FindStringSubmatch(f.Name()); m != nil {
				// An add a crash interrupted: its bytes count only when the row committed.
				k, err := kindMaybe[connKey](q, "key:"+m[1], "connection-key")
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
			if k, err := kindMaybe[connKey](q, "key:"+f.Name(), "connection-key"); err != nil {
				return err
			} else if k != nil && k.State == "retired" {
				os.Remove(filepath.Join(root, name, f.Name())) // a retirement committed before a crash removed the value
			} else if k == nil {
				by := "import"
				if err := kindPut(q, "connection-key", connKey{ID: "key:" + f.Name(), ConnectionID: name, Fingerprint: f.Name(), State: "active", AddedAt: float64(nowMs()), AddedBy: &by}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// toolNames: a provider's tools, sorted.
func toolNames(provider string) []string {
	return slices.Sorted(maps.Keys(gatewayProviders[provider].Tools))
}

// idList: the ids once each, in order; an id longer than 320 is refused.
func idList(l []string, field string) ([]string, error) {
	seen, out := map[string]bool{}, []string{}
	for _, s := range l {
		if len(s) > 320 {
			return nil, fail(400, field+": list of ids")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func init() {
	// Agent dispatch mints the run token for the profile's connections (dispatch.go).
	op("connections_list", func(q *Request, _ *contract.ConnectionsListRequest) (contract.ConnectionsListReply, error) {
		out, err := connectionsList(q)
		return replyAs[contract.ConnectionsListReply](out, err)
	})
	op("connections_create", func(q *Request, r *contract.ConnectionsCreateRequest) (contract.ConnectionsCreateReply, error) {
		out, err := connectionsCreate(q, r)
		return replyAs[contract.ConnectionsCreateReply](out, err)
	})
	op("connections_key_add", func(q *Request, r *contract.ConnectionsKeyAddRequest) (contract.ConnectionsKeyAddReply, error) {
		out, err := connectionsKeyAdd(q, r)
		return replyAs[contract.ConnectionsKeyAddReply](out, err)
	})
	op("connections_key_request", func(q *Request, r *contract.ConnectionsKeyRequestRequest) (contract.ConnectionsKeyRequestReply, error) {
		out, err := connectionsKeyRequest(q, r)
		return replyAs[contract.ConnectionsKeyRequestReply](out, err)
	})
	op("connections_delete", connectionsDelete)
	op("connections_key_state", func(q *Request, r *contract.ConnectionsKeyStateRequest) (contract.ConnectionsKeyStateReply, error) {
		out, err := connectionsKeyState(q, r)
		return replyAs[contract.ConnectionsKeyStateReply](out, err)
	})
	op("connections_set_tools", func(q *Request, r *contract.ConnectionsSetToolsRequest) (contract.ConnectionsSetToolsReply, error) {
		out, err := connectionsSetTools(q, r)
		return replyAs[contract.ConnectionsSetToolsReply](out, err)
	})
	op("connections_set_access", func(q *Request, r *contract.ConnectionsSetAccessRequest) (contract.ConnectionsSetAccessReply, error) {
		out, err := connectionsSetAccess(q, r)
		return replyAs[contract.ConnectionsSetAccessReply](out, err)
	})
	op("connections_set_intake", func(q *Request, r *contract.ConnectionsSetIntakeRequest) (contract.ConnectionsSetIntakeReply, error) {
		out, err := connectionsSetIntake(q, r)
		return replyAs[contract.ConnectionsSetIntakeReply](out, err)
	})
	op("connections_set_limits", func(q *Request, r *contract.ConnectionsSetLimitsRequest) (contract.ConnectionsSetLimitsReply, error) {
		out, err := connectionsSetLimits(q, r)
		return replyAs[contract.ConnectionsSetLimitsReply](out, err)
	})
	op("connections_token_create", func(q *Request, r *contract.ConnectionsTokenCreateRequest) (contract.ConnectionsTokenCreateReply, error) {
		out, err := connectionsTokenCreate(q, r)
		return replyAs[contract.ConnectionsTokenCreateReply](out, err)
	})
	op("connections_token_list", func(q *Request, _ *contract.ConnectionsTokenListRequest) (contract.ConnectionsTokenListReply, error) {
		return connectionsTokenList(q)
	})
	op("connections_token_revoke", connectionsTokenRevoke)
}

// connectionView: a connection as connections_list shows it.
type connectionView struct {
	apiConnection
	Keys    []connKey         `json:"keys"`
	Spend   connSpend         `json:"spend"`
	History []json.RawMessage `json:"history"`
}

type connSpend struct {
	Basis string `json:"basis"`
}

func connectionsList(q *Request) (any, error) {
	keys, err := kindAll[connKey](q, "connection-key", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].AddedAt < keys[j].AddedAt })
	hist, err := q.db().AuditOfKind(q.ctx(), "connection")
	if err != nil {
		return nil, err
	}
	connections, err := kindAll[apiConnection](q, "api-connection", nil)
	if err != nil {
		return nil, err
	}
	out := []connectionView{}
	for _, c := range connections {
		v := connectionView{apiConnection: c, Keys: []connKey{}, Spend: connSpend{Basis: "unavailable"}, History: []json.RawMessage{}}
		for _, k := range keys {
			if k.ConnectionID == c.Name {
				v.Keys = append(v.Keys, k)
			}
		}
		for _, h := range hist {
			if h.Target == "connection:"+c.Name {
				v.History = append(v.History, json.RawMessage(h.Data))
			}
		}
		out = append(out, v)
	}
	return struct {
		Providers   map[string]json.RawMessage `json:"providers"`
		Connections []connectionView           `json:"connections"`
	}{providerCatalogue, out}, nil
}

func connectionsCreate(q *Request, r *contract.ConnectionsCreateRequest) (any, error) {
	name, provider := r.Name, r.Provider
	if _, known := gatewayProviders[provider]; !connName.MatchString(name) || !known {
		return nil, fail(400, "Expected name and a known provider")
	}
	if slices.Contains(contract.ExecutorMCPServers, name) {
		return nil, fail(400, "Connection name "+name+" is an executor MCP server name")
	}
	if old, err := kindMaybe[apiConnection](q, "connection:"+name, "api-connection"); err != nil {
		return nil, err
	} else if old != nil {
		return nil, fail(409, "API key name exists")
	}
	holder := directorOf(q)
	if r.Holder != nil {
		holder = strings.ToLower(*r.Holder)
	}
	if q.person != nil && !fullAccess(q.person) && holder != q.person.Email {
		return nil, fail(403, "Only the owner creates an API key for another person")
	}
	comment := deref(r.Comment)
	if len(comment) > 2000 {
		comment = comment[:2000]
	}
	c := apiConnection{ID: "connection:" + name, Name: name, Provider: provider, Holder: holder, Access: connAccess{}.stored(),
		Intake: "all", Tools: toolNames(provider), Comment: comment, CreatedAt: jsonBytes(nowMs())}
	if err := kindPut(q, "api-connection", c); err != nil {
		return nil, err
	}
	publish(q)
	return c, nil
}

// connectionsKeyAdd (owner): writes the value file and the row; answers with the fingerprint only.
func connectionsKeyAdd(q *Request, r *contract.ConnectionsKeyAddRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	value := r.Value
	if len(value) < 8 || len(value) > 4096 || !keyValue.MatchString(value) {
		return nil, fail(400, "Expected one key value without spaces")
	}
	qid, fp := deref(r.QuestionId), fingerprint(value)
	if qid != "" {
		// A retry of an add that already answered this request (its reply was lost) passes here and replays below.
		if err := keyRequestOf(q, qid, c.Name, keyAnswer(c.Name, fp, "active")); err != nil {
			return nil, err
		}
	}
	if old, err := kindMaybe[connKey](q, "key:"+fp, "connection-key"); err != nil {
		return nil, err
	} else if old != nil {
		if qid != "" && old.ConnectionID == c.Name && old.State == "active" { // Fleet already holds it: answer with it
			if _, err := answerQuestion(q, qid, keyAnswer(c.Name, fp, "active"), actor(q, "operator"), nil); err != nil {
				return nil, err
			}
			return *old, nil
		}
		return nil, fail(409, "Key already added")
	}
	dir, err := connDir(c.Name)
	if err != nil {
		return nil, err
	}
	by := actor(q, "operator")
	k := connKey{ID: "key:" + fp, ConnectionID: c.Name, Fingerprint: fp, State: "active", AddedAt: float64(nowMs()), AddedBy: &by}
	if err := kindPut(q, "connection-key", k); err != nil {
		return nil, err
	}
	// The value is staged before commit and takes its key name only after the row committed; a rolled-back add removes
	// it, and connSync settles a stage a crash left behind against the committed row.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	staged, final := filepath.Join(dir, "."+fp+".add"), filepath.Join(dir, fp)
	if err := os.WriteFile(staged, []byte(value+"\n"), 0o600); err != nil {
		return nil, err
	}
	os.Chmod(staged, 0o600)
	onEnd(q.tx, func(committed bool) {
		if !committed {
			os.Remove(staged)
		} else if err := os.Rename(staged, final); err != nil {
			log.Printf("key %s: %v", fp, err)
		}
	})
	if qid != "" { // the agent's question gets the fingerprint only, never the value
		if _, err := answerQuestion(q, qid, keyAnswer(c.Name, fp, "active"), actor(q, "operator"), nil); err != nil {
			return nil, err
		}
	}
	publish(q)
	return k, nil
}

// keyRequestReply: a new key request as the project's questions show it.
type keyRequestReply struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	RequestID string `json:"requestId"`
	Text      string `json:"text"`
	State     string `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	Actor     string `json:"actor"`
}

// connectionsKeyRequest: an agent (or a person) asks the owner for a key value (docs/designs/fleet-connections.md §1,
// key requests): a secret question the owner answers through connections_key_add with questionId. No argument carries a
// value (the request is closed: an extra key is 400).
func connectionsKeyRequest(q *Request, r *contract.ConnectionsKeyRequestRequest) (any, error) {
	if _, err := projectContext(q, r.ProjectId); err != nil {
		return nil, err
	}
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	rid, reason := r.RequestId, deref(r.Reason)
	if rid == "" || len(rid) > 200 || len([]rune(reason)) > 500 {
		return nil, fail(400, "Expected requestId and a reason of at most 500 characters")
	}
	text := fmt.Sprintf("Add a %s key for connection %s", c.Provider, c.Name)
	if reason != "" {
		text += ": " + reason
	}
	d, err := q.db().ConnKeyRequestByRequest(q.ctx(), db.ConnKeyRequestByRequestParams{ProjectID: r.ProjectId, RequestID: rid})
	if err == nil {
		if d.Text != text {
			return nil, fail(409, "Question identity conflict")
		}
		return json.RawMessage(d.Question), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	now := nowMs()
	v := keyRequestReply{ID: newID(), ProjectID: r.ProjectId, RequestID: rid, Text: text, State: "waiting", CreatedAt: now, Actor: actor(q, "operator")}
	asked := time.UnixMilli(now).UTC()
	ask := questionAsk{ID: v.ID, Kind: "secret", Text: text, ProjectID: r.ProjectId, AskedBy: &v.Actor, RequestID: &rid, AskedAt: &asked,
		Options: jsonBytes(struct {
			Connection string `json:"connection"`
		}{c.Name})}
	if strings.HasPrefix(q.agent, "execution:") { // a host agent: the question shows in its conversation
		eid := strings.TrimPrefix(q.agent, "execution:")
		sid, err := q.db().ConnExecutionSession(q.ctx(), eid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		ask.ExecutionID = &eid
		if err == nil && sid != nil && *sid != "" {
			ask.SessionID = sid
		}
	}
	_, err = askQuestionAs(q, ask)
	return v, err
}

// connectionsDelete (§1, owner) once every key is retired (no value file left), so a test connection can go. Run tokens
// drop the name (a new connection with it is not granted to them), open key requests are withdrawn, the history moves to
// an audit row that a new connection of the same name does not inherit.
func connectionsDelete(q *Request, r *contract.ConnectionsDeleteRequest) (contract.ConnectionsDeleteReply, error) {
	var no contract.ConnectionsDeleteReply
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return no, err
	}
	name := c.Name
	if _, err := getForUpdate(q, c.ID, "api-connection"); err != nil {
		return no, err
	}
	keys, err := connKeys(q, name)
	if err != nil {
		return no, err
	}
	for _, k := range keys {
		if k.State != "retired" {
			return no, fail(409, "Retire every key of "+name+" first")
		}
	}
	toks, err := kindAll[runToken](q, "run-token", nil)
	if err != nil {
		return no, err
	}
	for _, t := range toks {
		if slices.Contains(t.Connections, name) {
			t.Connections = slices.DeleteFunc(t.Connections, func(n string) bool { return n == name })
			if err := kindPut(q, "run-token", t); err != nil {
				return no, err
			}
		}
	}
	if err := q.db().DeleteAPIConnection(q.ctx(), c.ID); err != nil {
		return no, err
	}
	if c.ID != "" {
		secrets, err := q.db().ConnOpenKeyRequests(q.ctx(), name)
		if err != nil {
			return no, err
		}
		if err := questionsWithdraw(q, secrets); err != nil {
			return no, err
		}
	}
	if err := audit(q, "deleted-connection:"+name, "connection", nil, "deleted", auditExtra{}); err != nil {
		return no, err
	}
	if dir, err := connDir(name); err == nil {
		onEnd(q.tx, func(committed bool) {
			if committed { // every key is retired: only stale stages can remain, and connSync must not re-register them
				if err := os.RemoveAll(dir); err != nil {
					log.Printf("connection %s: %v", name, err)
				}
			}
		})
	}
	publish(q)
	return contract.ConnectionsDeleteReply{Name: name, Deleted: true}, nil
}

// connectionsKeyState: rotation (§2): active <-> draining, any -> retired (the value file is deleted; the row stays);
// disabled -> active is the owner's.
func connectionsKeyState(q *Request, r *contract.ConnectionsKeyStateRequest) (any, error) {
	id := r.KeyId
	if id != "" && !strings.HasPrefix(id, "key:") {
		id = "key:" + id
	}
	kp, err := kindMaybe[connKey](q, id, "connection-key")
	if err != nil {
		return nil, err
	}
	if kp == nil {
		return nil, fail(404, "Unknown key")
	}
	k := *kp
	c, err := connNamed(q, k.ConnectionID)
	if err != nil {
		return nil, err
	}
	// The request names the key, not the connection: the holder rule is checked on the key's connection here.
	if q.person != nil && !fullAccess(q.person) && q.person.Email != c.Holder {
		return nil, fail(403, "Only the holder or the owner changes this API key")
	}
	to := string(r.State)
	if k.State == "retired" {
		return nil, fail(409, "A retired key stays retired")
	}
	if k.State == "disabled" && to != "retired" && q.person != nil && !fullAccess(q.person) {
		return nil, fail(403, "Only the owner handles key values")
	}
	if to == k.State {
		return k, nil
	}
	if err := connChange(q, c, "key:"+k.Fingerprint, k.State, to); err != nil {
		return nil, err
	}
	now := nowMs()
	k.State, k.UpdatedAt = to, &now
	if to == "active" {
		enabled := float64(now)
		k.DisabledReason, k.DisabledAt, k.Problem, k.EnabledAt = nil, nil, nil, &enabled
	}
	if err := kindPut(q, "connection-key", k); err != nil {
		return nil, err
	}
	// The key left disabled (enabled again or retired): its refused-key question no longer needs the owner.
	if err := problemClose(q, "key:"+k.Fingerprint+":disabled"); err != nil {
		return nil, err
	}
	if to == "retired" {
		if dir, err := connDir(c.Name); err == nil {
			// The value goes only once the retirement committed; connSync removes it if a crash came in between.
			path := filepath.Join(dir, k.Fingerprint)
			onEnd(q.tx, func(committed bool) {
				if committed {
					os.Remove(path)
				}
			})
		}
	}
	publish(q)
	return k, nil
}

// connUpdated: the connection written with its change time, then the snapshot republished.
func connUpdated(q *Request, c apiConnection) error {
	now := nowMs()
	c.UpdatedAt = &now
	if err := kindPut(q, "api-connection", c); err != nil {
		return err
	}
	publish(q)
	return nil
}

// connectionsSetTools (owner): the explicit provider tool grant.
func connectionsSetTools(q *Request, r *contract.ConnectionsSetToolsRequest) (any, error) {
	if _, err := getForUpdate(q, "connection:"+r.Connection, "api-connection"); err != nil && !errors.Is(err, errNotFound) {
		return nil, err
	}
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	seen, tools := map[string]bool{}, []string{}
	for _, name := range r.Tools {
		if _, known := gatewayProviders[c.Provider].Tools[name]; name == "" || seen[name] || !known {
			return nil, fail(400, "tools must be unique recognized provider tool names")
		}
		seen[name] = true
		tools = append(tools, name)
	}
	if js(tools) != js(c.Tools) {
		if err := connChange(q, c, "tools", c.Tools, tools); err != nil {
			return nil, err
		}
		c.Tools = tools
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func connectionsSetAccess(q *Request, r *contract.ConnectionsSetAccessRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	people, err := idList(r.Access.People, "people")
	if err != nil {
		return nil, err
	}
	for i, p := range people {
		people[i] = strings.ToLower(p)
	}
	projects, err := idList(r.Access.Projects, "projects")
	if err != nil {
		return nil, err
	}
	exps, err := idList(r.Access.Experiments, "experiments")
	if err != nil {
		return nil, err
	}
	next, prev := connAccess{People: people, Projects: projects, Experiments: exps}.stored(), c.Access.stored()
	if js(next) != js(prev) {
		if err := connChange(q, c, "access", c.Access, next); err != nil {
			return nil, err
		}
		c.Access = next
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func connectionsSetIntake(q *Request, r *contract.ConnectionsSetIntakeRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	in := string(r.Intake)
	if c.Intake != in {
		if err := connChange(q, c, "intake", c.Intake, in); err != nil {
			return nil, err
		}
		c.Intake = in
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// connectionsSetLimits: file tool limits (§9); the stored object is replaced as a whole; an omitted field takes the
// gateway's default.
func connectionsSetLimits(q *Request, r *contract.ConnectionsSetLimitsRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		v    *int64
	}{{"fileBytes", r.Limits.FileBytes}, {"replyBytes", r.Limits.ReplyBytes}, {"stallMs", r.Limits.StallMs}, {"answerMs", r.Limits.AnswerMs}} {
		if f.v != nil && (*f.v < 1 || *f.v > 1<<53) {
			return nil, fail(400, "limits."+f.name+" must be a positive integer")
		}
	}
	next := r.Limits
	previous := json.RawMessage("{}")
	if !isNull(c.Limits) {
		previous = c.Limits
	}
	if stored, err := c.limits(); err != nil || js(next) != js(stored) { // the fields are pointers: compared as written
		if err := connChange(q, c, "limits", previous, next); err != nil {
			return nil, err
		}
		c.Limits = nil
		if next != (contract.GatewayLimits{}) {
			c.Limits = jsonBytes(next)
		}
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// connectionsTokenCreate (owner, §4): a long-lived bearer for the owner's interactive sessions; the value is answered
// once. Full access (owner, grant "*" such as site-agents) mints during development; directedBy records the real actor.
func connectionsTokenCreate(q *Request, r *contract.ConnectionsTokenCreateRequest) (any, error) {
	name := r.Name
	if !connName.MatchString(name) {
		return nil, fail(400, "Expected token name")
	}
	toks, err := kindAll[runToken](q, "run-token", nil)
	if err != nil {
		return nil, err
	}
	for _, t := range toks {
		if deref(t.Personal) == name && !t.revoked() {
			return nil, fail(409, "Token name in use")
		}
	}
	exec, director := "token:"+name, directorOf(q)
	g, err := mintGrantFor(q, mintRun{ExecutionID: &exec, DirectedBy: &director, Personal: &name}, append([]string{}, r.Connections...))
	if err != nil {
		return nil, err
	}
	if g.Token == nil {
		return nil, fail(409, "No usable connection: "+strings.Join(g.Refused, ", "))
	}
	return struct {
		mintGrant
		Name string `json:"name"`
	}{g, name}, nil
}

func connectionsTokenList(q *Request) (contract.ConnectionsTokenListReply, error) {
	out := contract.ConnectionsTokenListReply{Tokens: []contract.TokenInfo{}}
	toks, err := kindAll[runToken](q, "run-token", nil)
	if err != nil {
		return out, err
	}
	for _, t := range toks {
		if t.Personal != nil {
			out.Tokens = append(out.Tokens, contract.TokenInfo{Name: *t.Personal, Connections: jsonBytes(t.Connections), CreatedAt: orNull(t.CreatedAt),
				RevokedAt: orNull(t.RevokedAt), DirectedBy: jsonBytes(t.DirectedBy)})
		}
	}
	return out, nil
}

func connectionsTokenRevoke(q *Request, r *contract.ConnectionsTokenRevokeRequest) (contract.ConnectionsTokenRevokeReply, error) {
	var no contract.ConnectionsTokenRevokeReply
	toks, err := kindAll[runToken](q, "run-token", nil)
	if err != nil {
		return no, err
	}
	n := 0
	for _, t := range toks {
		if deref(t.Personal) == r.Name && !t.revoked() {
			t.RevokedAt = jsonBytes(nowMs())
			if err := kindPut(q, "run-token", t); err != nil {
				return no, err
			}
			n++
		}
	}
	if n == 0 {
		return no, fail(404, "Unknown token")
	}
	publish(q)
	return contract.ConnectionsTokenRevokeReply{Name: r.Name, Revoked: true}, nil
}

func init() {

	onStart(func(ctx context.Context, a *App) {
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
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

func envMs(k string, d int) time.Duration {
	var n int
	if _, err := fmt.Sscan(envOr(k, fmt.Sprint(d)), &n); err != nil {
		n = d
	}
	return time.Duration(n) * time.Millisecond
}

// mintRun: what a run token is minted for: its execution, director, project, experiment and pipeline run, and its
// marks (a pipeline step's, a person's named token, Fleet's own reads).
type mintRun struct {
	ExecutionID, DirectedBy, ProjectID, ExperimentID, PipelineRunID, Personal, SystemActor *string
	Step                                                                                   bool
}

// mintRunOf: a run record's fields (the pipeline and dispatch files hold the run as a map); a field that is not text is
// absent.
func mintRunOf(run doc) mintRun {
	r, _ := asT[struct {
		ExecutionID   json.RawMessage `json:"executionId"`
		DirectedBy    json.RawMessage `json:"directedBy"`
		ProjectID     json.RawMessage `json:"projectId"`
		ExperimentID  json.RawMessage `json:"experimentId"`
		PipelineRunID json.RawMessage `json:"pipelineRunId"`
		Personal      json.RawMessage `json:"personal"`
		SystemActor   json.RawMessage `json:"systemActor"`
		Step          json.RawMessage `json:"step"`
	}](run)
	text := func(v json.RawMessage) *string {
		if s, ok := jsonAs[string](v); ok {
			return &s
		}
		return nil
	}
	step, _ := jsonAs[bool](r.Step)
	return mintRun{ExecutionID: text(r.ExecutionID), DirectedBy: text(r.DirectedBy), ProjectID: text(r.ProjectID), ExperimentID: text(r.ExperimentID),
		PipelineRunID: text(r.PipelineRunID), Personal: text(r.Personal), SystemActor: text(r.SystemActor), Step: step}
}

// Per-run tokens (§4): granted are the requested connections that exist, take intake and whose access names the run.
// connAllowed takes the connection and the run as the maps its callers hold (holder and access; the run's fields).
func connAllowed(c doc, run doc) bool {
	conn, err := asT[struct {
		Holder string     `json:"holder"`
		Access connAccess `json:"access"`
	}](c)
	if err != nil {
		return false
	}
	return connAllowedFor(apiConnection{Holder: conn.Holder, Access: conn.Access}, mintRunOf(run))
}

func connAllowedFor(c apiConnection, r mintRun) bool {
	a := c.Access
	has := func(list []string, v *string) bool { return v != nil && slices.Contains(list, *v) }
	if len(a.People) == 0 && len(a.Projects) == 0 && len(a.Experiments) == 0 {
		return r.DirectedBy != nil && c.Holder != "" && *r.DirectedBy == c.Holder
	}
	all := "*"
	return has(a.People, r.DirectedBy) || has(a.Projects, &all) || has(a.Projects, r.ProjectID) || has(a.Experiments, r.ExperimentID)
}

// mintGrant: what a mint answers: the token (nil when nothing was granted), the refused names and each granted
// connection as its client sees it.
type mintGrant struct {
	Token       *string    `json:"token"`
	Refused     []string   `json:"refused"`
	Connections []connSpec `json:"connections"`
}

// connSpec, toolSpec: a granted connection and its tools as the client's MCP server serves them; file and idempotent
// tell the client the transfer mode and how an edge error reads (its MCP server keeps them from the model), the waits
// let it outwait the gateway (§9).
type connSpec struct {
	Name         string          `json:"name"`
	Instructions json.RawMessage `json:"instructions"`
	Tools        []toolSpec      `json:"tools"`
	Limits       *connWaits      `json:"limits,omitempty"`
}

type toolSpec struct {
	Name        string                    `json:"name"`
	Description json.RawMessage           `json:"description"`
	InputSchema json.RawMessage           `json:"inputSchema"`
	Idempotent  bool                      `json:"idempotent"`
	File        *contract.GatewayFileMode `json:"file,omitempty"`
}

type connWaits struct {
	StallMs  *int64 `json:"stallMs,omitempty"`
	AnswerMs *int64 `json:"answerMs,omitempty"`
}

// mint and mintFor: mintGrantFor as the map their callers read (token text or nil, refused as []string, connections).
func mint(q *Request, run doc, requested []string) (doc, error) {
	return mintFor(q, mintRunOf(run), requested)
}

func mintFor(q *Request, run mintRun, requested []string) (doc, error) {
	g, err := mintGrantFor(q, run, requested)
	if err != nil {
		return nil, err
	}
	m := docOf(g)
	m["refused"] = g.Refused // pipeline_plan.go reads the list as []string
	return m, nil
}

func mintGrantFor(q *Request, run mintRun, requested []string) (mintGrant, error) {
	seen, uniq := map[string]bool{}, []string{}
	for _, r := range requested {
		if !seen[r] {
			seen[r] = true
			uniq = append(uniq, r)
		}
	}
	granted := []apiConnection{}
	for _, name := range uniq {
		c, err := kindMaybe[apiConnection](q, "connection:"+name, "api-connection")
		if err != nil {
			return mintGrant{}, err
		}
		if c != nil && c.Intake == "all" && connAllowedFor(*c, run) {
			granted = append(granted, *c)
		}
	}
	if len(granted) == 0 {
		return mintGrant{Refused: uniq, Connections: []connSpec{}}, nil
	}
	token := randomToken()
	sha := sha256hex([]byte(token))
	names, specs, refused := []string{}, []connSpec{}, []string{}
	var fileDownloads []string
	for _, c := range granted {
		names = append(names, c.Name)
		p := gatewayProviders[c.Provider]
		spec := connSpec{Name: c.Name, Instructions: orNull(p.Rest["instructions"]), Tools: []toolSpec{}}
		for _, t := range c.Tools {
			tool, ok := p.Tools[t]
			if !ok {
				continue
			}
			spec.Tools = append(spec.Tools, toolSpec{Name: t, Description: orNull(tool.Rest["description"]), InputSchema: orNull(tool.Rest["inputSchema"]),
				Idempotent: tool.Idempotent, File: tool.File})
			if tool.File != nil && *tool.File == contract.GatewayFileModeDownload && !slices.Contains(fileDownloads, c.Name) {
				fileDownloads = append(fileDownloads, c.Name)
			}
		}
		if l, err := c.limits(); err == nil && (l.StallMs != nil || l.AnswerMs != nil) {
			spec.Limits = &connWaits{StallMs: l.StallMs, AnswerMs: l.AnswerMs}
		}
		specs = append(specs, spec)
	}
	for _, n := range uniq {
		if !slices.Contains(names, n) {
			refused = append(refused, n)
		}
	}
	project := run.ProjectID
	if project != nil && *project == "" {
		project = nil
	}
	row := runToken{ID: "run-token:" + sha, Sha256: sha, ExecutionID: run.ExecutionID, DirectedBy: run.DirectedBy, ProjectID: project,
		ExperimentID: run.ExperimentID, PipelineRunID: run.PipelineRunID, Connections: names, CreatedAt: jsonBytes(nowMs()), FileDownloads: fileDownloads,
		Step: run.Step, Personal: run.Personal, SystemActor: run.SystemActor} // systemActor: its calls are Fleet's own activity (connRecord)
	if err := kindPut(q, "run-token", row); err != nil {
		return mintGrant{}, err
	}
	publish(q)
	return mintGrant{Token: &token, Refused: refused, Connections: specs}, nil
}

func revokeTokens(q *Request, executionID string) error {
	n, err := q.db().RevokeExecutionTokens(q.ctx(), db.RevokeExecutionTokensParams{At: float64(nowMs()), ExecutionID: executionID})
	if err != nil {
		return err
	}
	if n > 0 {
		publish(q)
	}
	return nil
}

// At start no api step call of an earlier process is in flight: their tokens are revoked.
func revokeSteps(q *Request) error {
	return q.db().RevokeStepTokensBefore(q.ctx(), db.RevokeStepTokensBeforeParams{At: float64(nowMs()), Started: float64(processStarted)})
}

// Spool ingest (§3): every gateway line becomes one tool_run with connection_cost, idempotent by callId. Each
// unrecorded line runs in its own transaction locked only by "usageKey:" + key (or NOLOCK when no usageKey), never
// acquiring GLOBAL. A past day's file is deleted once its lines are committed; a file with a line that could not be
// imported is kept.
func connIngestPass(ctx context.Context, a *App) (int, error) {
	if gatewayDir() == "" {
		return 0, nil
	}
	dir := filepath.Join(gatewayDir(), "state", "spool")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	added := 0
	yesterday := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02")
	names := []string{}
	for _, e := range entries {
		if spoolFile.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, f := range names {
		// ponytail: the whole file is read each pass (one day of lines); keep a read offset if a day's file gets large.
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		// Only complete lines while the gateway may still write the day (today, yesterday); a past day is read whole, so
		// a last line without its newline is imported or keeps the file.
		past := f[:10] < yesterday
		if !past {
			b = b[:bytes.LastIndexByte(b, '\n')+1]
		}
		// Whole lines of any length (no Scanner token cap, W3-2); a line not imported keeps the file.
		unimported := 0
		var parsed []contract.CallLine
		var callIDs []string
		for _, line := range bytes.Split(b, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var l contract.CallLine
			if err := json.Unmarshal(line, &l); err != nil {
				// Kept in the file; a line here means the gateway and the contract disagree.
				log.Printf("spool %s: line not imported: %v", f, err)
				unimported++
				continue
			}
			parsed = append(parsed, l)
			if l.CallId != "" && l.TokenSha256 != nil && l.ExecutionId != nil {
				callIDs = append(callIDs, "call:"+l.CallId)
			}
		}
		// Rows already recorded, with their transfer phase ("" for a one-line call).
		existing := map[string]string{}
		if len(callIDs) > 0 {
			if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
				rs, err := q.db().ConnCallPhases(q.ctx(), callIDs)
				if err != nil {
					return err
				}
				for _, r := range rs {
					existing[r.ID] = r.Phase
				}
				return nil
			}); err != nil {
				return added, err
			}
		}
		for _, l := range parsed {
			if l.CallId == "" || l.TokenSha256 == nil || l.ExecutionId == nil {
				continue
			}
			phase, recorded := existing["call:"+l.CallId]
			if l.Phase != nil && *l.Phase == contract.CallPhaseCompleted {
				// A transfer's completion line updates its started row, also one this pass just inserted from the same file.
				if phase == string(contract.CallPhaseCompleted) {
					continue
				}
				if err := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error { return connComplete(q, l) }); err != nil {
					return added, err
				}
				continue
			}
			if recorded {
				continue
			}
			lock := NOLOCK
			if l.UsageKey != nil && *l.UsageKey != "" {
				lock = "usageKey:" + *l.UsageKey
			}
			var ok bool
			if err := runTx(ctx, a, nil, true, lock, func(q *Request) error {
				var err error
				ok, err = connRecord(q, l)
				if err != nil {
					return err
				}
				if ok {
					publish(q)
				}
				return nil
			}); err != nil {
				return added, err
			}
			if ok {
				added++
			}
		}
		if past && unimported > 0 {
			log.Printf("spool %s: past day kept, %d line(s) not imported", f, unimported)
		} else if past {
			os.Remove(filepath.Join(dir, f))
		}
	}
	return added, nil
}

// callData: a call row's data (tool_run) from its gateway line and run token.
type callData struct {
	Tool                    string                 `json:"tool"`
	Connection              string                 `json:"connection"`
	Outcome                 contract.CallOutcome   `json:"outcome"`
	ErrorCode               *string                `json:"errorCode"`
	Status                  int64                  `json:"status"`
	KeyFingerprint          *string                `json:"keyFingerprint"`
	Usage                   *contract.CallUsage    `json:"usage"`
	DirectedBy              *string                `json:"directedBy"`
	StartedAt               int64                  `json:"startedAt"`
	Ms                      int64                  `json:"ms"`
	Transfer                *contract.CallTransfer `json:"transfer,omitempty"`
	Phase                   *contract.CallPhase    `json:"phase,omitempty"`
	UploadAccounted         bool                   `json:"uploadAccounted,omitempty"`
	TokenName               *string                `json:"tokenName,omitempty"`
	PipelineRunID           *string                `json:"pipelineRunId,omitempty"`
	DirectorAccessRevokedAt json.RawMessage        `json:"directorAccessRevokedAt,omitempty"`
}

// callExecution: a call row as insertExecution (store.go) takes it.
type callExecution struct {
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	State             string   `json:"state"`
	ProjectID         *string  `json:"projectId"`
	ParentExecutionID *string  `json:"parentExecutionId"`
	ExperimentID      *string  `json:"experimentId"`
	TaskID            *string  `json:"taskId"`
	SessionID         *string  `json:"sessionId"`
	DirectedBy        *string  `json:"directedBy"`
	StartedAt         *int64   `json:"startedAt"`
	EndedAt           *int64   `json:"endedAt"`
	PipelineID        *string  `json:"pipelineId"`
	PipelineRevision  *int64   `json:"pipelineRevision"`
	StepID            *string  `json:"stepId"`
	ProfileID         *string  `json:"profileId"`
	ProfileRevision   *int64   `json:"profileRevision"`
	SystemActor       *string  `json:"systemActor"`
	Data              callData `json:"data"`
}

func connRecord(q *Request, l contract.CallLine) (bool, error) {
	if l.CallId == "" || l.TokenSha256 == nil || l.ExecutionId == nil {
		return false, nil
	}
	t, err := kindMaybe[runToken](q, "run-token:"+*l.TokenSha256, "run-token")
	if err != nil || t == nil {
		return false, err
	}
	id := "call:" + l.CallId
	state := "failed"
	if l.Outcome == contract.CallOutcomeOk {
		state = "succeeded"
	}
	data := callData{Tool: l.Tool, Connection: l.Connection, Outcome: l.Outcome, ErrorCode: l.ErrorCode, Status: l.Status,
		KeyFingerprint: l.KeyFingerprint, Usage: l.Usage, DirectedBy: t.DirectedBy, StartedAt: l.StartedAt, Ms: l.Ms, Transfer: l.Transfer,
		Phase: l.Phase, TokenName: t.Personal, PipelineRunID: t.PipelineRunID}
	if l.Phase != nil && *l.Phase == contract.CallPhaseCompleted && isUpload(l) {
		data.UploadAccounted = true
	}
	parent := t.ExecutionID
	if t.Personal != nil {
		parent = nil
	}
	var pr db.ConnCallParentRow
	if pid := deref(parent); pid != "" {
		var err error
		if pr, err = q.db().ConnCallParent(q.ctx(), pid); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	var startedAt, endedAt *int64
	if l.StartedAt > 0 {
		start, end := l.StartedAt, l.StartedAt+max(l.Ms, 0)
		startedAt, endedAt = &start, &end
	}
	director := firstString(deref(t.DirectedBy), deref(pr.DirectedBy))
	if deref(t.ProjectID) == "" && pr.SessionID == nil {
		if !isNull(t.DirectorAccessRevokedAt) {
			data.DirectorAccessRevokedAt = t.DirectorAccessRevokedAt
		} else if director != "" {
			active, err := q.db().ConnPersonExists(q.ctx(), director)
			if err != nil {
				return false, err
			}
			if !active {
				data.DirectorAccessRevokedAt = jsonBytes(nowMs())
			}
		}
	}
	var directedBy *string
	if director != "" {
		directedBy = &director
	}
	inserted, err := insertExecution(q, docOf(callExecution{ID: id, Kind: "tool_run", State: state, ProjectID: t.ProjectID, ParentExecutionID: parent,
		ExperimentID: t.ExperimentID, TaskID: pr.TaskID, SessionID: pr.SessionID, DirectedBy: directedBy, StartedAt: startedAt, EndedAt: endedAt,
		PipelineID: pr.PipelineID, PipelineRevision: pr.PipelineRevision, StepID: pr.StepID, ProfileID: pr.ProfileID, ProfileRevision: pr.ProfileRevision,
		SystemActor: t.SystemActor, Data: data}))
	if err != nil || !inserted {
		return false, err
	}
	if l.Outcome != contract.CallOutcomeOk {
		if err := q.db().ConnCallProblem(q.ctx(), db.ConnCallProblemParams{ID: id, Problem: problemOf(string(l.Outcome), deref(l.ErrorCode))}); err != nil {
			return false, err
		}
	}
	if err := connAccount(q, id, l, t.Step); err != nil {
		return false, err
	}
	return true, nil
}

// connAccount applies provider usage and key consequences after a call or upload completion is recorded.
func connAccount(q *Request, id string, l contract.CallLine, step bool) error {
	var cost any
	if u := l.Usage; u != nil {
		amount := u.Amount
		if l.UsageKey != nil {
			// A finished job's charge (Meshy consumed_credits) counts once per (connection, job); later polls cost 0. A
			// pipeline step's job is counted by recordJob instead.
			// Concurrent ingests of two polls of one job serialize on the key.
			if err := acquireTxLock(q, "usageKey:"+*l.UsageKey); err != nil {
				return err
			}
			n, err := q.db().ConnUsageKeyCount(q.ctx(), db.ConnUsageKeyCountParams{UsageKey: *l.UsageKey, ID: id})
			if err != nil {
				return err
			}
			if n > 0 || step {
				amount = 0
			} else if err := q.db().ConnSetUsageKey(q.ctx(), db.ConnSetUsageKeyParams{UsageKey: *l.UsageKey, ID: id}); err != nil {
				return err
			}
		}
		// The cost names its amount under its unit as well (a JSON object keyed by the provider's unit).
		cost = map[string]json.RawMessage{"unit": jsonBytes(u.Unit), u.Unit: jsonBytes(amount), "amount": jsonBytes(amount), "connection": jsonBytes(l.Connection)}
	}
	if err := writeConnectionCost(q, id, cost); err != nil {
		return err
	}
	at := l.StartedAt
	if l.ProviderAttemptAt != nil {
		at = *l.ProviderAttemptAt
	}
	if d := l.Disabled; d != nil && d.Key != "" {
		if err := connDisable(q, d.Key, d.Status, at); err != nil {
			return err
		}
	}
	if p := l.KeyProblem; p != nil && p.Key != "" {
		k, err := kindMaybe[connKey](q, "key:"+p.Key, "connection-key")
		if err != nil {
			return err
		}
		if k != nil {
			msg := p.Message
			if len(msg) > 500 {
				msg = msg[:500]
			}
			if at == 0 {
				at = nowMs()
			}
			if k.EnabledAt != nil && int64(*k.EnabledAt) >= at {
				return nil
			}
			k.Problem = &keyProblem{Status: p.Status, Message: msg, At: at}
			if err := kindPut(q, "connection-key", *k); err != nil {
				return err
			}
		}
	}
	return nil
}

func isUpload(l contract.CallLine) bool {
	return l.Transfer != nil && l.Transfer.Direction == contract.CallTransferDirectionUpload
}

// transferPatch: what a transfer's completed line writes over its started row's data; an upload also writes its key,
// usage (null when the line has none) and that it is accounted.
type transferPatch struct {
	Outcome         contract.CallOutcome   `json:"outcome"`
	Status          int64                  `json:"status"`
	ErrorCode       *string                `json:"errorCode"`
	Ms              int64                  `json:"ms"`
	Phase           contract.CallPhase     `json:"phase"`
	Transfer        *contract.CallTransfer `json:"transfer,omitempty"`
	KeyFingerprint  json.RawMessage        `json:"keyFingerprint,omitempty"`
	Usage           json.RawMessage        `json:"usage,omitempty"`
	UploadAccounted bool                   `json:"uploadAccounted,omitempty"`
}

// connComplete applies a file transfer's completed line (§9) to its started row. A repeated pass leaves the
// completed row unchanged. If the started line was lost, the completed line is recorded alone.
func connComplete(q *Request, l contract.CallLine) error {
	id := "call:" + l.CallId
	p := problemOf(string(l.Outcome), deref(l.ErrorCode))
	state, problem := "failed", &p
	if l.Outcome == contract.CallOutcomeOk {
		state, problem = "succeeded", nil
	}
	ms := max(l.Ms, 0)
	patch := transferPatch{Outcome: l.Outcome, Status: l.Status, ErrorCode: l.ErrorCode, Ms: ms, Phase: contract.CallPhaseCompleted, Transfer: l.Transfer}
	upload := isUpload(l)
	if upload {
		patch.KeyFingerprint, patch.Usage, patch.UploadAccounted = jsonBytes(l.KeyFingerprint), jsonBytes(l.Usage), true
	}
	n, err := q.db().ConnTransferComplete(q.ctx(), db.ConnTransferCompleteParams{ID: id, Ms: ms, Problem: problem, Patch: jsonBytes(patch), Upload: upload})
	if err != nil {
		return err
	}
	if n == 1 {
		if _, err := executionTransition(q, id, executionEvent{To: state, KeepEnd: true, Correct: true}); err != nil {
			return err
		}
		if upload {
			// A file upload has no usageOnce. Its usage and key effects are known only after the POST.
			if err := connAccount(q, id, l, false); err != nil {
				return err
			}
		}
		if err := q.db().ConnTransferTotals(q.ctx(), db.ConnTransferTotalsParams{ID: id, Ms: ms}); err != nil {
			return err
		}
		publish(q)
		return nil
	}
	if exists, err := q.db().ConnExecutionExists(q.ctx(), id); err != nil || exists {
		return err
	}
	ok, err := connRecord(q, l)
	if ok {
		publish(q)
	}
	return err
}

func connDisable(q *Request, fp string, status, attemptedAt int64) error {
	k, err := kindMaybe[connKey](q, "key:"+fp, "connection-key")
	if err != nil || k == nil || k.State == "retired" || k.State == "disabled" {
		return err
	}
	if k.EnabledAt != nil && attemptedAt > 0 && int64(*k.EnabledAt) > attemptedAt {
		return nil
	}
	st := fmt.Sprint(status)
	reason, now := "provider answered HTTP "+st, nowMs()
	k.State, k.DisabledReason, k.DisabledAt = "disabled", &reason, &now
	if err := kindPut(q, "connection-key", *k); err != nil {
		return err
	}
	publish(q)
	return problemRaise(q, "", Problem{Cause: "key:" + fp + ":disabled", Options: []any{"seen"},
		Text: fmt.Sprintf("API key %s of %v was refused by the provider (HTTP %s) and is disabled. Re-enable it on the API keys screen once it works again, or retire it.", fp, k.ConnectionID, st)})
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
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
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
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
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
	req, _ := http.NewRequestWithContext(ctx, "GET", gatewayURL()+"/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+usageToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	var report usageReport
	if res.StatusCode/100 != 2 || json.NewDecoder(res.Body).Decode(&report) != nil {
		return
	}
	at := orNull(report.At)
	err = runTx(ctx, a, nil, true, "", func(q *Request) error {
		for _, row := range report.Keys {
			k, err := kindMaybe[connKey](q, "key:"+row.Key, "connection-key")
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
			if err := kindPut(q, "connection-key", *k); err != nil {
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

// keyAnswer: the only answer a key request records besides decline: the JSON text {connection, fingerprint, state}.
func keyAnswer(connection, fp, state string) string {
	return js(struct {
		Connection  string `json:"connection"`
		Fingerprint string `json:"fingerprint"`
		State       string `json:"state"`
	}{connection, fp, state})
}

// keyRequestOf: the open key request qid for connection, or one already answered with replay (a retried add).
func keyRequestOf(q *Request, qid, connection, replay string) error {
	qn, err := q.db().ConnKeyRequest(q.ctx(), qid)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "Unknown key request")
	}
	if err != nil {
		return err
	}
	if qn.State != "open" && (replay == "" || qn.State != "answered" || qn.Answer != replay) {
		return fail(409, "Key request already settled")
	}
	if qn.Connection != connection {
		return fail(400, "The key request names another connection")
	}
	return nil
}

// keyAnswerCheck: the answer hook of a key request (tasks area calls it with the question as its map). Only the owner
// settles it, with decline or with the fingerprint answer of a key added to its connection, so no answer path carries a
// value.
func keyAnswerCheck(q *Request, question doc, answer string) error {
	qn, _ := asT[struct {
		Kind    string `json:"kind"`
		Options struct {
			Connection json.RawMessage `json:"connection"`
		} `json:"options"`
	}](question)
	if qn.Kind != "secret" {
		return nil
	}
	if q.agent != "" || q.person == nil || !fullAccess(q.person) { // never the asking agent, even with the owner's rights
		return fail(403, "Only the owner answers a key request")
	}
	if answer == "decline" {
		return nil
	}
	var a struct{ Connection, Fingerprint, State string }
	conn, _ := jsonAs[string](qn.Options.Connection)
	if json.Unmarshal([]byte(answer), &a) != nil || a.Connection != conn || keyAnswer(a.Connection, a.Fingerprint, a.State) != answer {
		return fail(400, "Answer decline, or add the key in its field")
	}
	k, err := kindMaybe[connKey](q, "key:"+a.Fingerprint, "connection-key")
	if err != nil {
		return err
	}
	if k == nil || k.ConnectionID != conn || a.State != "active" || k.State != "active" {
		return fail(400, "Answer decline, or add the key in its field")
	}
	return nil
}
