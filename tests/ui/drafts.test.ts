// Drafts (packages/ui drafts.ts, controls/text-field.md#drafts) and structural sharing (app/src/share.ts).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRoot } from 'solid-js';
import { createDraft, setDraftBackend, clearDraft } from '../../packages/ui/src/drafts.ts';
import { share } from '../../app/src/share.ts';

test('share keeps equal parts and returns the previous value when nothing changed', () => {
  const prev = { a: [{ id: 1, v: 'x' }, { id: 2, v: 'y' }], b: { c: 1 } };
  assert.equal(share(prev, structuredClone(prev)), prev);
  const next = share(prev, { a: [{ id: 2, v: 'y' }, { id: 1, v: 'z' }], b: { c: 1 } });
  assert.equal(next.a[0], prev.a[1], 'matched by id after a reorder');
  assert.notEqual(next.a[1], prev.a[0]);
  assert.equal(next.b, prev.b);
  assert.deepEqual(share([1, 2], [1]), [1], 'a shorter array is new');
  assert.deepEqual(share({ a: 1, b: 2 } as Record<string, number>, { a: 1 }), { a: 1 }, 'a removed key is new');
});

test('a draft outlives its component, waits for the backend, and clears on send', () => {
  const store = new Map<string, string>();
  let known = false;
  setDraftBackend({
    read: k => { if (!known) throw Error('person unknown'); return store.get(k) ?? ''; },
    write: (k, t) => { if (!known) throw Error('person unknown'); if (t) store.set(k, t); else store.delete(k); },
  });
  const mount = (key: string) => createRoot(dispose => { const d = createDraft(key); return { d, dispose }; });
  const a = mount('question:q1');
  a.d[1]('typed before the person is known');
  a.dispose();
  const b = mount('question:q1');
  assert.equal(b.d[0](), 'typed before the person is known', 'a remount keeps the text (memory)');
  // (Node runs Solid's server build, where effects do not run: the refresh on refreshDrafts is checked in the browser.)
  known = true; b.d[1]('typed after');
  assert.equal(store.get('question:q1'), 'typed after', 'once the backend answers, typed text is stored');
  b.dispose();
  store.set('question:q2', 'from a reload');
  const c = mount('question:q2');
  assert.equal(c.d[0](), 'from a reload', 'a reload reads the stored draft');
  c.d[2]();
  assert.equal(store.has('question:q2'), false, 'clear forgets it');
  c.dispose();
  clearDraft('question:q1');
  setDraftBackend(null);
});
