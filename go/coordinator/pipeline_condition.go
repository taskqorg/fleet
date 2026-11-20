package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"fleet/contract"
)

// pipelineRule: one JSON object of a step's when, field by field (validatePipelineCondition checks it before any run
// exists).
type pipelineRule = map[string]json.RawMessage

// pipelineWhen: a validated when (contract.PipelineBooleanCondition or PipelineEnumCondition) and the rule hash its
// decisions carry.
type pipelineWhen struct {
	Kind   string                           `json:"kind"`
	Source contract.PipelineConditionSource `json:"source"`
	Equals json.RawMessage                  `json:"equals"`
	Domain []string                         `json:"domain"`
	sha    string
}

// parsePipelineWhen: the when of a definition step (nil when it has none); v was validated with its definition.
func parsePipelineWhen(v any) (*pipelineWhen, error) {
	b := jsonBytes(v)
	if jsonKind(b) == 'n' || len(b) == 0 {
		return nil, nil
	}
	w := &pipelineWhen{}
	if err := json.Unmarshal(b, w); err != nil {
		return nil, fmt.Errorf("when is not a valid condition: %w", err)
	}
	w.sha = sha256hex([]byte(canonical(v)))
	return w, nil
}

// jsonKind: the first byte of a JSON value ('n' null, 't'/'f' Boolean, '"' string, '{' object, '[' array).
func jsonKind(raw []byte) byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0
	}
	return raw[0]
}

func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if jsonKind(raw) != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func jsonBool(raw json.RawMessage) (bool, bool) {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

func pipelineRuleOf(raw []byte) pipelineRule {
	var r pipelineRule
	if jsonKind(raw) != '{' || json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return r
}

func jsonList(raw json.RawMessage) []json.RawMessage {
	var l []json.RawMessage
	if jsonKind(raw) != '[' || json.Unmarshal(raw, &l) != nil {
		return nil
	}
	return l
}

func ruleKeysOnly(r pipelineRule, allowed ...string) bool {
	for k := range r {
		found := false
		for _, a := range allowed {
			found = found || k == a
		}
		if !found {
			return false
		}
	}
	return true
}

// validatePipelineCondition accepts one literal predicate over an earlier output.
// Project programs own thresholds and compound policies; this is not an expression interpreter.
func validatePipelineCondition(v any, earlier map[string]bool) error {
	w := pipelineRuleOf(jsonBytes(v))
	if w == nil {
		return fmt.Errorf("when must be a Boolean or enum condition")
	}
	s := pipelineRuleOf(w["source"])
	step, _ := jsonString(s["step"])
	if s == nil || !ruleKeysOnly(s, "step", "path") || !earlier[step] {
		return fmt.Errorf("when.source must name a strictly earlier step")
	}
	path := jsonList(s["path"])
	if len(path) == 0 {
		return fmt.Errorf("when.source.path must contain literal object keys")
	}
	for _, key := range path {
		if text, ok := jsonString(key); !ok || text == "" {
			return fmt.Errorf("when.source.path must contain nonempty string keys")
		}
	}
	kind, _ := jsonString(w["kind"])
	switch kind {
	case "boolean":
		if !ruleKeysOnly(w, "kind", "source", "equals") {
			return fmt.Errorf("Boolean when has an unknown field")
		}
		if _, ok := jsonBool(w["equals"]); !ok {
			return fmt.Errorf("Boolean when.equals must be an explicit Boolean")
		}
	case "enum":
		if !ruleKeysOnly(w, "kind", "source", "domain", "equals") {
			return fmt.Errorf("enum when has an unknown field")
		}
		domain, seen := jsonList(w["domain"]), map[string]bool{}
		if len(domain) == 0 {
			return fmt.Errorf("enum when.domain must be nonempty")
		}
		for _, value := range domain {
			text, ok := jsonString(value)
			if !ok || text == "" || seen[text] {
				return fmt.Errorf("enum when.domain must contain unique nonempty strings")
			}
			seen[text] = true
		}
		if value, ok := jsonString(w["equals"]); !ok || !seen[value] {
			return fmt.Errorf("enum when.equals must belong to its domain")
		}
	default:
		return fmt.Errorf("when.kind must be boolean or enum")
	}
	return nil
}

// unconditionalRuleSha: the rule hash of a step without when (the canonical {"kind":"unconditional"}).
var unconditionalRuleSha = sha256hex([]byte(`{"kind":"unconditional"}`))

// pipelineBlockedDecision: the decision of a when whose source evidence is unavailable or invalid (selectPipelineStep).
func pipelineBlockedDecision(when *pipelineWhen, reason contract.PipelinePlanReason, at int64) contract.PipelineConditionDecision {
	step := when.Source.Step
	return contract.PipelineConditionDecision{Kind: contract.PipelineDecisionKind(when.Kind), Outcome: contract.PipelineDecisionOutcomeBlocked, Reason: reason,
		EvaluatedAtMs: at, RuleSha256: when.sha, SourceStepId: &step, Path: when.Source.Path}
}

// pipelineConditionDecision runs only after the caller verified the source's
// successful observation, artifact ownership and exact bytes. Invalid evidence
// blocks; it is never a false decision or authority to dispatch a fallback.
func pipelineConditionDecision(when *pipelineWhen, output any, at int64) contract.PipelineConditionDecision {
	d := contract.PipelineConditionDecision{Kind: contract.PipelineDecisionKindUnconditional, Outcome: contract.PipelineDecisionOutcomeSelected,
		Reason: contract.PipelinePlanReasonUnconditional, EvaluatedAtMs: at, RuleSha256: unconditionalRuleSha}
	if when == nil {
		return d
	}
	d = pipelineBlockedDecision(when, "", at)
	block := func(reason contract.PipelinePlanReason) contract.PipelineConditionDecision {
		d.Outcome, d.Reason = contract.PipelineDecisionOutcomeBlocked, reason
		return d
	}
	value := json.RawMessage(jsonBytes(output))
	for _, key := range when.Source.Path {
		if jsonKind(value) == 'n' {
			return block(contract.PipelinePlanReasonConditionNull)
		}
		object := pipelineRuleOf(value)
		if object == nil {
			return block(contract.PipelinePlanReasonConditionType)
		}
		var present bool
		value, present = object[key]
		if !present {
			return block(contract.PipelinePlanReasonConditionMissing)
		}
	}
	if jsonKind(value) == 'n' {
		return block(contract.PipelinePlanReasonConditionNull)
	}
	match := false
	switch when.Kind {
	case "boolean":
		b, ok := jsonBool(value)
		if !ok {
			return block(contract.PipelinePlanReasonConditionType)
		}
		equals, _ := jsonBool(when.Equals)
		d.ObservedBoolean = &b
		match = b == equals
	case "enum":
		text, ok := jsonString(value)
		if !ok {
			return block(contract.PipelinePlanReasonConditionType)
		}
		d.ObservedString = &text
		found := false
		for _, allowed := range when.Domain {
			found = found || text == allowed
		}
		if !found {
			return block(contract.PipelinePlanReasonConditionDomain)
		}
		equals, _ := jsonString(when.Equals)
		match = text == equals
	default:
		return block(contract.PipelinePlanReasonConditionType)
	}
	d.Outcome, d.Reason = contract.PipelineDecisionOutcomeSelected, contract.PipelinePlanReasonConditionTrue
	if !match {
		d.Outcome, d.Reason = contract.PipelineDecisionOutcomeNotSelected, contract.PipelinePlanReasonConditionFalse
	}
	return d
}
