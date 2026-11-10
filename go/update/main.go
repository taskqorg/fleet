// fleet-update: installs a newer Fleet release on this host by itself, one run per call; a systemd timer or a launchd
// agent repeats it every 5 minutes (docs/designs/fleet-self-update.md, section 3).
//
//	fleet-update pds18      root on PDS18: coordinator side, then the PDS18 executor
//	fleet-update executor   the executor service user on every other host
//	fleet-update selftest
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

// releaseKey is the Ed25519 public key whose private half signs SHA256SUMS in .github/workflows/release.yml
// (GitHub Actions secret FLEET_RELEASE_KEY, environment "release").
const releaseKey = "eT+0MVUmjUvvItJGhR3rUqW5PBzvE8QbD9Uq/wjAHlQ="

const releasesURL = "https://github.com/taskqorg/fleet/releases"

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:]); err != nil {
		log.Print("fleet-update: ", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: fleet-update pds18 | executor | selftest")
	}
	key, err := publicKey()
	if err != nil {
		return err
	}
	u := newUpdater(key)
	switch args[0] {
	case "selftest":
		fmt.Printf("fleet-update %s: selftest ok\n", version)
		return nil
	case "pds18":
		return u.runPDS18()
	case "executor":
		return u.runExecutor()
	}
	return errors.New("usage: fleet-update pds18 | executor | selftest")
}

func publicKey() (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(releaseKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("compiled-in release key is not an Ed25519 public key")
	}
	return b, nil
}

// executorDataDir is the executor's data directory: FLEET_DATA_DIR, else the release default (section 5).
func executorDataDir() string {
	if d := os.Getenv("FLEET_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Fleet", "executor")
	}
	return filepath.Join(home, ".local", "state", "fleet", "executor")
}

func (u *updater) runExecutor() error {
	home, _ := os.UserHomeDir()
	s := filepath.Join(executorDataDir(), "update")
	if err := os.MkdirAll(s, 0o700); err != nil {
		return err
	}
	unlock, ok, err := lock(filepath.Join(s, "update.lock"))
	if err != nil || !ok {
		return err
	}
	defer unlock()
	stateFile := filepath.Join(s, "update-state.json")
	st := &State{}
	readJSON(stateFile, st)
	signal := filepath.Join(s, "update.json")
	restart := []string{"systemctl", "--user", "restart", "fleet-executor"}
	if runtime.GOOS == "darwin" {
		restart = []string{"launchctl", "kickstart", "-k", "gui/" + strconv.Itoa(os.Getuid()) + "/com.ufobe.fleet-executor"}
	}
	root := filepath.Join(home, "releases")
	p := &part{name: "executor", root: root, stage: root, bins: []string{"fleet-executor", "fleet-update"},
		restart: command(restart...), health: u.executorHealth(signal), st: st}
	pruneReleases(root)
	var in signalFile
	if !readJSON(signal, &in) {
		log.Print("no update.json yet: the executor has not polled")
		return nil
	}
	r := u.cycle(p, in.AutoUpdate, func() string { return in.Target })
	if r != nil {
		st.Last = r
		return writeJSON(stateFile, st, 0o600)
	}
	return nil
}

func (u *updater) runPDS18() error {
	const s, coordRoot, execRoot = "/var/lib/fleet-update", "/home/fleet/releases", "/opt/fleet-agent/releases"
	if err := os.MkdirAll(s, 0o755); err != nil {
		return err
	}
	unlock, ok, err := lock(filepath.Join(s, "update.lock"))
	if err != nil || !ok {
		return err
	}
	defer unlock()
	stateFile := filepath.Join(s, "pds18-state.json")
	st := map[string]*State{}
	readJSON(stateFile, &st)
	for _, k := range []string{"coordinator", "executor"} {
		if st[k] == nil {
			st[k] = &State{}
		}
	}
	stage := filepath.Join(s, "stage")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	pruneReleases(coordRoot)
	pruneReleases(execRoot)
	pruneDumps("/backup/fleet/pre-deploy", coordRoot)
	coord := &part{name: "coordinator", root: coordRoot, stage: stage, owner: "fleet", dump: true,
		bins:    []string{"fleet-coordinator", "fleet-gateway", "fleet-executor", "fleet-update"},
		restart: command("systemctl", "--user", "-M", "fleet@", "restart", "fleet", "fleet-gateway", "fleet-access-guard"),
		health:  u.coordinatorHealth, st: st["coordinator"]}
	exec := &part{name: "executor", root: execRoot, stage: stage, bins: []string{"fleet-executor"},
		restart: command("systemctl", "--user", "-M", "fleet-agent@", "restart", "fleet-executor"),
		health:  u.executorHealth("/home/fleet-agent/.local/state/fleet/executor/update/update.json"), st: st["executor"]}
	_, autoUpdate := u.coordinatorSelf()
	save := func() error { return writeJSON(stateFile, st, 0o644) }
	if r := u.cycle(coord, autoUpdate, u.latest); r != nil {
		coord.st.Last = r
		if err := save(); err != nil {
			return err
		}
		if r.State == "ok" {
			if err := installUpdater(filepath.Join(coordRoot, r.Version, "fleet-update"), "/opt/fleet/update/fleet-update"); err != nil {
				log.Print("copy fleet-update to /opt/fleet/update: ", err)
			}
		}
	}
	// The PDS18 executor follows the coordinator side in the same run, to its healthy version only.
	healthy := func() string {
		v := releaseVersion(filepath.Join(coordRoot, "current"))
		if v == "" || !exists(filepath.Join(coordRoot, "current", "healthy")) {
			return ""
		}
		return v
	}
	if r := u.cycle(exec, autoUpdate, healthy); r != nil {
		exec.st.Last = r
		return save()
	}
	return nil
}

// installUpdater copies the verified fleet-update to the root-owned path the root unit runs.
func installUpdater(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := copyFile(src, tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func lookupOwner(name string) (uid, gid int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, nil
}
