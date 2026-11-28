package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fleet/contract"
)

func (e *executor) run(c M) error {
	cid, err := cmdID(c, "id")
	if err != nil {
		return err
	}
	execution, err := cmdID(c, "executionId")
	if err != nil {
		return err
	}
	session, err := workID(c) // its executionId for a host job without a session
	if err != nil {
		return err
	}
	body := enc(c)
	// The command as its contract type (contract.Command); a payload that does not fit is refused by its kind below.
	var cmd contract.Command
	typedErr := json.Unmarshal([]byte(body), &cmd)
	kind := contract.CommandKind(cmd.Kind())
	if typedErr != nil || cmd.Value() == nil {
		kind = commandKind(body) // a payload that does not fit, or a kind this executor does not know (refused below)
	}
	if typedErr != nil {
		cmd = contract.Command{Raw: json.RawMessage(body)} // receipts and events still read its identities
	}
	if old, ok := e.commandBody(cid, false); ok {
		if old != body {
			return errors.New("command identity conflict")
		}
		return nil
	}
	if err := e.inTx(func() error {
		if _, err := e.ex.Exec("INSERT INTO commands(id,body) VALUES(?,?)", cid, body); err != nil {
			return err
		}
		return e.receipt(cmd, "accepted", "")
	}); err != nil {
		return err
	}
	payload := c["payload"]
	switch kind {
	case contract.CommandKindStop:
		result := "unknown"
		if l, ok := e.live[execution]; ok {
			l.stopped = true
			if l.worker != nil {
				if workerStop(l.worker.sock, cid) == nil {
					result = "accepted"
				} else {
					l.cleanupUnknown = true
				}
			} else if l.cleanup() == nil {
				result = "accepted"
			} else {
				l.cleanupUnknown = true
			}
		} else if err := e.releaseLaunchWindow(execution, session); err != nil {
			return err
		}
		if err := e.receipt(cmd, result, ""); err != nil {
			return err
		}
		if result == "unknown" {
			_, err := e.db.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
			return err
		}
		return nil
	case contract.CommandKindSteer:
		var sp contract.SteerPayload
		perr := typedErr
		if perr == nil && cmd.Steer.Payload != nil {
			sp = *cmd.Steer.Payload
		}
		sent := false
		if l, ok := e.live[execution]; ok {
			if l.worker != nil {
				if perr == nil && sp.Text != "" {
					sent = workerSteer(l.worker.sock, cid, sp.Text)
				}
			} else if l.wire != nil {
				w := l.wire
				w.mu.Lock()
				if perr == nil && w.session != nil && w.stdin != nil && sp.Text != "" {
					if frame := w.session.Steer(sp.Text, cid); frame != nil {
						_, err := writeFrame(w.stdin, w.log, frame)
						sent = err == nil
					}
				}
				w.mu.Unlock()
			}
		}
		if sent {
			return nil
		}
		if perr != nil {
			if err := e.event(cmd, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid steer payload: " + perr.Error()}); err != nil {
				return err
			}
		}
		return e.finish(cmd, "failed", "")
	case contract.CommandKindReadiness:
		data := e.readiness(payload, os.Getenv("FLEET_NATIVE_ACCOUNTS"), e.forbidden())
		ok := data.Ok
		req, _ := contractOf[contract.ProjectPayload](payload)
		project, _ := projectContext(req.Project)
		e.recheckProjectSites(payload, project.AgentSiteAccess)
		if err := e.event(cmd, 0, contract.FleetEventTypeReadiness, data); err != nil {
			return err
		}
		if !ok {
			if err := e.event(cmd, 1, contract.FleetEventTypeReadinessFailed, contract.ReadinessFailed{Reason: readinessFailure(data)}); err != nil {
				return err
			}
		}
		return e.finish(cmd, okStatus(ok), "")
	case contract.CommandKindBaseUpdated:
		data, ok := checkoutSync(payload, e.host, projectRoots(), e.forbidden())
		k := contract.FleetEventTypeSyncFailed
		if ok {
			k = contract.FleetEventTypeCheckoutSynced
		}
		if err := e.event(cmd, 0, k, data); err != nil {
			return err
		}
		return e.finish(cmd, okStatus(ok), "")
	case contract.CommandKindTeardown:
		// Session finish: teardown script, keep on dirty/teardown failure, safe branch delete. Lookup only: never create a
		// worktree; the session dir must be a real dir under the executor or session roots. A payload that does not fit
		// its type deletes no branch.
		var tp contract.ProjectPayload
		if typedErr == nil && cmd.Teardown.Payload != nil {
			tp = *cmd.Teardown.Payload
		} else if typedErr != nil {
			// The payload does not fit: its project and working directory still locate the worktree; branch and
			// deleteBranch are left unset, so no branch is deleted.
			loose := typed[struct {
				Project jsonValue `json:"project"`
				Rest    struct {
					WorkingDirectory jsonValue `json:"workingDirectory"`
				}
			}](c["payload"])
			if m, ok := loose.Project.V.(M); ok {
				tp.Project = map[string]json.RawMessage{}
				for k, v := range m {
					tp.Project[k] = json.RawMessage(enc(v))
				}
			}
			if wd, ok := loose.Rest.WorkingDirectory.V.(string); ok {
				tp.Rest = map[string]json.RawMessage{"workingDirectory": json.RawMessage(enc(wd))}
			}
		}
		key := workspaceKey(cmd)
		dir := e.commandDir(session, key)
		data := refusedTeardown("no-worktree")
		if project, err := projectContext(tp.Project); err != nil {
			data = refusedTeardown(err.Error())
		} else if e.insideDirs(dir) && exists(filepath.Join(dir, "worktree", ".git")) {
			wd := typedRaw[*string](tp.Rest["workingDirectory"])
			repo, hasRepo := repoPath(wd, project, e.host)
			_, top, created, ok, err := worktree(dir, workspaceSession(session, key), repo, hasRepo, projectRootsFor(project, e.host), e.forbidden(), project, false)
			switch {
			case err != nil:
				data = refusedTeardown(err.Error())
			case ok && !created:
				personal := projectPersonal(tp.Project)
				data = teardownWith(dir, workspaceSession(session, key), top, project, teardownOptions{
					deleteBranch: tp.DeleteBranch != nil && *tp.DeleteBranch, detached: tp.Branch != nil && !*tp.Branch,
					dropOutput:        (tp.OutputsStored != nil && *tp.OutputsStored) || personal,
					ignoreUncommitted: personal && tp.Step != nil && *tp.Step})
			}
		}
		reason := str0(data.Reason)
		ok := data.Removed || reason == "uncommitted" || reason == "unpushed" || reason == "no-worktree"
		// The teardown command reuses the last execution, whose seq 0.. are taken by its turn: use a fresh seq.
		if err := e.event(cmd, uint64(nowMs()), contract.FleetEventTypeTeardown, data); err != nil {
			return err
		}
		return e.finish(cmd, okStatus(ok), "")
	case contract.CommandKindArtifactCopy, contract.CommandKindArtifactVerify:
		// Long transfers run on the transfer worker, which sends the final receipt; the journal row resumes them.
		b, _ := payload.(M) // open JSON: the transfer job is the payload as received, plus the command and spool
		b = maps.Clone(b)
		if b == nil {
			b = M{}
		}
		b["command"] = M{"id": cid, "executionId": execution}
		b["spool"] = filepath.Join(e.root, "spool")
		k := "verify"
		if kind == contract.CommandKindArtifactCopy {
			k = "copy"
		}
		if err := enqueue(e.db, cid, k, b); err != nil {
			return err
		}
		_, err := e.db.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
		return err
	case contract.CommandKindAccountInspect:
		return e.accountInspect(cmd)
	case contract.CommandKindConnectionRemove:
		if typedErr != nil || cmd.ConnectionRemove.Payload == nil || !removeConnection(connectionsDir(), cmd.ConnectionRemove.Payload.Connection) {
			return e.finish(cmd, "failed", "")
		}
		return e.finish(cmd, "completed", "")
	case contract.CommandKindPublishing:
		return e.runPublishingCommand(cmd, execution, cid)
	case contract.CommandKindScript:
		return e.script(cmd, execution, session)
	case contract.CommandKindStart, contract.CommandKindInput:
		p, _ := payload.(M) // open JSON: the harness adapters still read the start payload as a map
		return e.start(cmd, cid, execution, session, p)
	}
	return e.finish(cmd, "failed", "")
}

