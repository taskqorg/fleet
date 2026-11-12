// Synthetic fixture data shared by the book and the page mockups. Shapes follow the inventory, values are invented.
import { states, type StateOf, type Harness, type Stage } from '../src/registry.ts';
import type { TreeNode, ThreadBlock } from '../src/controls.tsx';

export const NOW = Date.now();
export const min = 60e3, hr = 60 * min;

export type Task = {
  id: number; title: string; project: string; state: StateOf<'task'>; reason?: string; agent: Harness; host: string;
  started: number; cost: number | null; est: boolean; pinned?: boolean; priority: 0 | 1 | 2; stage?: Stage;
};
export const AGENTS: Harness[] = ['codex', 'claude', 'antigravity'];
export const HOSTS = ['pds18', 'windows', 'mac'];
export const PROJECTS = ['cs', 'game', 'fleet', 'design'];
export const TASK_STATES = Object.keys(states.task) as StateOf<'task'>[];
const WORDS = ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Compare 3D methods', 'Investigate disk alert', 'Write release notes', 'Character hands rework', 'Migrate store layout'];
const REASONS: Partial<Record<StateOf<'task'>, string>> = { waiting: 'acceptance by you', failed: 'render step exit 1', queued: 'no free Windows slot', paused: 'by Alex' };
// Skewed state mix: most tasks are finished, a few need attention.
const MIX: StateOf<'task'>[] = ['completed', 'completed', 'completed', 'completed', 'completed', 'in_progress', 'in_progress', 'queued', 'queued', 'waiting', 'failed', 'paused', 'cancelled'];

export const mkTask = (id: number): Task => {
  const state = MIX[id * 7 % MIX.length];
  return {
    id, title: `${WORDS[id % WORDS.length]} ${id % 97}`, project: PROJECTS[id % 4], state, reason: REASONS[state],
    agent: AGENTS[id % 3], host: HOSTS[id % 3], started: NOW - (id * 37 % 900) * min,
    cost: id % 11 === 0 ? null : (id % 130) / 10, est: id % 4 === 0,
    priority: id % 5 === 0 ? 0 : id % 2 ? 1 : 2,
    stage: state === 'in_progress' ? (['checks', 'pr_open', undefined, 'merging'] as const)[id % 4] : state === 'completed' && id % 3 === 0 ? 'merged' : state === 'failed' && id % 2 ? 'checks_failed' : undefined,
  };
};
export const tasks = (n: number, top = 10000) => Array.from({ length: n }, (_, i) => mkTask(top - i));

export const TREE: TreeNode[] = [
  { id: 'w', name: 'Worker · codex · windows', state: 'succeeded', time: { value: 540, basis: 'measured' }, tokens: { value: 120000, basis: 'measured' }, cost: { value: 0.3, basis: 'measured' }, children: [
    { id: 'w1', name: 'edit index.html', state: 'succeeded', time: { value: 120, basis: 'measured' } },
    { id: 'w2', name: 'run checks', note: 'reused from run 2', state: 'succeeded', time: { value: 60, basis: 'measured' } }] },
  { id: 'r', name: 'Reviewer · claude · mac', state: 'waiting', time: { value: 240, basis: 'measured' }, tokens: { value: 62000, basis: 'measured' }, cost: { value: 0.12, basis: 'estimated' }, children: [
    { id: 'r1', name: 'screenshot phone', state: 'running', lastSeen: NOW - 4 * min, time: { value: 40, basis: 'measured' } },
    { id: 'r2', name: 'question: accept?', state: 'waiting', time: { value: 720, basis: 'measured' } }] },
  { id: 'm', name: 'Merge · program', state: 'preparing', time: { value: null, basis: 'unavailable', reason: 'not started' }, children: [
    { id: 'm1', name: 'merge to main', state: 'preparing' }] },
];

export const transcript = (): ThreadBlock[] => [
  { kind: 'boundary', key: '1:0', executionId: 'b61f0c2a-0000', state: 'running', harness: 'claude', model: 'claude-opus-5-5', effort: 'high', startedAt: NOW - 10 * min },
  { kind: 'text', key: '2:0', role: 'operator', markdown: 'Rework the hero copy; keep it short.' },
  { kind: 'reasoning', key: '3:0', markdown: 'The hero is in index.html.\nRead it first.' },
  { kind: 'tool', key: '4:0', name: 'Read', input: { path: 'index.html' }, output: '<h1>Fleet</h1>', state: 'completed', startedAt: NOW - 9 * min, endedAt: NOW - 9 * min + 1200 },
  ...Array.from({ length: 40 }, (_, i): ThreadBlock => i % 2
    ? { kind: 'tool', key: `${10 + i}:0`, name: 'Bash', input: { command: 'npm test' }, output: 'ok', state: 'completed', startedAt: NOW - 8 * min, endedAt: NOW - 8 * min + (i % 9) * 1000 }
    : { kind: 'text', key: `${10 + i}:0`, role: 'agent', harness: 'claude', model: 'claude-opus-5-5', effort: 'high', markdown: `Step ${i} done.${i % 4 === 0 ? ' Long replies wrap and keep their measured height.\n\n```\nnpm test\n```' : ''}` }),
  { kind: 'question', key: '60:0', questionId: 'q1', text: 'Use Postgres 17?', options: ['yes', 'no'], state: 'open' },
];

export type Host = {
  id: string; platform: string; online: boolean; lastSeen: number; harnesses: number;
  running: number; slots: number; queued: number; byHarness: Partial<Record<Harness, number>>; store: { total: number | null; segments: { label: string; bytes: number }[]; warnAt: number; stale?: number };
  capacity: { value: string; status: { kind: 'applied'; at: number } | { kind: 'pending' } | { kind: 'apply_error'; error: string } };
};
const G = 1e9;
export const hosts: Host[] = [
  { id: 'pds18', platform: 'linux', online: true, lastSeen: NOW - 5e3, harnesses: 4, running: 6, slots: 8, queued: 2, byHarness: { codex: 4, claude: 2 },
    store: { total: 1000 * G, segments: [{ label: 'cs', bytes: 400 * G }, { label: 'fleet', bytes: 220 * G }], warnAt: 50 * G }, capacity: { value: '1 TB', status: { kind: 'applied', at: NOW - 18 * min } } },
  { id: 'windows', platform: 'windows', online: true, lastSeen: NOW - 40e3, harnesses: 3, running: 3, slots: 3, queued: 5, byHarness: { codex: 1, claude: 1, antigravity: 1 },
    store: { total: 200 * G, segments: [{ label: 'game', bytes: 156 * G }, { label: 'design', bytes: 26 * G }], warnAt: 25 * G }, capacity: { value: '200 GB', status: { kind: 'apply_error', error: 'Garage admin timeout' } } },
  { id: 'mac', platform: 'macos', online: false, lastSeen: NOW - 2 * hr, harnesses: 3, running: 1, slots: 2, queued: 0, byHarness: { claude: 1 },
    store: { total: 500 * G, segments: [{ label: 'design', bytes: 150 * G }], warnAt: 25 * G, stale: NOW - 2 * hr }, capacity: { value: '500 GB', status: { kind: 'pending' } } },
];
