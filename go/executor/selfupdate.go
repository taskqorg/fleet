package main

import (
	"encoding/json"
	"errors"
	"fleet/contract"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Self-update (docs/designs/fleet-self-update.md, sections 4 and 5): the executor's version, release defaults for its
// environment, and <data dir>/update/update.json, the file fleet-update reads.

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

const defaultCoordinatorURL = "https://fleet.ufobe.com"

// envOr: the environment value, else the release default.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func defaultDataDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Fleet", "executor")
	}
	return filepath.Join(home, ".local", "state", "fleet", "executor")
}

func defaultSecretFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fleet", "host-secret")
}

// releaseDefaults: config/executor-defaults.json of the running release, read once.
var releaseDefaults = sync.OnceValue(func() (d struct {
	StoreEndpointHosts []string `json:"storeEndpointHosts"`
}) {
	exe, err := os.Executable()
	if err != nil {
		return d
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "config", "executor-defaults.json")); err == nil {
		json.Unmarshal(b, &d)
	}
	return d
})

// storeEndpointHosts: FLEET_STORE_ENDPOINT_HOSTS (comma-separated), else the release default.
func storeEndpointHosts() []string {
	if v := os.Getenv("FLEET_STORE_ENDPOINT_HOSTS"); v != "" {
		return strings.Split(v, ",")
	}
	return releaseDefaults().StoreEndpointHosts
}

// updateSignal is update.json; written only when a value other than at changes, durably (temp file, then rename).
type updateSignal struct {
	RestartTarget *string `json:"restartTarget,omitempty"`
	HostID        string  `json:"hostId"`
	Target        *string `json:"target"`
	AutoUpdate    string  `json:"autoUpdate"`
	Running       string  `json:"running"`
	PollOk        bool    `json:"pollOk"`
	PollError     string  `json:"pollError,omitempty"`
	At            string  `json:"at"`
}

var lastSignal *updateSignal
var signalMu sync.Mutex

func writeUpdateSignal(root string, s updateSignal) error {
	if l := lastSignal; l != nil && l.HostID == s.HostID && l.AutoUpdate == s.AutoUpdate && l.Running == s.Running && l.PollOk == s.PollOk && l.PollError == s.PollError && sameTarget(l.RestartTarget, s.RestartTarget) &&
		(l.Target == nil) == (s.Target == nil) && (l.Target == nil || *l.Target == *s.Target) {
		return nil
	}
	s.At = time.Now().UTC().Format(time.RFC3339)
	dir := filepath.Join(root, "update")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	tmp := filepath.Join(dir, "update.json.tmp-"+strconv.Itoa(os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, "update.json"))
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	lastSignal = &s
	return nil
}

// pollSignal classifies one poll answer: pollOk on 200; pollError "network" for a connection error, timeout or an edge
// redirect, "http-<status>" otherwise.
func pollSignal(a answer) (ok bool, perr string) {
	switch {
	case a.kind == answerOK:
		return true, ""
	case a.status == 0 || a.kind == answerEdge && a.status >= 300 && a.status < 400:
		return false, "network"
	}
	return false, "http-" + strconv.Itoa(a.status)
}

// lastUpdate: last of <data dir>/update/update-state.json (written by fleet-update), nil when absent.
func lastUpdate(root string) *contract.UpdateResult {
	var st struct {
		Last *contract.UpdateResult `json:"last"`
	}
	b, err := os.ReadFile(filepath.Join(root, "update", "update-state.json"))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return nil
	}
	return st.Last
}

// selftest: no network; an in-memory journal opens and the harness table is loaded.
func selftest([]string) error {
	db, err := openJournal(":memory:")
	if err != nil {
		return err
	}
	db.Close()
	if len(Harnesses) == 0 {
		return errors.New("no harnesses loaded")
	}
	fmt.Printf("fleet-executor %s: selftest ok\n", version)
	return nil
}

func sameTarget(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
