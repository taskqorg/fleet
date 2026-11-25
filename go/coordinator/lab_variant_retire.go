package main

import (
	"bytes"
	"encoding/json"
	"sort"

	"fleet/contract"
	"fleet/coordinator/db"
)

// labRetireVariants: a decided experiment's Lab variants leave the store (fleet-definition-editors.md, One home per
// kind: a variant belongs to its experiment). Every store-only agent or pipeline its variants name (a JSON string
// anywhere in a variant) is deleted as definition_delete deletes it, unless the decision adopts the
// variant that names it (that one goes to the repository by its pull request) or something else still uses it
// (definition_delete's own refusal: another live experiment, a project role, a pipeline step, an open session, an
// unfinished run). The revisions stay, so the experiment's trials keep the content they ran. It answers the deleted
// keys.
func labRetireVariants(q *Request, e db.Experiment, adopted string) ([]contract.DefinitionKey, error) {
	var variants []json.RawMessage
	_ = json.Unmarshal(e.Variants, &variants)
	// The experiment's own pipeline is its baseline, never a variant: it stays.
	keep, names := map[string]bool{}, map[string]bool{}
	for _, raw := range variants {
		var label struct {
			Label string `json:"label"`
		}
		_ = json.Unmarshal(raw, &label)
		into := names
		if adopted != "" && label.Label == adopted {
			into = keep
		}
		stringsIn(raw, into)
	}
	// A changed decision that adopts a variant an earlier decision deleted brings it back, revisions and all.
	for id := range keep {
		for _, kind := range []string{"agent", "pipeline"} {
			if _, err := q.db().DefRestoreVariant(q.ctx(), db.DefRestoreVariantParams{Kind: kind, ID: id}); err != nil {
				return nil, err
			}
		}
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

// stringsIn: every string token of a JSON document (values and keys), into set.
func stringsIn(raw []byte, set map[string]bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	for {
		t, err := d.Token()
		if err != nil {
			return
		}
		if s, ok := t.(string); ok {
			set[s] = true
		}
	}
}
