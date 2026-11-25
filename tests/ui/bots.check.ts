// Requirement: fleet.md Persistent bots (FLT-20 PR 4/5) in Library's Bots root (pages/library.md): an earlier /bots link
// lands in Library, both same-named HAL0 instances open by their code with managed tasks (active/waiting/historical),
// runs (quiet/skipped/queued/failed/unconfirmed) and Run now / Pause / Resume / Stop / Talk to it controls. Questions
// show on Home and cost on System > Spend, so the bot page has neither tab.
// Risk: the bot view leaks tasks/runs across same-named HAL0 instances, misrenders SolidJS 2.0 controls, or breaks
// Run/Pause/Stop flows in Chromium.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import type { Browser } from 'playwright';
import { lit } from '../harness.ts';
import type { seeder } from './seed.ts';

export async function checkBots(browser: Browser, origin: string, seed: ReturnType<typeof seeder>) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const p = await ctx.newPage();
  const errors: string[] = [];
  p.on('pageerror', e => errors.push(e.stack || e.message));
  let automationStep = '';

  const call = async (name: string, body: object) => {
    const r = await fetch(`${origin}/api/ops/${name}`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    });
    assert.equal(r.status, 200, `${name} (${r.status}): ${await r.clone().text()}`);
    return r.json() as Promise<Record<string, any>>;
  };

  try {
    const halA = randomUUID();
    const halB = randomUUID();
    const settingsA = {
      name: 'HAL0',
      purpose: 'Check Project A health and Needs you',
      profileId: 'pm',
      instructions: 'Inspect tasks and telemetry',
      soul: 'Retain unconfirmed observations',
      scheduleTime: '09:00',
      scheduleZone: 'Etc/UTC',
      wakeOnNeedsYou: true,
    };
    const settingsB = {
      ...settingsA,
      purpose: 'Check Project B field notes',
      scheduleTime: '',
      scheduleZone: '',
      wakeOnNeedsYou: false,
    };

    const botA = (await call('bot_settings_save', { botId: halA, projectId: 'hello-world', baseRevision: 0, settings: settingsA })).bot;
    const botB = (await call('bot_settings_save', { botId: halB, projectId: 'hello-world-alt', baseRevision: 0, settings: settingsB })).bot;

    // Seed tasks in Project A (active, waiting, historical) and Project B (active).
    const tAct = seed.task({ projectId: 'hello-world', title: 'HAL0 A active fix', state: 'in_progress' });
    const tWait = seed.task({ projectId: 'hello-world', title: 'HAL0 A waiting gate', state: 'waiting', waitReason: 'question' });
    const tHist = seed.task({ projectId: 'hello-world', title: 'HAL0 A completed check', state: 'completed' });
    const tOtherB = seed.task({ projectId: 'hello-world-alt', title: 'HAL0 B isolated task', state: 'in_progress' });

    await call('bot_task_link', { botId: halA, taskId: tAct, linkKind: 'created' });
    await call('bot_task_link', { botId: halA, taskId: tWait, linkKind: 'continued' });
    await call('bot_task_link', { botId: halA, taskId: tHist, linkKind: 'managed' });
    await call('bot_task_link', { botId: halB, taskId: tOtherB, linkKind: 'created' });

    // Seed attributed executions, totals, and runs (quiet, skipped, failed, unconfirmed) for HAL0-A + one run for HAL0-B.
    const eQuiet = 'exec-ui-quiet-' + randomUUID().slice(0, 8);
    const eFail = 'exec-ui-fail-' + randomUUID().slice(0, 8);
    const eUnconf = 'exec-ui-unconf-' + randomUUID().slice(0, 8);
    const eB = 'exec-ui-b-' + randomUUID().slice(0, 8);
    const qAskId = randomUUID();
    const qClosedId = randomUUID();
    const nowSec = Math.floor(Date.now() / 1000) - 120;

    seed.bulk(() => {
      seed.sql(`
        INSERT INTO execution(id, kind, state, observation, project_id, session_id, bot_id, host_id, model, problem, started_at, ended_at, data) VALUES
          (${lit(eQuiet)}, 'agent_run', 'succeeded', 'confirmed', 'hello-world', ${lit(botA.homeSessionId)}, ${lit(halA)}, 'pds18', 'gpt-6.2-sol', NULL, to_timestamp(${nowSec - 60}), to_timestamp(${nowSec - 50}), ${lit({ id: eQuiet })}::jsonb),
          (${lit(eFail)}, 'agent_run', 'failed', 'confirmed', 'hello-world', ${lit(botA.homeSessionId)}, ${lit(halA)}, 'pds18', 'gpt-6.2-sol', 'exit 2 in check', to_timestamp(${nowSec - 40}), to_timestamp(${nowSec - 30}), ${lit({ id: eFail })}::jsonb),
          (${lit(eUnconf)}, 'agent_run', 'running', 'unconfirmed', 'hello-world', ${lit(botA.homeSessionId)}, ${lit(halA)}, 'pds18', 'gpt-6.2-sol', NULL, now(), NULL, ${lit({ id: eUnconf })}::jsonb),
          (${lit(eB)}, 'agent_run', 'succeeded', 'confirmed', 'hello-world-alt', ${lit(botB.homeSessionId)}, ${lit(halB)}, 'pds18', 'gpt-6.2-sol', NULL, to_timestamp(${nowSec - 15}), to_timestamp(${nowSec - 10}), ${lit({ id: eB })}::jsonb);
        INSERT INTO execution_totals(execution_id, project_id, model, tokens_input, tokens_input_basis, tokens_output, tokens_output_basis, wall_ms, wall_ms_basis, active_ms, active_ms_basis) VALUES
          (${lit(eQuiet)}, 'hello-world', 'gpt-6.2-sol', 1200, 'measured', 300, 'measured', 10000, 'measured', 8000, 'measured'),
          (${lit(eFail)}, 'hello-world', 'gpt-6.2-sol', 500, 'measured', 100, 'measured', 10000, 'measured', 7000, 'measured'),
          (${lit(eB)}, 'hello-world-alt', 'gpt-6.2-sol', 400, 'measured', 80, 'measured', 5000, 'measured', 4000, 'measured');
        INSERT INTO bot_run(id, bot_id, project_id, bot_revision, memory_revision, session_id, execution_id, cause, state, observation, quiet, reason, sent_by, source, started_at, ended_at) VALUES
          (${lit(randomUUID())}, ${lit(halA)}, 'hello-world', 1, 0, ${lit(botA.homeSessionId)}, ${lit(eQuiet)}, 'schedule', 'succeeded', 'confirmed', true, '', 'Fleet', 'schedule', to_timestamp(${nowSec - 60}), to_timestamp(${nowSec - 50})),
          (${lit(randomUUID())}, ${lit(halA)}, 'hello-world', 1, 0, ${lit(botA.homeSessionId)}, NULL, 'needs_you', 'skipped', 'confirmed', false, 'paused', 'Fleet', 'needs_you', NULL, to_timestamp(${nowSec - 45})),
          (${lit(randomUUID())}, ${lit(halA)}, 'hello-world', 1, 0, ${lit(botA.homeSessionId)}, ${lit(eFail)}, 'manual', 'failed', 'confirmed', false, 'exit 2 in check', 'owner@trial.test', 'bot_run', to_timestamp(${nowSec - 40}), to_timestamp(${nowSec - 30})),
          (${lit(randomUUID())}, ${lit(halA)}, 'hello-world', 1, 0, ${lit(botA.homeSessionId)}, ${lit(eUnconf)}, 'needs_you', 'running', 'unconfirmed', false, '', 'Fleet', 'needs_you', to_timestamp(${nowSec - 20}), NULL),
          (${lit(randomUUID())}, ${lit(halB)}, 'hello-world-alt', 1, 0, ${lit(botB.homeSessionId)}, ${lit(eB)}, 'manual', 'succeeded', 'confirmed', true, '', 'owner@trial.test', 'bot_run', to_timestamp(${nowSec - 15}), to_timestamp(${nowSec - 10}));
        INSERT INTO question(id, kind, project_id, text, asked_by, state) VALUES
          (${lit(qAskId)}, 'operator_decision', 'hello-world', 'HAL0 decision: apply hotfix now?', ${lit('execution:' + eQuiet)}, 'open'),
          (${lit(qClosedId)}, 'operator_decision', 'hello-world', 'Previous withdrawn question', ${lit('execution:' + eQuiet)}, 'withdrawn');
      `);
    });

    // 1. An earlier /bots link lands in Library; both HAL0 instances are leaves of its Bots root and open by code.
    await p.goto(`${origin}/bots`);
    await p.waitForURL(`${origin}/library/bots`);
    const tree = p.getByRole('tree', { name: 'Library' });
    // The tree is virtualized and long; the Library search narrows it to the two bots.
    await p.getByRole('button', { name: 'Find in library', exact: true }).click();
    await p.getByRole('searchbox', { name: 'Find in library', exact: true }).fill('HAL0');
    const itemA = tree.locator(`[data-tree-key="bot:${halA}"]`);
    await itemA.waitFor();
    await tree.locator(`[data-tree-key="bot:${halB}"]`).waitFor();
    await itemA.click();
    await p.waitForURL(/\/library\/bots\/B-/);
    const pathA = new URL(p.url()).pathname;

    const detail = p.locator('[data-ui="detail"]');
    await detail.getByRole('heading', { name: 'HAL0', exact: true }).waitFor();
    await detail.getByRole('button', { name: 'Talk to it', exact: true }).waitFor();
    await detail.getByRole('button', { name: 'Run now', exact: true }).waitFor();
    await detail.getByRole('button', { name: 'Pause', exact: true }).waitFor();
    assert.equal(await p.getByRole('tab', { name: /^(Analytics|Questions)/ }).count(), 0, 'no Analytics or Questions tab');

    // 2. Tasks tab: active, waiting, and historical buckets.
    const tabs = p.getByRole('tablist', { name: 'Bot sections' });
    await tabs.getByRole('tab', { name: /^Tasks/ }).click();
    await detail.getByText('HAL0 A active fix', { exact: true }).waitFor();
    await detail.getByRole('group', { name: 'Task state' }).getByRole('button', { name: /^Waiting/ }).click();
    await detail.getByText('HAL0 A waiting gate', { exact: true }).waitFor();
    assert.equal(await detail.getByText('HAL0 A active fix', { exact: true }).count(), 0);
    await detail.getByRole('group', { name: 'Task state' }).getByRole('button', { name: /^Historical/ }).click();
    await detail.getByText('HAL0 A completed check', { exact: true }).waitFor();
    assert.equal(await detail.getByText('HAL0 B isolated task', { exact: true }).count(), 0);

    // 3. Runs tab: quiet, skipped, failed, and unconfirmed filters.
    await tabs.getByRole('tab', { name: /^Runs/ }).click();
    const runGroup = detail.getByRole('group', { name: 'Run filter' });
    await runGroup.getByRole('button', { name: /^Quiet/ }).click();
    await detail.locator('[data-ui="data-table"]').getByText('Quiet', { exact: true }).waitFor();
    await runGroup.getByRole('button', { name: /^Skipped/ }).click();
    await detail.locator('[data-ui="data-table"]').getByText('The bot is paused', { exact: true }).first().waitFor();
    await runGroup.getByRole('button', { name: /^Failed/ }).click();
    // A failed run's stored cause reads as its plain sentence; the code is only its tooltip (formats.md, Failure cause).
    const failedCause = detail.locator('[data-ui="data-table"]').getByText('The run failed. Read the detail.', { exact: true }).first();
    await failedCause.waitFor();
    assert.equal(await failedCause.getAttribute('data-tip'), 'exit 2 in check');
    await runGroup.getByRole('button', { name: /^Unconfirmed/ }).click();
    await detail.locator('[data-ui="data-table"]').getByText('Needs you', { exact: true }).first().waitFor();

    // 6. Pause & Resume control updates badge cleanly, then Run now + Stop exercises active run lifecycle.
    const pauseReq = p.waitForResponse(r => r.url().endsWith('/api/ops/bot_pause') && r.status() === 200);
    await detail.getByRole('button', { name: 'Pause', exact: true }).click();
    await pauseReq;
    await detail.getByRole('button', { name: 'Resume', exact: true }).waitFor();
    const resumeReq = p.waitForResponse(r => r.url().endsWith('/api/ops/bot_pause') && r.status() === 200);
    await detail.getByRole('button', { name: 'Resume', exact: true }).click();
    await resumeReq;

    // Settle any prior active seeded run so Run now admits a fresh queued run cleanly, then Stop it.
    seed.sql(`UPDATE execution SET state='succeeded', observation='confirmed', problem=NULL, ended_at=now() WHERE id=${lit(eUnconf)};`);
    seed.sql(`UPDATE bot_run SET state='succeeded', observation='confirmed', ended_at=now() WHERE execution_id=${lit(eUnconf)};`);
    const runNowReq = p.waitForResponse(r => r.url().endsWith('/api/ops/bot_run') && r.status() === 200);
    await detail.getByRole('button', { name: 'Run now', exact: true }).click();
    await runNowReq;
    const stopBtn = detail.getByRole('button', { name: 'Stop', exact: true });
    await stopBtn.waitFor();
    const stopReq = p.waitForResponse(r => r.url().endsWith('/api/ops/bot_stop') && r.status() === 200);
    await stopBtn.click();
    await stopReq;

    // 7. Select HAL0-B and verify clean two-project isolation in Web UI.
    await tree.locator(`[data-tree-key="bot:${halB}"]`).click();
    await p.waitForURL(u => u.pathname.startsWith('/library/bots/B-') && u.pathname !== pathA);
    await tabs.getByRole('tab', { name: /^Tasks/ }).click();
    await detail.getByText('HAL0 B isolated task', { exact: true }).waitFor();
    assert.equal(await detail.getByText('HAL0 A active fix', { exact: true }).count(), 0);

    // FLT-26: the operator can configure and pause independent triggers, and open the selected wake's execution.
    automationStep = 'open Automations';
    await tabs.getByRole('tab', { name: 'Automations' }).click();
    automationStep = 'create the daily automation';
    await detail.getByRole('button', { name: 'New automation' }).click();
    await detail.getByLabel('Name', { exact: true }).fill('HAL0 tariff refresh');
    await detail.getByLabel('Instructions', { exact: true }).fill('Compare official provider tariffs in dry-run mode.');
    await detail.getByRole('button', { name: 'Daily schedule', exact: true }).click();
    await detail.getByLabel('Daily wake time', { exact: true }).fill('00:00');
    await detail.getByLabel('Time zone', { exact: true }).fill('Asia/Tokyo');
    await detail.getByRole('button', { name: 'Create automation' }).click();
    const daily = detail.locator('section').filter({ has: p.getByRole('heading', { name: 'HAL0 tariff refresh', exact: true }) });
    await daily.getByText('Next scheduled run', { exact: true }).waitFor();

    automationStep = 'create the filtered event automation';
    await detail.getByRole('button', { name: 'New automation' }).click();
    await detail.getByLabel('Name', { exact: true }).fill('Review decisions');
    await detail.getByRole('button', { name: 'Needs you', exact: true }).click();
    await detail.getByRole('button', { name: 'Questions', exact: true }).click();
    const eventSaved = p.waitForResponse(r => r.url().endsWith('/api/ops/bot_automation_save'));
    await detail.getByRole('button', { name: 'Create automation' }).click();
    const eventResponse = await eventSaved;
    assert.equal(eventResponse.status(), 200);
    assert.equal((await eventResponse.json()).automation.settings.event, 'question');
    const event = detail.locator('section').filter({ has: p.getByRole('heading', { name: 'Review decisions', exact: true }) });
    await event.getByText('Questions', { exact: true }).waitFor();
    automationStep = 'edit the event automation';
    await event.getByRole('button', { name: 'Edit', exact: true }).click();
    await detail.getByLabel('Name', { exact: true }).fill('Review new decisions');
    await detail.getByRole('button', { name: 'Save changes' }).click();
    const updatedEvent = detail.locator('section').filter({ has: p.getByRole('heading', { name: 'Review new decisions', exact: true }) });

    automationStep = 'run the daily automation';
    await daily.getByRole('button', { name: 'Run now', exact: true }).click();
    await daily.getByRole('img', { name: /Queued|Running/ }).waitFor();
    automationStep = 'pause the daily automation independently';
    await daily.getByRole('button', { name: 'Pause automation' }).click();
    await daily.getByRole('button', { name: 'Enable automation' }).waitFor();
    assert.equal(await daily.getByText('Next scheduled run', { exact: true }).count(), 0);
    await updatedEvent.getByRole('button', { name: 'Pause automation' }).waitFor();
    automationStep = 'open the automation run history';
    await daily.getByRole('button', { name: 'Run history' }).click();
    await p.waitForURL(/tab=runs&automation=/);
    const history = detail.getByRole('region', { name: 'Bot runs' });
    await history.getByText('HAL0 tariff refresh · revision 1', { exact: true }).waitFor();
    automationStep = 'open the Fleet execution card';
    await history.getByRole('button', { name: 'Run: show details', exact: true }).click();
    await p.getByRole('link', { name: 'Open', exact: true }).and(p.locator('a[href]')).waitFor();

    automationStep = 'check browser errors';
    assert.deepEqual(errors, [], `unexpected page errors: ${errors.join('; ')}`);
    console.log('ok Library bots (HAL0 two-project isolation by code, tasks, runs, pause/resume, automations)');
  } catch (err) {
    if (automationStep && process.env.GITHUB_ACTIONS === 'true') {
      const line = err instanceof Error ? err.stack?.match(/\/tests\/ui\/bots\.check\.ts:(\d+):/)?.[1] : undefined;
      const location = line ? `file=tests/ui/bots.check.ts,line=${line}` : 'file=tests/ui/bots.check.ts';
      console.error(`::error ${location},title=FLT-26 browser check::Failed to ${automationStep}`);
    }
    throw err;
  } finally {
    await ctx.close();
  }
}
