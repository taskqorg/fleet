// Drafts check (packages/ui drafts.ts, app/src/share.ts): while live changes reread the session view every second, the
// question answer and the composer keep their DOM node, focus and text; a reload brings both drafts back.
// Prints a report; exits 1 when a claim fails. Run: pnpm run app:bundle && node tests/ui/drafts.check.ts
import { chromium, type Page } from 'playwright';
import { checkServer } from './seed.ts';

const { origin, seed, close } = await checkServer();
seed.bulk(() => {
  seed.put('session', { id: 'sess-d', createdAt: Date.now() - 600000, agentConfig: { id: 'baseline', harness: 'codex' } });
  seed.execution({ id: 'sx-d', kind: 'agent_run', sessionId: 'sess-d', hostId: 'pds18', harness: 'codex', state: 'succeeded' });
  seed.put('message', { id: 'msg-d', sessionId: 'sess-d', executionId: 'sx-d', text: 'Check the drafts.' });
  seed.question({ kind: 'operator_decision', sessionId: 'sess-d', executionId: 'sx-d', text: 'Which slot?', options: ['today', 'tomorrow'] });
});
const failures: string[] = [];
const claim = (ok: boolean, text: string) => { console.log((ok ? '  ok   ' : '  FAIL ') + text); if (!ok) failures.push(text); };
const rename = (i: number) => fetch(origin + '/api/ops/session_rename', { method: 'POST', headers: { 'content-type': 'application/json', origin }, body: JSON.stringify({ sessionId: 'sess-d', title: 'Drafts ' + i }) });

const b = await chromium.launch();
try {
  const page = await b.newPage({ viewport: { width: 1440, height: 900 } });
  const errors: string[] = []; page.on('pageerror', e => errors.push(e.message));
  await page.goto(origin + '/conversations/sess-d');
  const field = async (sel: string, text: string, label: string, p: Page) => {
    const box = p.locator(sel).first();
    await box.waitFor({ timeout: 30000 });
    await box.click(); await p.keyboard.type(text);
    await box.evaluate(n => { (window as unknown as { probe: Element }).probe = n; });
    let reread = 0;
    for (let i = 0; i < 6; i++) { const r = await rename(label.length * 10 + i); reread += r.ok ? 1 : 0; await p.waitForTimeout(1000); }
    const st = await box.evaluate(n => ({ same: (window as unknown as { probe: Element }).probe === n, focus: document.activeElement === n, value: (n as HTMLInputElement).value }));
    const last = 'Drafts ' + (label.length * 10 + 5), shown = await p.getByText(last, { exact: true }).count();
    claim(reread === 6 && shown > 0, `${label}: 6 live changes reached the page (${reread} renamed, "${last}" shown ${shown}x)`);
    claim(st.same && st.focus && st.value === text, `${label}: same node, focus and text through them (${JSON.stringify(st)})`);
  };
  await field('.session-view input.qcard-answer', 'answer draft', 'answer', page);
  await field('[data-ui=composer] textarea', 'composer draft', 'composer', page);
  await page.waitForTimeout(500);
  await page.reload();
  await page.locator('[data-ui=composer] textarea').first().waitFor();
  await page.waitForTimeout(1500);
  claim(await page.locator('[data-ui=composer] textarea').first().inputValue() === 'composer draft', 'reload keeps the composer draft');
  claim(await page.locator('.session-view input.qcard-answer').first().inputValue() === 'answer draft', 'reload keeps the answer draft');
  claim(errors.length === 0, `no page errors (${errors.join('; ')})`);
} finally { await b.close(); await close(); }
if (failures.length) { console.log(`${failures.length} failed`); process.exit(1); }
