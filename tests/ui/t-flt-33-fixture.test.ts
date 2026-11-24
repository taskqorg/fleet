// Requirement: T-FLT-33 (OR-33, OR-37, fleet.md observation confidence): preserve the unresolved Tasks Running filter,
// separate task age (first execution start) from current execution activity, and base the execution-duration warning
// only on credible active run evidence while showing stale, stalled, and unconfirmed signals separately.
// Risk: non-running tasks or stale/unconfirmed/short continuations trigger false red "running too long" warnings from task age.
import assert from 'node:assert/strict';
import test from 'node:test';
import { SYSTEM_VIEWS } from '../../app/src/system-views.ts';
import { describeTaskFacts, isUnconfirmedActiveTask } from '../../app/src/task-facts.ts';
import { formatView, listKey, parseView, toPageParams } from '../../packages/view-url/view-url.ts';

export const legacyUrl = '/tasks?group=none&preset=Running&project=fixture&q=sample&sort=created&state=held,in_progress,paused,queued,waiting';
export const now = '2026-11-23T18:00:00Z';
const nowMs = Date.parse(now);
const firstStart = '2026-11-23T04:00:00Z';
const longStart = '2026-11-23T15:30:00Z';
const shortStart = '2026-11-23T17:45:00Z';
const recent = '2026-11-23T17:59:30Z';
const stale = '2026-11-23T16:30:00Z';

const completed = { kind: 'agent_run', state: 'succeeded', observation: 'confirmed', startedAt: firstStart, endedAt: '2026-11-23T12:02:00Z', lastSeenAt: '2026-11-23T12:02:00Z' };
const active = { kind: 'agent_run', state: 'running', observation: 'confirmed', startedAt: longStart, endedAt: null, lastSeenAt: recent };

export const cases = [
  { name: 'paused', state: 'paused', waitReason: null, runs: [completed], finding: null, expected: 'neutral task age' },
  { name: 'owner waiting', state: 'waiting', waitReason: 'question', runs: [completed], finding: null, expected: 'neutral task age and waiting state' },
  { name: 'in progress after a completed run', state: 'in_progress', waitReason: null, runs: [completed], finding: null, expected: 'neutral task age' },
  { name: 'short confirmed continuation', state: 'in_progress', waitReason: null, runs: [completed, { ...active, startedAt: shortStart }], finding: null, expected: '15m current execution without warning' },
  { name: 'long confirmed continuation', state: 'in_progress', waitReason: null, runs: [completed, active], finding: null, expected: '2h 30m current execution with duration warning' },
  { name: 'stale running record before reconciliation', state: 'in_progress', waitReason: null, runs: [completed, { ...active, lastSeenAt: stale }], finding: null, expected: 'stale execution evidence without duration warning' },
  { name: 'stalled confirmed continuation', state: 'in_progress', waitReason: null, runs: [completed, { ...active, lastSeenAt: stale }], finding: 'stuck', expected: 'stalled indicator without duration warning' },
  { name: 'unconfirmed continuation', state: 'in_progress', waitReason: null, runs: [completed, { ...active, observation: 'unconfirmed', lastSeenAt: stale }], finding: null, expected: 'unconfirmed indicator without duration warning' },
].map(c => ({ ...c, startedAt: firstStart }));

function toTaskRow(c: (typeof cases)[number]) {
  const activeRun = [...c.runs].reverse().find(r => r.state === 'preparing' || r.state === 'running');
  return {
    state: c.state,
    startedAt: c.startedAt,
    stateAt: c.startedAt,
    agent: activeRun ? 'codex' : null,
    lastAgent: 'codex',
    executions: c.runs.length,
    openQuestions: c.waitReason === 'question' ? 1 : 0,
    activeState: activeRun?.state ?? null,
    activeStartedAt: activeRun?.startedAt ?? null,
    activeObservation: activeRun?.observation ?? null,
    activeLastSeenAt: activeRun?.lastSeenAt ?? null,
    activeStalled: c.finding === 'stuck',
  };
}

test('legacy Running links preserve the unresolved state filter, project, search, grouping and sort (requirement: T-FLT-33, OR-33; risk: renaming or dropping the preset breaks saved views and shared links)', () => {
  const view = parseView(legacyUrl);
  assert.ok(view);
  const preset = SYSTEM_VIEWS.tasks.find(p => p.name === 'Running');
  assert.ok(preset);
  assert.deepEqual(preset.view.state?.toSorted(), view.state);
  assert.equal(formatView(view), legacyUrl);
  assert.deepEqual(toPageParams(view), {
    filter: { projectId: ['fixture'], search: 'sample', state: ['held', 'in_progress', 'paused', 'queued', 'waiting'] },
    group: 'none',
    sort: 'created',
  });
  assert.equal(listKey(view), listKey({ ...view, preset: undefined }));
  assert.ok(cases.every(c => view.state?.includes(c.state)));
});

