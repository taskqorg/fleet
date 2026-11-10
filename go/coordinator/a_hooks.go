package main

// Area A seams into the other areas. Each is a function variable with a default that keeps the host side working alone;
// the owning area assigns it from its own init(). Owners: agent tools (agent-tools.ts), tasks (tasks.ts), artifacts,
// pipelines, projects and profiles, threads, connections.

// Agent tools.
var (
	// hookAgentReady: agent.ready(command) in the poll's ready filter (a steer of a finished execution waits forever).
	hookAgentReady = func(q *Request, c doc) (bool, error) {
		if c["kind"] != "steer" {
			return true, nil
		}
		e, err := aget(q, c["executionId"], "execution")
		return err == nil && !legacyTerminal(e), err
	}
	hookAgentPromote   = agentPromote
	hookAgentDecorate  = agentDecorate
	hookAgentOnReceipt = agentOnReceipt
	// hookAgentKnownNative: the native session id already bound to the execution's session, "" when none.
	hookAgentKnownNative   = agentKnownNative
	hookAgentCaptureNative = agentCaptureNative
)

// Tasks: ask a question, settle a task, take a task.
var (
	hookTaskTake = func(q *Request, taskID, taker string, from any) error { return takeTask(q, taskID, taker, astr(from)) }
	// hookTaskAsk: tasks.ask, a question row; returns its id.
	hookTaskAsk = askQuestion
)

// Artifacts and pipelines.
var (
	// hookArtifactGate: fields merged into an execution whose start command completed (artifacts.ts requirement).
	hookArtifactGate = func(q *Request, e doc, declared []any) (doc, error) { return doc{}, nil }
	// hookPipelineOnExecution: runner.onExecution after an execution ended or became unknown; runs after commit.
	hookPipelineOnExecution = func(a *App, executionID string) {}
	// hookReadArtifact: the bytes of a stored artifact (delegation website sources); 503 while no store is reachable.
	hookReadArtifact = func(a *App, id string) ([]byte, error) { return nil, fail(503, "No reachable store") }
	// hookPlacement: one placement pass (placement.ts runPlacement); artifacts area provides the S3 side.
	hookPlacement = func(a *App) error { return nil }
	// hookArtifactsOf: all('artifact', {executionId}) without contentBase64 (artifacts.ts adapter).
	hookArtifactsOf = func(q *Request, executionID string) ([]doc, error) { return []doc{}, nil }
)

// Projects, profiles, threads, connections.
var (
	// hookProjectSettings: project_context(projectId).settings; error when unknown.
	hookProjectSettings = func(id string) (doc, error) {
		p, err := projectContext(id)
		if err != nil {
			return nil, err
		}
		return mapOf(p["settings"]), nil
	}
	// hookProfile: readTrustedProfile(directory, id).
	hookProfile = readTrustedProfile
	// hookProfileDigest: profileDigest(agentConfig).
	hookProfileDigest = func(cfg doc) string { return profileDigest(cfg) }
	// hookReplyText: the assistant reply texts of an execution (threads.ts replyText).
	hookReplyText = replyText
	// hookConnectionsMint: a per-run token for granted connections; nil when none.
	hookConnectionsMint = func(q *Request, run doc, granted []string) (doc, error) { return nil, nil }
	// hookGitNetwork: gitNetwork(session).
	hookGitNetwork = func(s doc) bool {
		if cfg, ok := s["agentConfig"].(map[string]any); ok {
			if v, ok := cfg["gitNetwork"].(bool); ok {
				return v
			}
		}
		p := astr(s["projectId"])
		role := astr(s["role"])
		if p == "" || (role != "pm" && role != "reviewer" && role != "worker") {
			return false
		}
		st, err := hookProjectSettings(p)
		return err == nil && len(anys(st["repos"])) > 0
	}
)

func anys(v any) []any { xs, _ := v.([]any); return xs }

func projectSetting(projectID any, key string) any {
	p, ok := projectID.(string)
	if !ok || p == "" {
		return nil
	}
	st, err := hookProjectSettings(p)
	if err != nil {
		return nil
	}
	return st[key]
}
