import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync} from 'node:fs';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
import {spawnSync} from 'node:child_process';

// R1, R3; risk: timing observations become a new deploy gate, leak credentials or break the watcher's final-line contract.
test('landing observations retain authoritative publication, optional site evidence and existing exit/stdout semantics', () => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-watch-'));
  try {
    const bin = join(dir, 'bin'); mkdirSync(bin);
    const put = (name: string, text: string) => writeFileSync(join(bin, name), '#!/bin/sh\nset -eu\n' + text, {mode: 0o700});
    put('git', 'case "$1" in fetch) exit 0;; tag) echo v1.2.3;; *) exit 1;; esac\n');
    put('gh', `case "$1" in
      api) echo 'MERGED CLEAN 0123456789 - auto-merge off';;
      release) [ "\${WATCH_FIXTURE_PUBLICATION_UNKNOWN:-0}" != 1 ] || exit 1; echo '2026-11-11T12:00:00Z';;
      *) exit 1;; esac\n`);
    put('curl', `case "$*" in
      *'/api/health'*) [ "\${WATCH_FIXTURE_SITE_DOWN:-0}" != 1 ] || exit 22; echo '{"version":"1.2.3","database":"ok"}';;
      *'/api/ops/hosts_list'*) echo '{"serverNow":1000,"staleAfterMs":30000,"hosts":[{"id":"fixture","lastSeenAt":999,"version":"1.2.3","autoUpdate":"on"}]}';;
      *) exit 1;; esac\n`);
    const envFile = join(dir, 'access.env');
    writeFileSync(envFile, 'CF_ACCESS_CLIENT_ID=fixture-id-not-a-secret\nCF_ACCESS_CLIENT_SECRET=fixture-value-must-not-appear\n', {mode: 0o600});
    const source = readFileSync(new URL('../../scripts/watch-landing.sh', import.meta.url), 'utf8');
    const original = 'env_file=~/.config/ufobe-agents/site-access.env';
    assert.equal(source.split(original).length, 2);
    // Change only the external credential-file location in this disposable copy; never read the real host file.
    const quote = (s: string) => "'" + s.replaceAll("'", "'\\''") + "'";
    const run = (extra: Record<string, string> = {}, missing = false) => {
      const script = join(dir, 'watch.sh');
      writeFileSync(script, source.replace(original, 'env_file=' + quote(missing ? join(dir, 'missing') : envFile)));
      return spawnSync('bash', [script, '7'], {encoding: 'utf8', timeout: 5000, env: {...process.env, PATH: bin + ':' + process.env.PATH, ...extra}});
    };
    for (const [extra, publication, site] of [
      [{}, '2026-11-11T12:00:00Z', 'site_healthy version=1.2.3'],
      [{WATCH_FIXTURE_PUBLICATION_UNKNOWN: '1'}, 'unavailable', 'site_healthy version=1.2.3'],
      [{WATCH_FIXTURE_SITE_DOWN: '1'}, '2026-11-11T12:00:00Z', 'site_health unavailable'],
    ] as [Record<string,string>,string,string][]) {
      const r = run(extra); assert.equal(r.status, 0, r.stderr);
      assert.equal(r.stdout, 'merged 0123456789\nreleased v1.2.3\non hosts v1.2.3\n');
      assert.match(r.stderr, /observed_at=\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ hosts_ready/);
      assert.ok(r.stderr.includes(`published_at=${publication} source=github_release`));
      assert.ok(r.stderr.includes(site));
      assert.ok(!r.stderr.includes('fixture-value-must-not-appear'));
      assert.ok(!r.stdout.includes('fixture-value-must-not-appear'));
    }
    const absent = run({}, true); assert.equal(absent.status, 3); assert.match(absent.stdout, /no site access: check hosts_list yourself\n$/);
  } finally { rmSync(dir, {recursive: true, force: true}); }
});
