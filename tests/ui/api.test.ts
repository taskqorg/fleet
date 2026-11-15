// Regression (flaky issue #349): a 2xx reply whose body fails to read made op() resolve {}, and the conversations view
// rendered a thread window without blocks ("Cannot read properties of undefined (reading 'length')").
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerAccessValidator, registerAccessBaseline, ensureAccessGeneration } from '../../app/src/access.ts';
registerAccessValidator(async () => ({ identityKey: 'unit@fixture.test', grantsVersion: 'one' }));
registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
await ensureAccessGeneration();
import { op, OpError, writeOnce, sendingId, authorizedDraftScope, preparedResourcesSelected } from '../../app/src/api.ts';

const reply = (status: number, body: () => Promise<unknown>) => (globalThis as any).fetch = async () => ({ type: 'basic', status, ok: status < 300, statusText: 'x', json: body });

// Requirement: ordinary fresh Projects uses advertised prepared reads; risk: rollout stays opt-in
// forever or an invalid query silently overrides the owner's intentional opt-out.
test('prepared selection defaults on and retains explicit per-tab opt-out', () => {
  const saved = { location: Object.getOwnPropertyDescriptor(globalThis, 'location'), storage: Object.getOwnPropertyDescriptor(globalThis, 'sessionStorage') };
  const values = new Map<string, string>();
  try {
    Object.defineProperty(globalThis, 'location', { configurable: true, value: { search: '' } });
    Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: { getItem: (k: string) => values.get(k) ?? null, setItem: (k: string, v: string) => values.set(k, v) } });
    assert.equal(preparedResourcesSelected(), true);
    (globalThis as any).location.search = '?preparedResources=off'; assert.equal(preparedResourcesSelected(), false);
    (globalThis as any).location.search = ''; assert.equal(preparedResourcesSelected(), false);
    (globalThis as any).location.search = '?preparedResources=invalid'; assert.equal(preparedResourcesSelected(), false);
    (globalThis as any).location.search = '?preparedResources=on'; assert.equal(preparedResourcesSelected(), true);
    Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, get() { throw Error('storage denied'); } });
    (globalThis as any).location.search = '?preparedResources=off'; assert.equal(preparedResourcesSelected(), false);
    (globalThis as any).location.search = ''; assert.equal(preparedResourcesSelected(), true);
  } finally {
    if (saved.location) Object.defineProperty(globalThis, 'location', saved.location); else delete (globalThis as any).location;
    if (saved.storage) Object.defineProperty(globalThis, 'sessionStorage', saved.storage); else delete (globalThis as any).sessionStorage;
  }
});

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
    const r = await writeOnce('message_send', { sessionId: 's', requestId: 'r1', text: 'hi' } as any, () => unknown++, { wait: 0 });
    assert.deepEqual(r, { id: 'x1' });
    assert.deepEqual(sent, ['r1', 'r1']);
    assert.equal(stored.size, 1);
    assert.equal(unknown, 1);
    (globalThis as any).fetch = async () => ({ type: 'basic', status: 409, ok: false, statusText: 'x', json: async () => ({ message: 'Request identity conflict' }) });
    await assert.rejects(writeOnce('message_send', { sessionId: 's', requestId: 'r1', text: 'other' } as any, () => unknown++, { wait: 0 }), /conflict/);
  } finally { (globalThis as any).fetch = saved; }
});

test('a gateway timeout is an unknown outcome; an abort stops the retry', async () => {
  const saved = (globalThis as any).fetch; let calls = 0;
  (globalThis as any).fetch = async () => (calls++ === 0 ? { type: 'basic', status: 524, ok: false, statusText: 'timeout', json: async () => ({}) } : { type: 'basic', status: 200, ok: true, statusText: 'OK', json: async () => ({ id: 'x1' }) });
  try {
    assert.deepEqual(await writeOnce('message_send', { sessionId: 's', requestId: 'r', text: 't' } as any, () => {}, { wait: 0 }), { id: 'x1' });
    (globalThis as any).fetch = async () => { throw new TypeError('network error'); };
    const ac = new AbortController();
    const p = writeOnce('message_send', { sessionId: 's', requestId: 'r', text: 't' } as any, () => ac.abort(), { signal: ac.signal, wait: 0 });
    await assert.rejects(p, (e: unknown) => e instanceof OpError && e.message === 'aborted');
  } finally { (globalThis as any).fetch = saved; }
});

