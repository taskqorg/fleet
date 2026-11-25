package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Requirement: a view operation (contract view: true, docs/designs/fleet-data-layer.md) returns what its screen
// shows, as summaries. Risk: a field that carries a stored blob joins a view reply and every reread ships it again
// (HAL0's session_get: 7.6 MB of projectSnapshot, dispatch and friends per change, 2026-11-25). The check is silent
// otherwise: the screen still renders.
var viewBlobFields = map[string]string{
	"projectSnapshot":   "an execution's project snapshot",
	"workspaceSnapshot": "an execution's workspace snapshot",
	"dispatch":          "an execution's dispatch record",
	"agentConfig":       "a stored agent configuration (use a summary such as SessionAgent)",
	"botContext":        "a bot run's context",
	"nativeSessions":    "native session records",
	"handoffs":          "handoff packets (read on demand)",
	"definitions":       "pinned definitions",
	"output":            "tool output (tool_output loads one call)",
	"input":             "tool input (tool_output loads one call)",
}

type schemaDoc struct {
	Operations map[string]struct {
		View bool `json:"view"`
	} `json:"operations"`
	Defs map[string]json.RawMessage `json:"$defs"`
}

func TestViewRepliesCarryNoBlobs(t *testing.T) {
	dir := filepath.Join("..", "..", "contracts")
	files, err := filepath.Glob(filepath.Join(dir, "*.schema.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("contracts not found: %v", err)
	}
	docs := map[string]schemaDoc{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d schemaDoc
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		docs[filepath.Base(f)] = d
	}
	names := []string{}
	for f := range docs {
		names = append(names, f)
	}
	sort.Strings(names)
	views := 0
	for _, f := range names {
		for op, o := range docs[f].Operations {
			if !o.View {
				continue
			}
			views++
			reply := opType(op) + "Reply"
			if _, ok := docs[f].Defs[reply]; !ok {
				t.Errorf("%s: view %s has no $defs %s", f, op, reply)
				continue
			}
			seen := map[string]bool{}
			var walk func(file, def, path string)
			walk = func(file, def, path string) {
				key := file + "#" + def
				if seen[key] {
					return
				}
				seen[key] = true
				raw, ok := docs[file].Defs[def]
				if !ok {
					return
				}
				var node any
				json.Unmarshal(raw, &node)
				var visit func(n any, path string)
				visit = func(n any, path string) {
					switch v := n.(type) {
					case map[string]any:
						if props, ok := v["properties"].(map[string]any); ok {
							for name, p := range props {
								if why, bad := viewBlobFields[name]; bad {
									t.Errorf("view %s carries %s at %s.%s: %s", op, name, path, name, why)
								}
								visit(p, path+"."+name)
							}
						}
						if ref, ok := v["$ref"].(string); ok {
							rf, rd, _ := strings.Cut(ref, "#/$defs/")
							if rf == "" {
								rf = file
							}
							walk(rf, rd, path)
						}
						for k, c := range v {
							if k != "properties" {
								visit(c, path)
							}
						}
					case []any:
						for _, c := range v {
							visit(c, path)
						}
					}
				}
				visit(node, path)
			}
			walk(f, reply, op)
		}
	}
	if views == 0 {
		t.Fatal("no view operation found (contract view: true)")
	}
}

// opType: an operation name as its type prefix (session_view: SessionView), as contracts/gen-ts.mjs names it.
func opType(op string) string {
	out := ""
	for _, p := range strings.FieldsFunc(op, func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}
