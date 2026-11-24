// Requirement: task detail tells current work, question and evidence (OR-39).
// Risk: failed reports appear complete, obsolete acceptance remains actionable, or the statement buries the decision.
import assert from 'node:assert/strict';
import {build} from 'esbuild';
import type {Browser} from 'playwright';
export async function checkTaskStory(browser: Browser, origin: string) {
  const context = await browser.newContext(), page = await context.newPage();
  // Shared error contract: read-only errors retain alert text without an inert action; supplied retries invoke the caller.
  const fixture = await build({stdin: {contents: `import {render} from 'preact'; import {AgeChip, ErrorBanner} from './packages/ui/src/index.ts';
    render(<><AgeChip t={Date.now() - 58000}/><AgeChip t={Date.now()} clockOffsetMs={60000}/><ErrorBanner text="Read-only problem"/><ErrorBanner text="Retriable problem" onRetry={()=>document.querySelector('output').textContent='retried'}/><output/></>,document.body);`, resolveDir: process.cwd(), loader: 'tsx'}, bundle: true, write: false, format: 'iife', jsx: 'automatic', jsxImportSource: 'preact'});
  await page.goto('about:blank');
  await page.setContent('<body></body>');
  const clock = new Date('2026-11-18T12:00:00Z');
  await page.clock.install({ time: clock });
  await page.clock.pauseAt(clock.getTime() + 1000);
  await page.addScriptTag({content: fixture.outputFiles[0].text});
  await page.clock.runFor(150); // Flush the render's passive effect and subscribe the clock.
  const ages = page.locator('time.chip-age');
  const age = ages.first();
  assert.equal(await ages.last().textContent(), '1m', 'age uses the source clock offset');
  assert.equal(await age.textContent(), '58s');
  const tooltip = await age.getAttribute('data-tip');
  await page.clock.runFor(1000);
  assert.equal(await age.textContent(), '59s');
  await page.clock.runFor(1000);
  assert.equal(await age.textContent(), '1m');
  assert.equal(await age.getAttribute('aria-label'), '1 minute ago');
  assert.equal(await age.getAttribute('data-tip'), tooltip);
  await page.clock.resume();
  const alerts = page.getByRole('alert');
  assert.equal(await alerts.count(), 2);
  assert.equal(await alerts.filter({hasText: 'Read-only problem'}).getByRole('button').count(), 0);
  await alerts.filter({hasText: 'Retriable problem'}).getByRole('button', {name: 'Retry'}).click();
  assert.equal(await page.locator('output').textContent(), 'retried');
  const question = {id: 'current', kind: 'operator_decision', state: 'open', text: 'Retry installation? Retry starts another execution; fail ends this task.', options: ['retry', 'fail']};
  const cases = [
    {state: 'waiting', waitReason: 'Access unavailable; supply access to continue', questions: [question], executions: []},
    {state: 'waiting', waitReason: 'question', questions: [{id: 'old', kind: 'acceptance', state: 'withdrawn', text: 'Accept obsolete result?', options: ['accepted', 'rejected']}, question], executions: [{id: 'run', state: 'succeeded', report: {outcome: 'failed', summary: 'Patch created. Android installation not verified or published.', at: 1}}, {id: 'new-run', state: 'failed', problem: 'Native session home mismatch; preparation stopped.'}]},
    {state: 'completed', questions: [], executions: [{id: 'run', state: 'succeeded', report: {outcome: 'succeeded', summary: 'Installed and verified on Android; published result.', at: 2}}]},
  ];
  try {
    for (const [i, fixture] of cases.entries()) {
      await page.route('**/api/ops/task_get', route => route.fulfill({json: {id: 'story', title: 'Install Android app', expectedOutcome: 'Full original request. '.repeat(100), completionRequirements: { device: 'Verify on Android' }, latestResult: {id: 'patch', name: 'implementation.patch', mediaType: 'text/plain'}, dependencies: [], ...fixture}}));
      await page.goto(origin + '/tasks/story');
      const detail = page.locator('[data-ui=detail]');
      await detail.getByRole('heading', {name: 'Install Android app'}).waitFor();
      assert.equal(await detail.getByText('Accept obsolete result?', {exact: true}).count(), 0);
      assert.equal(await detail.locator('[data-ui=decision-block]').count(), i === 2 ? 0 : 1);
      await detail.getByRole('link', {name: 'implementation.patch'}).waitFor();
      if (i !== 2) {
        await detail.getByText(question.text, {exact: true}).waitFor();
        assert(await detail.locator('[data-ui=decision-block]').evaluate(el => el.getBoundingClientRect().top < document.querySelector('[data-ui=outcome-block]')!.getBoundingClientRect().top));
      }
      if (fixture.executions.length) await detail.getByText(fixture.executions[0].report!.summary, {exact: true}).waitFor();
      if (i === 1) {
        await detail.getByText('Native session home mismatch; preparation stopped.', {exact: true}).waitFor();
        assert(await detail.locator('[role=alert]').evaluate(el => el.getBoundingClientRect().top < document.querySelector('[data-ui=outcome-block]')!.getBoundingClientRect().top));
        assert.equal(await detail.getByRole('button', {name: /accepted|rejected/}).count(), 0);
        assert.equal(await detail.locator('[role=alert]').getByRole('button', {name: 'Retry'}).count(), 0);
      }
      await detail.getByText('device: Verify on Android', { exact: true }).waitFor();
      assert.equal(await detail.locator('.outcome-meta').count(), 0, 'unverified requirements have no completion counter');
      await detail.getByRole('button', {name: 'Expand outcome'}).click();
      assert.equal(await detail.getByRole('button', {name: 'Collapse outcome'}).count(), 1);
      await page.unroute('**/api/ops/task_get');
    }
    // Verify unified resource accounting rollups (elapsed vs executionWall, own/delegated/spawned, multi-currency/subscription, recursive ExecTree).
    const resSummary = (wallMs: number, tok: number, cost: any[]) => ({
      executions: 2,
      tokens: { input: tok, output: 1000, cacheRead: 0, cacheWrite: 0, reasoning: 0, total: tok + 1000, basis: 'measured' },
      durations: { elapsedMs: 120000, executionWallMs: wallMs, activeMs: wallMs, apiMs: 20000, cpuMs: 9200, measuredCpuRuns: 2, totalRuns: 2, basis: 'measured' },
      peakMemoryBytes: { value: 536870912, basis: 'measured' },
      tools: { totalCalls: 4, failedCalls: 1, turns: 3, byTool: [{ name: 'Bash', calls: 4, errors: 1 }], basis: 'measured' },
      cost,
    });
    const costs = [
      { value: 1.5, currency: 'USD', basis: 'measured' },
      { value: 0.75, currency: 'EUR', basis: 'estimated', reason: 'tokens' },
      { value: 0.4, currency: 'USD', basis: 'subscription', note: 'Claude Max' },
    ];
    await page.route('**/api/ops/task_get', route => route.fulfill({
      json: {
        id: 'story-resources',
        title: 'Unified resources story',
        state: 'completed',
        questions: [],
        dependencies: [],
        cost: costs,
        resources: {
          own: resSummary(30000, 10000, [costs[0]]),
          delegated: resSummary(15000, 5000, [costs[1]]),
          spawned: resSummary(10000, 4000, [costs[2]]),
          total: resSummary(45000, 19000, costs),
          spawnedTasks: [{ taskId: 'spawn-1', code: 'T-99', title: 'Child spawned task', state: 'completed', resources: resSummary(10000, 4000, [costs[2]]) }],
          hidden: 2,
        },
        executions: [
          { id: 'exec-root', kind: 'agent_run', state: 'succeeded', harness: 'claude', model: 'opus', hostId: 'mac', startedAt: Date.now() - 60000, endedAt: Date.now() - 15000 },
          { id: 'exec-child', parentExecutionId: 'exec-root', kind: 'child_agent', state: 'succeeded', harness: 'codex', model: 'gpt', hostId: 'mac', startedAt: Date.now() - 50000, endedAt: Date.now() - 25000 },
          { id: 'exec-tool-1', parentExecutionId: 'exec-root', kind: 'tool_run', state: 'succeeded', hostId: 'mac', startedAt: Date.now() - 45000, endedAt: Date.now() - 40000 },
          { id: 'exec-tool-2', parentExecutionId: 'exec-root', kind: 'tool_run', state: 'failed', problem: 'exit 1', hostId: 'mac', startedAt: Date.now() - 35000, endedAt: Date.now() - 33000 },
        ],
      },
    }));
    await page.goto(origin + '/tasks/story-resources');
    const rDetail = page.locator('[data-ui=detail]');
    await rDetail.getByRole('heading', { name: 'Unified resources story' }).waitFor();
    assert.equal(await rDetail.locator('.totals .b-unavailable').count(), 0, 'OR-39: zero unavailable totals in task detail');
    await rDetail.locator('.totals').getByText('Elapsed', { exact: true }).waitFor();
    await rDetail.locator('.totals').getByText('Execution wall', { exact: true }).waitFor();
    await rDetail.locator('.totals').getByText('2m', { exact: true }).waitFor();
    await rDetail.locator('.totals').getByText('45s', { exact: true }).waitFor();
    await rDetail.locator('.totals').getByText('$1.50', { exact: true }).waitFor();
    await rDetail.locator('.totals').getByText('≈ 0.75 EUR', { exact: true }).waitFor();
    await rDetail.locator('.totals').getByText('≈ $0.40', { exact: true }).waitFor();
    await rDetail.getByRole('heading', { name: 'Resource breakdown' }).waitFor();
    await rDetail.getByRole('link', { name: 'Child spawned task' }).waitFor();
    await rDetail.getByText('2 hidden (restricted project)', { exact: true }).waitFor();
    await rDetail.locator('[data-ui="execution-tree"]').getByText('Tool calls · 2 · 1 failed', { exact: true }).waitFor();
    await page.unroute('**/api/ops/task_get');

    // T-FLT-33: Tasks Running preset preserves unresolved filter and conveys scope; TaskFacts separates task age from
    // credible active run duration and renders stale/stalled/unconfirmed separately without false running-too-long alerts.
    const t33Clock = Date.parse('2026-11-23T18:00:00Z');
    const firstStart = '2026-11-23T04:00:00Z';
    const longStart = '2026-11-23T15:30:00Z';
    const shortStart = '2026-11-23T17:45:00Z';
    const recent = '2026-11-23T17:59:30Z';
    const stale = '2026-11-23T16:30:00Z';
    const mkRow = (id: string, title: string, extra: Record<string, unknown>) => ({
      id,
      code: `T-FLT-${id}`,
      projectId: 'fleet',
      title,
      state: 'in_progress',
      pauseReason: null,
      waitReason: null,
      priority: 'normal',
      agent: 'codex',
      host: 'owner-mac',
      startedAt: firstStart,
      createdAt: firstStart,
      updatedAt: recent,
      openQuestions: 0,
      stateAt: firstStart,
      executions: 2,
      lastAgent: 'codex',
      activeState: null,
      activeStartedAt: null,
      activeObservation: null,
      activeLastSeenAt: null,
      activeStalled: false,
      sortKey: ['fleet', 0, id],
      ...extra,
    });
    const t33Rows = [
      mkRow('301', 'Paused old task', { state: 'paused', agent: null }),
      mkRow('302', 'Waiting on owner', { state: 'waiting', waitReason: 'question', agent: null, openQuestions: 1 }),
      mkRow('303', 'In progress no active run', { state: 'in_progress', agent: null }),
      mkRow('304', 'Short active continuation', { activeState: 'running', activeStartedAt: shortStart, activeObservation: 'confirmed', activeLastSeenAt: recent }),
      mkRow('305', 'Long active continuation', { activeState: 'running', activeStartedAt: longStart, activeObservation: 'confirmed', activeLastSeenAt: recent }),
      mkRow('306', 'Stale running record', { activeState: 'running', activeStartedAt: longStart, activeObservation: 'confirmed', activeLastSeenAt: stale }),
      mkRow('307', 'Stalled active continuation', { activeState: 'running', activeStartedAt: longStart, activeObservation: 'confirmed', activeLastSeenAt: stale, activeStalled: true }),
      mkRow('308', 'Unconfirmed continuation', { activeState: 'running', activeStartedAt: longStart, activeObservation: 'unconfirmed', activeLastSeenAt: stale }),
    ];
    const t33Page = await context.newPage();
    await t33Page.clock.install({ time: t33Clock });
    await t33Page.route('**/api/ops/task_page', route => route.fulfill({
      json: {
        tasks: t33Rows,
        nextAfter: null,
        counts: { queued: 0, in_progress: 6, waiting: 1, held: 0, paused: 1, completed: 0, failed: 0, cancelled: 0 },
        total: t33Rows.length,
        groups: [{ key: 'fleet', label: 'fleet', count: t33Rows.length }],
      },
    }));
    await t33Page.goto(origin + '/tasks?preset=Running&state=held,in_progress,paused,queued,waiting');
    await t33Page.locator('.vl-row[data-key="305"]').waitFor();
    await t33Page.clock.runFor(150);
    const runningPreset = t33Page.locator('[data-ui=saved-views] button', { hasText: 'Running' });
    assert.match(await runningPreset.getAttribute('data-tip') ?? '', /unresolved tasks/i);
    assert.match(await runningPreset.getAttribute('aria-description') ?? '', /unresolved tasks/i);
    await t33Page.getByText('Unresolved tasks: queued, in progress, waiting, held, and paused.', { exact: true }).waitFor();

    for (const id of ['301', '302', '303', '304']) {
      const row = t33Page.locator(`.vl-row[data-key="${id}"]`);
      assert.equal(await row.locator('.chip-age.t-danger').count(), 0, `row ${id} must not show red duration warning`);
      assert.equal(await row.locator('[aria-label*="running too long"]').count(), 0, `row ${id} must not say running too long`);
    }
    const longRow = t33Page.locator('.vl-row[data-key="305"]');
    assert.equal(await longRow.locator('.chip-age.t-danger[aria-label*="running too long"]').count(), 1, 'genuinely active 2h30m run shows red running too long alert');
    assert.equal(await longRow.locator('.row-facts [data-tip="Task age since first run"]').count(), 1, 'task age shown separately from current run');

    assert.equal(await t33Page.locator('.vl-row[data-key="306"] .chip-age.t-danger').count(), 0);
    assert.equal(await t33Page.locator('.vl-row[data-key="306"] .chip-age.t-attention[aria-label*="run evidence stale"]').count(), 1);
    assert.equal(await t33Page.locator('.vl-row[data-key="307"] .chip-age.t-danger').count(), 0);
    assert.equal(await t33Page.locator('.vl-row[data-key="307"] .chip-age.t-attention[aria-label*="run stalled"]').count(), 1);
    assert.equal(await t33Page.locator('.vl-row[data-key="308"] .chip-age.t-danger').count(), 0);
    assert.equal(await t33Page.locator('.vl-row[data-key="308"] .chip-age.t-attention[aria-label*="run unconfirmed"]').count(), 1);
    assert.equal(await t33Page.locator('.vl-row[data-key="308"] .badge.unconf').count(), 1);
    await t33Page.unroute('**/api/ops/task_page');
    await t33Page.close();
  } finally { await context.close(); }
}
