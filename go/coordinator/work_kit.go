// The work kit (docs/designs/fleet-standard.md S6, DESIGN_RULES.md): the one scheduler of retried item work. A queue
// registers itself (kitRegister) with its period; the kit selects its due items, runs each step in one transaction,
// retries failures with exponential backoff written by one guarded statement into the item row's attempts,
// next_attempt_at and last_error, groups cohorts and bisects failed ones. Nothing per item is kept in memory: an item's
// retry state lives in its row. One wake channel, fleet_work, carries the name of the queue that has new work.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// kitClaim: one due item as its queue's selection saw it.
type kitClaim struct {
	Key      string
	Group    string // non-empty: may share a cohort transaction with claims of the same group
	Attempts int    // the item's attempts when selected
	Guard    []any  // what the selection (or the step) saw; the failure write applies only while it is still there
	// Call: work the step leaves to do after its transaction commits and outside any transaction (a provider call, object
	// I/O). The kit runs it and then the step again at once with its Result, which that next transaction records. A crash
	// or a failed record between the two loses Result: the queue's step reads what it committed before the call as an
	// outcome it cannot know, and never repeats an effect it cannot repeat safely.
	Call   func(ctx context.Context) any
	Result any
}

// kitRetry: how one failure of an item of a queue is written: attempts, next_attempt_at and last_error in Table's
// row(s) of the key, only while Guard (SQL over $5..., qualified by the table name) still holds. Upsert creates a
// missing row.
type kitRetry struct {
	Table, Key, Guard, Set string
	Upsert                 bool
	Class                  func(error) string
	// OnFail runs in the failure write's transaction (the ownership repair of an execution).
	OnFail func(q *Request, c *kitClaim, err error) error
}

// kitQueue: a queue of item work. Due selects at most limit due items at now; Step makes one item's transition and
// its effects; Batch (optional) does the same for a cohort of same-group claims. Neither opens a transaction. The
// running kit passes the queue every Period and on a fleet_work wake naming it, while On (nil: always) holds.
type kitQueue struct {
	Name   string
	Period time.Duration
	On     func() bool
	Limit  int
	Due    func(q *Request, now time.Time, limit int) ([]*kitClaim, error)
	Step   func(q *Request, now time.Time, c *kitClaim) error
	Batch  func(q *Request, now time.Time, cs []*kitClaim) error
	Retry  kitRetry
	// Workers (queues without Batch): how many claims of one pass run at once, so one slow Call (a provider call, a
	// script check) does not hold the others; 0 or 1 runs them one by one.
	Workers int
}

// kitQueues: the registered queues in registration (pass) order.
var kitQueues []*kitQueue

// kitRegister adds a queue to the kit; called from the owning file's init.
func kitRegister(q *kitQueue) {
	for _, o := range kitQueues {
		if o.Name == q.Name {
			panic("work kit: queue " + q.Name + " registered twice")
		}
	}
	if q.Period <= 0 || q.Limit <= 0 || q.Due == nil || q.Step == nil || q.Retry.Table == "" || q.Retry.Key == "" || q.Retry.Class == nil {
		panic("work kit: queue " + q.Name + " lacks its period, limit, selection, step or retry")
	}
	kitQueues = append(kitQueues, q)
}

// errKitSkip: the item cannot run now (a busy lock, a clearing build); it is tried again next pass, nothing is recorded.
// errKitAgain: the step learned something (a new scope) and runs again at once in a new transaction.
var (
	errKitSkip  = errors.New("work skipped this pass")
	errKitAgain = errors.New("work runs again")
)

type kitPassKey struct{}

// kitLock takes the advisory locks of a step: without waiting inside the kit's passes (a busy lock skips the item), waiting
// when a step runs on its own.
func kitLock(q *Request, keys ...string) error {
	err := acquireTxLocksMode(q.ctx(), q.tx, q.ctx().Value(kitPassKey{}) != nil, keys...)
	if errors.Is(err, errAdvisoryBusy) {
		return fmt.Errorf("%w: %w", errKitSkip, err)
	}
	return err
}

// Retry pause: base*2^attempts plus up to half a step of jitter, never above the maximum (there, up to a quarter below
// it), as client-go's workqueue limiter with wait.Jitter. Success resets attempts.
var (
	kitRetryBase = max(envMs("FLEET_WORK_RETRY_BASE_MS", 100), time.Millisecond)
	kitRetryMax  = max(envMs("FLEET_WORK_RETRY_MAX_MS", 5*60*1000), kitRetryBase)
)

