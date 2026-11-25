import type { BotListItem, BotRunRecord, BotRunState, BotSettings, BotTaskRecord } from '../../contracts/generated/ops-bots.ts';
import { states, type StateOf } from '../../packages/ui/src/registry.ts';
import type { AvatarPicture, AvatarState } from '../../packages/ui/src/controls/avatar.tsx';

export type BotTab = 'activity' | 'automations' | 'tasks' | 'runs' | 'settings';
export type BotTaskBucket = 'all' | 'active' | 'waiting' | 'history';
export type BotRunFilter = 'all' | 'active' | 'quiet' | 'skipped' | 'queued' | 'failed' | 'unconfirmed';

export const BOT_TABS: readonly { id: BotTab; label: string }[] = [
  { id: 'activity', label: 'Overview' },
  { id: 'automations', label: 'Automations' },
  { id: 'tasks', label: 'Tasks' },
  { id: 'runs', label: 'Runs' },
  { id: 'settings', label: 'Settings & Memory' },
];

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

const BOT_RUN_STATE_SET = new Set(Object.keys(states.botRun));

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

/** What woke a bot run (BotRunCause), in words: a list never shows the stored value. */
export const WAKE: Record<string, string> = { manual: 'Woken by hand', schedule: 'Daily schedule', needs_you: 'Needs you', message: 'New message' };
export const wakeLabel = (c: string) => WAKE[c] ?? 'Other wake';

/** Why a wake did not run (BotRunRecord.reason): the two stored codes in words; any other reason is already a sentence. */
const SKIP: Record<string, string> = { paused: 'The bot is paused', active_run: 'Another run of this bot was still going' };
export const reasonLabel = (r: string | null | undefined) => r ? SKIP[r] ?? r : undefined;

/** Where a wake came from (BotRunRecord.source): the stored operation names in words; any other source is already words. */
const SOURCE: Record<string, string> = { bot_run: 'Run now', schedule: 'Schedule', needs_you: 'Needs you', message: 'Message' };
export const sourceLabel = (s: string | null | undefined) => s ? SOURCE[s] ?? s : undefined;

/** A bot's activity as its avatar shows it (controls/avatar.md): paused, waiting for a person, working, failed or asleep. */
export function botAvatarState(b: Pick<BotListItem, 'paused' | 'activeRunId' | 'lastRunState' | 'needsYou'>): AvatarState {
  if (b.paused) return 'paused';
  if (b.needsYou) return 'waiting';
  if (b.activeRunId) return 'working';
  return b.lastRunState === 'failed' ? 'failed' : 'asleep';
}

/** A bot's picture for `Avatar`, read through /api/results; undefined while it shows its letter. */
export function botPicture(b: Pick<BotListItem, 'avatar'>): AvatarPicture | undefined {
  const a = b.avatar;
  if (!a) return undefined;
  return {
    src: `/api/results/${encodeURIComponent(a.artifactId)}`,
    still: a.stillArtifactId ? `/api/results/${encodeURIComponent(a.stillArtifactId)}` : undefined,
    video: a.mediaType.startsWith('video/'),
    glow: a.glow || undefined,
  };
}
