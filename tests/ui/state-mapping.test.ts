// Requirement: state-badge.md "Dot and word" (OR-55): every screen reads a state through the registry. Risk (the copies this
// replaced): a preparing session reads Queued on Home and Working in its view, an unknown state reads Working on Home and
// Asleep elsewhere, and Live's open tasks leave out a paused one.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { life, statesOf, openStates, finishedStates, isFinished, lifeOf } from '../../packages/ui/src/states.ts';

test('life: one word per state, the same on Home and in the session view', () => {
  assert.equal(life('session', 'preparing'), 'working');
  assert.equal(life('session', 'running'), 'working');
  assert.equal(life('session', 'idle'), 'asleep');
  assert.equal(life('session', 'some-new-state'), 'asleep');
  assert.equal(life('task', 'held'), 'waiting');
  assert.equal(life('task', undefined), 'asleep');
});

test('statesOf: the list filter of a word is every state that reads as it', () => {
  assert.deepEqual(statesOf('session', ['working']), ['preparing', 'running']);
  assert.deepEqual(statesOf('session', ['queued']), []);
  assert.deepEqual(statesOf('task', ['working', 'queued']), ['queued', 'in_progress']);
  for (const f of Object.keys(lifeOf) as (keyof typeof lifeOf)[]) {
    assert.deepEqual(statesOf(f, ['working', 'waiting', 'queued', 'done', 'failed', 'stopped', 'asleep']).toSorted(), Object.keys(lifeOf[f]).toSorted());
  }
});

test('openStates: a paused task and an idle session are open; finished ones are not', () => {
  assert.deepEqual(openStates('task'), ['queued', 'in_progress', 'waiting', 'held', 'paused']);
  assert.deepEqual(openStates('session'), ['idle', 'preparing', 'running', 'waiting']);
  assert.deepEqual(openStates('execution'), ['preparing', 'running', 'waiting']);
  assert.ok(isFinished('task', 'cancelled') && !isFinished('task', 'paused'));
  for (const f of Object.keys(lifeOf) as (keyof typeof lifeOf)[]) {
    for (const s of finishedStates[f]) assert.ok(s in lifeOf[f], `${f}.${s} is a state of the family`);
  }
});
