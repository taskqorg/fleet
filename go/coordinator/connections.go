// API key connections (docs/designs/fleet-connections.md). Key values live only in
// <FLEET_SECRET_DIR>/connections/<connection>/<fingerprint> (0600); rows hold the fingerprint and state. Every change
// republishes <FLEET_GATEWAY_DIR>/snapshot.json (no key values) once the change committed.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
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

func envMs(k string, d int) time.Duration {
	var n int
	if _, err := fmt.Sscan(envOr(k, fmt.Sprint(d)), &n); err != nil {
		n = d
	}
	return time.Duration(n) * time.Millisecond
}

// apiConnection: a connection row (api_connection: the columns and the payload fields). Writers keep the typed record
// shape (typed_records.go): columns first, then all remaining fields in payload.
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

type apiConnectionPayload struct {
	Access    connAccess      `json:"access"`
	Intake    string          `json:"intake"`
	Tools     []string        `json:"tools"`
	Comment   string          `json:"comment"`
	CreatedAt json.RawMessage `json:"createdAt,omitempty"`
	UpdatedAt *int64          `json:"updatedAt,omitempty"`
	Limits    json.RawMessage `json:"limits,omitempty"`
}

func apiConnectionOf(r db.ApiConnection) (apiConnection, error) {
	var p apiConnectionPayload
	if err := lenientDecode(r.Payload, &p); err != nil {
		return apiConnection{}, err
	}
	return apiConnection{
		ID: r.ID, Name: r.Name, Provider: deref(r.Provider), Holder: deref(r.Holder), ProjectID: r.ProjectID,
		Access: p.Access, Intake: p.Intake, Tools: p.Tools, Comment: p.Comment,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Limits: p.Limits,
	}, nil
}

