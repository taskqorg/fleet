import { test } from 'node:test';
import assert from 'node:assert/strict';
import { startCoordinator } from '../harness.ts';
import type { Ops } from '../../contracts/generated/ops.ts';

test('Conversations composer targets the newest eligible session across the task (requirement: Conversations Controls; risk: foreign or uncertain runs receive messages, or a retry duplicates a turn)', async t => {
  const c = await startCoordinator({ name: 'thread-composer', env: { FLEET_LOST_AFTER_MS: '3600000' } });
  t.after(() => c.stop());
  c.sql(`
    INSERT INTO task(id,project_id,title) VALUES ('composer-task','desiign','Composer task'), ('other-task','desiign','Other task');
    INSERT INTO session(id,project_id,task_id,created_at) VALUES
      ('selected','desiign','composer-task',now()-interval '3 min'),
      ('running','desiign','composer-task',now()-interval '2 min'),
      ('uncertain','desiign','composer-task',now()-interval '1 min'),
      ('foreign','hidden-project','composer-task',now()),
      ('unrelated','desiign','other-task',now());
    INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,started_at,last_seen_at) VALUES
      ('selected-run','agent_run','running','confirmed','desiign','composer-task','selected',now(),now()),
      ('target-run','agent_run','running','confirmed','desiign','composer-task','running',now(),now()),
      ('uncertain-run','agent_run','running','unconfirmed','desiign','composer-task','uncertain',now(),now()),
      ('foreign-run','agent_run','running','confirmed','hidden-project','composer-task','foreign',now(),now()),
      ('unrelated-run','agent_run','running','confirmed','desiign','other-task','unrelated',now(),now());
  `);
  const op = async <K extends keyof Ops>(name: K, body: Ops[K]['request'], who = 'member@trial.test') => {
    const response = await fetch(c.base + '/api/ops/' + name, {
      method: 'POST', headers: { authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json' },
      body: JSON.stringify(body),
    });
    return { status: response.status, body: await response.json() as Ops[K]['reply'] };
  };
  const conversation = { conversationId: 'selected' };
  for (const who of ['member@trial.test', 'owner@trial.test']) {
    const target = await op('thread_composer', conversation, who);
    assert.equal(target.status, 200, JSON.stringify(target.body));
    assert.deepEqual([target.body.key.task, target.body.sessionId, target.body.active?.id], ['composer-task', 'running', 'target-run']);
  }
  assert.equal((await op('thread_composer', conversation, 'outsider@trial.test')).status, 404);
  assert.equal((await op('thread_composer', { conversationId: 'foreign' })).status, 404);

  const message = {
    sessionId: 'running', requestId: 'composer-retry', text: 'Keep going',
    composer: { ...conversation, executionId: 'target-run' }, delivery: 'steer',
  };
  assert.equal((await op('message_send', { ...message, sessionId: 'foreign' })).status, 404);
  assert.equal((await op('message_send', { ...message, sessionId: 'unrelated' })).status, 409);
  assert.equal((await op('message_send', { ...message, composer: { ...conversation, executionId: 'selected-run' } })).status, 409);
  c.sql(`INSERT INTO host_observation(id,project_id,payload) VALUES ('trial-host','desiign','{"capabilities":["steer"]}')
    ON CONFLICT (id) DO UPDATE SET project_id=EXCLUDED.project_id,payload=EXCLUDED.payload;
    UPDATE execution SET host_id='composer-host' WHERE id='target-run';`);
  const sent = await op('message_send', message);
  assert.equal(sent.status, 200, JSON.stringify(sent.body));
  assert.equal(c.sql("SELECT count(*) FROM queued_message WHERE session_id='running' AND request_id='composer-retry'").trim(), '1');
  assert.equal(c.sql("SELECT count(*) FROM command WHERE kind='steer' AND session_id='running'").trim(), '0',
    'the default host capability cannot authorize steering on another host');
  c.sql(`INSERT INTO host_observation(id,project_id,payload) VALUES ('composer-host','desiign','{"capabilities":["steer"]}');
    UPDATE host_observation SET payload='{}' WHERE id='trial-host';`);
  const steered = await op('message_send', { ...message, requestId: 'composer-steer' });
  assert.equal(steered.status, 200, JSON.stringify(steered.body));
  assert.equal(c.sql("SELECT execution_id || ':' || host_id FROM command WHERE kind='steer' AND session_id='running'").trim(), 'target-run:composer-host');
  c.sql("UPDATE execution SET observation='unconfirmed' WHERE id='target-run'");
  const next = await op('thread_composer', conversation);
  assert.equal(next.body.active?.id, 'selected-run');
  assert.deepEqual(await op('message_send', message), sent, 'the known request reconciles after its original run becomes unconfirmed');
  assert.equal((await op('message_send', { ...message, requestId: 'new-intent' })).status, 409);
  assert.equal(c.sql("SELECT state FROM execution WHERE id='target-run'").trim(), 'running', 'unconfirmed execution stays in history');
  c.sql("UPDATE execution SET observation='unconfirmed' WHERE id='selected-run'");
  c.sql("INSERT INTO session(id,project_id,task_id,created_at) VALUES ('newest','desiign','composer-task',now())");
  const fallback = await op('thread_composer', conversation);
  assert.equal(fallback.body.sessionId, 'newest');
  assert.equal(fallback.body.active, undefined);
});
