// Requirement: fleet-telemetry.md section 9: the browser reporter sends one error once per window with its repeats
// counted, caps reports per tab, trims fields to the server's byte limits, keeps only app-produced URL parts and never throws.
// Risk: a render loop floods the coordinator, an oversized report is refused, a foreign query value (a token) is sent,
// or the reporter's own failure breaks the page.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
const bundle = await build({ entryPoints: ['app/src/client-errors.ts'], bundle: true, write: false, platform: 'node', format: 'esm' });
const m = await import('data:text/javascript;base64,' + Buffer.from(bundle.outputFiles[0].text).toString('base64'));

function harness(o: Record<string, unknown> = {}) {
  let t = 1_000_000;
  const sent: any[] = [], timers: { at: number; fn: () => void }[] = [];
  const r = m.createReporter({ send: (x: any) => sent.push(x), path: () => '/delivery', version: () => 'ui-1', userAgent: 'test', now: () => t,
    setTimer: (fn: () => void, ms: number) => timers.push({ at: t + ms, fn }), ...o });
  const advance = (ms: number) => { t += ms; for (const x of timers.splice(0)) { if (x.at <= t) x.fn(); else timers.push(x); } };
  return { r, sent, advance };
}

test('repeats inside the window go out once as a count', () => {
  const { r, sent, advance } = harness();
  const boom = () => { try { new Date(NaN).toISOString(); } catch (e) { return e; } };
  for (let i = 0; i < 50; i++) r.capture(boom(), 'error');
  assert.equal(sent.length, 1);
  assert.equal(sent[0].count, 1);
  assert.equal(sent[0].name, 'RangeError');
  advance(5 * 60_000);
  assert.equal(sent.length, 2, 'the window end sends the counted repeats');
  assert.equal(sent[1].count, 49);
  r.capture(boom(), 'error');
  assert.equal(sent.length, 2, 'a new window starts at the last send');
  r.flush();
  assert.equal(sent.length, 3);
  assert.equal(sent[2].count, 1);
  r.flush();
  assert.equal(sent.length, 3, 'nothing pending, nothing sent');
  r.capture(new Error('other'), 'rejection');
  assert.equal(sent.length, 4, 'another error is its own report');
  assert.equal(sent[3].source, 'rejection');
});

test('a tab sends at most maxReports reports', () => {
  const { r, sent } = harness({ maxReports: 3 });
  for (let i = 0; i < 100; i++) r.capture(new Error('loop ' + i), 'render');
  assert.equal(sent.length, 3);
  assert.deepEqual(r.stats(), { sent: 3, repeats: 0, dropped: 97 });
});

test('fields fit the server limits and non-errors are described', () => {
  const { r, sent } = harness({ userAgent: 'u'.repeat(900), version: () => 'v'.repeat(400) });
  const e = new Error('ж'.repeat(3000)); e.stack = 'Error\n' + 'at x (y:1:2)\n'.repeat(2000);
  r.capture(e, 'error');
  const x = sent[0], bytes = (s: string) => new TextEncoder().encode(s).length;
  assert.ok(bytes(x.message) <= m.LIMITS.message && bytes(x.stack) <= m.LIMITS.stack && bytes(x.userAgent) <= m.LIMITS.userAgent && bytes(x.version) <= m.LIMITS.version);
  assert.ok(!x.message.includes('�'), 'never cut inside a character');
  assert.ok(Date.parse(x.at) > 0);
  r.capture('plain text', 'rejection'); r.capture({ code: 7 }, 'rejection'); r.capture(undefined, 'rejection');
  assert.deepEqual(sent.slice(1).map((s: any) => [s.name, s.message]), [['NonError', 'plain text'], ['NonError', '{"code":7}'], ['NonError', 'undefined']]);
});