test('the long confirmed run and the old task supply different duration inputs (requirement: T-FLT-33; risk: task age is confused with current run age)', () => {
  const hoursSince = (t: string) => (Date.parse(now) - Date.parse(t)) / 3_600_000;
  for (const c of cases) assert.equal(hoursSince(c.startedAt), 14, c.name);
  assert.equal(hoursSince(active.startedAt), 2.5);
  assert.equal(hoursSince(shortStart), 0.25);
  assert.equal(hoursSince(stale), 1.5);
});

test('TaskFacts separates task age from credible active execution evidence across paused, waiting, idle in_progress, active, stale, stalled, and unconfirmed runs (requirement: T-FLT-33, OR-37; risk: task.startedAt or stale/stalled/unconfirmed runs trigger false running-too-long warnings)', () => {
  const byName = Object.fromEntries(cases.map(c => [c.name, describeTaskFacts(toTaskRow(c), nowMs)]));

  for (const idleName of ['paused', 'owner waiting', 'in progress after a completed run']) {
    const f = byName[idleName];
    assert.equal(f.active.kind, 'none', idleName);
    assert.equal(f.showTaskAge, true, idleName);
    assert.equal(f.taskAgeAt, Date.parse(firstStart), idleName);
    assert.equal(f.runningHarness, null, `${idleName} has no active harness`);
  }

  const short = byName['short confirmed continuation'];
  assert.deepEqual(short.active, { kind: 'active', startedAt: Date.parse(shortStart), warnAfterSeconds: 7200 });
  assert.equal(short.showTaskAge, true);
  assert.equal(short.runningHarness, 'codex');

  const long = byName['long confirmed continuation'];
  assert.deepEqual(long.active, { kind: 'active', startedAt: Date.parse(longStart), warnAfterSeconds: 7200 });
  assert.ok(nowMs - long.active.startedAt >= long.active.warnAfterSeconds * 1000);
  assert.equal(long.showTaskAge, true);
  assert.equal(long.runningHarness, 'codex');

  const staleCase = byName['stale running record before reconciliation'];
  assert.deepEqual(staleCase.active, { kind: 'stale', startedAt: Date.parse(longStart), lastSeenAt: Date.parse(stale) });
  assert.equal(staleCase.showTaskAge, true);

  const stalledCase = byName['stalled confirmed continuation'];
  assert.deepEqual(stalledCase.active, { kind: 'stalled', startedAt: Date.parse(longStart), lastSeenAt: Date.parse(stale) });
  assert.equal(stalledCase.showTaskAge, true);

  const unconfirmedCase = byName['unconfirmed continuation'];
  assert.deepEqual(unconfirmedCase.active, { kind: 'unconfirmed', startedAt: Date.parse(longStart), lastSeenAt: Date.parse(stale) });
  assert.equal(unconfirmedCase.showTaskAge, true);
  assert.equal(isUnconfirmedActiveTask(toTaskRow(cases.find(c => c.name === 'unconfirmed continuation')!)), true);
  assert.equal(isUnconfirmedActiveTask(toTaskRow(cases.find(c => c.name === 'long confirmed continuation')!)), false);
});

test('TaskFacts handles single-run active tasks and preparing runs without duplicate or false warnings (requirement: T-FLT-33; risk: preparing runs or fresh single runs show redundant or premature warnings)', () => {
  const singleActive = describeTaskFacts({
    state: 'in_progress',
    startedAt: longStart,
    stateAt: longStart,
    agent: 'claude',
    lastAgent: 'claude',
    executions: 1,
    openQuestions: 0,
    activeState: 'running',
    activeStartedAt: longStart,
    activeObservation: 'confirmed',
    activeLastSeenAt: recent,
    activeStalled: false,
  }, nowMs);
  assert.equal(singleActive.active.kind, 'active');
  assert.equal(singleActive.showTaskAge, false, 'single run with identical start avoids repeating 2h twice');

  const preparing = describeTaskFacts({
    state: 'in_progress',
    startedAt: firstStart,
    stateAt: firstStart,
    agent: 'claude',
    lastAgent: 'claude',
    executions: 2,
    openQuestions: 0,
    activeState: 'preparing',
    activeStartedAt: null,
    activeObservation: 'confirmed',
    activeLastSeenAt: null,
    activeStalled: false,
  }, nowMs);
  assert.equal(preparing.active.kind, 'preparing');
  assert.equal(preparing.showTaskAge, true);
});
