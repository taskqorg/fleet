// Regression (flaky issue #349): a 2xx reply whose body fails to read made op() resolve {}, and the conversations view
// rendered a thread window without blocks ("Cannot read properties of undefined (reading 'length')").
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { op, OpError } from '../../app/src/api.ts';

const reply = (status: number, body: () => Promise<unknown>) => (globalThis as any).fetch = async () => ({ type: 'basic', status, ok: status < 300, statusText: 'x', json: body });

test('a 2xx reply whose body does not read rejects instead of resolving empty', async () => {
  const saved = (globalThis as any).fetch;
  try {
    reply(200, () => Promise.reject(new TypeError('network error')));
    await assert.rejects(op('thread_entries', {} as any), (e: unknown) => e instanceof OpError && e.message === 'incomplete reply');
    const ac = new AbortController(); ac.abort();
    await assert.rejects(op('thread_entries', {} as any, { signal: ac.signal }), (e: unknown) => e instanceof OpError && e.message === 'aborted');
    reply(500, async () => null);
    await assert.rejects(op('thread_entries', {} as any), (e: unknown) => e instanceof OpError && e.status === 500);
    reply(500, () => Promise.reject(new SyntaxError('not JSON')));
    await assert.rejects(op('thread_entries', {} as any), (e: unknown) => e instanceof OpError && e.status === 500);
    reply(200, async () => ({ entries: [] }));
    assert.deepEqual(await op('thread_entries', {} as any), { entries: [] });
  } finally { (globalThis as any).fetch = saved; }
});
