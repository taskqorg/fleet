// Requirement: J5 (docs/designs/fleet-ia-and-journeys.md section 3): System marks a profile no ready binding can serve,
// from the models each host's probe found and the models runs proved rejected, and lists the router's model skips.
// Risk: an unprobed binding hides a profile as incompatible, an alias ("opus") reads as a missing model, or a skip of
// another subscription shows on this account.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { accountProfiles, bindingRefusal, modelSkips, profileGaps, type CompatAccount } from '../../app/src/compat.ts';

const acct = (bindings: CompatAccount['bindings'], harness = 'claude'): CompatAccount => ({ id: 'a1', name: 'claude max', harness, intake: 'all', bindings });

test('bindingRefusal: unknown never refuses; aliases count; learned rejection wins', () => {
  assert.equal(bindingRefusal({ hostId: 'h', binding: 'b' }, 'claude-opus-5-5'), undefined);
  assert.equal(bindingRefusal({ hostId: 'h', binding: 'b', models: ['opus'] }, 'claude-opus-5-5'), undefined);
  assert.match(bindingRefusal({ hostId: 'h', binding: 'b', models: ['sonnet'] }, 'claude-opus-5-5') ?? '', /probe/);
  assert.match(bindingRefusal({ hostId: 'h', binding: 'b', models: ['opus'], rejectedModels: { 'claude-opus-5-5': 1 } }, 'claude-opus-5-5') ?? '', /modelNotFound/);
});

test('profileGaps: a gap only when every ready binding is known not to serve the model', () => {
  const profiles = [{ id: 'reviewer-opus', harness: 'claude', model: 'claude-opus-5-5' }, { id: 'pm', harness: 'claude', model: 'claude-sonnet-5-5' }];
  const probed = acct([{ hostId: 'pds18', binding: 'x', readiness: 'ready', models: ['sonnet', 'haiku'], modelsObservedAt: 5 }]);
  assert.deepEqual(profileGaps(profiles, [probed]).map(g => g.profile.id), ['reviewer-opus']);
  const plusUnknown = acct([...probed.bindings!, { hostId: 'mac', binding: 'y', readiness: 'ready' }]);
  assert.deepEqual(profileGaps(profiles, [plusUnknown]), []);
  assert.deepEqual(profileGaps(profiles, [plusUnknown], 'pds18').map(g => g.profile.id), ['reviewer-opus']);
  assert.deepEqual(profileGaps(profiles, [acct(probed.bindings, 'codex')]), [], 'another harness serves no claude profile');
});

test('accountProfiles and modelSkips', () => {
  const a = acct([{ hostId: 'mac', binding: 'y', readiness: 'ready', models: ['opus'] }, { hostId: 'pds18', binding: 'x', readiness: 'ready', models: ['sonnet'] }]);
  const [p] = accountProfiles(a, [{ id: 'reviewer-opus', harness: 'claude', model: 'claude-opus-5-5' }]);
  assert.deepEqual([p.serving, p.refusing, p.of], [['mac'], ['pds18'], 2]);
  const skips = modelSkips([{ executionId: 'e1', at: '2026-11-25T10:00:00Z', winner: { host: 'mac' }, candidates: [
    { host: 'pds18', cause: 'subscription:a1:model:claude-opus-5-5', skipped: 'claude max on pds18 does not offer claude-opus-5-5 (probe)' },
    { host: 'pds18', cause: 'subscription:a2:model:claude-opus-5-5', skipped: 'other' },
    { host: 'mac' },
  ] }], 'a1');
  assert.deepEqual(skips.map(s => [s.host, s.model, s.won]), [['pds18', 'claude-opus-5-5', 'mac']]);
});
