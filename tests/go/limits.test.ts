// Outside tests of subscription limits (docs/designs/fleet-people-access.md section 8), against the real binary and
// Postgres:   FLEET_TRIAL_TESTS=tests/go/limits.test.ts node tests/trial/run.ts   (or tests/go/run.sh)
// Each test names its requirement and the risk it guards.
import {test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const OWNER = 'owner@trial.test', MEMBER = 'member@trial.test';
const test = (name: string, fn: () => Promise<void>) => nodeTest(name, {timeout: 30_000}, fn);
type Reply = {status: number, body: any};
async function call(method: string, path: string, body: unknown, headers: Record<string, string>): Promise<Reply> {
  const r = await fetch(BASE + path, {method, headers: {'content-type': 'application/json', ...headers}, body: JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: object = {}, person = OWNER) => call('POST', '/api/ops/' + name, b, {authorization: 'Bearer ' + SECRET, 'x-fleet-person': person});
const poll = {platform: 'darwin', capabilities: [], stores: [], problems: [], projectRoots: []};

test('L1 subscription_limits_refresh sends account-inspect mode limits to an online host with a ready binding, and the probe reading shows in subscription_limits (requirement: fleet-people-access.md section 8 "Read without a run"; risk: a refresh never reaches a host, or a reading without a run is never shown)', async () => {
  const secret = ok(await op('hosts_register', {id: 'trial-host'})).secret;
  const h = (path: string, body: object) => call('POST', path, body, {authorization: 'Bearer ' + secret});
  ok(await h('/api/hosts/poll', poll));
  const a = ok(await op('account_create', {name: 'limits-sub', harness: 'codex'}));
  ok(await h('/api/hosts/accounts', {accountId: a.id, hostId: 'trial-host', binding: 'limits-bind', platform: 'darwin', readiness: 'ready'}));
  assert.equal((await op('subscription_limits_refresh', {subscriptionId: a.id}, MEMBER)).status, 404, 'only the holder or the owner refreshes; another person\'s subscription is not found');
  const r = ok(await op('subscription_limits_refresh', {subscriptionId: a.id}));
  assert.equal(r.sent.length, 1, JSON.stringify(r));
  assert.equal(r.sent[0].hostId, 'trial-host');
  assert.equal(ok(await op('subscription_limits_refresh', {subscriptionId: a.id})).sent[0].executionId, r.sent[0].executionId, 'a repeated refresh reuses the read on its way');
  const {commands} = ok(await h('/api/hosts/poll', poll));
  const c = commands.find((x: any) => x.executionId === r.sent[0].executionId);
  assert.equal(c?.kind, 'account-inspect');
  assert.equal(c?.payload?.mode, 'limits');
  assert.equal(c?.payload?.binding, 'limits-bind');
  const resetsAt = Date.now() + 3_600_000;
  ok(await h('/api/hosts/events', {hostId: 'trial-host', executionId: c.executionId, events: [{id: c.id + '-1', seq: 1, type: 'limit_reading', timestamp: Date.now(),
    data: {type: 'limit_reading', harness: 'codex', source: 'probe', subscriptionId: a.id, hostId: 'trial-host', readings: [{period: '5h', pct: 30, resetsAt}]}}]}));
  const limits = ok(await op('subscription_limits'));
  const sub = limits.subscriptions.find((s: any) => s.id === a.id);
  const reading = sub?.readings?.find((x: any) => x.period === '5h');
  assert.equal(reading?.pct, 30, JSON.stringify(sub));
});
