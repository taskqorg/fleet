// Requirement: fleet.md Persistent bots (FLT-20 PR 4/5): rendered Fleet Web (/bots) shows HAL0 across isolated
// projects with managed tasks (active/waiting/historical), runs (quiet/skipped/queued/failed/unconfirmed),
// open/answered questions (including question_ask), errors, analytics by recorded execution project, and
// Run now / Pause / Resume / Stop / Home controls.
// Risk: Bots Web view drops question_ask rows, leaks tasks/runs across same-named HAL0 instances, misrenders
// SolidJS 2.0 controls, or breaks Run/Pause/Stop/answer flows in Chromium.
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

    // 1. Open /bots and verify both HAL0 instances render with distinct project labels.
    await p.goto(`${origin}/bots`);
    const tree = p.getByRole('tree', { name: 'Bots' });
    await tree.getByRole('treeitem', { name: /HAL0.*Hello World · field notes/ }).waitFor();
    const itemA = tree.locator(`[data-tree-key="${halA}"]`);
    await itemA.waitFor();
    await itemA.click();
    await p.waitForURL(`**/bots/${halA}`);

    const detail = p.locator('[data-ui="detail"]');
    await detail.getByRole('heading', { name: 'HAL0', exact: true }).waitFor();
    await detail.getByRole('button', { name: 'Home', exact: true }).waitFor();
    await detail.getByRole('button', { name: 'Run now', exact: true }).waitFor();
    await detail.getByRole('button', { name: 'Pause', exact: true }).waitFor();
    await detail.getByText('HAL0 decision: apply hotfix now?', { exact: true }).waitFor();

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
    await detail.locator('[data-ui="data-table"]').getByText('paused', { exact: true }).first().waitFor();
    await runGroup.getByRole('button', { name: /^Failed/ }).click();
    await detail.locator('[data-ui="data-table"]').getByText('exit 2 in check', { exact: true }).first().waitFor();
    await runGroup.getByRole('button', { name: /^Unconfirmed/ }).click();
    await detail.locator('[data-ui="data-table"]').getByText('needs_you', { exact: true }).waitFor();

    // 4. Questions & Errors tab: question_ask row appears, typed answer submits, and unconfirmed work shows.
    await tabs.getByRole('tab', { name: /^Questions & Errors/ }).click();
    await detail.getByText('HAL0 decision: apply hotfix now?', { exact: true }).waitFor();
    await detail.getByText('Bot run (needs_you): unconfirmed outcome', { exact: true }).waitFor();
    const answerBox = detail.getByLabel('Typed answer', { exact: true });
    await answerBox.fill('Apply hotfix');
    const ansReq = p.waitForResponse(r => r.url().endsWith('/api/ops/questions_answer') && r.status() === 200);
    await answerBox.press('Enter');
    await ansReq;
    await detail.getByText('No open questions for this bot.', { exact: true }).waitFor();

    // 5. Analytics tab: renders hello-world totals and attributed executions.
    await tabs.getByRole('tab', { name: /^Analytics/ }).click();
    await detail.getByText('Spend & Usage by Recorded Execution Project', { exact: true }).waitFor();
    await detail.locator('[data-ui="data-table"][aria-label="Bot analytics by project"]').getByText('Hello World', { exact: true }).waitFor();
    await detail.locator('[data-ui="data-table"][aria-label="Attributed executions"]').getByText('agent_run').first().waitFor();

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
    await tree.locator(`[data-tree-key="${halB}"]`).click();
    await p.waitForURL(`**/bots/${halB}`);
    await tabs.getByRole('tab', { name: /^Tasks/ }).click();
    await detail.getByText('HAL0 B isolated task', { exact: true }).waitFor();
    assert.equal(await detail.getByText('HAL0 A active fix', { exact: true }).count(), 0);

    // FLT-26: the operator can configure and pause independent triggers, and open the selected wake's execution.
    await tabs.getByRole('tab', { name: 'Automations' }).click();
    await detail.getByRole('button', { name: 'New automation' }).click();
    await detail.getByLabel('Name', { exact: true }).fill('HAL0 tariff refresh');
    await detail.getByLabel('Instructions', { exact: true }).fill('Compare official provider tariffs in dry-run mode.');
    await detail.getByRole('button', { name: 'Daily schedule', exact: true }).click();
    await detail.getByLabel('Daily wake time', { exact: true }).fill('00:00');
    await detail.getByLabel('Time zone', { exact: true }).fill('Asia/Tokyo');
    await detail.getByRole('button', { name: 'Create automation' }).click();
    const daily = detail.locator('section').filter({ has: p.getByRole('heading', { name: 'HAL0 tariff refresh', exact: true }) });
    await daily.getByText('Next scheduled run', { exact: true }).waitFor();

    await detail.getByRole('button', { name: 'New automation' }).click();
    await detail.getByLabel('Name', { exact: true }).fill('Review decisions');
    await detail.getByRole('button', { name: 'Needs you', exact: true }).click();
    await detail.getByRole('button', { name: 'Questions', exact: true }).click();
    await detail.getByRole('button', { name: 'Create automation' }).click();
    const event = detail.locator('section').filter({ has: p.getByRole('heading', { name: 'Review decisions', exact: true }) });
    await event.getByText('Questions', { exact: true }).waitFor();
    await event.getByRole('button', { name: 'Edit', exact: true }).click();
    await detail.getByLabel('Name', { exact: true }).fill('Review new decisions');
    await detail.getByRole('button', { name: 'Save changes' }).click();
    const updatedEvent = detail.locator('section').filter({ has: p.getByRole('heading', { name: 'Review new decisions', exact: true }) });

    await daily.getByRole('button', { name: 'Run now', exact: true }).click();
    await daily.getByRole('img', { name: /Queued|Running/ }).waitFor();
    await daily.getByRole('button', { name: 'Pause automation' }).click();
    await daily.getByRole('button', { name: 'Enable automation' }).waitFor();
    assert.equal(await daily.getByText('Next scheduled run', { exact: true }).count(), 0);
    await updatedEvent.getByRole('button', { name: 'Pause automation' }).waitFor();
    await daily.getByRole('button', { name: 'Run history' }).click();
    await p.waitForURL(/tab=runs&automation=/);
    const history = detail.getByRole('region', { name: 'Bot runs' });
    await history.getByText('HAL0 tariff refresh · revision 1', { exact: true }).waitFor();
    await history.getByRole('button', { name: 'Run: show details', exact: true }).click();
    await p.getByRole('link', { name: 'Open', exact: true }).and(p.locator('a[href]')).waitFor();

    assert.deepEqual(errors, [], `unexpected page errors: ${errors.join('; ')}`);
    console.log('ok Bots Web UI (HAL0 two-project isolation, tasks, runs, questions_answer, errors, analytics, pause/resume)');
  } finally {
    await ctx.close();
  }
}
