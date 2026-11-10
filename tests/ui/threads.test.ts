// Thread window state (docs/designs/fleet-conversations.md, Stage 2 item 17).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fromPage, prepend, append, fed, cmpKey, touches, type Block, type Page } from '../../app/src/threads.ts';

const b = (key: string, text = key): Block => ({ kind: 'text', key, markdown: text });
const page = (keys: string[], o: Partial<Page> = {}): Page => ({ entries: keys.map(k => b(k)), first: keys[0] ?? null, last: keys.at(-1) ?? null, hasBefore: false, hasAfter: false, feedCursor: 0, ...o });
const keys = (w: { blocks: Block[] }) => w.blocks.map(x => x.key);

test('keys compare by seq, then index', () => {
  assert.ok(cmpKey('9:1', '10:0') < 0); assert.ok(cmpKey('10:2', '10:1') > 0); assert.equal(cmpKey('5:0', '5:0'), 0);
});

test('prepend and append extend the window without repeats', () => {
  let w = fromPage(page(['10:0', '11:0'], { hasBefore: true, total: 5 }));
  w = prepend(w, page(['8:0', '9:0', '10:0'], { hasBefore: false }));
  assert.deepEqual(keys(w), ['8:0', '9:0', '10:0', '11:0']); assert.equal(w.first, '8:0'); assert.equal(w.hasBefore, false); assert.equal(w.total, 5);
  w = { ...w, hasAfter: true };
  w = append(w, page(['11:0', '12:0'], { hasAfter: false }));
  assert.deepEqual(keys(w), ['8:0', '9:0', '10:0', '11:0', '12:0']); assert.equal(w.last, '12:0'); assert.equal(w.hasAfter, false);
});

test('a jump replaces the window', () => {
  const w = fromPage(page(['40:0', '41:0'], { hasBefore: true, hasAfter: true }), 90);
  assert.deepEqual(keys(w), ['40:0', '41:0']); assert.equal(w.total, 90);
});

test('fed blocks: replaced in place, inserted inside the window by key, appended at the live end, counted past last', () => {
  let w = fromPage(page(['10:0', '12:0'], { total: 2 }));
  w = fed(w, [b('11:0'), b('12:0', 'edited'), b('13:0')]);
  assert.deepEqual(keys(w), ['10:0', '11:0', '12:0', '13:0']); assert.equal(w.blocks[2].markdown, 'edited'); assert.equal(w.total, 4); assert.equal(w.last, '13:0');
  // A late commit with a lower key lands where its key says; one before the window is left to a later page read.
  w = fed(w, [b('10:1'), b('5:0')]); assert.deepEqual(keys(w), ['10:0', '10:1', '11:0', '12:0', '13:0']);
  // After a jump (newer history not loaded) a fed block past last only counts in the pill.
  let j = fromPage(page(['20:0'], { hasAfter: true }));
  j = fed(j, [b('30:0'), b('31:0')]); assert.deepEqual(keys(j), ['20:0']); assert.equal(j.fresh, 2);
  // A closing event replaces its stream entry.
  w = fed(w, [b('14:0', 'Hello')], ['11:0']); assert.deepEqual(keys(w), ['10:0', '10:1', '12:0', '13:0', '14:0']);
});

test('feed rows that can touch a session thread', () => {
  const s = new Set(['s1']), c = (kind: string, sessionId: string | null) => ({ seq: 1, kind, id: 'x', taskId: null, sessionId, hostId: null, op: 'I' });
  assert.equal(touches(c('event', 's1'), s), true); assert.equal(touches(c('event', 's2'), s), false);
  assert.equal(touches(c('question', null), s), true); assert.equal(touches(c('host', 's1'), s), false);
});