// kitFailingAttempts: the attempts after which an item's pause has reached the maximum; from then on it is failing.
func kitFailingAttempts() int {
	n := 0
	for n < 62 && float64(kitRetryBase)*math.Pow(2, float64(n)) < float64(kitRetryMax) {
		n++
	}
	return n + 1
}

// kitRetryEvery: the maximum pause as the owner reads it.
func kitRetryEvery() string {
	n, unit := kitRetryMax.Seconds(), "second"
	if kitRetryMax >= time.Minute {
		n, unit = kitRetryMax.Minutes(), "minute"
	}
	if n != 1 {
		unit += "s"
	}
	return strconv.FormatFloat(n, 'f', -1, 64) + " " + unit
}

type workKit struct {
	a      *App
	now    func() time.Time
	cohort int
	queues []*kitQueue
	mu     sync.Mutex
	rng    *rand.Rand
	health map[string]*kitHealthState
}

type kitHealthState struct {
	failures int
	until    time.Time
}

// kitQueueHealth: a queue whose own bookkeeping (selection, a failure write) keeps failing backs off as a whole.
type kitQueueHealth struct {
	Queue    string
	Failures int
}

func newWorkKit(a *App, now func() time.Time, seed uint64) *workKit {
	k := &workKit{a: a, now: now, cohort: resourceCohortSize, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), health: map[string]*kitHealthState{}}
	k.queues = kitQueues
	for _, q := range k.queues {
		k.health[q.Name] = &kitHealthState{}
	}
	return k
}

var kitDefault sync.Map // *App -> *workKit, for steps run outside a started processor

// kitOf: the processor's kit of a, or one with the real clock.
func kitOf(a *App) *workKit {
	if k, ok := kitDefault.Load(a); ok {
		return k.(*workKit)
	}
	k, _ := kitDefault.LoadOrStore(a, newWorkKit(a, time.Now, uint64(time.Now().UnixNano())))
	return k.(*workKit)
}

// backoff: the pause after a failure that follows attempts earlier ones.
func (k *workKit) backoff(attempts int) time.Duration {
	k.mu.Lock()
	r := k.rng.Float64()
	k.mu.Unlock()
	d := float64(kitRetryBase) * math.Pow(2, float64(min(max(attempts, 0), 62)))
	limit := float64(kitRetryMax)
	if d < limit {
		return time.Duration(d + r*min(d/2, limit-d))
	}
	return time.Duration(limit * (1 - r/4))
}

// kitHealth: the queues whose bookkeeping is failing, for the work-stalled rule.
func (k *workKit) kitHealth() []kitQueueHealth {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []kitQueueHealth
	for _, q := range k.queues {
		if h := k.health[q.Name]; h.failures > 0 {
			out = append(out, kitQueueHealth{q.Name, h.failures})
		}
	}
	return out
}

func (k *workKit) sick(q *kitQueue, now time.Time) {
	k.mu.Lock()
	h := k.health[q.Name]
	f := h.failures
	h.failures++
	k.mu.Unlock()
	d := k.backoff(f)
	k.mu.Lock()
	h.until = now.Add(d)
	k.mu.Unlock()
}

func (k *workKit) well(q *kitQueue) {
	k.mu.Lock()
	k.health[q.Name].failures = 0
	k.mu.Unlock()
}

func (k *workKit) queue(name string) *kitQueue { return kitQueueNamed(name) }

// read runs fn in one read-only transaction.
func (k *workKit) read(ctx context.Context, fn func(q *Request) error) error {
	return k.a.store.readOnce(ctx, backgroundRequest(ctx, k.a, nil), fn)
}

// tx runs fn in one write transaction attempt.
func (k *workKit) tx(ctx context.Context, fn func(q *Request) error) error {
	return k.a.store.writeOnce(ctx, backgroundRequest(ctx, k.a, nil), nil, fn)
}

// txRetried runs fn in one write transaction, retried on serialization failures and deadlocks.
func (k *workKit) txRetried(ctx context.Context, fn func(q *Request) error) error {
	return k.a.store.Write(ctx, backgroundRequest(ctx, k.a, nil), nil, fn)
}

