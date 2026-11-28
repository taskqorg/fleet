package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"regexp"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

var captureRequestKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)

type designCaptureInstallation struct {
	Host         string                         `json:"host"`
	BaseArtifact string                         `json:"baseArtifact"`
	Producer     contract.DesignCaptureProducer `json:"producer"`
	Runtime      contract.DesignCaptureRuntime  `json:"runtime"`
	LaunchSHA256 string                         `json:"launchSha256"`
}

type designCapturePayload struct {
	Command         string                        `json:"command"`
	PermitExpiresAt int64                         `json:"permitExpiresAt"`
	TimeoutMs       int                           `json:"timeout_ms"`
	Inputs          []contract.InputArtifact      `json:"inputs"`
	DesignCapture   contract.DesignCaptureCommand `json:"designCapture"`
}

func designCaptureJobID(execution string) string {
	b := sha256.Sum256([]byte("fleet:design-capture:v1:" + execution))
	b[6] = b[6]&15 | 0x50
	b[8] = b[8]&63 | 0x80
	id := hex.EncodeToString(b[:16])
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}

func loadDesignCaptureInstallation() (designCaptureInstallation, error) {
	var cfg designCaptureInstallation
	d := json.NewDecoder(strings.NewReader(os.Getenv("FLEET_DESIGN_CAPTURE_INSTALLATION")))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(new(json.RawMessage)) != io.EOF || cfg.Host != contract.DesignCaptureHost || cfg.BaseArtifact == "" ||
		!cfg.Producer.Valid() || !cfg.Runtime.Valid() || !validDesignHash(cfg.LaunchSHA256) {
		return cfg, fail(503, "An approved PDS18 designer renderer is unconfigured")
	}
	return cfg, nil
}

func captureArtifactMatches(x *Artifact, execution, name string, limit int64) bool {
	return x != nil && x.State == "stored" && x.Scope == "prod" && x.Role == "original" &&
		deref(x.ProjectID) == "fleet" && (execution == "" || deref(x.ExecutionID) == execution) &&
		(name == "" || x.Name == name) && x.Size > 0 && x.Size <= limit && validDesignHash(x.ContentHash)
}

func loadCaptureArtifact(q *Request, id, owner, name string, limit int64) (*Artifact, error) {
	x, err := q.db().ArtByID(q.ctx(), id)
	if err != nil {
		return nil, fail(409, "Capture artifact is missing or outside its required execution")
	}
	a := artifactOf(x)
	if x.DeletedAt != nil || !captureArtifactMatches(&a, owner, name, limit) {
		return nil, fail(409, "Capture artifact is missing, changed, or outside its required execution")
	}
	return &a, nil
}

func designCaptureCaller(q *Request) (contract.DesignCaptureScope, error) {
	var scope contract.DesignCaptureScope
	id, ok := strings.CutPrefix(q.agent, "execution:")
	if !ok || id == "" || q.host == nil {
		return scope, fail(403, "Capture requires an authenticated Fleet designer turn")
	}
	return designCaptureAuthority(q, id, q.hostID())
}

func lockCaptureExecution(q *Request, id string) error {
	if err := lockExecutionTask(q, id); err != nil {
		return err
	}
	return q.db().DispExecLock(q.ctx(), id)
}