func (e *executor) start(c contract.Command, cid, execution, session string, p M) error {
	// A session with a recorded launch is running, or its process from before a restart is not confirmed gone.
	var busy string
	err := e.db.QueryRow("SELECT execution FROM launches WHERE session=? LIMIT 1", session).Scan(&busy)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if busy != "" {
		reason := "session busy: execution " + busy + " is live, or its process from before an executor restart is not confirmed gone"
		if _, live := e.live[busy]; !live {
			reason += "; a stop for execution " + busy + " accepts the uncertainty and releases the session"
		}
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	sp, err := startPayload(c)
	if err != nil {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid start payload: " + err.Error()}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	cfg, hasCfg := p["agentConfig"] // the profile as received: evidence and hashes keep its bytes
	var profile contract.Profile
	if hasCfg {
		if profile, err = contract.ParseProfile([]byte(enc(cfg))); err != nil {
			// The run fails with the validation message as its problem, never as an unreported end.
			if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid profile: " + err.Error()}); err != nil {
				return err
			}
			return e.finish(c, "failed", "")
		}
	}
	name := harnessName(sp)
	if hv, has := p["harness"]; has && hv == nil || p == nil && harnessNull(c) {
		name = "invalid"
	}
	key := str0(sp.WorkspaceKey)
	dir := e.commandDir(session, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !e.insideDirs(dir) {
		return e.finish(c, "failed", "")
	}
	if err := os.WriteFile(filepath.Join(dir, ".fleet-instructions.tmp"), []byte(str0(sp.Instructions)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, ".fleet-instructions.tmp"), filepath.Join(dir, "AGENTS.md")); err != nil {
		return err
	}
	// The coordinator owns the native id per harness:account continuation; no id means a new native session.
	native := str0(sp.NativeSessionId)
	if sp.NativeSessionId != nil && !safeNativeID(native) {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid native session id"}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	// failSeq: the first event seq a failure may use; the setup event takes 1, so a failure after it uses 2 (two events
	// with one id are a conflict the coordinator refuses, and the failure reason was lost that way).
	failSeq := uint64(1)
	fail := func(seq uint64, reason string) error {
		seq = max(seq, failSeq)
		if err := e.event(c, seq, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		return e.finish(c, "failed", native)
	}
	var evidence M
	selectedMCP := M{}
	if hasCfg {
		ev, selected, err := profilePrepareParsed(profile, cfg, str0(sp.Account), p["skills"])
		if err != nil {
			return fail(1, err.Error())
		}
		evidence = ev
		selectedMCP = selected
	}
	h := Harnesses[name]
	if h == nil {
		return fail(1, "unknown harness")
	}
	attachTurn(p, filepath.Join(dir, "input"), name) // the same folder as FLEET_INPUT_DIR below
	cmd, nativeInput, err := h.Command(dir, native, p)
	if err != nil {
		return fail(1, err.Error())
	}
	fleetTools := profile.FleetTools == nil || *profile.FleetTools
	collaboration := profile.CollaborationTools == nil || *profile.CollaborationTools
	if name == "codex" && !collaboration {
		cmd.Args = append(cmd.Args, "--disable", "multi_agent", "--disable", "multi_agent_v2")
	}
	project, err := projectContext(sp.Project)
	if err != nil {
		return fail(1, err.Error())
	}
	repo, hasRepo := repoPath(sp.WorkingDirectory, project, e.host)
	checkoutTop := ""
	w, top, created, hasW, err := worktree(dir, workspaceSession(session, key), repo, hasRepo, projectRootsFor(project, e.host), e.forbidden(), project, sp.Branch != nil && !*sp.Branch)
	if err != nil {
		return fail(1, err.Error())
	}
	if hasW {
		checkoutTop = top
		if created && project.Scripts != nil && project.Scripts.Setup != nil && str0(sp.Setup) != "skip" {
			r := projectScript("setup", *project.Scripts.Setup, top, w, session)
			failSeq = 2
			if err := e.event(c, 1, contract.FleetEventTypeSetup, r); err != nil {
				return err
			}
			if !r.Ok {
				return fail(2, "setup-failed")
			}
		}
		cmd.Dir = w
	}
	work := dir
	if hasW {
		work = w
	}
	// Output collected when the run ends, and the folder artifact_get downloads into.
	outputDir, inputDir := filepath.Join(work, "output"), filepath.Join(dir, "input")
	os.MkdirAll(outputDir, 0o755)
	os.MkdirAll(inputDir, 0o755)
	setEnvKV(cmd, "FLEET_OUTPUT_DIR="+outputDir, "FLEET_INPUT_DIR="+inputDir)
	if err := e.stageArtifactInputs(sp.Inputs, inputDir, execution); err != nil {
		return fail(1, err.Error())
	}
	resolved, err := resolveAttemptContext(project, e.host, projectRootsFor(project, e.host), e.forbidden(), inputDir)
	if err != nil {
		return fail(1, err.Error())
	}
	p, evidence = prepareInstructions(cmd, name, p, str0(sp.Instructions), resolved, evidence)
	if err := e.event(c, 0, contract.FleetEventTypePreparation, evidence); err != nil {
		return err
	}
	var gitAccess []string
	if sp.Project != nil {
		// Repo of this host's environment (env.repo), else the project's single/first repo.
		var envRepo *string
		for _, en := range project.Environments {
			if str0(en.HostId) == e.host {
				envRepo = en.Repo
				break
			}
		}
		access, err := gitEnv(project, gitRepo(project, envRepo), connectionsDir())
		if err != nil {
			return fail(1, err.Error())
		}
		gitAccess = access
		gitApply(cmd, access)
	}
	requirements := contract.ProjectExecution{}
	if r := projectRequirements(project, profile.Id); r != nil {
		requirements = *r
	}
	var browserCfg M
	var browserAccess *browserAuthority
	for _, tool := range requirements.Tools {
		var err error
		if tool == "browser" {
			browserCfg, browserAccess, err = e.prepareExecutionBrowser(p, execution, commandHome(cmd), project.AgentSiteAccess != nil)
			if err != nil {
				return fail(1, "coding-tool-unready: browser ("+err.Error()+")")
			}
		} else {
			err = codingTool(tool, commandHome(cmd))
		}
		if err != nil {
			return fail(1, "coding-tool-unready: "+tool)
		}
	}
	observed := e.observeHarness(cmd, name)
	servers := mcpAtRoot(selectedMCP, work)
	codeReadRoots := []string{work, inputDir}
	captureDir, launched := "", false
	defer func() {
		if captureDir != "" && !launched {
			_ = os.Remove(captureDir)
			_ = os.Remove(filepath.Dir(captureDir))
		}
	}()
	if browserCfg != nil {
		dir := browserCaptureDir(e.root, execution)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(1, "browser capture directory: "+err.Error())
		}
		captureDir = dir
		browserCfg["captureDir"] = captureDir
		if hasW && str0(sp.ProjectId) == "fleet" && profile.Id == "codex-designer" &&
			sp.Branch != nil && !*sp.Branch && (sp.ReadOnly == nil || !*sp.ReadOnly) {
			if browserAccess == nil {
				node, err := managedBrowserRuntime(browserCfg)
				if err != nil {
					return fail(1, "private scene browser: "+err.Error())
				}
				browserAccess = &browserAuthority{Config: browserCfg, Node: node}
			}
			browserCfg["privateScenes"] = true
			scope := browserSceneScope{Checkout: work, Execution: execution}
			browserAccess.Scene = &scope
			setEnvKV(cmd, "FLEET_SCENE_DIR="+scope.directory())
		}
		codeReadRoots = append(codeReadRoots, captureDir)
		servers["project-browser"] = projectBrowserServer(browserCfg, browserAccess != nil)
	}
	if env := requirements.Environment; env != nil {
		if !hasW {
			return fail(1, "project-env-unready: no worktree")
		}
		servers["project-env"] = projectEnvServer(str0(sp.ProjectId), env, workspaceSession(session, key), w)
	}
	if requirements.Forge != nil && *requirements.Forge {
		repo := gitRepo(project, nil)
		if repo == nil {
			repo = &contract.ProjectRepo{}
		}
		if reason := publishingReadiness(str0(repo.Remote), str0(repo.Forge), str0(repo.Host)); reason != "" {
			return fail(1, reason)
		}
		ref, refOK := contract.ParseRepoRemote(str0(repo.Remote), str0(repo.Forge), str0(repo.Host))
		store := os.Getenv("FLEET_FORGE_CONFIG_DIR")
		branchBytes, err := exec.Command("git", "-C", work, "symbolic-ref", "--short", "HEAD").Output()
		if err != nil || !refOK || !forgeRepoGrantedRef(ref) || !filepath.IsAbs(store) {
			return fail(1, "project-forge-unready")
		}
		prefix, mergeMethod := "fleet", "squash"
		var autoMerge bool
		if project.Worktree != nil && str0(project.Worktree.BranchPrefix) != "" {
			prefix = str0(project.Worktree.BranchPrefix)
		}
		if in := project.Integration; in != nil {
			if m := str0(in.MergeMethod); m != "" {
				mergeMethod = m
			}
			if in.AutoMerge != nil {
				autoMerge = *in.AutoMerge
			}
		}
		base := str0(repo.DefaultBranch)
		if base == "" {
			base = "main"
		}
		context := forgeContext{
			Repositories:     os.Getenv("FLEET_FORGE_REPOSITORIES"),
			CredentialHome:   os.Getenv("HOME"),
			Provider:         ref.Provider,
			Host:             ref.Host,
			Repo:             ref.Slug,
			Remote:           str0(repo.Remote),
			Connection:       str0(repo.Connection),
			ConnectionsDir:   connectionsDir(),
			Base:             base,
			Branch:           strings.TrimSpace(string(branchBytes)),
			SessionBranch:    sessionBranch(project, session),
			Prefix:           prefix,
			AutoMerge:        autoMerge,
			MergeMethod:      mergeMethod,
			Workdir:          work,
			Home:             commandHome(cmd),
			Store:            store,
			GlabStore:        resolveGlabStore(store),
			HostId:           e.host,
			CommandId:        cid,
			ExecutionId:      execution,
			ProjectId:        str0(sp.ProjectId),
			RepoId:           str0(repo.Id),
			Journal:          filepath.Join(e.root, "journal.sqlite"),
			ReadOnly:         sp.ReadOnly != nil && *sp.ReadOnly,
			ReportPublishing: true,
		}
		servers["project-publishing"] = M{"command": exe(), "args": []any{"mcp", "project-publishing", enc(context)}, "tools": []any{"capabilities", "publish_draft", "status", "ci_logs", "rerun_failed", "ci_recovery", "record_code_review", "mark_ready", "request_merge", "cancel_merge_request"}}
		servers["project-forge"] = M{"command": exe(), "args": []any{"mcp", "project-forge", enc(context)}, "tools": []any{"publish_draft", "delivery", "ci_logs"}}
	}
	profiles := anyStrings(profile.DelegationProfiles)
	fleetMCP := []any{}
	var codeFleetTools *codeFleet
	if fleetTools {
		var projectID any
		if sp.ProjectId != nil {
			projectID = *sp.ProjectId
		}
		context := M{"hostId": e.host, "commandId": cid, "executionId": execution, "profiles": profiles, "projectId": projectID, "workdir": work, "tempDir": runTempDir(e.root, execution), "inputDir": inputDir, "spool": filepath.Join(e.root, "spool"), "operations": p["fleetOperations"]}
		journal := filepath.Join(e.root, "journal.sqlite")
		if err := os.MkdirAll(filepath.Join(e.root, "runs"), 0o700); err != nil {
			return err
		}
		server, err := delegationServer(context, journal, fleetContextFile(e.root, execution))
		if err != nil {
			return fail(1, "fleet MCP context: "+err.Error())
		}
		codeFleetTools = &codeFleet{journal, context}
		fleetMCP = jArr(server, "tools")
		servers["fleet"] = server
	}
	observed["fleetTools"], observed["collaborationTools"] = fleetTools, collaboration
	observed["cwd"], observed["worktree"], observed["platform"] = work, hasW, platform()
	// Granted connections: one `<executor> mcp connection` server each, calling fleet-gateway with this run's token. The
	// token and gateway URL reach the servers through the private run file below, never the harness environment or argv.
	var connectionTools []string
	runFile := ""
	defer func() {
		if runFile != "" && !launched {
			os.Remove(runFile) // K2-6: a launch that failed never needs its run token
		}
		if !launched {
			os.Remove(codeSpecFile(e.root, execution))     // nor its code server spec
			os.Remove(fleetContextFile(e.root, execution)) // nor its Fleet server context
		}
	}()
	granted := sp.Connections
	if len(granted) > 0 {
		gateway := os.Getenv("FLEET_GATEWAY_URL")
		if gateway == "" || sp.RunToken == nil {
			return fail(1, "connections granted but FLEET_GATEWAY_URL or the run token is missing")
		}
		// The run token, gateway URL and Access file path reach only the connection servers, through a private run file
		// in the executor's data directory; the harness environment never carries them. The work and input folders are
		// the base of relative upload paths and the parent of downloaded attachments (fleet-connections.md §9).
		if strings.ContainsAny(work+inputDir+gateway+str0(sp.RunToken)+accessFile(), "\n\r") {
			return fail(1, "a run file value holds a line break") // it would add a key line of its own
		}
		lines := "FLEET_RUN_TOKEN=" + str0(sp.RunToken) + "\nFLEET_GATEWAY_URL=" + gateway + "\nFLEET_WORK_DIR=" + work + "\nFLEET_INPUT_DIR=" + inputDir + "\n"
		if f := accessFile(); f != "" && isFile(f) {
			lines += "FLEET_CF_ACCESS_FILE=" + f + "\n"
		}
		lines += uploadDenyLines(e.root)
		dir := filepath.Join(e.root, "runs")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		runFile = filepath.Join(dir, execution+".env")
		if err := os.WriteFile(runFile, []byte(lines), 0o600); err != nil {
			return err
		}
	}
	gatewayConns := map[string]bool{}
	codeMode := ""
	if profile.CodeMode != nil {
		codeMode = string(*profile.CodeMode)
	}
	var bridged []any // the granted connections with the tools their direct servers expose, for code mode
	for _, raw := range granted {
		// A grant names its connection and tools; the rest of the spec reaches the connection server as sent.
		spec := decRaw(raw) // decoded once: the checked name and tools are those of the spec forwarded
		grant := typed[struct {
			Name  string `json:"name"`
			Tools []any  `json:"tools"`
		}](spec)
		conn := grant.Name
		if conn == "" || !onlyBytes(conn, func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' }) {
			continue
		}
		tools, specs := []any{}, []any{}
		for _, t := range grant.Tools {
			if n := typed[struct {
				Name string `json:"name"`
			}](t).Name; n != "" && onlyBytes(n, func(b byte) bool { return alnum(b) || b == '_' }) {
				tools, specs = append(tools, n), append(specs, jClone(t))
				connectionTools = append(connectionTools, "mcp__"+conn+"__"+n)
			}
		}
		if codeMode != "" {
			specMap, _ := spec.(M)
			b := maps.Clone(specMap)
			if b == nil {
				b = M{}
			}
			b["tools"] = specs
			bridged = append(bridged, b)
		}
		if _, taken := servers[conn]; taken {
			return fail(1, "MCP server name collides with a granted connection")
		}
		servers[conn] = M{"command": exe(), "args": []any{"mcp", "connection", conn, enc(spec), runFile}, "tools": tools, "env": []any{"HOME"}}
		gatewayConns[conn] = true
	}
	// Code mode (docs/designs/fleet-code-mode.md): one `code` server bridges the fleet server and the connection servers.
	// gitNetwork: Codex's sandbox network on and the checkout's common .git writable; code mode's sh gets the same.
	var gitRoots []any
	if name == "codex" && sp.GitNetwork != nil && *sp.GitNetwork && checkoutTop != "" {
		if gitRoots, err = nativeGitRoots(checkoutTop, work); err != nil {
			return fail(1, err.Error())
		}
	}
	if codeMode != "" {
		auth := codeAuthorityFor(name, cfg, work, codeReadRoots, gitRoots, sp.GitNetwork != nil && *sp.GitNetwork, sp.ReadOnly != nil && *sp.ReadOnly, e.root)
		auth.env = M{"FLEET_INPUT_DIR": inputDir, "FLEET_OUTPUT_DIR": outputDir}
		if sp.Project != nil {
			auth.gitEnv = anyStrings(gitAccess)
		}
		// The worktree's Git directories stay readable to a sandboxed sh that hides the data directory. They come from the
		// checkout the executor made, now, never from .git files the sandboxed commands can rewrite.
		if jAt(auth.shell, "sandbox") == true && checkoutTop != "" {
			roots := gitRoots
			if roots == nil {
				roots, _ = nativeGitRoots(checkoutTop, work) // on a failure no extra read: sh lacks Git, nothing more
			}
			auth.shell["read"] = append(jArr(auth.shell, "read"), roots...)
		}
		if jAt(auth.shell, "sandbox") == true {
			auth.shell["pinned"] = true
			workRoot, err := canon(work)
			if err != nil {
				return fail(1, "code-mode work root: "+err.Error())
			}
			for _, key := range []string{"read", "write", "hide"} {
				var paths []any
				for _, path := range jArr(auth.shell, key) {
					s, _ := str(path) // the roots above are strings
					c, err := canon(s)
					if err != nil {
						return fail(1, "code-mode shell root: "+err.Error())
					}
					// The work root already covers its children. Avoid a separate bind of a movable descendant.
					if key == "read" && c != workRoot && under(c, workRoot) {
						continue
					}
					paths = append(paths, c)
				}
				auth.shell[key] = paths
			}
		}
		if err := addCodeServer(servers, codeMode, codeFleetTools, bridged, runFile, codeSpecFile(e.root, execution), work, auth); err != nil {
			return fail(1, err.Error())
		}
		observed["codeMode"], observed["codeTools"] = codeMode, jAt(servers, "code", "tools")
		if codeMode == "only" {
			connectionTools, fleetMCP = nil, []any{} // the fleet server is gone; its tools are reached through code
			if name == "codex" {
				// The shell is hidden: sh in code replaces it. Codex 0.167 has no switch for apply_patch.
				cmd.Args = append(cmd.Args, "--disable", "shell_tool", "--disable", "unified_exec")
			}
		}
	}
	// Codex: the fleet server's tools; other harnesses: the launch's servers without the connection servers.
	if name == "codex" {
		observed["fleetMcpTools"] = fleetMCP
	} else {
		var direct []string
		for _, k := range sortedKeys(servers) {
			if !gatewayConns[k] {
				direct = append(direct, k)
			}
		}
		observed["fleetMcpTools"] = anyStrings(direct)
	}
	// Built-in web for pipeline agent steps: off unless the step's run sets webRoute builtin.
	if name == "codex" && sp.WebRoute != nil {
		if str0(sp.WebRoute) == "builtin" {
			cmd.Args = append(cmd.Args, "-c", `web_search="live"`)
		} else {
			cmd.Args = append(cmd.Args, "-c", `web_search="disabled"`)
		}
	}
	extra := connectionTools
	if name == "claude" {
		if str0(sp.WebRoute) == "builtin" {
			extra = append(extra, "WebSearch", "WebFetch")
		}
		if len(sp.Skills) > 0 {
			extra = append(extra, "Skill")
		}
	}
	root := dir
	if hasW {
		root = w
	}
	if err := e.launch(execution, cid, session); err != nil {
		return err
	}
	const harnessLaunchSeq = 3
	spec := workerSpec{
		Kind:         "harness",
		Execution:    execution,
		CommandId:    cid,
		Session:      session,
		DataDir:      e.root,
		Dir:          dir,
		Work:         work,
		Root:         root,
		InputDir:     inputDir,
		Harness:      name,
		ACP:          acp(name),
		Native:       native,
		NativeInput:  string(nativeInput),
		CmdPath:      cmd.Path,
		CmdArgs:      cmd.Args,
		CmdEnv:       cmd.Env,
		CmdDir:       cmd.Dir,
		Profile:      p,
		Servers:      servers,
		Extra:        extra,
		GitRoots:     gitRoots,
		GatewayConns: gatewayConns,
		UseMCP:       len(servers) > 0,
		InitialSeq:   harnessLaunchSeq,
	}
	l, err := e.spawnWorker(c, spec, browserAccess)
	if err != nil {
		removeWorkerDir(e.root, execution)
		e.release(execution)
		if errors.Is(err, errWorkerStartUnknown) {
			captureDir = "" // Keep staging for a worker that may still start.
			return e.finish(c, "unknown", native)
		}
		return fail(1, err.Error())
	}
	e.live[execution], launched = l, true
	if err := e.event(c, harnessLaunchSeq, contract.FleetEventTypeHarness, observed); err != nil {
		return err
	}
	return e.receipt(c, "started", native)
}

// codexLaunchProfile gives writable native turns a Go cache beside the worktree.
func codexLaunchProfile(cmd *exec.Cmd, dir string, p M, gitRoots []any) (M, error) {
	if jIsTrue(p, "readOnly") {
		return p, nil
	}
	dir, err := canon(dir)
	if err != nil {
		return nil, err
	}
	cache := filepath.Join(dir, "go-cache")
	if err := os.Mkdir(cache, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	st, err := os.Lstat(cache)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm() != 0o700 || owner(st) != os.Geteuid() {
		return nil, errors.New("Go cache must be a real private directory owned by the executor user")
	}
	setEnv(cmd, "GOCACHE", filepath.Join(cache, "build"))
	setEnv(cmd, "GOMODCACHE", filepath.Join(cache, "modules"))
	setEnv(cmd, "GOPATH", filepath.Join(cache, "path")) // go install and tools that read GOPATH, not $HOME/go
	profile := maps.Clone(p)
	profile["writableRoots"] = append([]any{cache}, gitRoots...)
	return profile, nil
}

// observeHarness: harness version and model-visible built-in tools, cached per executable and argument set.
func (e *executor) observeHarness(cmd *exec.Cmd, harness string) M {
	exe := cmd.Path
	args := cmd.Args[1:]
	var flags []string
	for i := 0; i+1 < len(args); i++ {
		if contains([]string{"-c", "--enable", "--disable"}, args[i]) {
			flags = append(flags, args[i], args[i+1])
		}
	}
	key := harness + "\x00" + exe + "\x00"
	if harness == "codex" {
		key += strings.Join(flags, "\x00")
	}
	if v, ok := e.harnessCache[key]; ok {
		return maps.Clone(v) // callers set top-level keys only
	}
	var version any
	if o, err := exec.Command(exe, "--version").Output(); err == nil {
		version = strings.TrimSpace(string(o))
	}
	v := M{"harness": harness, "executable": exe, "version": version}
	switch harness {
	case "codex":
		// Same env, cwd and -c/--enable/--disable flags as the run; the prompt text is not sent anywhere.
		pargs := []string{"debug", "prompt-input"}
		for i := 0; i+1 < len(args); {
			if contains([]string{"-c", "--enable", "--disable"}, args[i]) {
				pargs = append(pargs, args[i], args[i+1])
				i += 2
			} else {
				i++
			}
		}
		probe := exec.Command(exe, append(pargs, "tools")...)
		probe.Env, probe.Dir = cmd.Env, cmd.Dir
		if probe.Env == nil {
			probe.Env = []string{}
		}
		var tools any
		if o, err := probe.Output(); err == nil {
			if all, err := dec(string(o)); err == nil {
				// A tool group {tools: [...]} nests; a leaf names a tool. Anything else is skipped.
				type node struct {
					Tools []any   `json:"tools"`
					Name  *string `json:"name"`
				}
				out := []any{}
				var walk func(v any)
				walk = func(v any) {
					for _, t := range typed[node](v).Tools {
						if x := typed[node](t); x.Tools != nil {
							walk(t)
						} else if x.Name != nil {
							out = append(out, *x.Name)
						}
					}
				}
				items, _ := all.([]any)
				for _, it := range items {
					walk(it)
				}
				tools = out
			}
		}
		v["builtinTools"] = tools
	case "claude":
		v["builtinTools"] = "reported in native system/init event"
	default:
		v["builtinTools"] = "not exposed by ACP"
	}
	e.harnessCache[key] = v
	return maps.Clone(v)
}

// script: a lab script step: `sh -c <command>` in its own process group, in the trial's own worktree at the pinned commit
// (or the session dir when the payload names no repository). Stop and timeout kill the whole group. Full stdout and stderr
// (output/script.stdout, output/script.stderr) and everything in FLEET_OUTPUT_DIR become artifacts.
func (e *executor) script(c contract.Command, execution, session string) error {
	dir := e.sessionDir(session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !e.insideDirs(dir) {
		return e.finish(c, "failed", "")
	}
	personal := personalScriptJob(c)
	staging := false
	refuse := func(reason string) error {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		if personal && staging {
			if err := e.event(c, 2, contract.FleetEventTypeScriptPersonalCleanup, contract.ScriptPersonalCleanup{Removed: e.removeScriptStaging(dir)}); err != nil {
				return err
			}
		}
		return e.finish(c, "failed", "")
	}
	sp, err := payloadOf[contract.ScriptPayload](c)
	if err != nil {
		return refuse("invalid script payload: " + err.Error())
	}
	command := sp.Command
	if command == "" {
		return refuse("script command missing")
	}
	if sp.TimeoutMs != nil {
		if *sp.TimeoutMs <= 0 || *sp.TimeoutMs > math.MaxInt64/int64(time.Millisecond) {
			return refuse("timeout_ms must be a positive integer")
		}
	}
	staging = true
	output, input := filepath.Join(dir, "output"), filepath.Join(dir, "input")
	for _, path := range []string{output, input} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			if personal {
				return refuse("create script staging: " + err.Error())
			}
			return err
		}
	}
	if err := e.stageArtifactInputs(sp.Inputs, input, execution); err != nil {
		return refuse(err.Error())
	}
	project, err := projectContext(sp.Project)
	if err != nil {
		return refuse(err.Error())
	}
	wd := typedRaw[*string](sp.Rest["workingDirectory"])
	repo, hasRepo := repoPath(wd, project, e.host)
	work := dir
	var trial *[2]string
	switch {
	case hasRepo && sp.Commit != nil:
		w, top, err := trialWorktree(dir, repo, *sp.Commit, projectRootsFor(project, e.host), e.forbidden())
		if err != nil {
			return refuse(err.Error())
		}
		work, trial = w, &[2]string{top, w}
	case !hasRepo && sp.Commit == nil:
	default:
		return refuse("script needs both a repository and a commit, or neither")
	}
	dropTrial := func() {
		if trial != nil {
			removeTrial(trial[0], trial[1])
		}
	}
	cmd := cleanCmd("sh", "-c", command)
	cmd.Dir = work
	realSession, _ := idOf(c, "sessionId") // "" for a host job: session is then its executionId, not a session
	cmd.Env = append(cmd.Env, "FLEET_INPUT_DIR="+input, "FLEET_OUTPUT_DIR="+output, "FLEET_SESSION_ID="+realSession, "FLEET_EXECUTION_ID="+execution,
		// Lab scripts ship in the release next to the binary (fleet-asset-pipelines §4); Blender is a host setting.
		"FLEET_LAB_SCRIPTS="+filepath.Join(filepath.Dir(exe()), "scripts", "lab"))
	if v := os.Getenv("FLEET_BLENDER"); v != "" {
		cmd.Env = append(cmd.Env, "FLEET_BLENDER="+v)
	}
	cid, _ := idOf(c, "id")
	if err := e.launch(execution, cid, session); err != nil {
		dropTrial()
		if personal {
			return refuse("journal script launch: " + err.Error())
		}
		return err
	}
	spec := workerSpec{
		Kind:       "script",
		Execution:  execution,
		CommandId:  cid,
		Session:    session,
		DataDir:    e.root,
		Dir:        dir,
		Work:       work,
		Root:       work,
		InputDir:   input,
		CmdPath:    cmd.Path,
		CmdArgs:    cmd.Args,
		CmdEnv:     cmd.Env,
		CmdDir:     cmd.Dir,
		UseMCP:     true,
		TimeoutMs:  sp.TimeoutMs,
		Trial:      trial,
		InitialSeq: 2,
	}
	l, err := e.spawnWorker(c, spec, nil)
	if err != nil {
		if personal && errors.Is(err, errWorkerStartUnknown) {
			return e.finishUnconfirmedScript(c)
		}
		removeWorkerDir(e.root, execution)
		dropTrial()
		e.release(execution)
		if errors.Is(err, errWorkerStartUnknown) {
			return e.finish(c, "unknown", "")
		}
		return refuse(err.Error())
	}
	e.live[execution] = l
	if err := e.event(c, 1, contract.FleetEventTypeScriptStarted, contract.ScriptStarted{Commit: sp.Commit, TimeoutMs: sp.TimeoutMs, TrialWorktree: work}); err != nil {
		return err
	}
	return e.receipt(c, "started", "")
}

// stageInputs: payload inputs [{artifactId, path}] are downloaded to FLEET_INPUT_DIR/<path> before the step starts.
// Paths are relative and never leave the input folder.
func (e *executor) stageInputs(p M, input string, execution any) error {
	var in struct {
		Inputs []contract.InputArtifact `json:"inputs"`
	}
	if json.Unmarshal([]byte(enc(p)), &in) != nil {
		return errors.New("invalid input")
	}
	executionID, _ := execution.(string)
	return e.stageArtifactInputs(in.Inputs, input, executionID)
}

func (e *executor) stageArtifactInputs(list []contract.InputArtifact, input, executionID string) error {
	if len(list) == 0 {
		return nil
	}
	root, err := os.OpenRoot(input)
	if err != nil {
		return fmt.Errorf("input folder: %w", err)
	}
	defer root.Close()
	c := newTransferClient(e.url, e.token, e.host, accessToken(accessFile()))
	for _, i := range list {
		id, rel := i.ArtifactId, i.Path
		if strings.HasPrefix(rel, "/") || anyPart(rel, func(x string) bool { return x == "" || x == "." || x == ".." }) {
			return fmt.Errorf("invalid input path %s", rel)
		}
		r, f := transferReply(c, contract.RouteArtifactsGet, contract.HostArtifactGetRequest{HostId: e.host, ExecutionId: optStr(executionID), ArtifactId: id})
		if f != nil {
			return fmt.Errorf("input %s: %s", rel, f.msg)
		}
		if r.Url == nil {
			return fmt.Errorf("input %s unavailable", rel)
		}
		// Every component is resolved inside the held input root (os.Root): a link whose target leaves the input folder,
		// present or swapped in at any moment, is refused, so an input can only be created inside the input folder.
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return fmt.Errorf("input %s: %w", rel, err)
		}
		out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("input %s: %w", rel, err)
		}
		src, f := c.get(*r.Url)
		if f == nil {
			_, err = io.Copy(out, src)
			src.Close()
		} else {
			err = errors.New(f.msg)
		}
		out.Close()
		if err != nil {
			root.Remove(rel) // no partial input left behind
			return fmt.Errorf("input %s: %w", rel, err)
		}
	}
	return nil
}

func anyPart(rel string, bad func(string) bool) bool {
	for _, x := range strings.Split(rel, "/") {
		if bad(x) {
			return true
		}
	}
	return false
}

func anyStrings(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}
