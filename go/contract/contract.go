// Package contract holds the Go types generated from contracts/*.schema.json, shared by the coordinator, the executor
// and the gateway. The generated files are committed; CI fails when go generate changes them.
package contract

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
)

//go:generate go run ../gen -unions ../../contracts/host-protocol.schema.json contract host_protocol_gen.go
//go:generate go run ../gen ../../contracts/gateway.schema.json contract gateway_gen.go
//go:generate go run ../gen ../../contracts/host-files.schema.json contract host_files_gen.go
//go:generate go run ../gen ../../contracts/ops.schema.json contract ops_gen.go
//go:generate go run ../gen ../../contracts/ops-accounts.schema.json contract ops_accounts_gen.go
//go:generate go run ../gen ../../contracts/ops-analytics.schema.json contract ops_analytics_gen.go
//go:generate go run ../gen ../../contracts/ops-artifacts.schema.json contract ops_artifacts_gen.go
//go:generate go run ../gen ../../contracts/ops-board.schema.json contract ops_board_gen.go
//go:generate go run ../gen ../../contracts/ops-bots.schema.json contract ops_bots_gen.go
//go:generate go run ../gen ../../contracts/ops-definitions.schema.json contract ops_definitions_gen.go
//go:generate go run ../gen ../../contracts/ops-executions.schema.json contract ops_executions_gen.go
//go:generate go run ../gen ../../contracts/ops-lab.schema.json contract ops_lab_gen.go
//go:generate go run ../gen ../../contracts/lab-registry.schema.json contract lab_registry_gen.go
//go:generate go run ../gen ../../contracts/ops-projects.schema.json contract ops_projects_gen.go
//go:generate go run ../gen ../../contracts/ops-publishing.schema.json contract ops_publishing_gen.go
//go:generate go run ../gen ../../contracts/ops-people.schema.json contract ops_people_gen.go
//go:generate go run ../gen ../../contracts/ops-sessions.schema.json contract ops_sessions_gen.go
//go:generate go run ../gen ../../contracts/ops-tasks.schema.json contract ops_tasks_gen.go
//go:generate go run ../gen ../../contracts/ops-telemetry.schema.json contract ops_telemetry_gen.go
//go:generate go run ../gen ../../contracts/ops-telegram.schema.json contract ops_telegram_gen.go
//go:generate go run ../gen ../../contracts/profile.schema.json contract profile_gen.go
//go:generate go run ../gen ../../contracts/vocabulary.json contract vocabulary_gen.go

// Operation is one entry of an "operations" list in contracts/ops-<domain>.schema.json: a read snapshot or a write, long (no surrounding
// transaction), the status codes its handler returns besides 401, 403 and 500, who may call it (Auth: person, project:<path>
// of the project id in the request, holder:<kind> of the named object, architect, owner or system), how a host agent calls
// it (Agent: no, tool, or bound:<binder> when the coordinator binds its arguments to the calling session, project or focus)
// and the tool description agents read.
type Operation struct {
	Read, Long bool
	// View: one screen's read (contract view: true, docs/designs/fleet-data-layer.md); db_explain explains its queries.
	View                     bool
	Errors                   []int
	Auth, Agent, Description string
	// Changes: the change kinds that can alter its reply, "<kind>" or "<kind>:<id prefix>"; each change row the feed
	// sends names the operations it touches (Change.touches), so the app rereads without predicates of its own.
	Changes     []string
	InputSchema json.RawMessage
}

// touchIndex: each change kind's declared (id prefix, operation) pairs, built once from Operations on first use (every
// generated init has run by then).
var touchIndex = sync.OnceValue(func() map[string][][2]string {
	out := map[string][][2]string{}
	for n, o := range Operations {
		for _, c := range o.Changes {
			k, prefix, _ := strings.Cut(c, ":")
			out[k] = append(out[k], [2]string{prefix, n})
		}
	}
	return out
})

