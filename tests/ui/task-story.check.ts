// Requirement: task detail tells current work, question and evidence (OR-39).
// Risk: failed reports appear complete, obsolete acceptance remains actionable, or the statement buries the decision.
import assert from 'node:assert/strict';
import {build} from 'esbuild';
import type {Browser} from 'playwright';
export async function checkTaskStory(browser: Browser, origin: string) {
  const context = await browser.newContext(), page = await context.newPage();
  // Shared error contract: read-only errors retain alert text without an inert action; supplied retries invoke the caller.
  const fixture = await build({stdin: {contents: `import {render} from 'preact'; import {ErrorBanner} from './packages/ui/src/index.ts';
    render(<><ErrorBanner text="Read-only problem"/><ErrorBanner text="Retriable problem" onRetry={()=>document.querySelector('output').textContent='retried'}/><output/></>,document.body);`, resolveDir: process.cwd(), loader: 'tsx'}, bundle: true, write: false, format: 'iife', jsx: 'automatic', jsxImportSource: 'preact'});
  await page.goto('about:blank');
  await page.setContent('<body></body>');
  await page.addScriptTag({content: fixture.outputFiles[0].text});
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
      await page.route('**/api/ops/task_get', route => route.fulfill({json: {id: 'story', title: 'Install Android app', expectedOutcome: 'Full original request. '.repeat(100), latestResult: {id: 'patch', name: 'implementation.patch', mediaType: 'text/plain'}, ...fixture}}));
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
    await rDetail.locator('[data-ui="execution-tree"]').getByText('Tool calls', { exact: true }).waitFor();
    await page.unroute('**/api/ops/task_get');
  } finally { await context.close(); }
}
