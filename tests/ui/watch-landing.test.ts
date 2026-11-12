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
    put('sleep', 'exit 0\n');
    put('git', 'case "$1" in fetch) exit 0;; tag) if [ "${WATCH_FIXTURE_UI:-0}" = 1 ]; then printf "%s\\n" v1.2.10 ui-1.2.3-v1.2.9; else echo v1.2.3; fi;; rev-parse) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;; *) exit 1;; esac\n');
    put('gh', `case "$1" in
      api) if [ -n "\${WATCH_FIXTURE_GRAPHQL:-}" ]; then eval "filter=\\\${$#}"; answer="\${WATCH_FIXTURE_GRAPHQL}"
        # WATCH_FIXTURE_GRAPHQL_STEPS: answers of the first polls, one per line, counted in WATCH_FIXTURE_COUNT; then WATCH_FIXTURE_GRAPHQL.
        if [ -n "\${WATCH_FIXTURE_GRAPHQL_STEPS:-}" ]; then n=$(($(cat "\${WATCH_FIXTURE_COUNT}" 2>/dev/null || echo 0) + 1)); echo "$n" > "\${WATCH_FIXTURE_COUNT}"
          step=$(printf '%s\n' "\${WATCH_FIXTURE_GRAPHQL_STEPS}" | sed -n "\${n}p"); [ -z "$step" ] || answer="$step"; fi
        printf '%s' "$answer" | jq -r "\$filter"; else echo 'MERGED CLEAN 0123456789 - - - auto-merge off'; fi;;
      release) [ "\${WATCH_FIXTURE_PUBLICATION_UNKNOWN:-0}" != 1 ] || exit 1; echo '2026-11-11T12:00:00Z';;
      *) exit 1;; esac\n`);
    put('curl', `case "$*" in
      *'/ui-release.json'*) if [ -n "\${WATCH_FIXTURE_BAD_UI:-}" ]; then echo "\${WATCH_FIXTURE_BAD_UI}"; exit 0; fi; echo '{"kind":"ui","version":"1.2.9","backendVersion":"1.2.3","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}';;
      *'/api/health'*) if [ -n "\${WATCH_FIXTURE_BAD_HEALTH:-}" ]; then echo "\${WATCH_FIXTURE_BAD_HEALTH}"; exit 0; fi; [ "\${WATCH_FIXTURE_SITE_DOWN:-0}" != 1 ] || exit 22; echo '{"version":"1.2.3","database":"ok"}';;
      *'/api/ops/hosts_list'*) [ "\${WATCH_FIXTURE_UI:-0}" != 1 ] || exit 99; echo '{"serverNow":1000,"staleAfterMs":30000,"hosts":[{"id":"fixture","lastSeenAt":999,"version":"1.2.3","autoUpdate":"on"}]}';;
      *) exit 1;; esac\n`);
    const envFile = join(dir, 'access.env');
    writeFileSync(envFile, 'CF_ACCESS_CLIENT_ID=fixture-id-not-a-secret\nCF_ACCESS_CLIENT_SECRET=fixture-value-must-not-appear\n', {mode: 0o600});
    const source = readFileSync(new URL('../../scripts/watch-landing.sh', import.meta.url), 'utf8');
    const original = 'env_file=~/.config/ufobe-agents/site-access.env';
    assert.equal(source.split(original).length, 2);
    // Change only the external credential-file location in this disposable copy; never read the real host file.
    const quote = (s: string) => "'" + s.replaceAll("'", "'\\''") + "'";
    const run = (extra: Record<string, string> = {}, missing = false, minutes = '45') => {
      const script = join(dir, 'watch.sh');
      writeFileSync(script, source.replace(original, 'env_file=' + quote(missing ? join(dir, 'missing') : envFile)));
      return spawnSync('bash', [script, '7', minutes], {encoding: 'utf8', timeout: 5000, env: {...process.env, PATH: bin + ':' + process.env.PATH, ...extra}});
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
    const ui = run({WATCH_FIXTURE_UI:'1'});
    assert.equal(ui.status,0,ui.stderr);
    assert.equal(ui.stdout,'merged 0123456789\nreleased ui-1.2.3-v1.2.9\non site ui-1.2.3-v1.2.9\n');
    const good={kind:'ui',version:'1.2.9',backendVersion:'1.2.3',commit:'a'.repeat(40)};
    for (const patch of [{kind:'native'},{version:'1.2.8'},{backendVersion:'1.2.2'},{commit:'b'.repeat(40)}]) {
      const bad=run({WATCH_FIXTURE_UI:'1',WATCH_FIXTURE_BAD_UI:JSON.stringify({...good,...patch})},false,'0');
      assert.equal(bad.status,2,bad.stderr); assert.ok(!bad.stdout.includes('on site'));
    }
    for (const health of [{version:'1.2.2',database:'ok'},{version:'1.2.3',database:'down'}]) {
      const bad=run({WATCH_FIXTURE_UI:'1',WATCH_FIXTURE_BAD_HEALTH:JSON.stringify(health)},false,'0');
      assert.equal(bad.status,2,bad.stderr); assert.ok(!bad.stdout.includes('on site'));
    }
    const absent = run({}, true); assert.equal(absent.status, 3); assert.match(absent.stdout, /no site access: check hosts_list yourself\n$/);
    // The queue line's exit cases (AGENTS.md step 4), each GraphQL answer through the script's own jq filter (the stub's last argument).
    const check = (name: string, conclusion: string | null, at: string | null, isRequired = true) => ({name, conclusion, at, isRequired});
    const pr = (o: Record<string, unknown>, checks: object[] = [], events: object[] = []) => JSON.stringify({data: {repository: {pullRequest: {
      state: 'OPEN', mergeStateStatus: 'BLOCKED', mergeCommit: null, autoMergeRequest: {enabledAt: 'x'}, mergeQueueEntry: null, ...o,
      commits: {nodes: [{commit: {statusCheckRollup: {contexts: {nodes: checks}}}}]}, timelineItems: {nodes: events}}}}});
    const red = check('go', 'FAILURE', '2026-11-12T10:00:00Z');
    for (const [graphql, out] of [
      [pr({}, [red, check('app', 'SUCCESS', '2026-11-12T10:00:00Z')]), 'not merged: OPEN, failed_checks (head)'],
      [pr({}, [red, check('go', 'FAILURE', '2026-11-12T10:10:00Z')]), 'not merged: OPEN, failed_checks (head)'],
      [pr({}, [check('go', 'CANCELLED', '2026-11-12T10:00:00Z')]), 'not merged: OPEN, failed_checks (head)'],
      [pr({autoMergeRequest: null}, [], [{__typename: 'RemovedFromMergeQueueEvent', reason: 'failed_checks'}]), 'not merged: OPEN, failed_checks'],
      [pr({autoMergeRequest: null}, [], [{__typename: 'RemovedFromMergeQueueEvent', reason: null}]), 'not merged: OPEN, unknown'],
      [pr({mergeStateStatus: 'DIRTY'}), 'not merged: OPEN, merge_conflict'],
      [pr({autoMergeRequest: null}, [], [{__typename: 'RemovedFromMergeQueueEvent', reason: 'merge_conflict'}]), 'not merged: OPEN, merge_conflict'],
      // A veto on a DIRTY pull request is a veto, not a conflict.
      [pr({mergeStateStatus: 'DIRTY', autoMergeRequest: null}, [], [{__typename: 'AutoMergeDisabledEvent'}]), 'not merged: OPEN, auto-merge off'],
      [pr({autoMergeRequest: null}, [red], [{__typename: 'AutoMergeDisabledEvent'}]), 'not merged: OPEN, auto-merge off'],
      [pr({state: 'CLOSED', autoMergeRequest: null}, [red]), 'not merged: CLOSED, auto-merge off'],
    ]) { const r = run({WATCH_FIXTURE_GRAPHQL: graphql}); assert.equal(r.status, 1, r.stderr); assert.equal(r.stdout, out + '\n'); }
    // Still waiting: a non-required red, a rerun after red (newer success, running, or queued without a start time) and a red
    // head already in the queue (the queue decides).
    for (const graphql of [
      pr({}, [check('reports', 'FAILURE', '2026-11-12T10:00:00Z', false), check('go', 'SUCCESS', '2026-11-12T10:00:00Z')]),
      pr({}, [red, check('go', 'SUCCESS', '2026-11-12T10:10:00Z')]),
      pr({}, [red, check('go', null, '2026-11-12T10:10:00Z')]),
      pr({}, [red, check('go', null, null)]),
      pr({}, [check('go', 'CANCELLED', null), check('go', 'SUCCESS', '2026-11-12T10:10:00Z')]),
      pr({mergeQueueEntry: {state: 'QUEUED'}}, [red]),
    ]) { const r = run({WATCH_FIXTURE_GRAPHQL: graphql}, false, '0'); assert.equal(r.status, 2, r.stderr); assert.equal(r.stdout, 'timeout at: queue\n'); }
    // PR #451 and the polls around it: a DIRTY beside a healthy queue entry, or once before the queue (right after a push), is
    // not a conflict; DIRTY, clean, DIRTY is not two in a row. Each sequence ends merged.
    const merged = pr({state: 'MERGED', mergeStateStatus: 'UNKNOWN', mergeCommit: {oid: '0123456789'}});
    const dirty = (queue: object | null = null) => pr({mergeStateStatus: 'DIRTY', mergeQueueEntry: queue});
    for (const [i, steps] of [[dirty({state: 'AWAITING_CHECKS'}), dirty({state: 'AWAITING_CHECKS'}), dirty({state: 'AWAITING_CHECKS'})], [dirty()], [dirty(), pr({}), dirty()]].entries()) {
      const r = run({WATCH_FIXTURE_GRAPHQL_STEPS: steps.join('\n'), WATCH_FIXTURE_COUNT: join(dir, 'count-' + i), WATCH_FIXTURE_GRAPHQL: merged});
      assert.equal(r.status, 0, r.stdout + r.stderr); assert.match(r.stdout, /^merged 0123456789$/m); assert.doesNotMatch(r.stdout, /merge_conflict/);
    }
    // A DIRTY seen once at the deadline may be stale: a timeout, not a conflict.
    const late = run({WATCH_FIXTURE_GRAPHQL: dirty()}, false, '0'); assert.equal(late.status, 2); assert.equal(late.stdout, 'timeout at: queue\n');
  } finally { rmSync(dir, {recursive: true, force: true}); }
});