test('the reporter never throws', () => {
  const { r } = harness({ send: () => { throw new Error('network'); }, path: () => { throw new Error('location'); } });
  assert.doesNotThrow(() => r.capture(new Error('x'), 'error'));
  assert.doesNotThrow(() => r.flush());
});

test('the path keeps only what the app writes', () => {
  assert.equal(m.reportPath('/delivery', '?sort=-since&at=NaN&token=secret'), '/delivery?at=NaN&sort=-since', 'the page\'s own keys stay as found');
  assert.equal(m.reportPath('/tasks/T-1', '?state=failed&code=abc'), '/tasks/T-1?state=failed');
  assert.equal(m.reportPath('/agents/x', '?access_token=abc'), '/agents/x', 'a path outside the view grammar drops its query');
  assert.equal(m.reportPath('', ''), '/');
});

test('a flush moves the window: the older timer sends nothing, and counts and stacks stay within limits', () => {
  const { r, sent, advance } = harness();
  const e = new Error('hidden'); e.stack = 'Error: hidden\n    at Date.toISOString (<anonymous>)\n    at f (https://h/assets/main-AAAA1111.js:1:2)\n' + '    at g (https://h/x.js:1:1)\n'.repeat(400);
  r.capture(e, 'error'); advance(60_000); r.capture(e, 'error');
  r.flush();
  assert.equal(sent.length, 2);
  assert.ok(new TextEncoder().encode(sent[1].stack).length <= 1024, 'a repeat count carries only the stack head');
  assert.equal(m.frameOf(sent[1].stack), 'at f (https://h/assets/main-AAAA1111.js:1:2)', 'the head keeps the top script frame');
  advance(5 * 60_000);
  assert.equal(sent.length, 2, 'the timer of the moved window sent a second report');
  const loop = harness();
  loop.r.capture(e, 'render');
  for (let i = 0; i < 1_000_005; i++) loop.r.capture(e, 'render');
  loop.r.flush();
  assert.equal(loop.sent[1].count, 1_000_000);
  assert.equal(loop.r.stats().dropped, 5);
});

test('past the first-report cap a new error is not tracked, so its repeats send nothing', () => {
  const { r, sent, advance } = harness({ maxReports: 1 });
  r.capture(new Error('a'), 'error'); r.capture(new Error('b'), 'error'); r.capture(new Error('b'), 'error');
  advance(10 * 60_000); r.flush();
  assert.equal(sent.length, 1);
  assert.equal(r.stats().dropped, 2);
});

test('a long message does not push the top frame out of a repeat count', () => {
  const { r, sent } = harness();
  const e = new Error('x'.repeat(1900)); e.stack = 'Error: ' + 'x'.repeat(1900) + '\n    at f (https://h/assets/main-AAAA1111.js:1:2)';
  r.capture(e, 'error'); r.capture(e, 'error'); r.flush();
  assert.equal(m.frameOf(sent[1].stack), 'at f (https://h/assets/main-AAAA1111.js:1:2)');
  const moved = Object.assign(new Error('y'), { stack: 'Error: y\n    at f (http://other:9/assets/main-ZZZZ9999.js:7:1)' });
  r.capture(Object.assign(new Error('y'), { stack: 'Error: y\n    at f (https://h/assets/main-AAAA1111.js:1:2)' }), 'error'); r.capture(moved, 'error');
  assert.equal(sent.length, 3, 'another build or column of one error is the same tab key');
});

test('the top frame skips built-ins like the server fingerprint', () => {
  assert.equal(m.frameOf('RangeError: x\n    at Date.toISOString (<anonymous>)\n    at a (https://h/assets/main-X.js:1:2)'), 'at a (https://h/assets/main-X.js:1:2)');
  assert.equal(m.frameOf('TypeError: x\nglobal code@https://h/main.js:3:9'), 'global code@https://h/main.js:3:9');
  assert.equal(m.frameOf('TypeError: x\n    at Array.map (<anonymous>)'), 'at Array.map (<anonymous>)');
});
