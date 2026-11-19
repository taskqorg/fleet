// The work kit: the one scheduler of resource work (DESIGN_RULES.md, R3). Every queue (execution work, project builds,
// metadata repairs) is a spec; the kit selects due items, runs each step in one transaction, retries failures with
// exponential backoff written by one guarded statement, groups cohorts and bisects failed ones. Nothing per item is
// kept in memory: an item's retry state lives in its row.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
)

// kitClaim: one due item as its queue's selection saw it.
type kitClaim struct {
	Key      string
	Group    string // non-empty: may share a cohort transaction with claims of the same group
	Attempts int    // the item's attempts when selected
	Guard    []any  // what the selection (or the step) saw; the failure write applies only while it is still there
}

// kitRetry: how one failure of an item of a queue is written: attempts, next attempt and error in table's row(s) of
// the key, only while Guard (SQL over $5..., qualified by the table name) still holds. Upsert creates a missing row.
type kitRetry struct {
	Table, Key, Error, Guard, Set string
	Upsert                        bool
	Class                         func(error) string
	// OnFail runs in the failure write's transaction (the ownership repair of an execution).
	OnFail func(q *Request, c *kitClaim, err error) error
}

// kitQueue: a queue of resource work. Due selects at most limit due items at now; Step makes one item's transition and
// its effects; Batch (optional) does the same for a cohort of same-group claims. Neither opens a transaction.
type kitQueue struct {
	Name  string
	Limit int
	Due   func(q *Request, now time.Time, limit int) ([]*kitClaim, error)
	Step  func(q *Request, now time.Time, c *kitClaim) error
	Batch func(q *Request, now time.Time, cs []*kitClaim) error
	Retry kitRetry
}

