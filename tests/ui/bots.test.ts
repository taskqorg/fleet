// Requirement: FLT-20 PR 4 Web controls expose active, waiting, and historical managed tasks,
// bot runs (quiet, skipped, queued, failed, unconfirmed), triggers and Run now / Pause / Stop / Talk to it
// controls; in Library a bot's questions show on Home and its cost on System > Spend (pages/library.md).
// Risk: UI task/run filters, status chips or trigger formatting misclassify states, or regress SolidJS 2.0 +
// packages/ui rules.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  botStatusBadge, classifyBotTaskBucket,
  filterBotRuns, filterBotTasks,
} from '../../app/src/bot-view.ts';
import type { BotRunRecord, BotTaskRecord } from '../../contracts/generated/ops-bots.ts';

test('bot task classification separates active, waiting, and historical managed tasks', () => {
  assert.equal(classifyBotTaskBucket('queued'), 'active');
  assert.equal(classifyBotTaskBucket('in_progress'), 'active');
  assert.equal(classifyBotTaskBucket('waiting'), 'waiting');
  assert.equal(classifyBotTaskBucket('held'), 'waiting');
  assert.equal(classifyBotTaskBucket('paused'), 'waiting');
  assert.equal(classifyBotTaskBucket('completed'), 'history');
  assert.equal(classifyBotTaskBucket('failed'), 'history');
  assert.equal(classifyBotTaskBucket('cancelled'), 'history');

  const mkTask = (taskId: string, state: string): BotTaskRecord => ({
    botId: 'b1', botName: 'HAL', taskId, projectId: 'p1', sessionId: null,
    executionId: null, linkKind: 'created', createdBy: 'bot', createdAt: '2026-01-01T00:00:00Z',
    taskCode: 'T-1', title: taskId, state, waitReason: null, pauseReason: null, priority: 'normal',
    acceptedAt: null, acceptedBy: null, openQuestions: 0, executionCount: 1, executionsByState: {}, updatedAt: '2026-01-01T00:00:00Z',
  });
  const tasks = [mkTask('t-act', 'in_progress'), mkTask('t-wait', 'waiting'), mkTask('t-done', 'completed')];
  assert.deepEqual(filterBotTasks(tasks, 'active').map(t => t.taskId), ['t-act']);
  assert.deepEqual(filterBotTasks(tasks, 'waiting').map(t => t.taskId), ['t-wait']);
  assert.deepEqual(filterBotTasks(tasks, 'history').map(t => t.taskId), ['t-done']);
  assert.equal(filterBotTasks(tasks, 'all').length, 3);
});

test('bot runs filtering isolates quiet, skipped, queued, failed, and unconfirmed wakes', () => {
  const mkRun = (id: string, over: Partial<BotRunRecord>): BotRunRecord => ({
    id, botId: 'b1', projectId: 'p1', botRevision: 1, memoryRevision: 0,
    sessionId: 's1', executionId: 'e1', cause: 'manual',
    state: 'succeeded', observation: 'confirmed', quiet: false,
    reason: null, sentBy: 'user@test', source: 'web', hostId: 'h1', problem: null,
    createdAt: '2026-01-01T00:00:00Z', startedAt: null, endedAt: null, linkedTaskIds: [],
    ...over,
  });
  const runs = [
    mkRun('r-quiet', { state: 'succeeded', quiet: true }),
    mkRun('r-skip', { state: 'skipped', reason: 'active_run' }),
    mkRun('r-queue', { state: 'queued' }),
    mkRun('r-fail', { state: 'failed', problem: 'exit 1' }),
    mkRun('r-unconf', { state: 'running', observation: 'unconfirmed' }),
  ];
  assert.deepEqual(filterBotRuns(runs, 'quiet').map(r => r.id), ['r-quiet']);
  assert.deepEqual(filterBotRuns(runs, 'skipped').map(r => r.id), ['r-skip']);
  assert.deepEqual(filterBotRuns(runs, 'queued').map(r => r.id), ['r-queue']);
  assert.deepEqual(filterBotRuns(runs, 'failed').map(r => r.id), ['r-fail']);
  assert.deepEqual(filterBotRuns(runs, 'unconfirmed').map(r => r.id), ['r-unconf']);
  assert.deepEqual(filterBotRuns(runs, 'active').map(r => r.id), ['r-queue', 'r-unconf']);
});

test('bot status badges preserve observation semantics', () => {
  assert.deepEqual(botStatusBadge({ paused: true, activeRunId: 'r1', lastRunState: 'failed' }), { family: 'task', state: 'paused' });
  assert.deepEqual(botStatusBadge({ paused: false, activeRunId: 'r1', lastRunState: 'failed' }), { family: 'botRun', state: 'running' });
  assert.deepEqual(botStatusBadge({ paused: false, activeRunId: null, lastRunState: 'skipped' }), { family: 'botRun', state: 'skipped' });
  assert.deepEqual(botStatusBadge({ paused: false, activeRunId: null, lastRunState: 'unexpected' }), { family: 'botRun', state: 'queued' });
  assert.equal(botStatusBadge({ paused: false, activeRunId: null, lastRunState: null }), null);
});

test('bots view exposes full Web controls without raw HTML controls or explicit any', () => {
  const src = readFileSync(new URL('../../app/src/views/bots.tsx', import.meta.url), 'utf8');
  for (const required of ['Run now', 'Pause', 'Resume', 'Stop', 'Talk to it', 'bot_run', 'bot_pause', 'bot_stop', 'bot_tasks', 'bot_runs']) {
    assert.ok(src.includes(required), `missing ${required} in app/src/views/bots.tsx`);
  }
  assert.ok(!/:\s*any\b|\bas\s+any\b/.test(src), 'bots.tsx must have zero explicit any');
  assert.ok(!/<(button|input|select|textarea|table|form|dialog|details)\b/.test(src), 'bots.tsx must use packages/ui controls only');
});