// Authenticated host callers resolve the originating director against the
// same live grants as native MCP. MCP already carries its authenticated person.
func designCaptureAuthority(q *Request, id, expectedHost string) (contract.DesignCaptureScope, error) {
	var scope contract.DesignCaptureScope
	if err := lockCaptureExecution(q, id); err != nil {
		return scope, err
	}
	e, err := getExecution(q, id)
	if err != nil {
		return scope, err
	}
	link, err := q.db().TaskExecutionLink(q.ctx(), id)
	if err != nil {
		return scope, err
	}
	profile, _ := profileOf(e.AgentConfig)
	s, err := sessionMaybe(q, e.SessionID)
	if err != nil {
		return scope, err
	}
	if s == nil || s.Role != "designer" || s.ProjectID != "fleet" || (expectedHost != "" && deref(e.HostID) != expectedHost) ||
		!fleetDesigner(deref(link.ProjectID), profile.ID) || e.State != "running" || e.unconfirmed() ||
		isTrue(e.StopRequested) || isTrue(e.TaskStopRequested) || deref(link.TaskID) == "" {
		return scope, fail(403, "Capture requires the active Fleet Design execution")
	}
	// Match execution_stop's task/row locks and project membership revocation's
	// definition lock. Parent and child precede the membership read or grant.
	if err := lockCaptureExecution(q, designCaptureJobID(id)); err != nil {
		return scope, err
	}
	if _, err := q.db().DefProjectAccessShare(q.ctx(), "fleet"); err != nil {
		return scope, err
	}
	project, err := projectContext(q, "fleet")
	if err != nil {
		return scope, err
	}
	if presetSettingsOf(project.object("settings")).Roles["designer"] != designProfileID {
		return scope, fail(403, "The official Fleet designer role is unavailable")
	}
	if q.person == nil {
		origin, err := hostRecordByID(q, deref(e.HostID))
		if err != nil {
			return scope, err
		}
		if origin == nil || origin.removed() {
			return scope, fail(403, "The originating designer host is unavailable")
		}
		sub := *q
		sub.host = &authHost{HostID: origin.ID, Holder: deref(origin.Holder)}
		person, err := hostAgentPerson(&sub, firstString(e.DirectedBy, s.DirectedBy, sub.host.Holder), e.TakenFrom)
		if err != nil {
			return scope, err
		}
		// Mirror the native MCP proof followed by its live database refresh.
		// Keep the outer host poll unscoped for its other commands.
		person, err = resolveAccessShareTx(q.ctx(), q.tx, person.Email)
		if err != nil {
			return scope, err
		}
		sub.person = person
		q = &sub
	}
	task := deref(link.TaskID)
	if err := activeDesignTask(q, task, "fleet"); err != nil {
		return scope, err
	}
	es, err := agentExecutions(q, e.SessionID)
	if err != nil {
		return scope, err
	}
	if len(es) == 0 || es[len(es)-1].ID != id {
		return scope, fail(409, "The designer execution has been superseded")
	}
	return contract.DesignCaptureScope{Project: "fleet", Task: task, Session: e.SessionID, Execution: id}, nil
}

