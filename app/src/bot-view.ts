import type { BotListItem, BotRunRecord, BotRunState, BotSettings, BotTaskRecord } from '../../contracts/generated/ops-bots.ts';
import type { AnalyticsBucketRow, AnalyticsTotals } from '../../contracts/generated/ops-analytics.ts';
import type { CostFigure, MeasuredNumber, ResourceBasis } from '../../contracts/generated/ops.ts';
import { msMeasured, numMeasured, type Measured } from '../../packages/ui/src/format.ts';
import type { StateOf } from '../../packages/ui/src/registry.ts';

export type BotTab = 'activity' | 'tasks' | 'runs' | 'questions' | 'analytics' | 'settings';
export type BotTaskBucket = 'all' | 'active' | 'waiting' | 'history';
export type BotRunFilter = 'all' | 'active' | 'quiet' | 'skipped' | 'queued' | 'failed' | 'unconfirmed';
export type BotAnalyticsWindowId = 'all' | '24h' | '7d' | '30d';

const DAY_MS = 86_400_000;

export const BOT_TABS: readonly { id: BotTab; label: string }[] = [
  { id: 'activity', label: 'Overview' },
  { id: 'tasks', label: 'Tasks' },
  { id: 'runs', label: 'Runs' },
  { id: 'questions', label: 'Questions & Errors' },
  { id: 'analytics', label: 'Analytics' },
  { id: 'settings', label: 'Settings & Memory' },
];

export const BOT_ANALYTICS_WINDOWS: readonly { id: BotAnalyticsWindowId; label: string }[] = [
  { id: 'all', label: 'All time' },
  { id: '30d', label: '30d' },
  { id: '7d', label: '7d' },
  { id: '24h', label: '24h' },
];

/** Compute [from, to) epoch bounds matching Fleet analytics semantics for bot views. */
export function analyticsWindowBounds(win: BotAnalyticsWindowId, nowMs: number = Date.now()): { from: number; to: number } {
  const to = nowMs + 60_000;
  if (win === 'all') return { from: 0, to };
  const days = win === '24h' ? 1 : win === '7d' ? 7 : 30;
  return { from: Math.max(0, nowMs - days * DAY_MS), to };
}

/** Group managed bot tasks into active, waiting, and historical buckets. */
export function classifyBotTaskBucket(state: string): Exclude<BotTaskBucket, 'all'> {
  if (state === 'queued' || state === 'in_progress') return 'active';
  if (state === 'waiting' || state === 'held' || state === 'paused') return 'waiting';
  return 'history';
}

export function filterBotTasks(tasks: readonly BotTaskRecord[], bucket: BotTaskBucket): BotTaskRecord[] {
  if (bucket === 'all') return [...tasks];
  return tasks.filter(t => classifyBotTaskBucket(t.state) === bucket);
}

/** Filter bot wake runs by lifecycle/observability state cleanly. */
export function filterBotRuns(runs: readonly BotRunRecord[], filter: BotRunFilter): BotRunRecord[] {
  switch (filter) {
    case 'all': return [...runs];
    case 'active': return runs.filter(r => r.state === 'queued' || r.state === 'running' || r.state === 'waiting');
    case 'quiet': return runs.filter(r => r.quiet);
    case 'skipped': return runs.filter(r => r.state === 'skipped');
    case 'queued': return runs.filter(r => r.state === 'queued');
    case 'failed': return runs.filter(r => r.state === 'failed');
    case 'unconfirmed': return runs.filter(r => r.observation === 'unconfirmed');
  }
}

/** Format configured bot wake triggers for header and overview display. */
export function formatBotTriggers(s: Pick<BotSettings, 'scheduleTime' | 'scheduleZone' | 'wakeOnNeedsYou'>): string {
  const parts: string[] = [];
  if (s.scheduleTime) {
    parts.push(`Daily at ${s.scheduleTime}${s.scheduleZone ? ` (${s.scheduleZone})` : ''}`);
  } else {
    parts.push('No daily schedule');
  }
  parts.push(s.wakeOnNeedsYou ? 'Wake on Needs you on' : 'Wake on Needs you off');
  return parts.join(' · ');
}

const BOT_RUN_STATE_SET = new Set<BotRunState>(['queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled', 'skipped']);

/** Resolve badge family and state for a bot summary or record. */
export function botStatusBadge(bot: Pick<BotListItem, 'paused' | 'activeRunId' | 'lastRunState'>):
  | { family: 'task'; state: StateOf<'task'> }
  | { family: 'botRun'; state: StateOf<'botRun'> }
  | null {
  if (bot.paused) return { family: 'task', state: 'paused' };
  if (bot.activeRunId) return { family: 'botRun', state: 'running' };
  if (bot.lastRunState && BOT_RUN_STATE_SET.has(bot.lastRunState as BotRunState)) {
    return { family: 'botRun', state: bot.lastRunState as StateOf<'botRun'> };
  }
  return bot.lastRunState ? { family: 'botRun', state: 'queued' } : null;
}

export function measureToUiNum(m: MeasuredNumber | undefined): Measured {
  return numMeasured(m ? { value: m.value, basis: m.basis as ResourceBasis } : undefined);
}

export function measureToUiMs(m: MeasuredNumber | undefined): Measured {
  return msMeasured(m?.value ?? null, (m?.basis as ResourceBasis | undefined) ?? 'unavailable');
}

export function sumTokenMeasures(measures: AnalyticsTotals['measures']): Measured {
  const tin = measures.tokens_input;
  const tout = measures.tokens_output;
  if (!tin && !tout) return numMeasured(undefined);
  if (tin?.value === null && tout?.value === null) {
    return numMeasured({ value: null, basis: tin.basis as ResourceBasis });
  }
  const value = (tin?.value ?? 0) + (tout?.value ?? 0);
  const basis = tin?.basis === 'subscription' || tout?.basis === 'subscription'
    ? 'subscription'
    : tin?.basis === 'estimated' || tout?.basis === 'estimated'
      ? 'estimated'
      : 'measured';
  return numMeasured({ value, basis });
}

export function primaryUsdCost(cost: readonly CostFigure[]): number {
  let usd = 0;
  for (const c of cost) {
    if (c.currency === 'USD' && c.value != null) usd += c.value;
  }
  return usd;
}

export function bucketProjectSortKey(row: AnalyticsBucketRow, col: 'project' | 'executions' | 'failed' | 'wall' | 'active' | 'tokens' | 'cost'): number | string {
  switch (col) {
    case 'project': return row.dimensions.project ?? 'unknown';
    case 'executions': return row.measures.executions?.value ?? -1;
    case 'failed': return row.measures.failed?.value ?? -1;
    case 'wall': return row.measures.wall_ms?.value ?? -1;
    case 'active': return row.measures.active_ms?.value ?? -1;
    case 'tokens': return sumTokenMeasures(row.measures).value ?? -1;
    case 'cost': return primaryUsdCost(row.cost);
  }
}
