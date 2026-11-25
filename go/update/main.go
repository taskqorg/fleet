// fleet-update: installs a newer Fleet release on this host by itself, one run per call; a systemd timer or a launchd
// agent repeats it, every 10 seconds on PDS18 and every 5 minutes on executors (docs/designs/fleet-self-update.md, section 3).
//
//	fleet-update pds18      root on PDS18: coordinator side, then the PDS18 executor
//	fleet-update executor   the executor service user on every other host
//	fleet-update selftest
package main

import (
	"time"

	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fleet/contract"
	"fleet/errtrack"
	"fleet/obs"
	"fmt"
	"log"
	"os"
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

const pdsCoordinatorReleases = "/home/fleet/releases"

const repoURL = "https://github.com/taskqorg/fleet.git"

func main() {
	log.SetFlags(0)
	// Unexpected errors and panics go to Bugsink (docs/designs/fleet-error-tracking.md), as in the coordinator.
	host, _ := os.Hostname()
	obs.Service, obs.HostID, obs.Version = "fleet-update", host, version
	if err := errtrack.Start(errtrack.DSN(), "fleet-update", host, version); err != nil {
		log.Print("fleet-update: error tracking off: ", err)
	}
	defer func() {
		if p := recover(); p != nil {
			errtrack.Panic(p, map[string]string{"span": "main"})
			panic(p)
		}
	}()
	if err := run(os.Args[1:]); err != nil {
		errtrack.Capture(err, map[string]string{"span": "main"})
		errtrack.Flush(2 * time.Second)
		log.Print("fleet-update: ", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	const usage = "usage: fleet-update pds18 | executor | selftest | install-local <verified-stage> <releases> | stage-candidate <kind> <name> <candidates> <asset json>"
	if len(args) != 1 && (len(args) != 3 || args[0] != "install-local") && (len(args) != 5 || args[0] != "stage-candidate") {
		return errors.New(usage)
	}
	key, err := publicKey()
	if err != nil {
		return err
	}
	u := newUpdater(key)
	switch args[0] {
	case "install-local":
		if len(args) != 3 {
			return errors.New(usage)
		}
		root, err := openReleases(args[2])
		if err != nil {
			return err
		}
		defer root.Close()
		unlock, ok, err := lockReleases(root)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("another Fleet installer is active")
		}
		defer unlock()
		ver := releaseVersion(args[1])
		if !verRe.MatchString(ver) {
			return errors.New("invalid staged release version")
		}
		p := &part{r: root, root: args[2], stage: args[2]}
		if filepath.Clean(args[2]) != pdsCoordinatorReleases {
			if err := u.prepareHarnesses(p, args[1]); err != nil {
				return err
			}
		}
		return u.install(p, ver, args[1])
	case "stage-candidate":
		path, err := u.stageCandidate(args[1], args[2], args[3], []byte(args[4]))
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	case "selftest":
		fmt.Printf("fleet-update %s: selftest ok\n", version)
		return nil
	case "pds18":
		return u.runPDS18()
	case "executor":
		return u.runExecutor()
	}
	return errors.New(usage)
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
	ensureTrigger(signal)
	restart := []string{"systemctl", "--user", "restart", "fleet-executor"}
	if runtime.GOOS == "darwin" {
		restart = []string{"launchctl", "kickstart", "-k", "gui/" + strconv.Itoa(os.Getuid()) + "/com.ufobe.fleet-executor"}
	}
	root := filepath.Join(home, "releases")
	r, err := openReleases(root)
	if err != nil {
		return err
	}
	defer r.Close()
	p := &part{r: r, name: "executor", root: root, stage: root, bins: []string{"fleet-executor", "fleet-update"},
		activity: u.executorActivity(signal), restart: command(restart...), health: u.executorHealth(signal), st: st}
	var in signalFile
	if !readJSON(signal, &in) {
		log.Print("no update.json yet: the executor has not polled")
		return nil
	}
	if res := u.cycle(p, in.AutoUpdate, func() string { return str(in.Target) }); res != nil {
		st.Last = res
		return writeJSON(stateFile, st, 0o600)
	}
	return nil
}

func (u *updater) runPDS18() error {
	const s, coordRoot, execRoot = "/var/lib/fleet-update", pdsCoordinatorReleases, "/opt/fleet-agent/releases"
	if err := os.MkdirAll(s, 0o755); err != nil {
		return err
	}
	unlock, ok, err := lock(filepath.Join(s, "update.lock"))
	if err != nil || !ok {
		return err
	}
	defer unlock()
	u.cacheDir = s
	stateFile := filepath.Join(s, "pds18-state.json")
	var st contract.Pds18StateFile
	readJSON(stateFile, &st)
	const execSignal = "/home/fleet-agent/.local/state/fleet/executor/update/update.json"
	var sig signalFile
	hostChanged := false
	if readJSON(execSignal, &sig) && sig.HostId != "" && sig.HostId != str(st.HostId) {
		st.HostId, hostChanged = &sig.HostId, true // the coordinator shows these results on that host's row
	}
	stage := filepath.Join(s, "stage")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	coordR, err := openReleases(coordRoot)
	if err != nil {
		return err
	}
	defer coordR.Close()
	execR, err := openReleases(execRoot)
	if err != nil {
		return err
	}
	defer execR.Close()
	pruneDumps("/backup/fleet/pre-deploy", coordR, &st.Coordinator)
	coord := &part{r: coordR, name: "coordinator", root: coordRoot, stage: stage, dump: true, app: "dist/app",
		bins:    []string{"fleet-coordinator", "fleet-gateway", "fleet-executor", "fleet-update"},
		restart: command("systemctl", "--user", "-M", "fleet@", "restart", "fleet", "fleet-gateway", "fleet-access-guard"),
		health:  u.coordinatorHealth, st: &st.Coordinator}
	exec := &part{r: execR, name: "executor", root: execRoot, stage: stage, bins: []string{"fleet-executor"},
		activity: u.executorActivity(execSignal), restart: command("systemctl", "--user", "-M", "fleet-agent@", "restart", "fleet-executor"),
		health: u.executorHealth(execSignal), st: &st.Executor}
	// The host's autoUpdate holds its executor only. The coordinator side (coordinator, gateway, guard, UI) serves every
	// host, so a hold on PDS18's work host must not freeze it: 2026-11-26 a hold set to let pds18-native's executor take
	// work kept a coordinator fix from the hosts that needed it. FLEET_RELEASE_PAUSE stops new releases for all.
	_, autoUpdate := u.coordinatorSelf()
	save := func() error { return writeJSON(stateFile, st, 0o644) }
	if r := u.cycle(coord, "on", u.latest); r != nil {
		coord.st.Last = r
		if err := save(); err != nil {
			return err
		}
		// From the root-owned stage, never from /home/fleet; a resumed check (no stage this run) leaves the copy as is.
		if src := filepath.Join(stage, r.Version+".tmp", "fleet-update"); r.State == "ok" && exists(src) {
			if err := installUpdater(src, "/opt/fleet/update/fleet-update"); err != nil {
				log.Print("copy fleet-update to /opt/fleet/update: ", err)
			}
		}
	}
	// UI state is independent; native rollout and discovery retain their own versions.
	backend, _ := u.coordinatorSelf()
	if err := u.cycleUI(backend, "on", "/home/fleet/ui", stage, filepath.Join(s, "ui-state.json")); err != nil {
		log.Print(err)
	}
	// The PDS18 executor follows the coordinator side in the same run, to its healthy version only.
	healthy := func() string {
		v := rootVersion(coordR, "current")
		if _, err := coordR.Stat("current/healthy"); v == "" || err != nil {
			return ""
		}
		return v
	}
	if r := u.cycle(exec, autoUpdate, healthy); r != nil {
		exec.st.Last = r
		return save()
	}
	if hostChanged {
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