test('a draft keeps its requestId across attempts and reloads until sent or definitely failed', () => {
  const m = new Map<string, string>(); const saved = (globalThis as any).localStorage;
  (globalThis as any).localStorage = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => m.set(k, v), removeItem: (k: string) => m.delete(k) };
  try {
    const scope = authorizedDraftScope({ kind: 'session', id: 's' });
    const a = sendingId(scope, 'hi');
    assert.equal(sendingId(scope, 'hi'), a, 'same text, same write');
    assert.notEqual(sendingId(scope, 'hi!'), a, 'edited text is a new write');
    const b = sendingId(scope, 'hi'); sendingId(scope, null);
    assert.notEqual(sendingId(scope, 'hi'), b, 'after a send the same text is a new write');
  } finally { (globalThis as any).localStorage = saved; }
});

// One per write family the app retries (sessions, projects, pipelines, questions, keys): a cut 2xx reply, then the same
// body again; the coordinator side of each replay is covered in tests/go (B2, pm-transport, D8, PM10).
for (const [name, body] of [
  ['session_start', { projectId: 'p', role: 'pm', requestId: 'r', text: 't' }],
  ['session_fork', { sessionId: 's', requestId: 'r' }],
  ['project_create', { id: 'p', requestId: 'r', content: {} }],
  ['project_update', { projectId: 'p', requestId: 'r', baseRevision: 1, content: {} }],
  ['pipeline_run_create', { pipelineId: 'x', requestId: 'r', draft: true }],
  ['questions_answer', { questionId: 'q', answer: 'yes' }],
  ['connections_key_add', { connection: 'c', value: 'v', questionId: 'q' }],
] as const) test(`${name}: a cut reply is retried with the same body and stored once`, async () => {
  const saved = (globalThis as any).fetch; const bodies: string[] = []; let calls = 0;
  (globalThis as any).fetch = async (url: string, init: any) => {
    assert.ok(url.endsWith('/' + name)); bodies.push(init.body);
    const cut = calls++ === 0;
    return { type: 'basic', status: 200, ok: true, statusText: 'OK', json: () => cut ? Promise.reject(new TypeError('network error')) : Promise.resolve({ ok: 1 }) };
  };
  try {
    assert.deepEqual(await writeOnce(name as any, body as any, () => {}, { wait: 0 }), { ok: 1 });
    assert.equal(bodies.length, 2); assert.equal(bodies[1], bodies[0], 'the retry sends the same write (same requestId), which the coordinator replays');
  } finally { (globalThis as any).fetch = saved; }
});

