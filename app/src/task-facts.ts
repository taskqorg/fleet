// Pure task row activity and duration-evidence derivation for Tasks (T-FLT-33).
// Separates task age (first execution start) from current active execution evidence so only
// confirmed, non-stale, non-stalled running executions trigger a duration warning.
import type { TaskRow as ContractTaskRow } from '../../contracts/generated/ops.ts';
import vocabulary from '../../contracts/vocabulary.json' with { type: 'json' };
import type { Harness } from '../../packages/ui/src/registry.ts';

export const TERMINAL_TASK_STATES = ['completed', 'failed', 'cancelled'] as const;
/** Matches coordinator ruleStuck (30m) for judging whether active execution contact is stale. */
export const ACTIVE_EXECUTION_STALE_MS = 30 * 60 * 1000;
/** Threshold (2h) for warning that a credibly active execution is running too long. */
export const ACTIVE_EXECUTION_WARN_SECONDS = 2 * 3600;

export type TaskFactsInput = Pick<
  ContractTaskRow,
  | 'state'
  | 'startedAt'
  | 'stateAt'
  | 'agent'
  | 'lastAgent'
  | 'executions'
  | 'openQuestions'
  | 'activeState'
  | 'activeStartedAt'
  | 'activeObservation'
  | 'activeLastSeenAt'
  | 'activeStalled'
>;

export type ActiveRunStatus =
  | { kind: 'none' }
  | { kind: 'preparing'; startedAt: number | null }
  | { kind: 'active'; startedAt: number; warnAfterSeconds: number }
  | { kind: 'stale'; startedAt: number | null; lastSeenAt: number }
  | { kind: 'stalled'; startedAt: number | null; lastSeenAt: number | null }
  | { kind: 'unconfirmed'; startedAt: number | null; lastSeenAt: number | null };

export type TaskFactsSummary = {
  terminal: boolean;
  openQuestions: number;
  executions: number;
  taskAgeAt: number | null;
  stateAgeAt: number | null;
  showTaskAge: boolean;
  active: ActiveRunStatus;
  runningHarness: Harness | null;
};

export function parseMs(v: string | number | null | undefined): number | null {
  if (v === null || v === undefined) return null;
  const n = typeof v === 'number' ? v : Date.parse(v);
  return Number.isFinite(n) ? n : null;
}

const HARNESS_IDS = new Set<string>(vocabulary.harnesses.map(h => h.id));
const isHarness = (h: string | null | undefined): h is Harness => !!h && HARNESS_IDS.has(h);

export function isUnconfirmedActiveTask(t: Pick<TaskFactsInput, 'state' | 'activeState' | 'activeObservation'>): boolean {
  if (TERMINAL_TASK_STATES.includes(t.state as (typeof TERMINAL_TASK_STATES)[number])) return false;
  return (t.activeState === 'preparing' || t.activeState === 'running') && t.activeObservation === 'unconfirmed';
}

export function describeTaskFacts(t: TaskFactsInput, now: number): TaskFactsSummary {
  const terminal = TERMINAL_TASK_STATES.includes(t.state as (typeof TERMINAL_TASK_STATES)[number]);
  const taskAgeAt = parseMs(t.startedAt);
  const stateAgeAt = parseMs(t.stateAt);
  const hasActive = !terminal && (t.activeState === 'preparing' || t.activeState === 'running');

  let active: ActiveRunStatus = { kind: 'none' };
  if (hasActive) {
    const startedAt = parseMs(t.activeStartedAt);
    const lastSeenAt = parseMs(t.activeLastSeenAt);
    if (t.activeObservation === 'unconfirmed') {
      active = { kind: 'unconfirmed', startedAt, lastSeenAt };
    } else if (t.activeStalled) {
      active = { kind: 'stalled', startedAt, lastSeenAt };
    } else if (t.activeState === 'preparing') {
      active = { kind: 'preparing', startedAt };
    } else if (lastSeenAt !== null && now - lastSeenAt >= ACTIVE_EXECUTION_STALE_MS) {
      active = { kind: 'stale', startedAt, lastSeenAt };
    } else if (lastSeenAt === null && startedAt !== null && now - startedAt >= ACTIVE_EXECUTION_STALE_MS) {
      active = { kind: 'stale', startedAt, lastSeenAt: startedAt };
    } else if (startedAt !== null && lastSeenAt !== null) {
      active = { kind: 'active', startedAt, warnAfterSeconds: ACTIVE_EXECUTION_WARN_SECONDS };
    } else {
      active = { kind: 'preparing', startedAt };
    }
  }

  // Show task age on non-terminal tasks unless a single active run shares the exact same start time.
  const sameAsSingleActiveRun =
    active.kind === 'active' && taskAgeAt !== null && Math.abs(active.startedAt - taskAgeAt) < 60_000;
  const showTaskAge = taskAgeAt !== null && !terminal && !sameAsSingleActiveRun;

  return {
    terminal,
    openQuestions: t.openQuestions ?? 0,
    executions: t.executions ?? 0,
    taskAgeAt,
    stateAgeAt,
    showTaskAge,
    active,
    runningHarness: hasActive && isHarness(t.agent) ? t.agent : null,
  };
}
