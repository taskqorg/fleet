// Outside concurrency, long-poll, heartbeat-debounce, and trigger tests against the real binary and Postgres:
//   FLEET_TRIAL_TESTS=tests/go/coordinator-concurrency.test.ts FLEET_TRIAL_SERVER=go/bin/fleet-coordinator node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const OWNER = 'owner@trial.test';
const test = (name: string, fn: () => Promise<void>) => nodeTest(name, {timeout: 30_000}, fn);
type Reply = {status: number, body: any};

async function call(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Reply> {
  const r = await fetch(BASE + path, {
    method,
    headers: {...(body === undefined ? {} : {'content-type': 'application/json'}), ...headers},
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  let parsed: any = text;
  try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}

const owner = {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER};
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: object = {}) => call('POST', '/api/ops/' + name, b, owner);
const pollBody = (capabilities: string[] = []) => ({platform: 'darwin', capabilities, stores: [], problems: []});

async function registerHost(id: string) {
  const {secret} = ok(await op('hosts_register', {id}));
  return (path: string, body: object) => call('POST', path, body, {authorization: 'Bearer ' + secret});
}

test('C1 concurrent host polls, non-terminal receipts, and session message appends execute cleanly in parallel (requirement: PR5 lock and poll decoupling from GLOBAL; risk: poll heartbeats, receipts, and messages contend or deadlock under concurrency)', async () => {
  const cmdHost = await registerHost('c1-rcpt-host');
  ok(await cmdHost('/api/hosts/poll', pollBody()));

  const s1 = ok(await call('POST', '/api/sessions', {}, owner)).sessionId as string;
  const s2 = ok(await call('POST', '/api/sessions', {}, owner)).sessionId as string;
  const e1 = ok(await call('POST', `/api/sessions/${s1}/messages`, {requestId: randomUUID(), text: 'first'}, owner));
  const e2 = ok(await call('POST', `/api/sessions/${s2}/messages`, {requestId: randomUUID(), text: 'second'}, owner));

  let cmds: any[] = [];
  for (let i = 0; i < 20; i++) {
    cmds = ok(await cmdHost('/api/hosts/poll?wait=250ms', pollBody())).commands;
    if (cmds.some(c => c.executionId === e1.id) && cmds.some(c => c.executionId === e2.id)) break;
  }
  const c1 = cmds.find(c => c.executionId === e1.id);
  const c2 = cmds.find(c => c.executionId === e2.id);
  assert.ok(c1 && c2, `expected start commands for both executions, got ${JSON.stringify(cmds)}`);

  const pollHosts = await Promise.all(Array.from({length: 30}, (_, i) => registerHost(`c1-h-${i}`)));
  const msgSessions = await Promise.all(Array.from({length: 5}, async () => ok(await call('POST', '/api/sessions', {}, owner)).sessionId as string));

  await Promise.all([
    ...pollHosts.map(async h => ok(await h('/api/hosts/poll', pollBody(['codex'])))),
    (async () => ok(await cmdHost('/api/hosts/receipts', {commandId: c1.id, executionId: e1.id, status: 'accepted'})))(),
    (async () => ok(await cmdHost('/api/hosts/receipts', {commandId: c2.id, executionId: e2.id, status: 'started'})))(),
    ...msgSessions.map(async (sid, idx) => ok(await call('POST', `/api/sessions/${sid}/messages`, {requestId: randomUUID(), text: `msg-${idx}`}, owner))),
    (async () => ok(await op('hosts_list')))(),
  ]);
});

test('C2 long-poll waits cleanly up to requested deadline and wakes immediately when dispatch queues a start command (requirement: PR5 long-poll channel wakeup < executor timeout; risk: idle hosts spin at 100ms or ignore wakeups until deadline)', async () => {
  const h = await registerHost('c2-lp-host');

  const t0 = performance.now();
  const empty = ok(await h('/api/hosts/poll?wait=120ms', pollBody()));
  const waitMs = performance.now() - t0;
  assert.equal(empty.commands.length, 0);
  assert.ok(waitMs >= 80 && waitMs < 2000, `expected timed poll near 120ms, got ${Math.round(waitMs)}ms`);

  const wakeStart = performance.now();
  const pendingPoll = h('/api/hosts/poll?wait=25s', pollBody());
  await new Promise(r => setTimeout(r, 80));

  // Pin placement: earlier cases leave other eligible hosts registered.
  const started = ok(await op('session_start', {requestId: randomUUID(), text: 'wake long poll', projectId: 'hello-world', profileId: 'pm', hostId: 'c2-lp-host'}));
  const exec = {id: started.executionId};

  const woken = ok(await pendingPoll);
  const wakeElapsed = performance.now() - wakeStart;
  assert.ok(wakeElapsed < 4000, `long-poll wakeup took ${Math.round(wakeElapsed)}ms`);
  assert.ok(woken.commands.some((c: any) => c.executionId === exec.id && c.kind === 'start'), `missing start command in ${JSON.stringify(woken)}`);
});

test('C3 unchanged heartbeats debounce change emissions while genuine state changes publish live and host stays online (requirement: PR5 heartbeat debounce and no-op churn suppression; risk: redundant polls flood change rows or falsely mark hosts offline)', async () => {
  const h = await registerHost('c3-hb-host');
  ok(await h('/api/hosts/poll', pollBody(['codex'])));

  const ctrl = new AbortController();
  const res = await fetch(BASE + '/api/stream', {headers: owner, signal: ctrl.signal});
  assert.equal(res.status, 200);
  const reader = res.body!.getReader();
  // Always release the stream, including assertion failures.
  nodeTest.after(() => ctrl.abort());
  const dec = new TextDecoder();
  let buf = '';
  let pendingRead = reader.read();
  const readChunk = async (ms: number) => {
    let timer: ReturnType<typeof setTimeout>;
    const out = await Promise.race([
      pendingRead,
      new Promise<null>(r => { timer = setTimeout(() => r(null), ms); }),
    ]);
    clearTimeout(timer!);
    if (out !== null) {
      if (!out.done && out.value) buf += dec.decode(out.value, {stream: true});
      pendingRead = reader.read();
    }
    return buf;
  };
  await readChunk(300);
  assert.match(buf, /event:\s*ready/, `expected stream ready, got ${buf}`);
  buf = '';

  // Unchanged heartbeats must keep host online and emit no redundant host change event.
  ok(await h('/api/hosts/poll', pollBody(['codex'])));
  ok(await h('/api/hosts/poll', pollBody(['codex'])));
  const hosts = ok(await op('hosts_list')).hosts as any[];
  assert.ok(hosts.some(x => x.id === 'c3-hb-host' && Date.now() - x.lastSeenAt < 30_000), `expected host online: ${JSON.stringify(hosts)}`);
  await readChunk(250);
  assert.ok(!/"kind":"host"/.test(buf), `unexpected host change on unchanged heartbeat: ${buf}`);

  // A genuine capability change flushes immediately and emits a host change event.
  ok(await h('/api/hosts/poll', pollBody(['codex', 'steer'])));
  let sawChange = false;
  for (let i = 0; i < 10; i++) {
    await readChunk(150);
    if (/"kind":"host"/.test(buf)) { sawChange = true; break; }
  }
  ctrl.abort();
  assert.ok(sawChange, `expected host change event after state change, got ${buf}`);
});
