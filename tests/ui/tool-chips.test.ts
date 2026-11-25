import { test } from 'node:test';
import assert from 'node:assert/strict';
import { foldBlocks, toolChips, toolKind, callTarget } from '../../packages/ui/src/controls/tool-fold.ts';

// Conversations v2 (owner pick c2): a step's calls show as chips per kind; HAL0's turns mixed shell, Fleet and artifact calls.
test('tool kinds', () => {
  const kinds = Object.fromEntries(['commandExecution', 'Bash', 'Read', 'apply_patch', 'Grep', 'webSearch', 'web_search', 'artifact_save',
    'contextCompaction', 'questions_list', 'mcp__fleet__task_get', 'mcp__brilliant__edit', 'TodoWrite'].map(n => [n, toolKind(n)]));
  assert.deepEqual(kinds, {
    commandExecution: 'shell', Bash: 'shell', Read: 'files', apply_patch: 'edits', Grep: 'search', webSearch: 'web', web_search: 'web',
    artifact_save: 'results', contextCompaction: 'context', questions_list: 'fleet', mcp__fleet__task_get: 'fleet', mcp__brilliant__edit: 'edits', TodoWrite: 'other',
  });
});

test('a target names a code, never a bare UUID', () => {
  assert.equal(callTarget({ taskId: '398183a2-0923-40b6-ad4f-7a8a544a79ba', code: 'T-FLT-64' }), 'T-FLT-64');
  assert.equal(callTarget({ sessionId: '1e289036-e602-41b7-a0c4-70eb9412bb3a' }), '');
});

test('chips count calls and failures; a recovered failure leaves the line quiet', () => {
  const tool = (key: string, name: string, state: string, input: unknown = {}) => ({ kind: 'tool' as const, key, name, state, input, output: state === 'failed' ? { error: 'path must be inside' } : null, startedAt: 0, endedAt: 1000, executionId: 'e' });
  const rows = foldBlocks([
    tool('1:0', 'artifact_save', 'failed', { path: '/tmp/a.png' }),
    tool('1:1', 'artifact_save', 'completed', { path: 'a.png' }),
    tool('1:2', 'commandExecution', 'completed', { command: 'ls' }),
    tool('1:3', 'artifact_save', 'completed', { path: 'b.md' }),
  ] as never);
  const line = rows[0] as Extract<(typeof rows)[number], { kind: 'tools' }>;
  assert.equal(line.state, 'done');
  assert.deepEqual(toolChips(line.calls).map(c => [c.label, c.n, c.failed]), [['Results', 3, 1], ['Shell', 1, 0]]);
  const failedLine = foldBlocks([tool('2:0', 'commandExecution', 'failed', { command: 'git push' })] as never)[0] as typeof line;
  assert.equal(failedLine.state, 'failed');
});
