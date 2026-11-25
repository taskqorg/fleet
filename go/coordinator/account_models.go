package main

// J5 account compatibility (docs/designs/fleet-ia-and-journeys.md section 3): each binding of a subscription carries the
// models its host's vendor CLI offered at inspection (probe) and the models a run on it failed with as not found
// (learned). The router skips a binding for a model it knows the binding does not serve; an unknown list never skips.

import (
	"encoding/json"
	"regexp"
	"strings"
)

// modelOffered: whether a probed list names the model. Vendor lists mix full ids and aliases ("opus" for
// "claude-opus-5-5"), so either name containing the other counts; a false skip costs a run, a false pass costs nothing new.
func modelOffered(list []string, model string) bool {
	m := strings.ToLower(model)
	for _, x := range list {
		x = strings.ToLower(x)
		if x == m || strings.Contains(m, x) || strings.Contains(x, m) {
			return true
		}
	}
	return false
}

// modelRefusal: why this binding cannot serve model, or "" when it can or nobody knows.
func (b accountBinding) modelRefusal(model string) string {
	if model == "" {
		return ""
	}
	if _, ok := b.RejectedModels[model]; ok {
		return "rejected " + model + " after start (modelNotFound)"
	}
	if b.Models != nil && !modelOffered(b.Models, model) {
		return "does not offer " + model + " (probe)"
	}
	return ""
}

// rejectedModelText: a run failure that says the vendor does not know the requested model.
var rejectedModelText = regexp.MustCompile(`(?i)model_?not_?found|modelNotFound|(unknown|invalid|unsupported) model|model[^\n]{0,80}(not found|does not exist|not available|not supported|is not supported|unsupported)`)

// noteModelRejected records, on the account's binding on host, that a run there failed because the vendor rejected
// model. The next route decision skips that binding for the model until a fresh probe (account_connect) clears it.
func noteModelRejected(q *Request, accountID, host, model, problem string) error {
	if accountID == "" || host == "" || model == "" || !rejectedModelText.MatchString(problem) {
		return nil
	}
	a, err := accountGet(q, accountID)
	if err != nil {
		return nil // the account was removed; nothing to learn
	}
	changed := false
	for i, raw := range a.Bindings {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		var h string
		if json.Unmarshal(m["hostId"], &h) != nil || h != host {
			continue
		}
		rejected := map[string]int64{}
		_ = json.Unmarshal(m["rejectedModels"], &rejected)
		if _, ok := rejected[model]; ok {
			return nil
		}
		rejected[model] = nowMs()
		m["rejectedModels"] = jsonBytes(rejected)
		a.Bindings[i] = jsonBytes(m)
		changed = true
	}
	if !changed {
		return nil
	}
	return accountPut(q, a)
}
