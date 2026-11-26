// Real coordinator and two real journal/transfer workers. Only the signed object-store transport is simulated.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash, randomUUID} from 'node:crypto';
import {once} from 'node:events';
import {existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {DatabaseSync} from 'node:sqlite';
import {test} from 'node:test';
import {setTimeout as delay} from 'node:timers/promises';
import {lit, root, startCoordinator} from '../harness.ts';
import {fakeS3} from '../s3-fake.ts';

async function until<T>(read: () => T | undefined, detail: string, signal: AbortSignal) {
  for (const end = Date.now() + 10_000; Date.now() < end; await delay(25, undefined, {signal})) {
    const value = read();
    if (value !== undefined) return value;
  }
  assert.fail(detail);
}

function gate() {
  let release!: () => void;
  const wait = new Promise<void>(done => { release = done; });
  return {wait, release};
}

// Requirement T-FLT-103: a missing remote stage recovers from the owner's exact spool bytes.
// Risks: replacing an in-flight reservation, treating authorization/signature errors as loss,
// or uploading again after the destination was already promoted.
test('remote verification recovers missing staging from the uploader spool', {timeout: 45_000}, async t => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-recovery-'));
  let stopCoordinator: (() => Promise<void>) | undefined;
  const stopWorkers: (() => Promise<void>)[] = [];
  const scenarios = (['missing', 'promoted', 'expired', 'legacy-slow', 'unreachable'] as const).map(mode => ({
    mode, id: randomUUID(), bytes: Buffer.from('invented ' + mode + ' bytes\0\xff', 'latin1'),
    first: gate(), replacement: gate(), staleProbe: gate(), stage: '', newStage: '', thirdStage: '', uploads: 0, reads: [] as string[],
    holdNextProbe: false, staleProbeStarted: false,
    denyProbe: mode === 'missing', deniedProbes: 0, streams: 0, streamFinished: false,
  }));
  const s3 = await fakeS3(async (req, res, key, query) => {
    const scenario = scenarios.find(s => key.includes(s.id) || key === finalKey(s.bytes));
    if (!scenario) return false;
    const {mode} = scenario;
    if (req.method === 'POST' && query.has('uploads')) scenario.uploads++;
    const storeError = (status: number, code: string) => {
      res.writeHead(status);
      res.end('<Error><Code>' + code + '</Code></Error>');
      return true;
    };
    if (req.method === 'HEAD' && key === scenario.stage) {
      if (scenario.denyProbe || mode === 'unreachable') {
        scenario.deniedProbes++;
        return storeError(403, 'AccessDenied');
      }
      if (scenario.holdNextProbe) {
        scenario.holdNextProbe = false;
        scenario.staleProbeStarted = true;
        await scenario.staleProbe.wait;
      }
    }
    if (req.method !== 'GET' || req.headers.range) return false;
    if (!query.has('X-Amz-Signature')) {
      if (mode !== 'legacy-slow' || key !== scenario.stage) return false;
      scenario.streams++;
      res.writeHead(200, {'content-length': scenario.bytes.length});
      if (scenario.streams === 1) {
        res.write(scenario.bytes.subarray(0, 1));
        await once(res, 'close');
        return true;
      }
      for (const byte of scenario.bytes) {
        await delay(65);
        if (res.destroyed) return true;
        res.write(Buffer.from([byte]));
      }
      scenario.streamFinished = true;
      res.end();
      return true;
    }
    // A denied probe may requeue the old GET. Only replacement keys count here.
    if (mode === 'missing' && key === scenario.stage) return storeError(404, 'NoSuchKey');
    scenario.reads.push(key);
    if (scenario.reads.length > 1) {
      if (mode === 'missing' && scenario.reads.length === 2) {
        scenario.newStage = key;
        await scenario.replacement.wait;
        s3.objects.delete(key);
        return storeError(404, 'NoSuchKey');
      }
      if (mode === 'missing' && scenario.reads.length === 3) scenario.thirdStage = key;
      return false;
    }
    scenario.stage = key;
    if (mode === 'expired') return storeError(403, 'RequestTimeTooSkewed');
    if (mode === 'legacy-slow') {
      // The fixture sends the old wire callback while the verifier's normal GET is held.
      await scenario.first.wait;
      res.end(scenario.bytes);
      return true;
    }
    if (mode === 'promoted') s3.objects.set(finalKey(scenario.bytes), s3.objects.get(key)!);
    s3.objects.delete(key);
    if (mode === 'missing' || mode === 'unreachable') await scenario.first.wait;
    return storeError(404, 'NoSuchKey');
  });
  t.after(async () => {
    for (const s of scenarios) { s.first.release(); s.replacement.release(); s.staleProbe.release(); }
    try {
      await Promise.all(stopWorkers.map(stop => stop()));
      await stopCoordinator?.();
    } finally {
      s3.close();
      rmSync(dir, {recursive: true, force: true});
    }
  });

  const coordinator = await startCoordinator({name: 'recovery', env: {
    FLEET_STORE_SECRETS: join(dir, 'keys'), FLEET_STORE_ENDPOINT_HOSTS: '127.0.0.1',
    FLEET_DEFAULT_ARTIFACT_LOCATIONS: 'verify-host,verify-alt', FLEET_CF_ACCESS_FILE: join(dir, 'unused'),
    FLEET_BUGSINK_DSN: '',
  }});
  stopCoordinator = coordinator.stop;

  async function post(path: string, token: string, body: unknown, person?: string, signal = t.signal) {
    const response = await fetch(coordinator.base + path, {
      method: 'POST', signal, headers: {
        authorization: 'Bearer ' + token, 'content-type': 'application/json',
        ...(person ? {'x-fleet-person': person} : {}),
      }, body: JSON.stringify(body),
    });
    return {status: response.status, body: await response.json()};
  }
  function ok(response: Awaited<ReturnType<typeof post>>) {
    assert.equal(response.status, 200);
    return response.body;
  }
  async function host(id: string) {
    const registered = ok(await post('/api/ops/hosts_register', coordinator.secret, {id}, 'owner@trial.test'));
    const secretFile = join(dir, id + '.secret');
    writeFileSync(secretFile, registered.secret, {mode: 0o600});
    return {id, secretFile, post: (path: string, body: object, signal?: AbortSignal) =>
      post('/api/hosts/' + path, registered.secret, {hostId: id, ...body}, undefined, signal)};
  }
  const uploader = await host('upload-host'), verifier = await host('verify-host'), alternate = await host('verify-alt');
  const endpoint = 'http://127.0.0.1:' + s3.port;
  for (const h of [verifier, alternate]) {
    ok(await h.post('stores', {store: {
      id: h === verifier ? 'test-store' : 'alternate-store', bucket: 'fleet', endpoint, localEndpoint: endpoint,
      accessKeyId: 'test-invented', secretAccessKey: 'test-invented',
    }}));
  }
  const executionId = randomUUID();
  coordinator.sql(`INSERT INTO execution(id,kind,state,observation,host_id,updated_at,data,seq,definitions)
    VALUES(${lit(executionId)},'host_job','running','confirmed',${lit(uploader.id)},now(),'{}',1,'{}')`);

  async function startExecutor(host: typeof uploader) {
    const data = join(dir, host.id);
    mkdirSync(data);
    const child = spawn(join(root, 'go/bin/fleet-executor'), [], {cwd: data, detached: true, env: {
      PATH: process.env.PATH, XDG_STATE_HOME: data, FLEET_DATA_DIR: data, FLEET_COORDINATOR_URL: coordinator.base,
      FLEET_HOST_ID: host.id, FLEET_HOST_SECRET_FILE: host.secretFile,
      FLEET_CF_ACCESS_FILE: join(dir, 'unused'), FLEET_BUGSINK_DSN: '',
    }, stdio: ['ignore', 'ignore', 'inherit']});
    let db: DatabaseSync | undefined;
    const gone = once(child, 'exit');
    stopWorkers.push(async () => {
      if (child.pid && child.exitCode === null && child.signalCode === null) {
        try { process.kill(-child.pid, 'SIGKILL'); } catch (err) {
          if ((err as NodeJS.ErrnoException).code !== 'ESRCH') throw err;
        }
      }
      await gone;
      db?.close();
    });
    const path = join(data, 'journal.sqlite');
    return until(() => {
      if (!existsSync(path)) return;
      const probe = new DatabaseSync(path);
      probe.exec('PRAGMA busy_timeout=5000');
      if (probe.prepare("SELECT 1 FROM sqlite_master WHERE name='transfers'").get()) return db = probe;
      probe.close();
    }, 'executor started its transfer journal', t.signal);
  }
  const uploaderDB = await startExecutor(uploader), verifierDB = await startExecutor(verifier);
  function enqueue(s: typeof scenarios[number]) {
    const path = join(dir, s.id + '.bin');
    writeFileSync(path, s.bytes);
    uploaderDB.prepare("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,'upload',?,?)").run(s.id, JSON.stringify({
      path, name: s.mode + '.bin', mediaType: 'application/octet-stream', executionId, required: true,
    }), Date.now());
  }
  for (const s of scenarios.slice(0, 3)) enqueue(s);

  const missing = scenarios[0];
  const request = {artifactId: missing.id, storeId: 'test-store'};
  await until(() => missing.stage || undefined, 'first remote verifier reached staging', t.signal);
  const renew = await uploader.post('artifacts/renew', request);
  assert.deepEqual(renew, {status: 200, body: {state: 'pending'}}, 'open verifier fences recovery');
  assert.equal(missing.uploads, 1);
  const spool = join(dir, missing.id + '.bin');
  assert.deepEqual(readFileSync(spool), missing.bytes);
  missing.first.release();
  await until(() => missing.deniedProbes || undefined, 'owner checked missing staging with the store', t.signal);
  assert.equal(missing.uploads, 1, 'store authorization failure did not trigger a replacement');
  const firstVerify = verifierDB.prepare("SELECT state,response FROM transfers WHERE kind='verify' AND json_extract(body,'$.artifactId')=? ORDER BY created_at LIMIT 1").get(missing.id);
  assert.equal(firstVerify?.state, 'failed');
  assert.match(String(firstVerify?.response), /NoSuchKey/);
  await until(() => Number(coordinator.sql(
    `SELECT count(*) FROM command WHERE kind='artifact-verify' AND status='failed' AND payload->'payload'->>'artifactId'=${lit(missing.id)}`,
  )) >= 2 || undefined, 'the old key recheck finished after the denied probe', t.signal);
  missing.holdNextProbe = true;
  missing.denyProbe = false;
  const staleRenew = uploader.post('artifacts/renew', request);
  await until(() => missing.staleProbeStarted || undefined, 'second renewal is probing the old key', t.signal);
  await until(() => missing.newStage || undefined, 'owner re-uploaded retained bytes', t.signal);
  assert.notEqual(missing.newStage, missing.stage);
  missing.staleProbe.release();
  assert.deepEqual(await staleRenew, {status: 200, body: {state: 'pending'}}, 'a late missing-key probe cannot recover the replacement');
  assert.deepEqual(ok(await verifier.post('artifacts/verified', {
    ...request, stagingKey: missing.stage.slice('/fleet/'.length), sha256: '0'.repeat(64),
  })), {state: 'unknown'}, 'the old digest cannot fail the replacement reservation');
  const queuedLegacyWork = (id: string) => Number(coordinator.sql(
    `SELECT count(*) FROM artifact_verify_work WHERE artifact_id=${lit(id)}`,
  ));
  const copyState = (id: string) => coordinator.sql(
    `SELECT state FROM artifact_copy WHERE artifact_id=${lit(id)} AND store_id='test-store'`,
  ).trim();
  async function checkLegacyDigest(sha256: string) {
    assert.equal(ok(await verifier.post('artifacts/verified', {...request, sha256})).state, 'verifying');
    await until(() => queuedLegacyWork(missing.id) === 0 || undefined, 'legacy check settled its work', t.signal);
    assert.equal(copyState(missing.id), 'copying', 'an old legacy digest cannot settle a replacement with different bytes');
    assert.equal(s3.objects.has(finalKey(missing.bytes)), false);
  }
  await checkLegacyDigest('0'.repeat(64));
  s3.objects.set(missing.newStage, Buffer.from('invented unrelated current bytes'));
  await checkLegacyDigest(createHash('sha256').update(missing.bytes).digest('hex'));
  s3.objects.set(missing.newStage, missing.bytes);
  missing.replacement.release();
  await until(() => missing.thirdStage || undefined, 'the owner can recover a missing replacement too', t.signal);
  assert.notEqual(missing.thirdStage, missing.newStage);

  for (const s of scenarios) {
    if (s.mode === 'legacy-slow') {
      enqueue(s);
      await until(() => s.stage || undefined, 'legacy verification staged the object', t.signal);
      const receipt = {artifactId: s.id, storeId: 'test-store', sha256: createHash('sha256').update(s.bytes).digest('hex')};
      for (let retry = 0; retry < 2; retry++) {
        const deadline = AbortSignal.any([t.signal, AbortSignal.timeout(800)]);
        const accepted = ok(await verifier.post('artifacts/verified', receipt, deadline));
        assert.equal(accepted.state, 'verifying');
      }
      assert.equal(s.streamFinished, false, 'the legacy callback answered before the slow read finished');
      await until(() => s.streams || undefined, 'background verification opened the object', t.signal);
      assert.equal(s.streams, 1, 'duplicate callbacks share one read');
      assert.equal(queuedLegacyWork(s.id), 1);
      assert.equal(copyState(s.id), 'pending');
      // Restart with the first read in flight. The replacement process has no HTTP callback to retry.
      await coordinator.restart();
    }
    if (s.mode === 'unreachable') {
      // An older colocated uploader can enter recovery if its accepted verification loses both objects.
      // Its periodic, storeless lease still renews normally.
      const local = randomUUID(), localExecution = randomUUID(), hash = 'a'.repeat(64);
      coordinator.sql(`INSERT INTO execution(id,kind,state,observation,host_id,updated_at,data,seq,definitions)
        VALUES(${lit(localExecution)},'host_job','running','confirmed',${lit(verifier.id)},now(),'{}',2,'{}');
        INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,state)
        VALUES(${lit(local)},'prod',${lit(hash)},1,'application/octet-stream','local-legacy.bin',${lit(localExecution)},'pending');
        INSERT INTO artifact_copy(artifact_id,store_id,state,staging_key,lease_expires_at)
        VALUES(${lit(local)},'test-store','pending',${lit('_/prod/.pending/' + local)},now() + interval '1 hour')`);
      const localCopy = {artifactId: local, storeId: 'test-store'};
      assert.equal(ok(await verifier.post('artifacts/verified', {...localCopy, sha256: hash})).state, 'verifying');
      await until(() => queuedLegacyWork(local) === 0 || undefined, 'legacy proof with missing bytes closed', t.signal);
      assert.equal(ok(await verifier.post('artifacts/renew', {artifactId: local})).state, 'pending');
      const missingLocal = await verifier.post('artifacts/renew', localCopy);
      assert.deepEqual(missingLocal, {status: 200, body: {state: 'missing'}});

      await startExecutor(alternate);
      enqueue(s);
      await until(() => s.stage || undefined, 'initial verifier opened the prior store', t.signal);
      coordinator.sql("UPDATE store SET state='unreachable' WHERE id='test-store'");
      const pending = await uploader.post('artifacts/renew', {artifactId: s.id, storeId: 'test-store'});
      assert.deepEqual(pending, {status: 200, body: {state: 'pending'}});
      s.first.release();
    }
    const row = await until(() => {
      const r = uploaderDB.prepare('SELECT state,body,response FROM transfers WHERE id=?').get(s.id);
      if (r?.state === 'failed') assert.fail(String(r.response));
      if (r?.state === 'done') return r;
    }, s.mode + ' transfer reached stored', t.signal);
    assert.deepEqual(JSON.parse(String(row.response)), {artifactId: s.id, state: 'stored'});
    assert.equal(JSON.parse(String(row.body)).recoveries ?? 0, s.mode === 'missing' ? 2 : s.mode === 'unreachable' ? 1 : 0);
    assert.equal(s.uploads, s.mode === 'missing' ? 3 : s.mode === 'unreachable' ? 2 : 1);
    if (s.mode === 'legacy-slow') {
      assert.equal(s.streams, 2, 'one resumed read completes the durable verification');
      assert.equal(s.streamFinished, true);
      s.first.release();
    } else if (s.mode === 'unreachable') {
      assert.equal(s.reads.length, 2);
      assert.equal(s.deniedProbes, 0, 'recovery does not probe a store already recorded unreachable');
      assert.ok(s.reads[1].includes('.alternate-store.'), 'the alternate store independently verifies its staging bytes');
    } else {
      const retryKeys = s.mode === 'missing' ? [s.newStage, s.thirdStage] : s.mode === 'expired' ? [s.stage] : [];
      assert.deepEqual(s.reads, [s.stage, ...retryKeys]);
    }
    const view = ok(await uploader.post('artifacts/get', {artifactId: s.id, executionId}));
    const downloaded = await fetch(view.url, {signal: t.signal});
    assert.equal(downloaded.status, 200);
    assert.deepEqual(Buffer.from(await downloaded.arrayBuffer()), s.bytes);
  }
});

function finalKey(bytes: Buffer) {
  return '/fleet/_/prod/' + createHash('sha256').update(bytes).digest('hex');
}