// Requirement: all ordinary operations and writes belong to one validated generation.
// Risk: late decoded replies or retries cross a revoke/identity boundary, or falsely report offline.
import { access, invalidateAccess, AccessChanged, accessGeneration } from '../../app/src/access.ts';
import { accessControlOp, createDraftPersistence, online } from '../../app/src/api.ts';
const tick = () => new Promise<void>(resolve => setImmediate(resolve));
test('access gate fences pending me, late reads/writes and retry identities before new baseline opens', async () => {
  const originalFetch = globalThis.fetch;
  let release!: (value: Response) => void;
  globalThis.fetch = (() => new Promise<Response>(resolve => { release = resolve; })) as typeof fetch;
  try {
    const read = op('session_get', { sessionId: 'old' }); await tick();
    const before = online.value;
    invalidateAccess('revoke');
    let validate!: (value: { identityKey: string; grantsVersion: string }) => void;
    let base!: (value: { cursor: number; activate(): void; dispose(): void }) => void;
    let activated = 0, disposed = 0;
    registerAccessValidator(() => new Promise(resolve => { validate = resolve; }));
    registerAccessBaseline(() => new Promise(resolve => { base = resolve; }));
    const gate = ensureAccessGeneration();
    let calls = 0;
    globalThis.fetch = (async () => { calls++; return Response.json({ ok: true }); }) as typeof fetch;
    const next = op('session_get', { sessionId: 'new' });
    await tick(); assert.equal(calls, 0, 'ordinary reads do not run while me is pending');
    release(Response.json({ id: 'private-old' })); await assert.rejects(read, AccessChanged);
    assert.equal(online.value, before, 'an access fence is not offline');
    validate({ identityKey: 'next@fixture.test', grantsVersion: 'two' }); await tick();
    assert.equal(calls, 0, 'me alone cannot release reads');
    base({ cursor: 20, activate() { activated++; }, dispose() { disposed++; } });
    await gate; await next; assert.equal(activated, 1); assert.equal(calls, 1);
    assert.equal(access.value.cursor, 20);
    let unknown = 0;
    globalThis.fetch = (async () => { invalidateAccess('revoke during write'); throw Error('lost write reply'); }) as typeof fetch;
    await assert.rejects(writeOnce('message_send', { sessionId: 's', requestId: 'same', text: 'body' }, () => unknown++, { wait: 0 }), AccessChanged);
    assert.equal(unknown, 0, 'fenced writes are never automatically retried in another generation');
    assert.equal(disposed, 1);
  } finally {
    globalThis.fetch = originalFetch;
    registerAccessValidator(async () => ({ identityKey: 'unit@fixture.test', grantsVersion: 'one' }));
    registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
    await ensureAccessGeneration();
  }
});
test('a superseded baseline is disposed and cannot open a newer generation; failed baseline stays closed', async () => {
  invalidateAccess('baseline race');
  let late!: (value: { cursor: number; activate(): void; dispose(): void }) => void;
  registerAccessBaseline(() => new Promise(resolve => { late = resolve; }));
  const old = ensureAccessGeneration(); await tick();
  invalidateAccess('newer resync');
  let disposed = 0;
  registerAccessBaseline(async () => { throw Error('unavailable'); });
  const failed = ensureAccessGeneration(); await assert.rejects(failed, /unavailable/);
  late({ cursor: 100, activate() { throw Error('must not activate'); }, dispose() { disposed++; } });
  await assert.rejects(old, AccessChanged); assert.equal(disposed, 1); assert.equal(access.value.state, 'closed');
  assert.equal(access.value.cursor, null);
  registerAccessBaseline(async () => ({ cursor: 101, activate() {}, dispose() {} })); await ensureAccessGeneration();
});
test('control boundary allowlists me and no-option baseline only', async () => {
  assert.throws(() => accessControlOp('session_get' as any, {}), /Invalid access control/);
  assert.throws(() => accessControlOp('changes_since', { cursor: 2 }), /no cursor/);
});
// Requirement: authored input survives for its owner only, after fresh target authorization.
// Risk: a stale clear erases newer text or restores another actor's private draft/request identity.
test('scoped drafts quarantine across grants/actors and late success cannot erase newer input', async () => {
  const storage = globalThis.localStorage, map = new Map<string, string>();
  (globalThis as any).localStorage = { getItem: (key: string) => map.get(key) ?? null, setItem: (key: string, value: string) => map.set(key, value), removeItem: (key: string) => map.delete(key) };
  try {
    const scope = authorizedDraftScope({ kind: 'session', id: 'draft-target' }), adapter = createDraftPersistence(scope);
    adapter.write('original'); const request = sendingId(scope, 'original');
    adapter.write('newer'); adapter.clearIfUnchanged('original'); assert.equal(adapter.read(), 'newer');
    const nextRequest = sendingId(scope, 'newer'); sendingId(scope, null, 'original'); assert.equal(sendingId(scope, 'newer'), nextRequest);
    invalidateAccess('same actor changed grants');
    assert.throws(() => adapter.clearIfUnchanged('newer'), AccessChanged);
    registerAccessValidator(async () => ({ identityKey: scope.identityKey, grantsVersion: 'changed' })); await ensureAccessGeneration();
    const same = authorizedDraftScope(scope.target); assert.equal(createDraftPersistence(same).read(), 'newer');
    assert.notEqual(sendingId(same, 'original'), nextRequest); // explicit changed-body attempt; never sent automatically
    invalidateAccess('new identity'); registerAccessValidator(async () => ({ identityKey: 'other@fixture.test', grantsVersion: 'other' })); await ensureAccessGeneration();
    const other = authorizedDraftScope(scope.target); assert.equal(createDraftPersistence(other).read(), '');
    assert.notEqual(sendingId(other, 'original'), request);
    assert.throws(() => sendingId(same, null), AccessChanged);
  } finally { (globalThis as any).localStorage = storage; }
});

