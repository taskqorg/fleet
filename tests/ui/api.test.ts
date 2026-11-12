// Regression (flaky issue #349): a 2xx reply whose body fails to read made op() resolve {}, and the conversations view
// rendered a thread window without blocks ("Cannot read properties of undefined (reading 'length')").
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { op, OpError, writeOnce } from '../../app/src/api.ts';

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

test('a write whose 2xx reply is cut is sent again with the same requestId and is stored once', async () => {
  const saved = (globalThis as any).fetch;
  // A coordinator that stores message_send once per requestId (agent.go submit) and cuts the first reply's body.
  const stored = new Map<string, string>(); const sent: string[] = []; let calls = 0;
  (globalThis as any).fetch = async (_: string, init: any) => {
    const b = JSON.parse(init.body); sent.push(b.requestId); if (!stored.has(b.requestId)) stored.set(b.requestId, b.text);
    const cut = calls++ === 0;
    return { type: 'basic', status: 200, ok: true, statusText: 'OK', json: () => cut ? Promise.reject(new TypeError('network error')) : Promise.resolve({ id: 'x1' }) };
  };
  try {
    let unknown = 0;
    const r = await writeOnce('message_send', { sessionId: 's', requestId: 'r1', text: 'hi' } as any, () => unknown++, 0);
    assert.deepEqual(r, { id: 'x1' });
    assert.deepEqual(sent, ['r1', 'r1']);
    assert.equal(stored.size, 1);
    assert.equal(unknown, 1);
    (globalThis as any).fetch = async () => ({ type: 'basic', status: 409, ok: false, statusText: 'x', json: async () => ({ message: 'Request identity conflict' }) });
    await assert.rejects(writeOnce('message_send', { sessionId: 's', requestId: 'r1', text: 'other' } as any, () => unknown++, 0), /conflict/);
  } finally { (globalThis as any).fetch = saved; }
});
