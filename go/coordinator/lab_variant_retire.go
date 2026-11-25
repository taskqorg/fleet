package main

import (
	"encoding/json"
	"sort"

	"fleet/contract"
	"fleet/coordinator/db"
)

// labRetireVariants: a decided experiment's Lab variants leave the store (fleet-definition-editors.md, One home per
// kind: a variant belongs to its experiment). Every store-only agent or pipeline the experiment names (its pipeline, or a
// JSON string anywhere in its variants) is deleted as definition_delete deletes it, unless the decision adopts the
// variant that names it (that one goes to the repository by its pull request) or something else still uses it
// (definition_delete's own refusal: another live experiment, a project role, a pipeline step, an open session, an
// unfinished run). The revisions stay, so the experiment's trials keep the content they ran. It answers the deleted
// keys.
func labRetireVariants(q *Request, e db.Experiment, adopted string) ([]contract.DefinitionKey, error) {
	var variants []json.RawMessage
	_ = json.Unmarshal(e.Variants, &variants)
	keep, names := map[string]bool{}, map[string]bool{}
	if e.PipelineID != "" {
		names[e.PipelineID] = true
	}
	for _, raw := range variants {
		var label struct {
			Label string `json:"label"`
		}
		_ = json.Unmarshal(raw, &label)
		into := names
		if adopted != "" && label.Label == adopted {
			into = keep
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		stringsIn(v, into)
	}
	ids := []string{}
	for id := range names {
		if !keep[id] && idPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := []contract.DefinitionKey{}
	for _, id := range ids {
		for _, kind := range []contract.DefinitionKind{contract.DefinitionKindAgent, contract.DefinitionKindPipeline} {
			h, err := headOf(q, string(kind), id, false)
			if err != nil {
				return out, err
			}
			if h == nil || h.state() != contract.DefinitionStateStoreOnly {
				continue
			}
			p, err := definitionDelete(q, &contract.DefinitionDeleteRequest{Kind: kind, Id: id, Preview: true})
			if err != nil {
				return out, err
			}
			if p.Refused != nil {
				continue
			}
			if _, err := definitionDelete(q, &contract.DefinitionDeleteRequest{Kind: kind, Id: id}); err != nil {
				return out, err
			}
			out = append(out, contract.DefinitionKey{Kind: kind, Id: id})
		}
	}
	return out, nil
}

// stringsIn: every string value in v, into set.
func stringsIn(v any, set map[string]bool) {
	switch x := v.(type) {
	case string:
		set[x] = true
	case []any:
		for _, y := range x {
			stringsIn(y, set)
		}
	case map[string]any:
		for _, y := range x {
			stringsIn(y, set)
		}
	}
}
