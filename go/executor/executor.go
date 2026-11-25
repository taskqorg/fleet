package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
	"fleet/errtrack"
)

var shutdown atomic.Bool

type executor struct {
	stopCtx      context.Context
	activity     *os.File
	updateReady  atomic.Pointer[string]
	db           *sql.DB
	ex           execer // db, or the open transaction
	root         string
	url          string
	token        string
	host         string
	live         map[string]*live
	sessions     string
	harnessCache map[string]M
	storeCfg     *storeFile
	declared     []string
	storeAt      time.Time
	storeLast    []contract.StoreReport
	loadMu       sync.Mutex
	loadMemAt    time.Time
	loadMemFree  float64
	loadPressure contract.HostLoad // swap, compressor, tmpfs and top consumers, sampled with memory
	loadMemTotal float64
	loadMemOK    bool
}

// inTx runs fn with every journal write of the executor inside one transaction.
func (e *executor) inTx(fn func() error) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	e.ex = tx
	defer func() { e.ex = e.db }()
	if err := fn(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (e *executor) baseCtx() context.Context {
	if e.stopCtx != nil {
		return e.stopCtx
	}
	return context.Background()
}

// tickEvery: the process loop period while the coordinator holds the poll (README, Side effects: observe()).
const tickEvery = 100 * time.Millisecond

// tickFailed: a process-loop failure during the held poll; the main loop treats it as fatal, like its own tick.
type tickFailed struct{ error }

func (t tickFailed) Unwrap() error { return t.error }

const longPollWait = 25 * time.Second

// exchange keeps command order and the single owner of live/process/journal state. Slow preparation must not
// stop host liveness; extra polls leave returned commands pending for the next exchange, never execute them twice.
// exchangeQuiet: how long exchange failures stay unlogged; a coordinator restart behind its held socket takes less.
const exchangeQuiet = 30 * time.Second

var errHostRemoved = errors.New("host removed")

// The generated envelope owns wire fields. Raw command bodies retain all journal identity fields,
// including extensions from older coordinators, until the internal command runner is migrated.
type pollReply struct {
	contract.PollReply
	bodies []M
}

func decodePoll(raw json.RawMessage) (*pollReply, error) {
	// A command that does not fit its contract type fails the whole reply (nothing runs or is acknowledged); the
	// coordinator stops such a command instead of sending it. A kind this executor does not know keeps its JSON (Raw).
	var reply pollReply
	if err := json.Unmarshal(raw, &reply.PollReply); err != nil {
		return nil, err
	}
	var journal struct {
		Commands []M `json:"commands"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber() // Match the existing journal encoding, including large integers and numeric spelling.
	if err := decoder.Decode(&journal); err != nil {
		return nil, err
	}
	reply.bodies = journal.Commands
	return &reply, nil
}

// reproject: `executor reproject`: one JSON line per stored record on stdin, {executionId, harness, timestamp, record}, in
// seq order; one projection per line on stdout. Needs no data dir, lock or journal.
func reproject() error {
	if NewBlocks == nil {
		return errors.New("reproject: no block projection built")
	}
	state := map[string]Blocks{}
	out := bufio.NewWriter(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<30)
	for in.Scan() {
		if strings.TrimSpace(in.Text()) == "" {
			continue
		}
		if _, err := dec(in.Text()); err != nil {
			return err
		}
		// A field of another type reads as absent; the record is the open native record, decoded as the journal does.
		line := typedRaw[struct {
			ExecutionId string    `json:"executionId"`
			Harness     *string   `json:"harness"`
			Timestamp   uint64    `json:"timestamp"`
			Record      jsonValue `json:"record"`
		}](in.Bytes())
		ex := line.ExecutionId
		b := state[ex]
		if b == nil {
			b = NewBlocks()
			state[ex] = b
		}
		h := "codex"
		if line.Harness != nil {
			h = *line.Harness
		}
		rec, _ := line.Record.V.(M)
		if rec == nil {
			rec = M{}
		}
		fmt.Fprintln(out, enc(b.Project(h, rec, line.Timestamp)))
	}
	if err := in.Err(); err != nil {
		return err
	}
	return out.Flush()
}

func runExecutor() error {
	if err := useManagedTools(); err != nil {
		return err
	}
	root := envOr("FLEET_DATA_DIR", defaultDataDir())
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, "executor.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return errors.New("executor already running")
	}
	journal := filepath.Join(root, "journal.sqlite")
	db, err := openJournal(journal)
	if err != nil {
		return err
	}
	defer db.Close()
	url := envOr("FLEET_COORDINATOR_URL", defaultCoordinatorURL)
	host := os.Getenv("FLEET_HOST_ID")
	secretFile := envOr("FLEET_HOST_SECRET_FILE", defaultSecretFile())
	if host == "" {
		return errors.New("FLEET_HOST_ID required")
	}
	// The host's own secret, from a private file; the coordinator derives the host from it.
	token, err := hostSecret(secretFile)
	if err != nil {
		return err
	}
	sessions := os.Getenv("FLEET_SESSIONS_ROOT")
	if sessions == "" {
		sessions = filepath.Join(root, "sessions")
	}
	e := &executor{db: db, ex: db, root: root, url: url, token: token, host: host, live: map[string]*live{}, sessions: sessions, harnessCache: map[string]M{}, storeCfg: storeConfig()}
	if err := requeueUnauthorized(db); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "spool"), 0o755); err != nil {
		return err
	}
	// Artifact transfers run on their own goroutines; the Access token for store tunnels from FLEET_CF_ACCESS_FILE.
	if err := spawnTransfers(journal, newTransferClient(url, token, host, accessToken(accessFile())), func(_ *transferClient, db *sql.DB, body M, response any) {
		// A remote store answers "verifying" until the store's host checks the copy; the bytes are uploaded either way.
		answer := transferAnswerOf(response)
		file := typed[transferFile](body)
		if st := answer.State; body["previews"] == true && (st == "stored" || st == "verifying") {
			buildPreviews(db, body, response)
		}
		if body["spool"] == true && file.Path != nil {
			os.Remove(*file.Path)
		}
	}); err != nil {
		return err
	}
	if e.storeCfg != nil {
		if r := storeRegistration(e.storeCfg, host); r != nil {
			if err := queueTo(db, contract.RouteStores, *r); err != nil {
				return err
			}
		}
	}
	odb, err := openJournal(journal)
	if err != nil {
		return err
	}
	spawnOutbox(odb, url, token, host)
	startTelemetry(root, odb, host)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(sessions, 0o700); err != nil {
		return err
	}
	stopCtx, cancelStop := context.WithCancel(context.Background())
	defer cancelStop()
	e.stopCtx = stopCtx
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	errtrack.Go("signals", func() { <-sig; shutdown.Store(true); cancelStop() })
	if err := e.reconcile(); err != nil {
		return err
	}
	e.startSweep() // after reconcile: the journal's launches are the runs that may still need their session directory
	status("working", "")
	var failingSince time.Time
	for {
		if shutdown.Load() {
			for _, l := range e.live {
				l.cleanup()
				<-l.exited
			}
			status("done", "")
			return nil
		}
		if err := e.tick(); err != nil {
			status("error", "")
			return err
		}
		err := e.exchange()
		var failed tickFailed
		if errors.As(err, &failed) {
			status("error", "")
			return failed.error
		}
		if errors.Is(err, errHostRemoved) {
			// The owner removed this host (fleet-system-management.md, Hosts): stop polling and exit 0, so the unit's
			// Restart=on-failure does not start it again.
			fmt.Fprintln(os.Stderr, "executor: this host was removed from Fleet; stopping its runs")
			for _, l := range e.live {
				l.cleanup()
				<-l.exited
			}
			status("done", "")
			return nil
		} else if err != nil {
			if shutdown.Load() {
				continue
			}
			// A redirected poll is already signalled once by the edge state; other failures log here.
			var r redirected
			if !errors.As(err, &r) {
				// A coordinator restart is a few seconds of refused or reset polls: retried silently, logged once it lasts.
				if failingSince.IsZero() {
					failingSince = time.Now()
				}
				if time.Since(failingSince) >= exchangeQuiet {
					fmt.Fprintf(os.Stderr, "executor exchange failed for %s; retained journal, retry pending: %v\n", time.Since(failingSince).Round(time.Second), err)
					status("error", "")
				}
			} else {
				failingSince = time.Time{}
				status("blocked", "auth") // edge login page or host token not accepted
			}
		} else {
			failingSince = time.Time{}
			status("working", "")
		}
		time.Sleep(tickEvery)
	}
}

// poll owns the store-report cache and update signal; command execution never runs in this goroutine.
func (e *executor) poll() (*pollReply, error) {
	return e.pollWait(e.baseCtx(), 0)
}

// activeExecutions returns the sorted, deduplicated execution IDs currently open in the journal or still holding
// an undelivered status receipt in the outbox. Reading SQLite avoids racing with the main goroutine's e.live map.
func (e *executor) activeExecutions() []string {
	out := []string{}
	if e.db == nil {
		return out
	}
	rows, err := e.db.Query(
		`SELECT DISTINCT json_extract(body,'$.executionId') FROM commands WHERE terminal=0
		 UNION
		 SELECT DISTINCT json_extract(body,'$.executionId') FROM outbox WHERE route=?`,
		contract.RouteReceipts.Path,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id sql.NullString
		if rows.Scan(&id) == nil && id.Valid && id.String != "" {
			out = append(out, id.String)
		}
	}
	sort.Strings(out)
	return out
}

const hostLoadMemTTL = 15 * time.Second

func (e *executor) sampleHostLoad() *contract.HostLoad {
	cpus := int64(runtime.NumCPU())
	out := &contract.HostLoad{Cpus: &cpus}
	if l, ok := load1(); ok && l >= 0 {
		out.Load1 = &l
	}
	e.loadMu.Lock()
	if e.loadMemAt.IsZero() || time.Since(e.loadMemAt) >= hostLoadMemTTL {
		free, total, ok := memory()
		e.loadMemAt = time.Now()
		e.loadMemFree, e.loadMemTotal, e.loadMemOK = free, total, ok
		e.loadPressure = contract.HostLoad{}
		hostPressure(&e.loadPressure)
	}
	free, total, ok := e.loadMemFree, e.loadMemTotal, e.loadMemOK
	p := e.loadPressure
	out.SwapUsed, out.SwapTotal, out.MemCompressed, out.TmpUsed, out.TmpTotal, out.Top = p.SwapUsed, p.SwapTotal, p.MemCompressed, p.TmpUsed, p.TmpTotal, p.Top
	e.loadMu.Unlock()
	if ok && total > 0 && free >= 0 {
		out.MemFree = &free
		out.MemTotal = &total
	}
	return out
}

func (e *executor) pollWait(ctx context.Context, wait time.Duration) (*pollReply, error) {
	v, roots := version, reportedRoots()
	body := contract.PollRequest{HostId: e.host, Platform: contract.Platform(platform()), Capabilities: capabilities(), Stores: e.stores(),
		Problems: edgeProblems(pollRoute), ProjectRoots: roots, Version: &v, HarnessVersions: managedVersions(), Update: lastUpdate(e.root),
		GitConnections: gitConnections(connectionsDir()), ActiveExecutions: e.activeExecutions(), Load: e.sampleHostLoad()}
	var a answer
	if wait > 0 {
		a = sendPoll(ctx, e.url, e.token, pollRoute+"?wait="+wait.String(), body)
	} else {
		a = sendWithClient(ctx, outboxClient, e.url, e.token, pollRoute, pollRoute, body)
	}
	if a.status == 410 && strings.Contains(a.text, "host-removed") {
		return nil, errHostRemoved
	}
	if err := ctx.Err(); err != nil && a.kind == answerRetry {
		// The exchange cancelled its own heartbeat poll (or the executor stops): not a poll failure for update.json.
		return nil, err
	}
	signalMu.Lock()
	defer signalMu.Unlock()
	sig := updateSignal{HostId: e.host, Running: version, AutoUpdate: "on"}
	if lastSignal != nil {
		sig.Target, sig.AutoUpdate = lastSignal.Target, lastSignal.AutoUpdate
	}
	sig.PollOk, sig.PollError = pollFields(a)
	var reply *pollReply
	if a.kind == answerOK {
		var err error
		reply, err = decodePoll(a.value)
		if err != nil {
			a.kind, a.text = answerRetry, "invalid poll reply: "+err.Error()
			sig.PollOk, sig.PollError = pollFields(a)
		} else {
			sig.Target = reply.Target
			if reply.AutoUpdate != nil {
				sig.AutoUpdate = *reply.AutoUpdate
			}
		}
	}
	if ready := e.updateReady.Load(); ready != nil && sig.Target != nil && *ready == *sig.Target {
		sig.RestartTarget = ready
	}
	if err := writeUpdateSignal(e.root, sig); err != nil {
		fmt.Fprintln(os.Stderr, "executor: update.json:", err)
	}
	_, err := answerValue(a)
	return reply, err
}

// heldPoll waits for the coordinator's held poll while this goroutine keeps ticking, so a process exit, its output,
// a script deadline and resource samples never wait up to longPollWait for the answer. It ticks only while
// executions are live; an idle host checks update drain once per exchange. A tick failure cancels
// the poll, waits for its goroutine and drops any reply: the executor stops, and commands without a receipt stay
// pending on the coordinator (host_poll.go selects status 'pending') for delivery again.
func (e *executor) heldPoll() (*pollReply, error) {
	ctx, cancel := context.WithCancel(e.baseCtx())
	defer cancel()
	type answered struct {
		reply *pollReply
		err   error
	}
	got := make(chan answered, 1)
	errtrack.Go("poll.held", func() {
		reply, err := e.pollWait(ctx, longPollWait)
		got <- answered{reply, err}
	})
	beat := time.NewTicker(tickEvery)
	defer beat.Stop()
	for {
		select {
		case a := <-got:
			return a.reply, a.err
		case <-beat.C:
			if len(e.live) == 0 {
				continue
			}
			if err := e.tick(); err != nil {
				cancel()
				<-got
				return nil, tickFailed{err}
			}
		}
	}
}

func (e *executor) tick() error {
	if err := e.tickProcesses(); err != nil {
		return err
	}
	return e.drainUpdate()
}

func (e *executor) tickProcesses() error {
	for execution, l := range e.live {
		done, err := e.tickOne(execution, l)
		if err != nil {
			return err
		}
		if done {
			if err := e.release(execution); err != nil {
				return err
			}
			delete(e.live, execution)
		}
	}
	return nil
}

func (e *executor) exchange() (err error) {
	// Retain the edge backoff for both ordinary polls and liveness polls.
	if edgeWait(pollRoute) {
		return nil
	}
	reply, err := e.heldPoll()
	if err != nil {
		return err
	}
	beatCtx, cancelBeat := context.WithCancel(e.baseCtx())
	stop, done := make(chan struct{}), make(chan struct{})
	var removed atomic.Bool
	errtrack.Go("update.drain", func() {
		defer close(done)
		beat := time.NewTicker(time.Second)
		defer beat.Stop()
		for {
			select {
			case <-stop:
				return
			case <-beat.C:
				if edgeWait(pollRoute) {
					continue
				}
				if _, err := e.pollWait(beatCtx, 0); errors.Is(err, errHostRemoved) {
					removed.Store(true)
					return
				}
			}
		}
	})
	defer func() {
		cancelBeat()
		close(stop)
		<-done
		if err == nil && removed.Load() {
			err = errHostRemoved
		}
	}()

	for index := range reply.Commands {
		if removed.Load() {
			return errHostRemoved
		}
		// The outer loop checks drain. Keep processes responsive between commands
		// without rescanning the journal for each command deferred during an update.
		if err := e.tickProcesses(); err != nil {
			return err
		}
		if err := e.runOrFail(reply.bodies[index]); err != nil {
			return err
		}
	}
	return nil
}

// runOrFail runs one command. A command that errors before its run is live fails, and a start or input says why:
// without the reason an execution failed with no problem and no preparation.failed event.
func (e *executor) runOrFail(m M) error {
	err := e.run(m)
	if err == nil {
		return nil
	}
	c := typedCommand(m)
	id, _ := idOf(c, "id")
	execution, _ := idOf(c, "executionId")
	if _, isLive := e.live[execution]; isLive {
		return nil
	}
	b, ok := e.commandBody(id, true)
	if !ok || b != enc(m) {
		return nil
	}
	if k := commandKind(b); k == contract.CommandKindStart || k == contract.CommandKindInput {
		// A fresh seq: the failure may follow events of this command that took the low ones.
		if err := e.event(c, uint64(nowMs()), contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "executor error before the harness started: " + err.Error()}); err != nil {
			return err
		}
	}
	return e.finish(c, "failed", "")
}
