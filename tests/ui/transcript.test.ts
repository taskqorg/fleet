// Conversation transcript from executor events as the coordinator stores them: the native record under `data`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { entries } from '../../app/src/transcript.ts';

test('replies of every harness and tool calls are shown', () => {
  const s = {
    executions: [{ id: 'x1', startedAt: 1 }, { id: 'x2', startedAt: 2 }],
    messages: [{ id: 'm1', executionId: 'x1', text: 'hi' }, { id: 'm2', executionId: 'x2', text: 'shalom' }],
    events: [
      { id: 'a', executionId: 'x1', type: 'item.completed', timestamp: 5, data: { item: { type: 'userMessage' }, fleetNormalized: { messages: [] } } },
      { id: 'b', executionId: 'x1', type: 'item.completed', timestamp: 6, data: { item: { type: 'commandExecution', command: 'ls', status: 'completed' } } },
      { id: 'c', executionId: 'x1', type: 'item.completed', timestamp: 7, data: { item: { type: 'agent_message', text: 'Hi!' }, fleetNormalized: { messages: [{ role: 'assistant', text: 'Hi!' }] } } },
      { id: 'd', executionId: 'x2', type: 'assistant', timestamp: 8, data: { type: 'assistant', fleetNormalized: { messages: [{ role: 'assistant', text: 'Sha' }] } } },
      { id: 'd2', executionId: 'x2', type: 'chunk', timestamp: 8, data: { fleetNormalized: { messages: [{ role: 'assistant', text: 'lom!', delta: true }] } } },
      { id: 'f', executionId: 'x2', type: 'item.completed', timestamp: 9, data: { item: { type: 'agent_message', text: 'Old.' } } },
      { id: 'e', executionId: 'x2', type: 'result', timestamp: 9, data: { fleetNormalized: { messages: [] } } },
    ],
  };
  assert.deepEqual(entries(s).map(e => (e as any).text ?? (e as any).call.name), ['hi', 'ls', 'Hi!', 'shalom', 'Shalom!', 'Old.']);
});