func apiConnectionMaybe(q *Request, id string) (*apiConnection, error) {
	if id == "" {
		return nil, nil
	}
	r, err := q.db().APIConnectionByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := apiConnectionOf(r)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func apiConnectionAll(q *Request) ([]apiConnection, error) {
	rs, err := q.db().APIConnections(q.ctx())
	if err != nil {
		return nil, err
	}
	out := make([]apiConnection, 0, len(rs))
	for _, r := range rs {
		c, err := apiConnectionOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func apiConnectionPut(q *Request, c apiConnection) error {
	if c.ID == "" {
		return fmt.Errorf("api-connection record without an id")
	}
	p := apiConnectionPayload{
		Access: c.Access, Intake: c.Intake, Tools: c.Tools, Comment: c.Comment,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Limits: c.Limits,
	}
	return q.db().APIConnectionPut(q.ctx(), db.APIConnectionPutParams{
		ID:        c.ID,
		Name:      c.Name,
		Provider:  strPtr(c.Provider),
		Holder:    strPtr(c.Holder),
		ProjectID: c.ProjectID,
		Payload:   jsonBytes(p),
	})
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

// connKey: a connection key row (connection_key; its state moves exclusively through key_states.go).
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

type connKeyPayload struct {
	AddedBy        *string        `json:"addedBy,omitempty"`
	UpdatedAt      *int64         `json:"updatedAt,omitempty"`
	EnabledAt      *float64       `json:"enabledAt,omitempty"`
	DisabledReason *string        `json:"disabledReason,omitempty"`
	DisabledAt     *int64         `json:"disabledAt,omitempty"`
	Problem        *keyProblem    `json:"problem,omitempty"`
	Reconcile      *keyReconcile  `json:"reconcile,omitempty"`
	UsageBaseline  *usageBaseline `json:"usageBaseline,omitempty"`
}

func connKeyOf(r db.ConnectionKey) (connKey, error) {
	var p connKeyPayload
	if err := lenientDecode(r.Payload, &p); err != nil {
		return connKey{}, err
	}
	return connKey{
		ID: r.ID, ConnectionID: r.ConnectionID, Fingerprint: deref(r.Fingerprint), State: r.State,
		ProjectID: r.ProjectID, AddedAt: timeMs(r.AddedAt),
		AddedBy: p.AddedBy, UpdatedAt: p.UpdatedAt, EnabledAt: p.EnabledAt,
		DisabledReason: p.DisabledReason, DisabledAt: p.DisabledAt, Problem: p.Problem,
		Reconcile: p.Reconcile, UsageBaseline: p.UsageBaseline,
	}, nil
}

func connKeyMaybe(q *Request, id string) (*connKey, error) {
	if id == "" {
		return nil, nil
	}
	r, err := q.db().ConnectionKeyByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k, err := connKeyOf(r)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func connKeysList(q *Request, connectionID *string) ([]connKey, error) {
	rs, err := q.db().ConnectionKeys(q.ctx(), connectionID)
	if err != nil {
		return nil, err
	}
	out := make([]connKey, 0, len(rs))
	for _, r := range rs {
		k, err := connKeyOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func connKeyPut(q *Request, k connKey) error {
	if k.ID == "" {
		return fmt.Errorf("connection-key record without an id")
	}
	p := connKeyPayload{
		AddedBy: k.AddedBy, UpdatedAt: k.UpdatedAt, EnabledAt: k.EnabledAt,
		DisabledReason: k.DisabledReason, DisabledAt: k.DisabledAt, Problem: k.Problem,
		Reconcile: k.Reconcile, UsageBaseline: k.UsageBaseline,
	}
	if err := q.db().ConnectionKeyPut(q.ctx(), db.ConnectionKeyPutParams{
		ID:           k.ID,
		ConnectionID: k.ConnectionID,
		Fingerprint:  strPtr(k.Fingerprint),
		AddedAt:      floatMsPtr(k.AddedAt),
		ProjectID:    k.ProjectID,
		Payload:      jsonBytes(p),
	}); err != nil {
		return err
	}
	if k.State != "" {
		return connectionKeyTransition(q, k.ID, k.State)
	}
	return nil
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

// runToken: a run token row (run_token).
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

type runTokenPayload struct {
	ExperimentID  *string  `json:"experimentId"`
	Connections   []string `json:"connections"`
	FileDownloads []string `json:"fileDownloads,omitempty"`
	SystemActor   *string  `json:"systemActor,omitempty"`
}

func runTokenOf(r db.RunToken) (runToken, error) {
	var p runTokenPayload
	if err := lenientDecode(r.Payload, &p); err != nil {
		return runToken{}, err
	}
	return runToken{
		ID: r.ID, Sha256: deref(r.Sha256), ExecutionID: r.ExecutionID, DirectedBy: r.DirectedBy,
		ProjectID: r.ProjectID, ExperimentID: p.ExperimentID, PipelineRunID: r.PipelineRunID,
		Connections: p.Connections, CreatedAt: msRaw(r.CreatedAt), FileDownloads: p.FileDownloads,
		Step: r.Step != nil && *r.Step, Personal: r.Personal, SystemActor: p.SystemActor,
		RevokedAt: msRaw(r.RevokedAt), DirectorAccessRevokedAt: msRaw(r.DirectorAccessRevokedAt),
	}, nil
}

func runTokenMaybe(q *Request, id string) (*runToken, error) {
	if id == "" {
		return nil, nil
	}
	r, err := q.db().RunTokenByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := runTokenOf(r)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func runTokensAll(q *Request) ([]runToken, error) {
	rs, err := q.db().RunTokens(q.ctx())
	if err != nil {
		return nil, err
	}
	out := make([]runToken, 0, len(rs))
	for _, r := range rs {
		t, err := runTokenOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func runTokenPut(q *Request, t runToken) error {
	if t.ID == "" {
		return fmt.Errorf("run-token record without an id")
	}
	var step *bool
	if t.Step {
		step = ptr(true)
	}
	p := runTokenPayload{
		ExperimentID:  t.ExperimentID,
		Connections:   t.Connections,
		FileDownloads: t.FileDownloads,
		SystemActor:   t.SystemActor,
	}
	return q.db().RunTokenPut(q.ctx(), db.RunTokenPutParams{
		ID:                      t.ID,
		Sha256:                  strPtr(t.Sha256),
		ExecutionID:             t.ExecutionID,
		PipelineRunID:           t.PipelineRunID,
		Personal:                t.Personal,
		Step:                    step,
		DirectedBy:              t.DirectedBy,
		CreatedAt:               rawMs(t.CreatedAt),
		RevokedAt:               rawMs(t.RevokedAt),
		DirectorAccessRevokedAt: rawMs(t.DirectorAccessRevokedAt),
		ProjectID:               t.ProjectID,
		Payload:                 jsonBytes(p),
	})
}

func (t runToken) revoked() bool { return !isNull(t.RevokedAt) }

// connNamed: the connection called name; 400 for a name that is not one, 404 for an unknown one.
func connNamed(q *Request, name string) (apiConnection, error) {
	if !connName.MatchString(name) {
		return apiConnection{}, fail(400, "Expected connection name")
	}
	c, err := apiConnectionMaybe(q, "connection:"+name)
	if err == nil && c == nil {
		err = fail(404, "Unknown API key "+name)
	}
	if err != nil {
		return apiConnection{}, err
	}
	return *c, nil
}

func connChange(q *Request, c apiConnection, field string, previous, value any) error {
	return audit(q, "connection:"+c.Name, field, previous, value, auditExtra{Holder: c.Holder})
}

func connKeys(q *Request, name string) ([]connKey, error) {
	if name == "" {
		return connKeysList(q, nil)
	}
	return connKeysList(q, &name)
}

// connUpdated: the connection written with its change time, then the snapshot republished.
func connUpdated(q *Request, c apiConnection) error {
	now := nowMs()
	c.UpdatedAt = &now
	if err := apiConnectionPut(q, c); err != nil {
		return err
	}
	publish(q)
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
