// Requirement: the live state of rendered references (docs/design-system/controls/entity-card.md "Live state of many
// references"): references mounting together become one entity_refs read (split by 200), references to one entity share
// one entry, the change feed is matched by entity key (a batch of 100 changes costs under a millisecond or so of matching),
// dirty references are read again together at most once a second, and a delivery change rereads every pull request.
// Risk: a conversation with 200 references sends 200 reads, polls, or rereads every reference on every change.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerAccessValidator, registerAccessBaseline, ensureAccessGeneration } from '../../app/src/access.ts';
registerAccessValidator(async () => ({ identityKey: 'unit@fixture.test', grantsVersion: 'one' }));
registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
await ensureAccessGeneration();
import { entityCards, liveRegistry, changedEntities } from '../../app/src/entity-cards.ts';
import type { Change } from '../../contracts/generated/ops.ts';

type Ref = { kind?: string; id?: string; code?: string };
const calls: Ref[][] = [];
const states = new Map<string, string>();
(globalThis as any).fetch = async (url: string, init: any) => {
  const name = url.split('/api/ops/')[1], body = JSON.parse(init.body);
  assert.equal(name, 'entity_refs');
  calls.push(body.refs);
  const items = (body.refs as Ref[]).map((r, ref) => ({ ref, kind: r.kind ?? 'task', id: r.id ?? 'id-' + r.code, code: r.code ?? null, state: states.get(r.id ?? '') ?? 'queued', title: 'T ' + (r.id ?? r.code) }))
    .filter(it => it.id !== 'hidden');
  return new Response(JSON.stringify({ items }), { status: 200, headers: { 'content-type': 'application/json' } });
};
const source = entityCards(() => {});
const tick = (ms = 0) => new Promise(r => setTimeout(r, ms));
const change = (kind: Change['kind'], id: string, more: Partial<Change> = {}): Change => ({ seq: 1, kind, id, op: 'U', taskId: null, sessionId: null, hostId: null, touches: [], ...more });

test('200 references mount with one read; 210 with two; one entity is one entry; a hidden one reads as null', async () => {
  const held = Array.from({ length: 200 }, (_, i) => source.live!({ kind: 'task', id: 't' + i }));
  const twin = source.live!({ kind: 'task', id: 't0' });
  await tick(5);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].length, 200);
  assert.equal(held[7].read()?.state, 'queued');
  assert.equal(twin.read()?.title, 'T t0');
  calls.length = 0;
  const more = Array.from({ length: 210 }, (_, i) => source.live!({ kind: 'conversation', id: 'c' + i }));
  const gone = source.live!({ kind: 'task', id: 'hidden' });
  await tick(5);
  assert.deepEqual(calls.map(c => c.length), [200, 11]);
  assert.equal(gone.read(), null);
  assert.equal(more[209].read()?.kind, 'conversation');
  calls.length = 0;
});

test('a feed batch is matched by entity key and rereads only what it touched, together, at most once a second', async () => {
  const batch = Array.from({ length: 100 }, (_, i) => change('execution', 'e' + i, { taskId: i === 50 ? 't5' : 'other' + i }));
  const t0 = performance.now();
  liveRegistry.onChanges(batch);
  const spent = performance.now() - t0;
  assert.ok(spent < 5, `matching 100 changes took ${spent.toFixed(2)} ms`);
  states.set('t5', 'in_progress');
  liveRegistry.onChanges([change('task', 't6')]);
  await tick(1100);
  assert.equal(calls.length, 1, 'one reread for both');
  assert.deepEqual(calls[0].map(r => r.id).sort(), ['t5', 't6']);
  calls.length = 0;
});

test('a delivery change rereads every pull request reference; changes name their entities', async () => {
  const pr = source.live!({ kind: 'pull_request', id: 'taskqorg/fleet#643' });
  await tick(5); calls.length = 0;
  states.set('taskqorg/fleet#643', 'merged');
  liveRegistry.onChanges([change('delivery', 'pass')]);
  await tick(1100);
  assert.deepEqual(calls.map(c => c.map(r => r.id)), [['taskqorg/fleet#643']]);
  assert.equal(pr.read()?.state, 'merged');
  assert.deepEqual(changedEntities(change('definition', 'agent/pm')), ['agent:pm']);
  assert.deepEqual(changedEntities(change('execution', 'e1', { taskId: 't1', sessionId: 's1' })), ['execution:e1', 'task:t1', 'conversation:s1']);
});

test('pull requests in agent text: a GitHub URL and owner/repo#n, never a path or a bare #n', async () => {
  const { PR_REF } = await import('../../packages/ui/src/markdown.ts');
  const hits = (t: string) => [...t.matchAll(PR_REF)].map(m => `${m[1] ?? m[4]}/${m[2] ?? m[5]}#${m[3] ?? m[6]}`);
  assert.deepEqual(hits('Opened https://github.com/taskqorg/fleet/pull/1198 and taskqorg/fleet#1196.'), ['taskqorg/fleet#1198', 'taskqorg/fleet#1196']);
  assert.deepEqual(hits('see #643, src/app/x#1, https://github.com/a/b/pull/12/files'), []);
});

test('a link to a Fleet page of one entity is its reference, not a raw URL (OR-38)', async () => {
  const { APP_URL_REF } = await import('../../packages/ui/src/markdown.ts');
  const ids = (t: string) => [...t.matchAll(APP_URL_REF)].map(m => m[1]);
  assert.deepEqual(ids('send a continuation in https://fleet.ufobe.com/conversations/02c02b71-b565-4d2e-8053-a0e52f19b9dc asking'), ['02c02b71-b565-4d2e-8053-a0e52f19b9dc']);
  assert.deepEqual(ids('https://storage.example.com/objects/02c02b71-b565-4d2e-8053-a0e52f19b9dc/file.tar'), []);
});

test('a UUID in agent text is a reference (OR-38); a bare id registers by id alone', async () => {
  const { UUID_REF } = await import('../../packages/ui/src/markdown.ts');
  assert.deepEqual([...'run 8be57cc0-7076-4d1b-b0f6-6976a21496fa failed; sha abcdef'.matchAll(UUID_REF)].map(m => m[0]), ['8be57cc0-7076-4d1b-b0f6-6976a21496fa']);
  calls.length = 0;
  const h = source.live!({ id: 'bare-1' });
  await tick(5);
  assert.deepEqual(calls[0], [{ id: 'bare-1' }]);
  assert.equal(h.read()?.kind, 'task');
});
