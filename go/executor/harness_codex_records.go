package main

// Codex records the executor reads: app-server JSON-RPC results and notification params, the thread item (app-server
// item/completed and `codex exec --json` item.completed), and the rate-limit windows. Frames and items are forwarded
// as received.

// codexResult: the result of initialize (userAgent), thread/start|resume|fork (thread.id) and turn/start (turn.id).
type codexResult struct {
	UserAgent *string `json:"userAgent"`
	Thread    struct {
		ID *string `json:"id"`
	} `json:"thread"`
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

// codexParams: the params of the notifications a session reads.
type codexParams struct {
	ThreadID   *string   `json:"threadId"`
	TurnID     *string   `json:"turnId"`
	Turn       codexTurn `json:"turn"`
	Item       jsonValue `json:"item"`
	TokenUsage any       `json:"tokenUsage"`
	RateLimits any       `json:"rateLimits"`
}

type codexTurn struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Error      any    `json:"error"`
	DurationMs any    `json:"durationMs"`
}

// codexItemStrings: the item fields contracts Item declares as strings, as received.
type codexItemStrings struct {
	Type    jsonValue `json:"type"`
	Text    jsonValue `json:"text"`
	Command jsonValue `json:"command"`
	Name    jsonValue `json:"name"`
	Status  jsonValue `json:"status"`
}

// codexItem: one thread item, in either the app-server (camelCase) or the exec (snake_case) spelling.
type codexItem struct {
	Type                  string    `json:"type"`
	ID                    any       `json:"id"`
	Server                string    `json:"server"`
	Tool                  jsonValue `json:"tool"`
	Name                  jsonValue `json:"name"`
	Arguments             jsonValue `json:"arguments"`
	Command               jsonValue `json:"command"`
	Changes               jsonValue `json:"changes"`
	Result                jsonValue `json:"result"`
	AggregatedOutput      jsonValue `json:"aggregatedOutput"`
	AggregatedOutputSnake jsonValue `json:"aggregated_output"`
	Output                jsonValue `json:"output"`
	Error                 jsonValue `json:"error"`
	Message               jsonValue `json:"message"`
	Status                any       `json:"status"`
	ExitCode              jsonValue `json:"exitCode"`
	ExitCodeSnake         jsonValue `json:"exit_code"`
	StartedAtMs           any       `json:"startedAtMs"`
	CompletedAtMs         any       `json:"completedAtMs"`
	Text                  any       `json:"text"`
	Summary               any       `json:"summary"`
	Content               any       `json:"content"`
	// collabAgentToolCall (spawn_agent etc.)
	ReceiverThreadIDs []any          `json:"receiverThreadIds"`
	Model             any            `json:"model"`
	AgentsStates      map[string]any `json:"agentsStates"`
}

// codexRateLimits: account/rateLimits/updated (and read) windows.
type codexRateLimits struct {
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

// codexWindow: one rate-limit window, in either spelling.
type codexWindow struct {
	WindowDurationMins any `json:"windowDurationMins"`
	WindowMinutes      any `json:"window_minutes"`
	ResetsAt           any `json:"resetsAt"`
	ResetsAtSnake      any `json:"resets_at"`
	UsedPercent        any `json:"usedPercent"`
	UsedPercentSnake   any `json:"used_percent"`
}
