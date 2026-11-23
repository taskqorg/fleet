import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { lit, startCoordinator } from '../harness.ts';

// Requirement: fleet.md History access gives operators history within their project grant.
// Risk: native events lose project ownership, trust host claims, or change identity on replay or backfill.
test('host project event ownership survives native replay and historical repair', { timeout: 30_000 }, async () => {
  const c = await startCoordinator({ name: 'event-ownership-' + randomUUID() });
  const owner = 'owner@trial.test', member = 'member@trial.test';
  const project = 'desiign', wrongProject = 'hello-world', hostId = 'event-project-host';
  const projectSession = randomUUID(), localSession = randomUUID();
  const projectRun = randomUUID(), localRun = randomUUID();
  const post = async (path: string, body: unknown, person: string | null = owner, secret = c.secret) => {
    const response = await fetch(c.base + path, {
      method: 'POST',
      headers: { authorization: 'Bearer ' + secret, 'content-type': 'application/json', ...(person ? { 'x-fleet-person': person } : {}) },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(3000),
    });
    return { status: response.status, data: await response.json() as any };
  };
  const op = (name: string, body: unknown, person = owner) => post('/api/ops/' + name, body, person);
  try {
    const host = await op('hosts_register', { id: hostId });
    assert.equal(host.status, 200);
    const wrongHost = await op('hosts_register', { id: 'other-event-host' });
    assert.equal(wrongHost.status, 200);
    for (const [sid, eid, pid] of [[projectSession, projectRun, project], [localSession, localRun, null]] as const) {
      c.sql(`INSERT INTO session(id,project_id,directed_by,created_at)
        VALUES(${lit(sid)},${lit(pid)},${lit(member)},now());
        INSERT INTO execution(id,kind,state,project_id,session_id,host_id,directed_by,data)
        VALUES(${lit(eid)},'agent_run','running',${lit(pid)},${lit(sid)},${lit(hostId)},${lit(member)},
          jsonb_build_object('id',${lit(eid)}::text,'sessionId',${lit(sid)}::text,'hostId',${lit(hostId)}::text,'projectId',${lit(pid)}::text));`);
    }
    const history = (executionId: string, person = member) => op('execution_events', { executionId }, person);
    const ingest = (executionId: string, events: unknown[], secret = host.data.secret) => post('/api/hosts/events', { executionId, events }, null, secret);
    const frame = (id: string, seq: number, normalized: object = {}) => ({
      id, seq, type: 'item.completed', timestamp: seq, data: { fleetNormalized: normalized },
    });
    const failed = frame('old', 1, { blocks: [{ kind: 'tool', name: 'read', state: 'failed' }] });
    const legacyID = 'event:' + projectRun + ':' + failed.id;
    c.sql(`INSERT INTO event(id,execution_id,session_id,event_seq,type,at,payload)
       VALUES(${lit(legacyID)},${lit(projectRun)},${lit(projectSession)},1,'item.completed',to_timestamp(0.001),${lit({ data: failed.data })}::jsonb);
      INSERT INTO event(id,execution_id,session_id,event_seq,type,at,payload) VALUES
       ('event:' || ${lit(localRun)} || ':old',${lit(localRun)},${lit(localSession)},0,'item.completed',to_timestamp(0),'{}'),
       ('event:' || ${lit(projectRun)} || ':unmatched',${lit(projectRun)},NULL,0,'item.completed',to_timestamp(0),'{}');
      DELETE FROM schema_migration WHERE version = 90;`);
    assert.deepEqual((await history(projectRun)).data.events, []);
    await c.restart();
    const repaired = await history(projectRun);
    assert.equal(repaired.status, 200);
    assert.deepEqual(repaired.data.events.map((e: any) => [e.id, e.projectId, e.data]), [[legacyID, project, failed.data]]);
    assert.equal(c.sql(`SELECT count(*) FROM event WHERE project_id IS NULL AND id IN
      ('event:' || ${lit(localRun)} || ':old', 'event:' || ${lit(projectRun)} || ':unmatched');`).trim(), '2');
    c.sql('DELETE FROM schema_migration WHERE version = 90;');
    await c.restart();
    assert.deepEqual((await history(projectRun)).data.events, repaired.data.events, 're-applying migration preserves repaired history');
    assert.equal((await ingest(projectRun, [failed])).status, 200, 'migrated frame replays unchanged');

    const completed = { ...frame('new', 2, { blocks: [{ kind: 'tool', name: 'read', state: 'completed' }] }), projectId: wrongProject };
    assert.equal((await ingest(projectRun, [completed], wrongHost.data.secret)).status, 403);
    assert.equal((await ingest(projectRun, [completed])).status, 200);
    assert.equal((await ingest(projectRun, [completed])).status, 200);
    assert.deepEqual((await history(projectRun)).data.events.map((e: any) => [e.seq, e.projectId, e.data.fleetNormalized.blocks[0].state]),
      [[1, project, 'failed'], [2, project, 'completed']]);
    assert.equal((await history(projectRun, 'outsider@trial.test')).status, 404);
    assert.equal((await ingest(projectRun, [{ ...completed, timestamp: 999 }])).status, 409);

    const delta = frame('chunk', 3, { deltaOf: 'text:1' });
    const close = frame('close', 4, { closes: ['text:1'] });
    assert.equal((await ingest(projectRun, [delta, close])).status, 200);
    assert.equal(c.sql(`SELECT count(*) FROM closed_delta_identity WHERE execution_id=${lit(projectRun)};`).trim(), '1');
    assert.equal((await ingest(projectRun, [delta])).status, 200, 'compacted delta replays unchanged');
    assert.equal((await ingest(projectRun, [{ ...delta, timestamp: 999 }])).status, 409);

    assert.equal((await ingest(localRun, [{ ...frame('local', 1), projectId: wrongProject }])).status, 200);
    const local = await history(localRun);
    assert.equal(local.status, 200);
    assert.deepEqual(local.data.events.map((e: any) => e.projectId ?? null), [null, null]);
    assert.equal((await history(localRun, 'outsider@trial.test')).status, 404);
    assert.equal(c.sql(`SELECT count(*) FROM execution WHERE id IN (${lit(projectRun)}, ${lit(localRun)}) AND state='running';`).trim(), '2');
  } finally {
    await c.stop();
  }
});
