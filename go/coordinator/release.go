package main

import (
	"context"
	"encoding/json"
	"errors"
	"fleet/contract"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Self-update (docs/designs/fleet-self-update.md, section 4): the coordinator's own release, the version it offers its
// executors, the version-behind problem and the host field autoUpdate.

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

var releaseVersionRe = regexp.MustCompile(`^[1-9][0-9]*\.[0-9]{6}\.(0|[1-9][0-9]*)$`)

// olderVersion reports whether release version a is older than b (numeric dot parts); a non-release a counts as
// older, a non-release b never.
func olderVersion(a, b string) bool {
	if !releaseVersionRe.MatchString(b) {
		return false
	}
	if !releaseVersionRe.MatchString(a) {
		return true
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range pa {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// releaseDir is the release directory of the running binary (releases/<version>/, the current link resolved).
var releaseDir = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(exe)
}

// ownRelease: release.json of the running binary's release, read once (it does not change under a running binary).
var ownRelease = sync.OnceValue(func() struct{ Version, Previous string } { return releaseJSON(releaseDir()) })

func releaseJSON(dir string) (r struct{ Version, Previous string }) {
	if b, err := os.ReadFile(filepath.Join(dir, "release.json")); err == nil {
		json.Unmarshal(b, &r)
	}
	return r
}

// healthyTarget: the coordinator's own version when its release has the healthy marker, else the version of
// releases/previous when that has one, else nil (no update offered). Two stat calls per request.
func healthyTarget() *string {
	dir := releaseDir()
	if _, err := os.Stat(filepath.Join(dir, "healthy")); err == nil && releaseVersionRe.MatchString(version) {
		v := version
		return &v
	}
	prev := filepath.Join(filepath.Dir(dir), "previous")
	if _, err := os.Stat(filepath.Join(prev, "healthy")); err == nil {
		if v := releaseJSON(prev).Version; releaseVersionRe.MatchString(v) {
			return &v
		}
	}
	return nil
}

// validAutoUpdate: "on", "off", or an RFC 3339 time ending in Z (hold until then; a past time means on).
func validAutoUpdate(v any) bool {
	s, ok := v.(string)
	if s == "on" || s == "off" {
		return true
	}
	_, err := time.Parse(time.RFC3339, s)
	return ok && err == nil && strings.HasSuffix(s, "Z")
}

func autoUpdateOf(h *authHost) string {
	if h.AutoUpdate != "" {
		return h.AutoUpdate
	}
	return "on"
}

// readPDS18State reads pds18-state.json into the shared contract type. Missing or non-object/syntactically invalid
// files return false; a JSON object with one malformed field still preserves the remaining valid fields.
func readPDS18State() (contract.Pds18StateFile, bool) {
	b, err := os.ReadFile(pds18StateFile)
	if err != nil {
		return contract.Pds18StateFile{}, false
	}
	var st contract.Pds18StateFile
	if err := json.Unmarshal(b, &st); err != nil {
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) || te.Struct == "" {
			return contract.Pds18StateFile{}, false
		}
	}
	return st, true
}

// pds18Host: the host id of the executor on the coordinator's machine, as fleet-update records it in pds18-state.json
// (from that executor's update.json). The coordinator's FLEET_HOST_ID names the default work host, not its machine.
func pds18Host() string {
	st, ok := readPDS18State()
	if !ok || st.HostId == nil {
		return ""
	}
	return *st.HostId
}

// ownAutoUpdate reads autoUpdate of the coordinator machine's host record for /api/health (no request context there).
func (a *App) ownAutoUpdate(ctx context.Context) string {
	host := pds18Host()
	if host == "" {
		return "on"
	}
	s := "on"
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		v, err := q.db().HostAutoUpdate(q.ctx(), "host:"+host) // no host, or no autoUpdate (NULL): on
		if err != nil {
			return err
		}
		s = v
		return nil
	}); err != nil {
		return "on"
	}
	return s
}

// pds18StateFile is written by fleet-update on PDS18; the coordinator merges it into its own host's row.
var pds18StateFile = "/var/lib/fleet-update/pds18-state.json"

var updateRank = map[contract.UpdateState]int{contract.UpdateStateOk: 0, contract.UpdateStateHeld: 1, contract.UpdateStatePending: 2, contract.UpdateStateError: 3, contract.UpdateStateRefused: 4, contract.UpdateStateFailed: 5}

// mergedUpdate: the update a host shows for PDS18, with the parts it was merged from.
type mergedUpdate struct {
	contract.UpdateResult
	Parts map[string]*contract.UpdateResult `json:"parts"`
}

// mergeUpdate: the worse of the polled result and the PDS18 parts; parts lists coordinator and executor results.
func mergeUpdate(polled any) any {
	st, ok := readPDS18State()
	if !ok {
		return polled
	}
	var worst *contract.UpdateResult
	parts := map[string]*contract.UpdateResult{}
	for _, part := range []struct {
		name string
		last *contract.UpdateResult
	}{
		{"coordinator", st.Coordinator.Last},
		{"executor", st.Executor.Last},
	} {
		if r := part.last; r != nil {
			parts[part.name] = r
			if worst == nil || updateRank[r.State] > updateRank[worst.State] {
				worst = r
			}
		}
	}
	if p, ok := decodeUpdate(polled); ok && (worst == nil || updateRank[p.State] > updateRank[worst.State]) {
		worst = &p
	}
	if worst == nil {
		return polled
	}
	return norm(mergedUpdate{*worst, parts}) // its decoded value: a record field, compared by its JSON with the stored one (host_poll.go)
}

// decodeUpdate reads a poll body's update through the generated contract type.
func decodeUpdate(v any) (contract.UpdateResult, bool) {
	var r contract.UpdateResult
	b, err := json.Marshal(v)
	if v == nil || err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	_, known := updateRank[r.State]
	return r, known
}

// updateQuestion opens one needs-you question per host and version for a refused or failed update, and withdraws the
// host's open update questions when an update succeeds.
func updateQuestion(q *Request, host string, holder any, r contract.UpdateResult) error {
	switch r.State {
	case contract.UpdateStateOk:
		return problemsClosePrefix(q, "update:"+host+":")
	case contract.UpdateStateRefused, contract.UpdateStateFailed:
	case contract.UpdateStateError:
		if r.Error == nil || !strings.HasPrefix(*r.Error, "managed CLI ") {
			return nil
		}
	default:
		return nil
	}
	reason := ""
	if r.Error != nil {
		reason = *r.Error
	}
	what := "refused"
	if r.State == contract.UpdateStateFailed || r.State == contract.UpdateStateError {
		what = "failed"
	} else if reason == "unhealthy" {
		what = "rolled back"
	}
	return problemRaise(q, "", Problem{Cause: "update:" + host + ":" + r.Version, Text: host + ": update to " + r.Version + " " + what + " (" + reason + ").", AddressedTo: holderName(holder), Options: []any{"ok"}})
}

func ptr[T any](v T) *T { return &v }

// selftest: no database, no network; the embedded schema and the release's shipped config parse.
func selftest() error {
	if !strings.Contains(schemaSQL, "CREATE TABLE") {
		return errors.New("embedded schema is empty")
	}
	files, _ := filepath.Glob(filepath.Join(releaseDir(), "config", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if !json.Valid(b) {
			return errors.New(f + ": not JSON")
		}
	}
	return nil
}

// holderName: a host record's holder, "" when it has none.
func holderName(holder any) string {
	h, _ := holder.(string)
	return h
}