// errKitSkip: the item cannot run now (a busy lock, a clearing build); it is tried again next pass, nothing is recorded.
// errKitAgain: the step learned something (a new scope) and runs again at once in a new transaction.
var (
	errKitSkip  = errors.New("resource work skipped this pass")
	errKitAgain = errors.New("resource work runs again")
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
	kitRetryBase = max(envMs("FLEET_RESOURCE_RETRY_BASE_MS", 100), time.Millisecond)
	kitRetryMax  = max(envMs("FLEET_RESOURCE_RETRY_MAX_MS", 5*60*1000), kitRetryBase)
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
	k.queues = resourceQueues()
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

// kitHealth: the queues whose bookkeeping is failing, for the resource-stalled rule.
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

func (k *workKit) queue(name string) *kitQueue {
	for _, q := range k.queues {
		if q.Name == name {
			return q
		}
	}
	panic("no resource queue " + name)
}

// tx runs fn in one write transaction attempt.
func (k *workKit) tx(ctx context.Context, fn func(q *Request) error) error {
	return runTxLocksOnce(ctx, k.a, nil, true, nil, fn)
}

// pass runs every queue once: select its due items, run them, write their failures.
func (k *workKit) pass(ctx context.Context) error {
	ctx = context.WithValue(ctx, kitPassKey{}, true)
	now := k.now()
	var errs error
	for _, q := range k.queues {
		k.mu.Lock()
		waiting := now.Before(k.health[q.Name].until)
		k.mu.Unlock()
		if waiting {
			continue
		}
		var claims []*kitClaim
		err := runTxLocksOnce(ctx, k.a, nil, false, nil, func(r *Request) error {
			var err error
			claims, err = q.Due(r, now, q.Limit)
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			k.sick(q, now)
			errs = errors.Join(errs, fmt.Errorf("resource %s selection: %w", q.Name, err))
			continue
		}
		stepErr, bookErr := k.run(ctx, q, now, claims)
		if bookErr != nil {
			k.sick(q, now)
		} else {
			k.well(q)
		}
		errs = errors.Join(errs, stepErr, bookErr)
	}
	return errs
}

// run runs claims of q: same-group claims as cohorts (a failed cohort is retried in halves down to single steps), the
// rest one by one. It returns the steps' errors and the errors of the kit's own failure writes.
func (k *workKit) run(ctx context.Context, q *kitQueue, now time.Time, claims []*kitClaim) (stepErr, bookErr error) {
	single := func(c *kitClaim) {
		s, b := k.one(ctx, q, now, c)
		stepErr, bookErr = errors.Join(stepErr, s), errors.Join(bookErr, b)
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

// step runs the step and the retry reset in one transaction; a step that learned something runs again at once, at most
// three times.
func (k *workKit) step(ctx context.Context, q *kitQueue, now time.Time, c *kitClaim) error {
	var err error
	for try := 0; try < 3; try++ {
		err = k.tx(ctx, func(r *Request) error {
			if err := q.Step(r, now, c); err != nil {
				return err
			}
			return kitReset(r, q.Retry, c.Key, now)
		})
		if !errors.Is(err, errKitAgain) {
			return err
		}
	}
	return err
}

// kitReset: a successful step's item starts its retries afresh. A row still paused (another failure after the
// selection) keeps its pause.
func kitReset(q *Request, rt kitRetry, key string, now time.Time) error {
	return exec(q, `UPDATE `+rt.Table+` SET attempts=0,`+rt.Error+`=NULL WHERE `+rt.Key+`=$1 AND next_attempt_at<=$2 AND (attempts<>0 OR `+rt.Error+` IS NOT NULL)`, key, now)
}

// fail writes one failure of c: one guarded statement in its own transaction, so it records nothing once the guard
// is gone (the step committed after all, or the item changed).
func (k *workKit) fail(ctx context.Context, q *kitQueue, now time.Time, c *kitClaim, cause error) error {
	rt := q.Retry
	next := now.Add(k.backoff(c.Attempts))
	args := append([]any{c.Key, c.Attempts + 1, next, rt.Class(cause)}, c.Guard...)
	set := `attempts=$2,next_attempt_at=$3,` + rt.Error + `=$4` + rt.Set
	sql := `UPDATE ` + rt.Table + ` SET ` + set + ` WHERE ` + rt.Key + `=$1 AND ` + rt.Guard
	if rt.Upsert {
		sql = `INSERT INTO ` + rt.Table + `(` + rt.Key + `,attempts,next_attempt_at,` + rt.Error + `) VALUES($1,$2,$3,$4) ON CONFLICT(` + rt.Key + `) DO UPDATE SET ` + set + ` WHERE ` + rt.Guard
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
		return fmt.Errorf("resource %s failure of %s not recorded: %w", q.Name, c.Key, err)
	}
	return nil
}

// runOne runs one claim of the named queue as a pass would, waiting for its locks.
func (k *workKit) runOne(ctx context.Context, name string, c *kitClaim) error {
	q := k.queue(name)
	s, b := k.one(ctx, q, k.now(), c)
	return errors.Join(s, b)
}

// startResourceProcessor runs the kit when FLEET_RESOURCE_PROCESSOR=on (startup default off): a pass on every
// fleet_resource_work notification and every 100 ms.
func init() { onStart(startResourceProcessor) }

func startResourceProcessor(ctx context.Context, a *App) {
	if os.Getenv("FLEET_RESOURCE_PROCESSOR") != "on" {
		return
	}
	k := kitOf(a)
	wake := make(chan struct{}, 1)
	go kitListen(ctx, a.store.pool.Config().ConnConfig.ConnString(), wake)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for ctx.Err() == nil {
			if err := k.pass(ctx); err != nil && ctx.Err() == nil {
				log.Printf("resource processor: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-ticker.C:
			}
		}
	}()
}

// kitListen: one listener per processor; a failed listener only delays work to the next tick.
func kitListen(ctx context.Context, url string, wake chan<- struct{}) {
	for ctx.Err() == nil {
		conn, err := pgx.Connect(ctx, url)
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN fleet_resource_work")
			for err == nil {
				if _, err = conn.WaitForNotification(ctx); err == nil {
					select {
					case wake <- struct{}{}:
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

// resourceStalled: the resource-stalled finding of a's processor, when one of its queues is failing.
func resourceStalled(a *App) (contract.Finding, bool) {
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
	f := contract.Finding{Rule: "resource-stalled", Subject: "resource processor", Value: float64(len(queues)), Cause: "problem:resource:stalled", Exemplars: []string{}}
	text(&f, fmt.Sprintf("The resource processor keeps failing to select or record its %s work. Retry pauses are up to %s.", strings.Join(queues, ", "), kitRetryEvery()))
	return f, true
}
