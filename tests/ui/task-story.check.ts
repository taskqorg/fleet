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
  } finally { await context.close(); }
}
