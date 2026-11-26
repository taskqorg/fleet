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
  const scenarios = (['missing', 'promoted', 'expired'] as const).map(mode => ({
    mode, id: randomUUID(), bytes: Buffer.from('invented ' + mode + ' bytes\0\xff', 'latin1'),
    first: gate(), replacement: gate(), staleProbe: gate(), stage: '', newStage: '', thirdStage: '', uploads: 0, reads: [] as string[],
    holdNextProbe: false, staleProbeStarted: false,
    denyProbe: mode === 'missing', deniedProbes: 0,
  }));
  const s3 = await fakeS3(async (req, res, key, query) => {
    const scenario = scenarios.find(s => key.includes(s.id) || key === finalKey(s.bytes));
    if (!scenario) return false;
    const {mode} = scenario;
    if (req.method === 'POST' && query.has('uploads')) scenario.uploads++;
    if (req.method !== 'GET') return false;
    const storeError = (status: number, code: string) => {
      res.writeHead(status);
      res.end('<Error><Code>' + code + '</Code></Error>');
      return true;
    };
    if (req.headers.range) {
      if (key === scenario.stage && scenario.denyProbe) {
        scenario.deniedProbes++;
        return storeError(403, 'AccessDenied');
      }
      if (key === scenario.stage && scenario.holdNextProbe) {
        scenario.holdNextProbe = false;
        scenario.staleProbeStarted = true;
        await scenario.staleProbe.wait;
      }
      return false;
    }
    if (!query.has('X-Amz-Signature')) return false;
    scenario.reads.push(key);
    if (scenario.reads.length > 1) {
      if (mode === 'missing' && scenario.reads.length === 2) {
        scenario.newStage = key;
        s3.objects.delete(key);
        await scenario.replacement.wait;
        return storeError(404, 'NoSuchKey');
      }
      if (mode === 'missing' && scenario.reads.length === 3) scenario.thirdStage = key;
      return false;
    }
    scenario.stage = key;
    if (mode === 'expired') return storeError(403, 'RequestTimeTooSkewed');
    if (mode === 'promoted') s3.objects.set(finalKey(scenario.bytes), s3.objects.get(key)!);
    s3.objects.delete(key);
    if (mode === 'missing') await scenario.first.wait;
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
    FLEET_DEFAULT_ARTIFACT_LOCATIONS: 'verify-host', FLEET_CF_ACCESS_FILE: join(dir, 'unused'),
    FLEET_BUGSINK_DSN: '',
  }});
  stopCoordinator = coordinator.stop;

  async function post(path: string, token: string, body: unknown, person?: string) {
    const response = await fetch(coordinator.base + path, {
      method: 'POST', signal: t.signal, headers: {
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
    return {id, secretFile, post: (path: string, body: object) =>
      post('/api/hosts/' + path, registered.secret, {hostId: id, ...body})};
  }
  const uploader = await host('upload-host'), verifier = await host('verify-host');
  const endpoint = 'http://127.0.0.1:' + s3.port;
  ok(await verifier.post('stores', {store: {
    id: 'test-store', bucket: 'fleet', endpoint, localEndpoint: endpoint,
    accessKeyId: 'test-invented', secretAccessKey: 'test-invented',
  }}));
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
  for (const s of scenarios) {
    const path = join(dir, s.id + '.bin');
    writeFileSync(path, s.bytes);
    uploaderDB.prepare("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,'upload',?,?)").run(s.id, JSON.stringify({
      path, name: s.mode + '.bin', mediaType: 'application/octet-stream', executionId, required: true,
    }), Date.now());
  }

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
  const firstVerify = verifierDB.prepare("SELECT state,response FROM transfers WHERE kind='verify' AND json_extract(body,'$.artifactId')=?").get(missing.id);
  assert.equal(firstVerify?.state, 'failed');
  assert.match(String(firstVerify?.response), /NoSuchKey/);
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
  missing.replacement.release();
  await until(() => missing.thirdStage || undefined, 'the owner can recover a missing replacement too', t.signal);
  assert.notEqual(missing.thirdStage, missing.newStage);

  for (const s of scenarios) {
    const row = await until(() => {
      const r = uploaderDB.prepare('SELECT state,body,response FROM transfers WHERE id=?').get(s.id);
      if (r?.state === 'failed') assert.fail(String(r.response));
      if (r?.state === 'done') return r;
    }, s.mode + ' transfer reached stored', t.signal);
    assert.deepEqual(JSON.parse(String(row.response)), {artifactId: s.id, state: 'stored'});
    assert.equal(JSON.parse(String(row.body)).recoveries ?? 0, s.mode === 'missing' ? 2 : 0);
    assert.equal(s.uploads, s.mode === 'missing' ? 3 : 1);
    const retryKeys = s.mode === 'missing' ? [s.newStage, s.thirdStage] : [s.mode === 'promoted' ? finalKey(s.bytes) : s.stage];
    assert.deepEqual(s.reads, [s.stage, ...retryKeys]);
    const view = ok(await uploader.post('artifacts/get', {artifactId: s.id, executionId}));
    const downloaded = await fetch(view.url, {signal: t.signal});
    assert.equal(downloaded.status, 200);
    assert.deepEqual(Buffer.from(await downloaded.arrayBuffer()), s.bytes);
  }
});

function finalKey(bytes: Buffer) {
  return '/fleet/_/prod/' + createHash('sha256').update(bytes).digest('hex');
}