// Touches: the operations whose declared Changes match a change of this kind and id, sorted.
func Touches(kind, id string) []string {
	out := []string{}
	for _, p := range touchIndex()[kind] {
		if strings.HasPrefix(id, p[0]) && !slices.Contains(out, p[1]) {
			out = append(out, p[1])
		}
	}
	sort.Strings(out)
	return out
}

// Operations: every coordinator operation; each name has <Name>Request and <Name>Reply. The generated init of each
// ops-<domain> file fills it.
var Operations = map[string]Operation{}

func addOperation(name string, o Operation) {
	if _, ok := Operations[name]; ok {
		panic("contract: operation " + name + " is in two ops-<domain>.schema.json files")
	}
	Operations[name] = o
}

// FieldError: a body field the schema requires is missing (Missing) or holds a value outside its enum.
type FieldError struct {
	Path    string
	Missing bool
}

func (e *FieldError) Error() string {
	if e.Missing {
		return "Expected " + e.Path
	}
	return "Invalid " + e.Path
}

// Missing: the required field name is absent or empty.
func Missing(name string) error { return &FieldError{Path: name, Missing: true} }

// Check validates a nested value: a generated struct (Validate) or enum (Valid); other values pass. The error names the
// field path from the outer struct.
func Check(v any, name string) error {
	switch x := v.(type) {
	case interface{ Validate() error }:
		if err := x.Validate(); err != nil {
			if fe, ok := err.(*FieldError); ok {
				return &FieldError{Path: name + "." + fe.Path, Missing: fe.Missing}
			}
			return err
		}
	case interface{ Valid() bool }:
		if !x.Valid() {
			return &FieldError{Path: name}
		}
	}
	return nil
}

// marshalRest: an open object (additionalProperties: true) as its named fields plus the keys in rest; a named field wins.
func marshalRest(named any, rest map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(named)
	if err != nil || len(rest) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range rest {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// unmarshalRest: an open object into its named fields, the keys they do not name into rest.
func unmarshalRest(b []byte, named any, rest *map[string]json.RawMessage) error {
	if err := json.Unmarshal(b, named); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	t := reflect.TypeOf(named).Elem()
	for i := 0; i < t.NumField(); i++ {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		delete(m, tag)
	}
	if len(m) > 0 {
		*rest = m
	}
	return nil
}

// HostRoute: one host route of contracts/host-protocol.schema.json (routes), typed by its request and reply. Long: no
// surrounding transaction; Plain: the body is decoded only (older hosts omit required fields), else required keys are
// checked and Validate runs, and a closed request refuses unknown keys.
type HostRoute[Req, Rep any] struct {
	Path        string
	Long, Plain bool
}

// StartNeeds: what a start or input asks of the executor that runs it.
type StartNeeds struct {
	ReadOnly     bool // read access: the executor must honour it
	Branchless   bool // a project start without a branch
	WorkspaceKey bool // a workspace key (its own worktree)
	Attachments  bool // message attachments
}

// MissingCapability is the one capability gate (dispatch and poll): the first capability the host lacks for n, and
// whether one is missing. caps is the host's last poll's list.
func MissingCapability(caps []string, n StartNeeds) (Capability, bool) {
	for _, c := range []struct {
		need bool
		cap  Capability
	}{{n.ReadOnly || n.Branchless, CapabilityStartAccessV1}, {n.WorkspaceKey, CapabilityWorkspaceKeyV1}, {n.Attachments, CapabilityAttachments}} {
		if c.need && !HasCapability(caps, string(c.cap)) {
			return c.cap, true
		}
	}
	return "", false
}

// HasCapability: caps holds c (a Capability or a prefixed one: ToolCapability, ForgeCapability, PublishingHostCapability).
func HasCapability(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// ToolCapability: the capability of a coding tool on the host's PATH (tool:<name>).
func ToolCapability(name string) string { return "tool:" + name }

// ForgeCapability: the capability of a forge connection to owner/repo (forge:<owner>/<repo>).
func ForgeCapability(repo string) string { return "forge:" + repo }

// FfmpegCapability: the host's ffmpeg version (ffmpeg:<version>).
func FfmpegCapability(version string) string { return "ffmpeg:" + version }
