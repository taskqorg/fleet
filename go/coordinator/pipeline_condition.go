package main

import "fmt"

// validatePipelineCondition accepts one literal predicate over an earlier output.
// Project programs own thresholds and compound policies; this is not an expression interpreter.
func validatePipelineCondition(v any, earlier map[string]bool) error {
	w := mapOf(v)
	if w == nil {
		return fmt.Errorf("when must be a Boolean or enum condition")
	}
	s := mapOf(w["source"])
	if s == nil || !keysOnly(s, "step", "path") || !earlier[astr(s["step"])] {
		return fmt.Errorf("when.source must name a strictly earlier step")
	}
	path := anys(s["path"])
	if len(path) == 0 {
		return fmt.Errorf("when.source.path must contain literal object keys")
	}
	for _, key := range path {
		if text, ok := key.(string); !ok || text == "" {
			return fmt.Errorf("when.source.path must contain nonempty string keys")
		}
	}
	switch w["kind"] {
	case "boolean":
		if !keysOnly(w, "kind", "source", "equals") {
			return fmt.Errorf("Boolean when has an unknown field")
		}
		if _, ok := w["equals"].(bool); !ok {
			return fmt.Errorf("Boolean when.equals must be an explicit Boolean")
		}
	case "enum":
		if !keysOnly(w, "kind", "source", "domain", "equals") {
			return fmt.Errorf("enum when has an unknown field")
		}
		domain, seen := anys(w["domain"]), map[string]bool{}
		if len(domain) == 0 {
			return fmt.Errorf("enum when.domain must be nonempty")
		}
		for _, value := range domain {
			text, ok := value.(string)
			if !ok || text == "" || seen[text] {
				return fmt.Errorf("enum when.domain must contain unique nonempty strings")
			}
			seen[text] = true
		}
		if value, ok := w["equals"].(string); !ok || !seen[value] {
			return fmt.Errorf("enum when.equals must belong to its domain")
		}
	default:
		return fmt.Errorf("when.kind must be boolean or enum")
	}
	return nil
}

// pipelineConditionDecision runs only after the caller verified the source's
// successful observation, artifact ownership and exact bytes. Invalid evidence
// blocks; it is never a false decision or authority to dispatch a fallback.
func pipelineConditionDecision(when doc, output any, at int64) doc {
	rule := when
	if rule == nil {
		rule = doc{"kind": "unconditional"}
	}
	d := doc{"kind": rule["kind"], "outcome": "selected", "reason": "unconditional", "evaluatedAtMs": at, "ruleSha256": sha256hex([]byte(canonical(rule)))}
	if when == nil {
		return d
	}
	source := mapOf(when["source"])
	d["sourceStepId"], d["path"] = source["step"], source["path"]
	block := func(reason string) doc {
		d["outcome"], d["reason"] = "blocked", reason
		return d
	}
	value := output
	for _, key := range anys(source["path"]) {
		if value == nil {
			return block("condition_null")
		}
		object := mapOf(value)
		if object == nil {
			return block("condition_type")
		}
		var present bool
		value, present = object[astr(key)]
		if !present {
			return block("condition_missing")
		}
	}
	if value == nil {
		return block("condition_null")
	}
	match := false
	switch when["kind"] {
	case "boolean":
		b, ok := value.(bool)
		if !ok {
			return block("condition_type")
		}
		d["observedBoolean"] = b
		match = b == when["equals"]
	case "enum":
		text, ok := value.(string)
		if !ok {
			return block("condition_type")
		}
		d["observedString"] = text
		found := false
		for _, allowed := range anys(when["domain"]) {
			found = found || text == allowed
		}
		if !found {
			return block("condition_domain")
		}
		match = text == when["equals"]
	default:
		return block("condition_type")
	}
	if match {
		d["reason"] = "condition_true"
	} else {
		d["outcome"], d["reason"] = "not_selected", "condition_false"
	}
	return d
}