// Requirement: transport baseline ordering is explicit, independent of subscriber registration order.
// Risk: a pre-ready event, late poll or animation frame advances an unauthorized cursor or releases private reads.
test('feed abandons pre-ready/pre-activation events, fences queued frames, and disposes transport', async () => {
  const saved = { EventSource: (globalThis as any).EventSource, requestAnimationFrame: (globalThis as any).requestAnimationFrame, cancelAnimationFrame: (globalThis as any).cancelAnimationFrame };
  const sources: FakeSource[] = [], frames = new Map<number, FrameRequestCallback>(); let frameId = 0;
  class FakeSource extends EventTarget {
    url: string; closed = false; onmessage: ((e: MessageEvent) => void) | null = null; onerror: (() => void) | null = null;
    constructor(url: string) { super(); this.url = url; sources.push(this); }
    close() { this.closed = true; }
    ready(cursor: number) { this.dispatchEvent(new MessageEvent('ready', { data: JSON.stringify({ cursor }) })); }
    change(seq: number) { this.onmessage?.(new MessageEvent('message', { data: JSON.stringify({ seq, kind: 'task', id: 'private', taskId: 'private', sessionId: null, hostId: null, op: 'U' }) })); }
  }
  (globalThis as any).EventSource = FakeSource;
  (globalThis as any).requestAnimationFrame = (fn: FrameRequestCallback) => { const id=++frameId;frames.set(id,fn);return id; };
  (globalThis as any).cancelAnimationFrame = (id: number) => frames.delete(id);
  const feed = await import('../../app/src/feed.ts');
  registerAccessValidator(async () => ({ identityKey: 'feed@fixture.test', grantsVersion: 'one' }));
  const received: number[] = []; let reloads = 0;
  const off = feed.subscribe({ changes: changes => received.push(...changes.map(change => change.seq)), resync: () => reloads++ });
  try {
    invalidateAccess('feed startup'); feed.startFeed(null);
    const early = ensureAccessGeneration(); await tick();
    assert.equal(sources[0].url, '/api/stream'); sources[0].change(1);
    await assert.rejects(early, /before baseline/); assert.equal(sources[0].closed, true); assert.equal(feed.currentCursor(), null);
    const raced = ensureAccessGeneration(); await tick();
    sources[1].ready(10); sources[1].change(11); // separate callbacks before promise activation continuation
    await assert.rejects(raced, AccessChanged); await tick();
    assert.equal(sources[1].closed, true); assert.equal(received.length, 0);
    const next = ensureAccessGeneration(); await tick(); sources.at(-1)!.ready(20); await next;
    assert.equal(feed.currentCursor(), 20); assert.equal(reloads, 1);
    sources.at(-1)!.change(21); assert.equal(frames.size, 1);
    invalidateAccess('frame fence'); assert.equal(frames.size, 0); assert.equal(feed.currentCursor(), null);
    sources[0].change(99); for (const fn of frames.values()) fn(0); assert.deepEqual(received, []);
  } finally {
    off(); feed.stopFeed();
    Object.assign(globalThis, saved);
    registerAccessValidator(async () => ({ identityKey: 'unit@fixture.test', grantsVersion: 'one' }));
    registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} })); await ensureAccessGeneration();
  }
});

