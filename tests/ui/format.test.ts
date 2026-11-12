import { test } from 'node:test';
import assert from 'node:assert/strict';
import { age, duration, bytes, count, money, measured, agentLabel, actor } from '../../packages/ui/src/format.ts';

const now = new Date(2026, 10, 9, 12, 0, 0).getTime();
const ago = (s: number) => now - s * 1000;

test('age boundaries', () => {
  assert.equal(age(ago(2), now).text, 'now');
  assert.equal(age(ago(40), now).text, '40s');
  assert.equal(age(ago(60), now).text, '1m');
  assert.equal(age(ago(4 * 60), now).long, '4 minutes ago');
  assert.equal(age(ago(3599), now).text, '59m');
  assert.equal(age(ago(3600), now).text, '1h');
  assert.equal(age(ago(86400 * 3), now).text, '3d');
  assert.equal(age(new Date(2026, 9, 12).getTime(), now).text, '12 Oct');
  assert.equal(age(new Date(2025, 10, 12).getTime(), now).text, '12 Nov 2025');
});

test('duration, bytes, count, money', () => {
  assert.equal(duration(40).text, '40s');
  assert.equal(duration(14 * 60).text, '14m');
  assert.equal(duration(3720).text, '1h 02m');
  assert.equal(duration(86400 * 3 + 3600 * 4).text, '3d 4h');
  assert.equal(bytes(18e9).text, '18 GB');
  assert.equal(bytes(4200).text, '4.2 KB');
  assert.equal(bytes(1e12).text, '1 TB');
  assert.equal(count(182000).text, '182k');
  assert.equal(count(12400).text, '12.4k');
  assert.equal(count(950).text, '950');
  assert.equal(money(0.42).text, '$0.42');
  assert.equal(money(0.000572).text, '$0.00057', 'one provider call never reads as $0.00');
  assert.equal(money(1e-7, 'EUR').text, '0.0000001 EUR');
});

test('measurement basis: unknown is never 0', () => {
  assert.equal(measured({ value: null, basis: 'unavailable', reason: 'no gateway' }, 'money').text, '—');
  assert.equal(measured({ value: 0.42, basis: 'estimated', reason: 'tokens' }, 'money').text, '≈ $0.42');
  assert.equal(measured({ value: 182000, basis: 'measured', partial: '2 of 3 measured' }, 'count').text, '≥ 182k');
  assert.equal(measured({ value: 0, basis: 'measured' }, 'money').text, '$0.00');
});

test('agent label: short model id and effort for codex, claude and antigravity', () => {
  assert.equal(agentLabel('claude-opus-5-5', 'high'), 'opus-5.5 · high');
  assert.equal(agentLabel('claude-sonnet-5-5', null), 'sonnet-5.5');
  assert.equal(agentLabel('gpt-6.1-sol', 'low'), 'gpt-6.1-sol · low');
  assert.equal(agentLabel('gemini-3.8-flash-low', undefined), 'gemini-3.8-flash-low');
  assert.equal(agentLabel(null, null), '');
});

test('actor: an agent run reads as agent, never its UUID (OR-38)', () => {
  assert.equal(actor('execution:ddfea63c-0e5d-40f8-a9d7-d1ca65fb6334'), 'agent');
  assert.equal(actor('owner@x.test'), 'owner@x.test');
  assert.equal(actor(null), '');
});

// OR-38; risk: a UUID or hash shows as a file's name.
test('fileLabel drops UUIDs and long hashes, keeps the extension', async () => {
  const { fileLabel } = await import('../../packages/ui/src/format.ts');
  assert.equal(fileLabel('out/mcp-lifecycle-5eb2ce291c7d227dd684ec83f9ddc05776e2fe9a0c4e62927b4592383e66fb28-16e7c862-68a7-44c7-b5cc-77d2dfb9a588.jsonl'), 'mcp-lifecycle.jsonl');
  assert.equal(fileLabel('report.md'), 'report.md');
  assert.equal(fileLabel('16e7c862-68a7-44c7-b5cc-77d2dfb9a588'), '');
  assert.equal(fileLabel('build-2026.log'), 'build-2026.log');
});
