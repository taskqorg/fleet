// Requirement: a successful answer updates the question card in place (OR-53, UR-58) without relying on the feed.
// Risk: stale controls permit a second conflicting reply, or a failed write falsely appears answered.
import assert from 'node:assert/strict';
import type {Browser} from 'playwright';
export async function checkQuestionReplies(browser: Browser, origin: string) {
  const context = await browser.newContext(), page = await context.newPage();
  await page.addInitScript(() => {
    (window as any).EventSource = class {
      constructor() { (window as any).feed = this; queueMicrotask(() => { if (!this.closed) this.handlers.ready?.({data: JSON.stringify({cursor: 0})}); }); }
      handlers: Record<string, (e: any) => void> = {};
      addEventListener(name: string, fn: (e: any) => void) { this.handlers[name] = fn; }
      closed = false;
      close() { this.closed = true; }
      onmessage?: (e: any) => void;
    };
  });
  try {
    for (const mode of ['withheld', 'normal', 'resync', 'failed']) {
      let answered = false;
      const question = () => ({id: 'invented-q', kind: 'agent_question', state: answered ? 'answered' : 'open', text: 'Invented reply question', options: ['yes'], answer: answered ? 'yes' : null, askedAt: new Date().toISOString(), answeredAt: answered ? new Date().toISOString() : null});
      await page.route('**/api/ops/session_get', r => r.fulfill({json: {id: 'invented-session', questions: [question()], executions: [], messages: []}}));
      const block = () => ({...question(), kind: 'question', key: '1:0', questionId: 'invented-q', askedAt: Date.now(), answeredAt: answered ? Date.now() : null});
      await page.route('**/api/ops/thread_entries', r => r.fulfill({json: {entries: [block()], first: '1:0', last: '1:0', total: 1, hasBefore: false, hasAfter: false, feedCursor: 0, sessionIds: ['invented-session']}}));
      await page.route('**/api/ops/thread_blocks', r => r.fulfill({json: {entries: [block()], removed: []}}));
      await page.route('**/api/ops/changes_since', r => r.fulfill({json: {changes: [], cursor: 0}}));
      await page.route('**/api/ops/questions_answer', r => {
        if (mode === 'failed') return r.fulfill({status: 409, json: {message: 'Invented write rejected'}});
        answered = true; return r.fulfill({json: question()});
      });
      await page.goto(origin + '/conversations/invented-session');
      // The open question is one card above the transcript; answered, it shows as one line in the transcript.
      const detail = page.locator('[data-ui=detail]'), inputs = detail.getByRole('textbox', {name: 'Answer: Invented reply question'});
      await inputs.first().waitFor();
      assert.equal(await inputs.count(), 1);
      await inputs.first().fill('yes');
      await inputs.first().press('Enter');
      if (mode === 'failed') {
        await detail.getByRole('alert').filter({hasText: 'Invented write rejected'}).waitFor();
        assert.equal(await inputs.count(), 1);
      } else {
        await inputs.first().waitFor({state: 'detached'});
        assert.equal(await inputs.count(), 0);
        await detail.locator('.qcard.answered').waitFor();
        if (mode !== 'withheld') {
          await page.evaluate(mode => {
            const f = (window as any).feed;
            if (mode === 'normal') f.onmessage({data: JSON.stringify({seq: 1, kind: 'question', id: 'invented-q', sessionId: 'invented-session'})});
            else f.handlers.resync({data: JSON.stringify({cursor: 1})});
          }, mode);
          await detail.locator('.qcard.answered').waitFor();
          assert.equal(await inputs.count(), 0);
        }
      }
      await page.unrouteAll({behavior: 'wait'});
    }
  } finally { await context.close(); }
}
