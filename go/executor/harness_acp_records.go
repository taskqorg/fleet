package main

// ACP v1 records the executor reads: JSON-RPC results, the params of session/request_permission and session/update,
// the session update, and Antigravity's step-format records. Frames are forwarded as received.

// acpResult: the result fields of initialize, session/new|resume|load, session/set_* and session/prompt.
type acpResult struct {
	ProtocolVersion   any `json:"protocolVersion"`
	AgentCapabilities struct {
		SessionCapabilities struct {
			Resume jsonValue `json:"resume"`
		} `json:"sessionCapabilities"`
		LoadSession bool `json:"loadSession"`
	} `json:"agentCapabilities"`
	SessionID     *string   `json:"sessionId"`
	ConfigOptions jsonValue `json:"configOptions"`
	Modes         struct {
		AvailableModes []struct {
			ID *string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
	Models struct {
		AvailableModels []struct {
			ModelID *string `json:"modelId"`
		} `json:"availableModels"`
		CurrentModelID string `json:"currentModelId"`
	} `json:"models"`
	StopReason *string `json:"stopReason"`
	Usage      any     `json:"usage"`
}

func (r acpResult) configOptions() []acpConfigOption {
	return typed[[]acpConfigOption](r.ConfigOptions)
}

// acpConfigOption: one session config option (a select for model or mode).
type acpConfigOption struct {
	ID           string           `json:"id"`
	Category     string           `json:"category"`
	Type         string           `json:"type"`
	CurrentValue string           `json:"currentValue"`
	Options      []acpOptionValue `json:"options"`
}

// acpOptionValue: a select value, or a group of them.
type acpOptionValue struct {
	Value   *string          `json:"value"`
	Options []acpOptionValue `json:"options"`
}

// acpParams: the params of session/request_permission and session/update.
type acpParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		ToolCallID string `json:"toolCallId"`
		RawInput   struct {
			Path string `json:"Path"`
		} `json:"rawInput"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Kind     string `json:"kind"`
	} `json:"options"`
	Update jsonValue `json:"update"`
}

// acpUpdate: a session update (message and thought chunks, tool calls, usage).
type acpUpdate struct {
	SessionUpdate string    `json:"sessionUpdate"`
	ToolCallID    any       `json:"toolCallId"`
	Name          jsonValue `json:"name"`
	Title         jsonValue `json:"title"`
	Kind          any       `json:"kind"`
	Status        any       `json:"status"`
	RawInput      any       `json:"rawInput"`
	RawOutput     any       `json:"rawOutput"`
	Content       jsonValue `json:"content"`
	Used          any       `json:"used"`
	Size          any       `json:"size"`
}

// acpStepUpdate: an Antigravity step-format step_update.
type acpStepUpdate struct {
	TextDelta *string `json:"text_delta"`
	StepType  string  `json:"step_type"`
	StepIndex any     `json:"step_index"`
	ToolName  any     `json:"tool_name"`
	State     any     `json:"state"`
	ToolInfo  struct {
		Parameters any `json:"parameters"`
		Result     any `json:"result"`
	} `json:"tool_info"`
}

// acpPlannerCall: one tool call of an Antigravity PLANNER_RESPONSE.
type acpPlannerCall struct {
	ID        any       `json:"id"`
	Name      any       `json:"name"`
	Arguments jsonValue `json:"arguments"`
	Args      any       `json:"args"`
}
