// Requirement: nav-rail.md "Bots": the zone shows the pinned bots in pin order, then the active ones, then up to the person's
// recent count of the others, most recently run first. Risk: a pinned bot drops out, an active bot hides behind the count,
// or a stale bot outranks a recent one.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { railBotRows, type RailBot } from '../../packages/ui/src/controls/rail-bot-rows.ts';

const bot = (id: string, state: RailBot['state'], at: number | undefined, pinned = false): RailBot => ({ id, name: id, state, at, pinned, href: '#' });

test('railBotRows: pinned in order, then active, then recent up to the count', () => {
  const bots = [bot('p2', 'asleep', 1, true), bot('p1', 'failed', 9, true), bot('old', 'asleep', 2), bot('run', 'working', 3),
    bot('ask', 'waiting', 8), bot('new', 'asleep', 7), bot('never', 'asleep', undefined), bot('stop', 'stopped', 5)];
  assert.deepEqual(railBotRows(bots, 0).map(b => b.id), ['p2', 'p1', 'ask', 'run']);
  assert.deepEqual(railBotRows(bots, 2).map(b => b.id), ['p2', 'p1', 'ask', 'run', 'new', 'stop']);
  assert.deepEqual(railBotRows(bots, 99).map(b => b.id), ['p2', 'p1', 'ask', 'run', 'new', 'stop', 'old', 'never']);
  assert.deepEqual(railBotRows(bots, -1).map(b => b.id), ['p2', 'p1', 'ask', 'run']);
});
