// Personal bearer tokens and per-run gateway token minting/revocation.
package main

import (
	"encoding/json"
	"slices"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// connectionsTokenCreate (owner, §4): a long-lived bearer for the owner's interactive sessions; the value is answered
// once. Full access (owner, grant "*" such as site-agents) mints during development; directedBy records the real actor.
func connectionsTokenCreate(q *Request, r *contract.ConnectionsTokenCreateRequest) (any, error) {
	name := r.Name
	if !connName.MatchString(name) {
		return nil, fail(400, "Expected token name")
	}
	toks, err := runTokensAll(q)
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
	toks, err := runTokensAll(q)
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
	toks, err := runTokensAll(q)
	if err != nil {
		return no, err
	}
	n := 0
	for _, t := range toks {
		if deref(t.Personal) == r.Name && !t.revoked() {
			t.RevokedAt = jsonBytes(nowMs())
			if err := runTokenPut(q, t); err != nil {
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

// mintRun: what a run token is minted for: its execution, director, project, experiment and pipeline run, and its
// marks (a pipeline step's, a person's named token, Fleet's own reads).
type mintRun struct {
	ExecutionID, DirectedBy, ProjectID, ExperimentID, PipelineRunID, Personal, SystemActor *string
	Step                                                                                   bool
}

// mintRunOf: a run record's fields (dispatch.go passes the execution's); a field that is not text is
// absent.
func mintRunOf(run any) mintRun {
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
// connGrantOf: the holder and access of a connection (delivery.go); false when there is none.
func connGrantOf(c *apiConnection) (apiConnection, bool) {
	if c == nil {
		return apiConnection{}, false
	}
	return apiConnection{Holder: c.Holder, Access: c.Access}, true
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
// mint: mintGrantFor a run given as its record's fields (mintRunOf).
func mint(q *Request, run any, requested []string) (mintGrant, error) {
	return mintGrantFor(q, mintRunOf(run), requested)
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
		c, err := apiConnectionMaybe(q, "connection:"+name)
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
	if err := runTokenPut(q, row); err != nil {
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