// Requirement: a late old polling baseline cannot reopen access or advance the new cursor.
// Risk: a no-cursor response obtained under previous grants is activated after a newer resync.
test('poll fallback fences an old baseline response and activates only the newest authorized cursor', async () => {
  const EventSource = (globalThis as any).EventSource, fetch = globalThis.fetch;
  const waiting: ((value: Response) => void)[] = [];
  (globalThis as any).EventSource = undefined;
  globalThis.fetch = ((_url: string, init: RequestInit) => { assert.deepEqual(JSON.parse(init.body as string), {}); return new Promise<Response>(resolve => waiting.push(resolve)); }) as typeof fetch;
  const feed = await import('../../app/src/feed.ts');
  registerAccessValidator(async () => ({ identityKey: 'poll@fixture.test', grantsVersion: 'poll' }));
  try {
    invalidateAccess('poll start');feed.startFeed(null);
    const old=ensureAccessGeneration();await tick();assert.equal(waiting.length,1);
    invalidateAccess('poll superseded');const current=ensureAccessGeneration();await tick();assert.equal(waiting.length,2);
    waiting[0](Response.json({cursor:900,changes:[],resync:false}));await assert.rejects(old,AccessChanged);assert.equal(feed.currentCursor(),null);
    waiting[1](Response.json({cursor:901,changes:[],resync:false}));await current;assert.equal(feed.currentCursor(),901);
  } finally {
    feed.stopFeed();(globalThis as any).EventSource=EventSource;globalThis.fetch=fetch;
    invalidateAccess('poll cleanup');registerAccessBaseline(async()=>({cursor:0,activate(){},dispose(){}}));await ensureAccessGeneration();
  }
});

// Requirement: explicit retries retain complete authored bodies, not a newly inferred delivery mode.
// Risk: a late/unknown send reuses an identity with different arguments, or async hashing writes across a fence.
import { prepareSending } from '../../app/src/api.ts';
test('scoped submissions retain original body/hash through edits and refuse stale async persistence', async () => {
  const storage = globalThis.localStorage, map = new Map<string,string>();
  (globalThis as any).localStorage = { getItem:(key:string)=>map.get(key)??null,setItem:(key:string,value:string)=>map.set(key,value),removeItem:(key:string)=>map.delete(key) };
  try {
    const scope=authorizedDraftScope({kind:'session',id:'frozen-submission'});
    const original=await prepareSending(scope,'message_send',{sessionId:'frozen-submission',text:'original',delivery:'steer'});
    const changed=await prepareSending(scope,'message_send',{sessionId:'frozen-submission',text:'new intent'});
    assert.notEqual(changed.requestId,original.requestId);
    const retry=await prepareSending(scope,'message_send',{sessionId:'frozen-submission',text:'original'});
    assert.deepEqual(retry,original,'explicit retry keeps original delivery, body and request identity');
    await prepareSending(scope,'message_send',{sessionId:'frozen-submission',text:'other intent'});sendingId(scope,null,'original');
    assert.notEqual((await prepareSending(scope,'message_send',{sessionId:'frozen-submission',text:'original'})).requestId,original.requestId,'late confirmed success removes only its own unresolved identity');
    assert.ok([...map.values()].some(text=>JSON.parse(text).sending?.bodySha?.length===64));
    await assert.rejects(prepareSending(scope,'message_send',{sessionId:'foreign',text:'foreign'}),/authorized target/);
    const editorScope=authorizedDraftScope({kind:'project-editor',id:'editor-storage-failure'}),adapter=createDraftPersistence(editorScope);
    (globalThis as any).localStorage.setItem=()=>{throw Error('quota');};
    assert.throws(()=>adapter.write('authored configuration'),/quota/);assert.equal(adapter.read(),'authored configuration','optional persistence failure retains authorized input in memory');
    const pending=prepareSending(scope,'message_send',{sessionId:'frozen-submission',text:'fenced hashing'});
    invalidateAccess('hashing fence');await assert.rejects(pending,AccessChanged);
    await ensureAccessGeneration();assert.throws(()=>adapter.write('late'),AccessChanged);
  } finally { (globalThis as any).localStorage=storage; }
});
