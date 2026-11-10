package main

import (
	"context"
	"encoding/json"
	"errors"
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

func autoUpdateOf(h doc) string {
	if s, ok := h["autoUpdate"].(string); ok && s != "" {
		return s
	}
	return "on"
}

// pds18Host: the host id of the executor on the coordinator's machine, as fleet-update records it in pds18-state.json
// (from that executor's update.json). The coordinator's FLEET_HOST_ID names the default work host, not its machine.
func pds18Host() string {
	var st struct {
		HostID string `json:"hostId"`
	}
	if b, err := os.ReadFile(pds18StateFile); err == nil {
		json.Unmarshal(b, &st)
	}
	return st.HostID
}

// ownAutoUpdate reads autoUpdate of the coordinator machine's host record for /api/health (no request context there).
func (a *App) ownAutoUpdate(ctx context.Context) string {
	host := pds18Host()
	if host == "" {
		return "on"
	}
	tx, err := a.store.pool.Begin(ctx)
	if err != nil {
		return "on"
	}
	defer tx.Rollback(ctx)
	var s *string
	if _, err := tx.Exec(ctx, "SELECT set_config('fleet.project_ids','*',true)"); err != nil {
		return "on"
	}
	if err := tx.QueryRow(ctx, "SELECT data->>'autoUpdate' FROM document WHERE kind='host' AND id=$1", "host:"+host).Scan(&s); err != nil || s == nil {
		return "on"
	}
	return *s
}

// pds18StateFile is written by fleet-update on PDS18; the coordinator merges it into its own host's row.
var pds18StateFile = "/var/lib/fleet-update/pds18-state.json"

var updateRank = map[UpdateState]int{UpdateStateOk: 0, UpdateStateHeld: 1, UpdateStatePending: 2, UpdateStateError: 3, UpdateStateRefused: 4, UpdateStateFailed: 5}

// mergeUpdate: the worse of the polled result and the PDS18 parts; parts lists coordinator and executor results.
func mergeUpdate(polled any) any {
	b, err := os.ReadFile(pds18StateFile)
	if err != nil {
		return polled
	}
	var st map[string]json.RawMessage
	if json.Unmarshal(b, &st) != nil {
		return polled
	}
	var worst *UpdateResult
	parts := doc{}
	for _, k := range []string{"coordinator", "executor"} {
		var part struct {
			Last *UpdateResult `json:"last"`
		}
		json.Unmarshal(st[k], &part)
		if r := part.Last; r != nil {
			parts[k] = r
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
	out := doc{"version": worst.Version, "state": worst.State, "at": worst.At, "parts": parts}
	if worst.Error != nil {
		out["error"] = *worst.Error
	}
	return out
}

// decodeUpdate reads a poll body's update through the generated contract type.
func decodeUpdate(v any) (UpdateResult, bool) {
	var r UpdateResult
	b, err := json.Marshal(v)
	if v == nil || err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	_, known := updateRank[r.State]
	return r, known
}

// updateQuestion opens one needs-you question per host and version for a refused or failed update, and withdraws the
// host's open update questions when an update succeeds.
func updateQuestion(q *Request, host string, holder any, r UpdateResult) error {
	switch r.State {
	case UpdateStateOk:
		return exec(q, "UPDATE question SET state='withdrawn',cause=NULL WHERE state='open' AND cause LIKE $1", "update:"+host+":%")
	case UpdateStateRefused, UpdateStateFailed:
	default:
		return nil
	}
	cause := "update:" + host + ":" + r.Version
	var n int
	if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM question WHERE cause=$1 AND state IN ('open','answered')", cause).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	reason := ""
	if r.Error != nil {
		reason = *r.Error
	}
	what := "refused"
	if r.State == UpdateStateFailed {
		what = "failed"
	} else if reason == "unhealthy" {
		what = "rolled back"
	}
	return openCauseQuestion(q, cause, holder, nil, host+": update to "+r.Version+" "+what+" ("+reason+").")
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
