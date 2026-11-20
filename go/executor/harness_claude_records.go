package main

// Claude Code stream-json records the executor reads (`claude -p --output-format stream-json`): the assistant/user
// message content blocks and the rate_limit_event. The record itself is forwarded as received.

// claudeMessage: the message of an assistant or user record.
type claudeMessage struct {
	Content []claudeContent `json:"content"`
}

// claudeContent: one content block: text, thinking, tool_use or tool_result.
type claudeContent struct {
	Type      string    `json:"type"`
	ID        jsonValue `json:"id"`          // tool_use
	ToolUseID jsonValue `json:"tool_use_id"` // tool_result
	Name      any       `json:"name"`
	Input     any       `json:"input"`
	Content   any       `json:"content"`
	IsError   any       `json:"is_error"`
	Text      any       `json:"text"`
	Thinking  any       `json:"thinking"`
}

// claudeContentOf: the content blocks of an event's message.
func claudeContentOf(e harnessEvent) []claudeContent { return typed[claudeMessage](e.Message).Content }

// claudeRateLimitEvent: a rate_limit_event; older releases put the limit fields on the record itself.
type claudeRateLimitEvent struct {
	Type          string    `json:"type"`
	RateLimitInfo jsonValue `json:"rate_limit_info"`
}

// claudeRateLimit: the limit fields of a rate_limit_event.
type claudeRateLimit struct {
	RateLimitType string  `json:"rateLimitType"`
	ResetsAt      any     `json:"resetsAt"`
	Status        *string `json:"status"`
	Utilization   any     `json:"utilization"`
}
