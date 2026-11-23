import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { startCoordinator } from '../harness.ts';

test('Bot revisions survive restart and are isolated by project (requirement: fleet.md Persistent bots; risk: retry duplicates settings, stale memory overwrites a newer revision, or a hidden project leaks)', async () => {
  const coordinator = await startCoordinator({ name: 'bots' });
  try {
    async function op(name: string, body: object, actor = 'owner@trial.test', status = 200) {
      const response = await fetch(coordinator.base + '/api/ops/' + name, {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: 'Bearer ' + coordinator.secret, 'x-fleet-person': actor },
        body: JSON.stringify(body),
      });
      const result = await response.json();
      assert.equal(response.status, status, JSON.stringify(result));
      return result;
    }

    const botId = randomUUID(), otherId = randomUUID();
    const settings = {
      name: 'Queue and health', purpose: 'Check fixture work', profileId: 'pm',
      instructions: 'Read current task state', soul: 'Use confirmed evidence',
      scheduleTime: '09:00', scheduleZone: 'Asia/Bangkok', wakeOnNeedsYou: true,
    };
    const original = { botId, projectId: 'hello-world', baseRevision: 0, settings };
    const created = (await op('bot_settings_save', original)).bot;
    assert.deepEqual((await op('bot_settings_save', original)).bot, created);
    assert.equal(created.revision, 1);
    await op('bot_settings_save', { ...original, botId: otherId, projectId: 'hello-world-alt' });
    assert.deepEqual((await op('bot_list', { excludeProjectIds: ['hello-world-alt'] })).bots.map((b: { id: string }) => b.id), [botId]);
    assert.deepEqual((await op('bot_list', {}, 'outsider@trial.test')).bots, []);
    await op('bot_get', { botId }, 'outsider@trial.test', 404);
    await op('bot_memory_save', { botId, baseRevision: 0, memory: 'Out of scope' }, 'outsider@trial.test', 404);
    await op('bot_settings_save', { ...original, settings: { ...settings, scheduleZone: 'Local' }, baseRevision: 1 }, 'owner@trial.test', 400);

    const updated = { ...settings, soul: 'Keep only confirmed evidence' };
    assert.equal((await op('bot_settings_save', { ...original, settings: updated, baseRevision: 1 })).bot.revision, 2);
    await op('bot_settings_save', original, 'owner@trial.test', 409);
    const memory = { botId, baseRevision: 0, memory: 'One pending task retained' };
    assert.equal((await op('bot_memory_save', memory)).bot.memoryRevision, 1);
    assert.equal((await op('bot_memory_save', memory)).bot.memoryRevision, 1);
    await op('bot_memory_save', { ...memory, memory: 'Stale overwrite' }, 'owner@trial.test', 409);

    await coordinator.restart();
    const retained = (await op('bot_get', { botId })).bot;
    assert.deepEqual(retained.settings, updated);
    assert.equal(retained.memory, memory.memory);
    assert.equal((await op('bot_get', { botId: otherId })).bot.memory, '');
    const changes = (await op('setting_changes', { target: 'bot:' + botId })).changes;
    assert.deepEqual(changes.map((c: { field: string; value: number }) => [c.field, c.value]), [
      ['memoryRevision', 1], ['settingsRevision', 2], ['settingsRevision', 1],
    ]);
  } finally {
    await coordinator.stop();
  }
});