// pass runs every queue once in registration order.
func (k *workKit) pass(ctx context.Context) error {
	var errs error
	for _, q := range k.queues {
		if err := k.passQueue(ctx, q); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// passQueue runs q once: select its due items, run them, write their failures.
func (k *workKit) passQueue(ctx context.Context, q *kitQueue) error {
	ctx = context.WithValue(ctx, kitPassKey{}, true)
	now := k.now()
	k.mu.Lock()
	waiting := now.Before(k.health[q.Name].until)
	k.mu.Unlock()
	if waiting {
		return nil
	}
	var claims []*kitClaim
	err := k.read(ctx, func(r *Request) error {
		var err error
		claims, err = q.Due(r, now, q.Limit)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		k.sick(q, now)
		return fmt.Errorf("work %s selection: %w", q.Name, err)
	}
	stepErr, bookErr := k.run(ctx, q, now, claims)
	if bookErr != nil {
		k.sick(q, now)
	} else {
		k.well(q)
	}
	return errors.Join(stepErr, bookErr)
}

// run runs claims of q: same-group claims as cohorts (a failed cohort is retried in halves down to single steps), the
// rest one by one. It returns the steps' errors and the errors of the kit's own failure writes.
func (k *workKit) run(ctx context.Context, q *kitQueue, now time.Time, claims []*kitClaim) (stepErr, bookErr error) {
	var mu sync.Mutex
	single := func(c *kitClaim) {
		s, b := k.one(ctx, q, now, c)
		mu.Lock()
		stepErr, bookErr = errors.Join(stepErr, s), errors.Join(bookErr, b)
		mu.Unlock()
	}
	if q.Batch == nil && q.Workers > 1 {
		var wg sync.WaitGroup
		slots := make(chan struct{}, q.Workers)
		for _, c := range claims {
			if ctx.Err() != nil {
				break
			}
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				continue
			}
			wg.Add(1)
			go func(c *kitClaim) {
				defer func() { <-slots; wg.Done() }()
				single(c)
			}(c)
		}
		wg.Wait()
		return errors.Join(stepErr, ctx.Err()), bookErr
	}
	batch := func(cs []*kitClaim) error {
		return k.tx(ctx, func(r *Request) error { return q.Batch(r, now, cs) })
	}
	for _, cohort := range kitCohorts(claims, k.cohort) {
		if ctx.Err() != nil {
			return errors.Join(stepErr, ctx.Err()), bookErr
		}
		if len(cohort) == 1 || q.Batch == nil {
			for _, c := range cohort {
				single(c)
			}
			continue
		}
		kitBisect(cohort, batch, single)
	}
	return stepErr, bookErr
}

// kitBisect: a failed cohort left no subset applied; its halves are tried again, a single claim takes its own step with
// its own retry. One failing claim among n costs about 2*log2(n) cohort transactions. A skipped cohort waits for the
// next pass whole.
func kitBisect(cs []*kitClaim, batch func([]*kitClaim) error, single func(*kitClaim)) {
	if len(cs) == 1 {
		single(cs[0])
		return
	}
	if err := batch(cs); err == nil || errors.Is(err, errKitSkip) || errors.Is(err, context.Canceled) {
		return
	}
	mid := len(cs) / 2
	kitBisect(cs[:mid], batch, single)
	kitBisect(cs[mid:], batch, single)
}

// kitCohorts keeps first-unconsumed priority and order within the selected claims: each claim with a group gathers the
// later claims of its group (at most size), so other groups between them do not fragment cohorts.
func kitCohorts(claims []*kitClaim, size int) [][]*kitClaim {
	consumed := make([]bool, len(claims))
	var out [][]*kitClaim
	for i, c := range claims {
		if consumed[i] {
			continue
		}
		consumed[i] = true
		cohort := []*kitClaim{c}
		for j := i + 1; c.Group != "" && j < len(claims) && len(cohort) < size; j++ {
			if !consumed[j] && claims[j].Group == c.Group {
				consumed[j] = true
				cohort = append(cohort, claims[j])
			}
		}
		out = append(out, cohort)
	}
	return out
}

// one runs one claim's step and, if it fails, writes its failure.
func (k *workKit) one(ctx context.Context, q *kitQueue, now time.Time, c *kitClaim) (stepErr, bookErr error) {
	err := k.step(ctx, q, now, c)
	if err == nil || errors.Is(err, errKitSkip) || ctx.Err() != nil {
		return nil, nil
	}
	return err, k.fail(ctx, q, now, c, err)
}

// kitCalls: the calls one item may make in one run of its step (a submit, a listing after an unknown submit, ...).
const kitCalls = 4

// step runs the step and the retry reset in one transaction; a step that learned something runs again at once, at most
// three times. A Call the committed step left runs outside any transaction, then the step again with its Result; that
// recording transaction is retried on conflicts, so a lost Result costs a crash or a real failure, not a busy row.
func (k *workKit) step(ctx context.Context, q *kitQueue, now time.Time, c *kitClaim) error {
	var err error
	for try, calls := 0, 0; ; {
		c.Call = nil
		run := k.tx
		if c.Result != nil {
			run = k.txRetried
		}
		err = run(ctx, func(r *Request) error {
			if err := q.Step(r, now, c); err != nil {
				return err
			}
			if c.Call != nil {
				return nil // the item's retries reset once its record commits; a failed record keeps the selection's guard
			}
			return kitReset(r, q.Retry, c.Key, now)
		})
		c.Result = nil
		if err == nil && c.Call != nil && calls < kitCalls && ctx.Err() == nil {
			calls++
			c.Result = c.Call(ctx)
			continue
		}
		c.Call = nil
		if !errors.Is(err, errKitAgain) || try >= 2 {
			return err
		}
		try++
	}
}

// kitSchedule: the owner's item key of queue name is next due at at, its retries started afresh (a run waiting for
// its job, an experiment's next look). Called in the owner's step transaction.
func kitSchedule(q *Request, name, key string, at time.Time) error {
	rt := kitQueueNamed(name).Retry
	return exec(q, `UPDATE `+rt.Table+` SET attempts=0,last_error=NULL,next_attempt_at=$2 WHERE `+rt.Key+`=$1`, key, at)
}

// kitWake: a producer's new work for item key of queue name, in the producer's transaction: the item is due at now
// unless it is already due or its failures pause it, and the queue is woken through fleet_work. A missing row is
// nothing to do.
func kitWake(q *Request, name, key string, now time.Time) error {
	rt := kitQueueNamed(name).Retry
	tag, err := q.tx.Exec(q.ctx(), `UPDATE `+rt.Table+` SET next_attempt_at=$2 WHERE `+rt.Key+`=$1 AND attempts=0 AND next_attempt_at>$2`, key, now)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return kitNotify(q, name)
}

// kitNotify: queue name has new work (an owner inserted an item row): one fleet_work wake intent in q's transaction.
func kitNotify(q *Request, name string) error {
	kitQueueNamed(name)
	return exec(q, `SELECT fleet_enqueue_wake('fleet_work',$1)`, name)
}

func kitQueueNamed(name string) *kitQueue {
	for _, q := range kitQueues {
		if q.Name == name {
			return q
		}
	}
	panic("work kit: no queue " + name)
}

// kitReset: a successful step's item starts its retries afresh. A row still paused (another failure after the
// selection) keeps its pause.
func kitReset(q *Request, rt kitRetry, key string, now time.Time) error {
	return exec(q, `UPDATE `+rt.Table+` SET attempts=0,last_error=NULL WHERE `+rt.Key+`=$1 AND next_attempt_at<=$2 AND (attempts<>0 OR last_error IS NOT NULL)`, key, now)
}

// kitRestart: an item started afresh by its owner (a rebuilt project) is due at now with no attempts.
func kitRestart(q *Request, rt kitRetry, key string, now time.Time) error {
	return exec(q, `UPDATE `+rt.Table+` SET attempts=0,next_attempt_at=$2,last_error=NULL WHERE `+rt.Key+`=$1`, key, now)
}

// restart runs fn, an owner starting key of queue name afresh (a rebuilt project), and the item's restart in one write
// transaction holding locks, retried on conflicts.
func (k *workKit) restart(ctx context.Context, name, key string, locks []string, fn func(q *Request) error) error {
	q := k.queue(name)
	return k.a.store.Write(ctx, backgroundRequest(ctx, k.a, nil), locks, func(r *Request) error {
		if err := fn(r); err != nil {
			return err
		}
		return kitRestart(r, q.Retry, key, k.now())
	})
}

// fail writes one failure of c: one guarded statement in its own transaction, so it records nothing once the guard
// is gone (the step committed after all, or the item changed).
func (k *workKit) fail(ctx context.Context, q *kitQueue, now time.Time, c *kitClaim, cause error) error {
	rt := q.Retry
	next := now.Add(k.backoff(c.Attempts))
	args := append([]any{c.Key, c.Attempts + 1, next, rt.Class(cause)}, c.Guard...)
	set := `attempts=$2,next_attempt_at=$3,last_error=$4` + rt.Set
	sql := `UPDATE ` + rt.Table + ` SET ` + set + ` WHERE ` + rt.Key + `=$1 AND ` + rt.Guard
	if rt.Upsert {
		sql = `INSERT INTO ` + rt.Table + `(` + rt.Key + `,attempts,next_attempt_at,last_error) VALUES($1,$2,$3,$4) ON CONFLICT(` + rt.Key + `) DO UPDATE SET ` + set + ` WHERE ` + rt.Guard
	}
	err := k.tx(ctx, func(r *Request) error {
		if err := exec(r, sql, args...); err != nil {
			return err
		}
		if rt.OnFail != nil {
			return rt.OnFail(r, c, cause)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("work %s failure of %s not recorded: %w", q.Name, c.Key, err)
	}
	return nil
}

// runOne runs one claim of the named queue as a pass would, waiting for its locks.
func (k *workKit) runOne(ctx context.Context, name string, c *kitClaim) error {
	q := k.queue(name)
	s, b := k.one(ctx, q, k.now(), c)
	return errors.Join(s, b)
}

// startWorkKit runs the kit: each queue whose On holds is passed every Period and at once on a fleet_work
// notification naming it (an empty payload names every queue); one pass at a time.
func init() { onStart(startWorkKit) }

func startWorkKit(ctx context.Context, a *App) {
	k := kitOf(a)
	var queues []*kitQueue
	for _, q := range k.queues {
		if q.On == nil || q.On() {
			queues = append(queues, q)
		}
	}
	if len(queues) == 0 {
		return
	}
	wake := make(chan string, 64)
	go kitListen(ctx, a.store.pool.Config().ConnConfig.ConnString(), wake)
	// Each lane (the queue name up to its first '-': resource-build, resource-repair and resource-work are one) has its own
	// loop, one pass at a time, so one lane's slow work (a pipeline's provider call) never holds another's.
	lanes := map[string][]*kitQueue{}
	var order []string
	for _, q := range queues {
		lane, _, _ := strings.Cut(q.Name, "-")
		if lanes[lane] == nil {
			order = append(order, lane)
		}
		lanes[lane] = append(lanes[lane], q)
	}
	wakes := make(map[string]chan string, len(lanes))
	for _, lane := range order {
		w := make(chan string, 64)
		wakes[lane] = w
		go kitLoop(ctx, k, lanes[lane], w)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case name := <-wake:
				lane, _, _ := strings.Cut(name, "-")
				for l, w := range wakes {
					if name == "" || l == lane {
						select {
						case w <- name:
						default:
						}
					}
				}
			}
		}
	}()
}

// kitLoop passes each queue of one lane every Period and at once on a wake naming it (an empty name names every
// queue); one pass at a time.
func kitLoop(ctx context.Context, k *workKit, queues []*kitQueue, wake <-chan string) {
	next := make(map[string]time.Time, len(queues))
	timer := time.NewTimer(0)
	defer timer.Stop()
	for ctx.Err() == nil {
		now := time.Now()
		soonest := now.Add(time.Hour)
		for _, q := range queues {
			if !now.Before(next[q.Name]) {
				if err := k.passQueue(ctx, q); err != nil && ctx.Err() == nil {
					log.Printf("work kit: %v", err)
				}
				next[q.Name] = time.Now().Add(q.Period)
			}
			if next[q.Name].Before(soonest) {
				soonest = next[q.Name]
			}
		}
		timer.Reset(time.Until(soonest))
		select {
		case <-ctx.Done():
			return
		case name := <-wake:
			for _, q := range queues {
				if name == "" || name == q.Name {
					next[q.Name] = time.Time{}
				}
			}
		case <-timer.C:
		}
	}
}

// kitListen: one fleet_work listener per kit; a failed listener only delays work to each queue's period.
func kitListen(ctx context.Context, url string, wake chan<- string) {
	for ctx.Err() == nil {
		conn, err := pgx.Connect(ctx, url)
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN fleet_work")
			for err == nil {
				var n *pgconn.Notification
				if n, err = conn.WaitForNotification(ctx); err == nil {
					select {
					case wake <- n.Payload:
					default:
					}
				}
			}
			_ = conn.Close(context.Background())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// workStalled: the work-stalled finding of a's kit, when the bookkeeping of one of its queues keeps failing.
func workStalled(a *App) (contract.Finding, bool) {
	v, ok := kitDefault.Load(a)
	if !ok {
		return contract.Finding{}, false
	}
	var queues []string
	for _, h := range v.(*workKit).kitHealth() {
		if h.Failures >= kitFailingAttempts() {
			queues = append(queues, h.Queue)
		}
	}
	if len(queues) == 0 {
		return contract.Finding{}, false
	}
	f := contract.Finding{Rule: "work-stalled", Subject: "work kit", Value: float64(len(queues)), Cause: "problem:work:stalled", Exemplars: []string{}}
	text(&f, fmt.Sprintf("The work kit keeps failing to select or record the work of %s. Retry pauses are up to %s.", strings.Join(queues, ", "), kitRetryEvery()))
	return f, true
}