// This pilot can return captures only to an origin running the signed native
// Linux staging implementation. Read the authenticated poll, not host setup.
func requireDesignCaptureOrigin(q *Request, host string) error {
	observed, err := unscoped(q, func() (db.HostObservationOfRow, error) {
		return q.db().HostObservationOf(q.ctx(), host)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var payload hostObservationPayload
	if err != nil || observed.LastSeenAt == nil || float64(nowMs())-q.app.effectiveHostSeen(host, float64(observed.LastSeenAt.UnixMilli())) >= hostStaleMs ||
		json.Unmarshal(observed.Payload, &payload) != nil || deref(payload.Platform) != string(contract.PlatformLinux) ||
		deref(payload.Version) == "" || !contract.HasCapability(payload.Capabilities, contract.DesignCaptureStageCapability) {
		return fail(403, "The designer origin needs the current signed Linux capture staging capability")
	}
	return nil
}

func captureCommandPayload(c agentCommand) (designCapturePayload, bool) {
	var p designCapturePayload
	ok := c.Kind == "script" && c.HostID == contract.DesignCaptureHost && c.ProjectID == "fleet" &&
		json.Unmarshal(c.Payload, &p) == nil && p.Command == contract.DesignCaptureScript &&
		p.DesignCapture.Request.Valid() && p.DesignCapture.Request.Job == c.ExecutionID && c.ID == c.ExecutionID &&
		p.DesignCapture.Producer.Valid() && p.DesignCapture.Producer.Commit == p.DesignCapture.Request.Base &&
		p.DesignCapture.InstallationDigest() != ""
	return p, ok
}

func designCapture(q *Request, r *contract.DesignCaptureRequest) (contract.DesignCaptureReply, error) {
	var out contract.DesignCaptureReply
	scope, err := designCaptureCaller(q)
	if err != nil {
		return out, err
	}
	if r.Action != contract.DesignCaptureActionStart && (r.OverlayArtifactId != nil || r.FixtureArtifactId != nil) {
		return out, fail(400, "Only capture start takes input artifact IDs")
	}
	if !captureRequestKey.MatchString(r.RequestId) {
		return out, fail(400, "Expected a stable capture requestId")
	}
	job := designCaptureJobID(scope.Execution)
	out.JobId = job
	c, err := commandGet(q, job)
	if errors.Is(err, errNotFound) && r.Action == contract.DesignCaptureActionStatus {
		if err := requireDesignCaptureOrigin(q, q.hostID()); err != nil {
			return out, err
		}
		cfg, err := loadDesignCaptureInstallation()
		if err != nil {
			return out, err
		}
		out.State = "configured"
		out.Result = map[string]json.RawMessage{"base": jsonBytes(cfg.Producer.Commit), "producer": jsonBytes(cfg.Producer)}
		return out, nil
	}
	if errors.Is(err, errNotFound) && string(r.Action) == "start" {
		return startDesignCapture(q, scope, r, out)
	}
	if err != nil {
		if errors.Is(err, errNotFound) {
			return out, fail(404, "No capture for this designer execution")
		}
		return out, err
	}
	p, ok := captureCommandPayload(c)
	if !ok || p.DesignCapture.Request.DesignCaptureScope != scope || p.DesignCapture.RequestID != r.RequestId {
		return out, fail(409, "Capture request identity differs")
	}
	if string(r.Action) == "start" && (len(p.Inputs) != 3 || p.Inputs[1].ArtifactId != deref(r.OverlayArtifactId) ||
		p.Inputs[2].ArtifactId != deref(r.FixtureArtifactId)) {
		return out, fail(409, "Capture request already has different immutable inputs")
	}
	e, err := getExecution(q, job)
	if err != nil {
		return out, err
	}
	out.State = e.State
	if e.State == "preparing" && !isTrue(e.StopRequested) && p.PermitExpiresAt <= time.Now().Unix() {
		if err := stopDesignerCapture(q, scope.Execution, "design-capture-permit-expired"); err != nil {
			return out, err
		}
		out.State = "cancelled"
		return out, nil
	}
	if string(r.Action) == "cancel" {
		if !isTrue(e.StopRequested) && e.terminal() {
			e.StopRequested = ptr(true)
			if err := executionPut(q, &e); err != nil {
				return out, err
			}
			out.State = "cancelled"
		} else if !isTrue(e.StopRequested) {
			if _, err := requestExecutionStopWithPolicy(q, job, false, "designer-capture-cancelled"); err != nil {
				return out, err
			}
			out.State = "stopping"
		}
		return out, nil
	}
	if isTrue(e.StopRequested) {
		out.State = "cancelled"
		return out, nil
	}
	if e.unconfirmed() {
		out.State = "unconfirmed"
		return out, nil
	}
	if string(r.Action) == "stage" && e.State == "succeeded" && c.Status == "completed" {
		return captureResult(q, p, out)
	}
	return out, nil
}

func startDesignCapture(q *Request, scope contract.DesignCaptureScope, r *contract.DesignCaptureRequest, out contract.DesignCaptureReply) (contract.DesignCaptureReply, error) {
	if err := requireDesignCaptureOrigin(q, q.hostID()); err != nil {
		return out, err
	}
	cfg, err := loadDesignCaptureInstallation()
	if err != nil {
		return out, err
	}
	ok, err := knownHost(q, cfg.Host)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, fail(503, "The approved capture host is unregistered")
	}
	base, err := loadCaptureArtifact(q, cfg.BaseArtifact, "", "", 60<<20)
	if err != nil {
		return out, err
	}
	overlay, err := loadCaptureArtifact(q, deref(r.OverlayArtifactId), scope.Execution, "", 3<<20)
	if err != nil {
		return out, err
	}
	fixture, err := loadCaptureArtifact(q, deref(r.FixtureArtifactId), scope.Execution, "", 256<<10)
	if err != nil {
		return out, err
	}
	job := out.JobId
	nonce := sha256.Sum256([]byte("fleet:design-capture-nonce:v1:" + job + ":" + r.RequestId))
	request := contract.DesignCaptureJobRequest{DesignCaptureScope: scope, Version: 1, Job: job,
		Nonce: hex.EncodeToString(nonce[:]), Base: cfg.Producer.Commit, BaseSHA256: base.ContentHash,
		OverlaySHA256: overlay.ContentHash, FixtureSHA256: fixture.ContentHash}
	if !request.Valid() {
		return out, fail(409, "Invalid capture job identity")
	}
	expires := time.Now().Add(15 * time.Minute).Unix()
	launch := contract.DesignCaptureCommand{RequestID: r.RequestId, Request: request, Producer: cfg.Producer, Runtime: cfg.Runtime, LaunchSHA256: cfg.LaunchSHA256}
	if _, err := capturePermit(launch, expires); err != nil {
		return out, err
	}
	p := designCapturePayload{Command: contract.DesignCaptureScript, TimeoutMs: contract.DesignCaptureTimeoutMS, PermitExpiresAt: expires,
		Inputs:        []contract.InputArtifact{{ArtifactId: base.ID, Path: "base.bundle"}, {ArtifactId: overlay.ID, Path: "overlay.json"}, {ArtifactId: fixture.ID, Path: "fixture.json"}},
		DesignCapture: launch}
	record := struct {
		ID                string   `json:"id"`
		ExecutionID       string   `json:"executionId"`
		HostID            string   `json:"hostId"`
		State             string   `json:"state"`
		CreatedAt         int64    `json:"createdAt"`
		Actor             string   `json:"actor"`
		ProjectID         string   `json:"projectId"`
		ParentExecutionID string   `json:"parentExecutionId"`
		DirectedBy        string   `json:"directedBy"`
		RequiredArtifacts []string `json:"requiredArtifacts"`
	}{ID: job, ExecutionID: job, HostID: cfg.Host, State: "preparing", CreatedAt: nowMs(),
		Actor: "program-worker", ProjectID: scope.Project, ParentExecutionID: scope.Execution,
		DirectedBy: q.person.Email, RequiredArtifacts: []string{"receipt.json", "capture.tar"}}
	if err := executionWrite(q, record); err != nil {
		return out, err
	}
	out.State = "preparing"
	return out, commandPut(q, &agentCommand{ID: job, ExecutionID: job, HostID: cfg.Host, ProjectID: scope.Project,
		Kind: "script", Payload: jsonBytes(p), Status: "pending", HandedOut: ptr(false)})
}

func captureResult(q *Request, p designCapturePayload, out contract.DesignCaptureReply) (contract.DesignCaptureReply, error) {
	rows, err := artifactsOf(q, out.JobId)
	if err != nil {
		return out, err
	}
	names := map[string]string{}
	for _, row := range rows {
		if row.Path != "capture.tar" && row.Path != "receipt.json" {
			continue
		}
		if names[row.Path] != "" {
			return out, fail(409, "Capture has conflicting stored outputs")
		}
		names[row.Path] = row.ID
	}
	a, err := loadCaptureArtifact(q, names["capture.tar"], out.JobId, "capture.tar", 100<<20)
	if err != nil {
		return out, err
	}
	r, err := loadCaptureArtifact(q, names["receipt.json"], out.JobId, "receipt.json", 8192)
	if err != nil {
		return out, err
	}
	b := p.DesignCapture
	result := contract.DesignCaptureReturn{CaptureArtifact: a.ID, ReceiptArtifact: r.ID, Job: contract.DesignCaptureStoredJob{
		Request: b.Request, RequestSHA256: b.Request.Digest(), ReceiptSHA256: r.ContentHash, Producer: b.Producer, Runtime: b.Runtime,
		Capture: contract.DesignCaptureArchive{Name: a.Name, Bytes: a.Size, SHA256: a.ContentHash}}}
	err = json.Unmarshal(jsonBytes(result), &out.Result)
	return out, err
}

func stopDesignerCapture(q *Request, execution, reason string) error {
	c, err := commandGet(q, designCaptureJobID(execution))
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	p, ok := captureCommandPayload(c)
	if !ok || p.DesignCapture.Request.Execution != execution {
		return nil
	}
	e, err := getExecution(q, c.ExecutionID)
	if err != nil || e.terminal() || isTrue(e.StopRequested) {
		return err
	}
	_, err = requestExecutionStopWithPolicy(q, c.ExecutionID, false, reason)
	return err
}

func init() { op("design_capture", designCapture) }

// The permit exists only in the host poll reply and its private command journal.
// The persisted command and every designer-facing reply omit it.
func capturePermit(job contract.DesignCaptureCommand, expires int64) (contract.DesignCapturePermit, error) {
	var empty contract.DesignCapturePermit
	path := os.Getenv("FLEET_DESIGN_CAPTURE_SIGNING_KEY_FILE")
	fd, err := os.Open(path)
	if err != nil {
		return empty, fail(503, "The PDS18 capture authorization signer is unavailable")
	}
	defer fd.Close()
	info, err := fd.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return empty, fail(503, "The PDS18 capture authorization signer is unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(fd, 128))
	seed, decode := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || decode != nil || len(seed) != ed25519.SeedSize {
		return empty, fail(503, "The PDS18 capture authorization signer is unavailable")
	}
	installation := job.InstallationDigest()
	if installation == "" {
		return empty, fail(503, "The PDS18 capture installation is invalid")
	}
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), job.Request.PermitMessage(expires, installation))
	return contract.DesignCapturePermit{ExpiresAt: expires, InstallationSHA256: installation, Signature: hex.EncodeToString(signature)}, nil
}

func withCapturePermit(cmd *contract.Command, permit contract.DesignCapturePermit) *contract.Command {
	out, script, payload := *cmd, *cmd.Script, *cmd.Script.Payload
	payload.Rest = maps.Clone(payload.Rest)
	if payload.Rest == nil {
		payload.Rest = make(map[string]json.RawMessage)
	}
	payload.Rest["designCapturePermit"] = jsonBytes(permit)
	script.Payload, out.Script = &payload, &script
	return &out
}
