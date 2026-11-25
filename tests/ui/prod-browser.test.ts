// Requirement: a walk or check script that opens the deployed site prints no credential (AGENTS.md, "Opening the
// deployed web app"). Risk: a Playwright TimeoutError's call log, quoting request headers and the CF_Authorization
// cookie, reaches the terminal or a report.
import test from 'node:test';
import assert from 'node:assert/strict';
import { redact } from '../../scripts/prod-browser.ts';

test('redact keeps the first line and hides every credential value', () => {
  const dump = new Error('page.goto: Timeout 20000ms exceeded. cookie: CF_Authorization=eyJhbGciOi.aaa.bbb; other=1 authorization: Bearer abc.def cf-access-client-secret: s3cr3t\n'
    + 'Call log:\n  - navigating to "https://fleet.ufobe.com/", waiting until "load"\n  - cf-access-client-id: x.access');
  const out = redact(dump);
  assert.ok(out.startsWith('page.goto: Timeout 20000ms exceeded.'), out);
  for (const leak of ['eyJhbGciOi', 'abc.def', 's3cr3t', 'Call log', 'x.access']) assert.ok(!out.includes(leak), `${leak} in ${out}`);
  assert.match(out, /cookie: \[redacted\]/);
  assert.equal(redact('headers: cf-access-client-secret: s3cr3t'), 'headers: cf-access-client-secret: [redacted]');
  assert.equal(redact('authorization: Bearer abc.def'), 'authorization: [redacted]');
  assert.equal(redact('{"Cookie":"CF_Authorization=abc"}'), '{"Cookie":"[redacted]"}');
  assert.ok(!redact('set CF_Authorization=abc; path=/').includes('abc'));
  process.env.CF_ACCESS_CLIENT_SECRET = 'zz-secret-value';
  assert.equal(redact("headers: [{ name: 'cf-access-client-secret', value: 'zz-secret-value' }]"), "headers: [{ name: 'cf-access-client-secret', value: '[redacted]' }]");
});
