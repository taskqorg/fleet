// Home check (docs/design-system/pages/home.md): starts the coordinator with fixtures (tests/ui/seed.ts), opens /home in
// Chromium at 1440 and 390 px in the dark theme and saves the canvas states (normal, picker, phone, quiet) as screenshots.
// Asserts the order of the groups, the question in place and the quiet summary. Not part of CI; never fails the build.
// Run: pnpm run build && node tests/ui/home.check.ts [--shots dir]
import { mkdirSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { checkServer } from './seed.ts';

const shots = process.argv.includes('--shots') ? process.argv[process.argv.indexOf('--shots') + 1] : 'design-report/home';
mkdirSync(shots, { recursive: true });
const { origin, seed, close } = await checkServer();
const call = async (name: string, body: object) => {
  const r = await fetch(`${origin}/api/ops/${name}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
  assert.equal(r.status, 200, `${name} (${r.status}): ${await r.clone().text()}`);
  return r.json() as Promise<Record<string, any>>;
};

const now = Date.now(), min = 60_000;
// A session with one agent message (its "now" line) and a run in the given state.
const session = (id: string, v: { project: string; title: string; profile: string; harness?: string; state: string; now: string; ago: number }) => {
  seed.put('session', { id, projectId: v.project, title: v.title, createdAt: now - v.ago - 5 * min, agentConfig: { id: v.profile, harness: v.harness ?? 'codex' } });
  seed.execution({ id: 'x-' + id, kind: 'agent_run', sessionId: id, projectId: v.project, hostId: 'pds18-native', harness: v.harness ?? 'codex', state: v.state, startedAt: now - v.ago });
  seed.put('event', { id: 'e-' + id, sessionId: id, executionId: 'x-' + id, projectId: v.project, type: 'item.completed', timestamp: now - v.ago,
    data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: v.now }] } } });
};
seed.bulk(() => {
  session('s-design', { project: 'hello-world', title: 'Home screen states', profile: 'pm', state: 'waiting', now: 'Showed the Home screen in three states; asks which card layout to keep.', ago: 1 * min });
  session('s-fleet', { project: 'hello-world', title: 'Fix receipt factory', profile: 'coding-worker', state: 'running', now: 'Swarm of 3 on T-FLT-51: review running.', ago: 12 * min });
  session('s-lab', { project: 'hello-world-alt', title: 'Compare codeMode variants', profile: 'coding-worker', harness: 'claude', state: 'running', now: 'Step 3: comparing codeMode on 3 variants.', ago: 8 * min });
  session('s-queued', { project: 'hello-world', title: 'Write release notes', profile: 'coding-worker', state: 'preparing', now: 'Waits for a free slot.', ago: 2 * min });
  session('s-failed', { project: 'hello-world', title: 'Rollout of PR 826', profile: 'coding-worker', state: 'failed', now: 'Rollout of PR 826 failed on windows.', ago: 9 * min });
});
seed.question({ kind: 'operator_decision', sessionId: 's-design', text: 'Keep the compact card or the wide card with progress?', options: ['Compact', 'Wide'], askedBy: 'agent' });
seed.sql(`UPDATE question SET project_id='hello-world' WHERE session_id='s-design'`);
for (const [name, purpose, project] of [['Release manager', 'delivery, rollouts', 'hello-world'], ['Fleet setup', 'settings, hosts, accounts', 'hello-world'], ['Spend auditor', 'costs, limits', 'hello-world-alt']])
  await call('bot_settings_save', { botId: randomUUID(), projectId: project, baseRevision: 0, settings: { name, purpose, profileId: 'pm', instructions: 'Watch', soul: '', scheduleTime: '', scheduleZone: '', wakeOnNeedsYou: false } });

const b = await chromium.launch();
const report: string[] = [];
try {
  const ctx = await b.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
  const page = await ctx.newPage();
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message)); page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  await page.goto(origin + '/');
  await page.waitForSelector('[data-ui=session-card]:not(.skeleton)', { timeout: 15000 }).catch(async e => { await page.screenshot({ path: `${shots}/home-fail.png` }); console.log(errors); throw e; });
  assert.equal(new URL(page.url()).pathname, '/home', '/ opens Home');
  const groups = await page.locator('.home-group').evaluateAll(els => els.map(e => e.getAttribute('aria-label')));
  assert.deepEqual(groups, ['Waiting for you', 'Failed', 'Working', 'Queued', 'Asleep'], 'groups in attention order');
  await page.getByRole('group', { name: 'Keep the compact card or the wide card with progress?' }).waitFor();
  await page.locator('[data-ui=bot-tile]').first().waitFor();
  await page.screenshot({ path: `${shots}/home-1440-normal.png` });
  report.push(`normal: groups ${groups.join(' · ')}`);

  await page.locator('.target-chip').click();
  await page.locator('.target-panel').waitFor();
  await page.screenshot({ path: `${shots}/home-1440-picker.png` });
  const pm = await page.locator('.pick-row', { hasText: 'PM · hello-world' }).first().innerText();
  assert.match(pm, /Waiting for you/, 'the project PM shows its open session state');
  report.push(`picker: ${pm.replace(/\s+/g, ' ')}`);
  await page.keyboard.press('Escape');

  // The question is answered in place: the card shows the answered line.
  await page.getByRole('button', { name: 'Compact' }).click();
  await page.locator('.qcard.answered').waitFor();
  report.push('answered in place');

  // A card opens the session at /home/<code>.
  await page.locator('[data-ui=session-card]', { hasText: 'Fix receipt factory' }).click();
  await page.waitForURL(/\/home\/[^/]+$/);
  report.push(`card opens ${new URL(page.url()).pathname}`);

  // Quiet: nothing waits and nothing failed.
  seed.sql(`UPDATE question SET state='answered', answer='Compact', answered_at=now() WHERE state='open'; UPDATE execution SET state='cancelled' WHERE id='x-s-failed'; UPDATE execution SET state='running' WHERE id='x-s-design'`);
  await page.goto(origin + '/home');
  await page.locator('[data-ui=quiet-summary] .qsum-title').waitFor({ timeout: 10000 }).catch(async e => { await page.screenshot({ path: `${shots}/home-fail.png` }); console.log((await call('sessions_list', { limit: 20 })).sessions.map((x: any) => x.id + ' ' + x.state), (await call('questions_list', { state: 'open' })).questions.length); throw e; });
  await page.screenshot({ path: `${shots}/home-1440-quiet.png` });
  report.push('quiet summary shown');

  const phone = await b.newContext({ viewport: { width: 390, height: 844 }, colorScheme: 'dark', isMobile: true, hasTouch: true });
  const p2 = await phone.newPage();
  await p2.goto(origin + '/home');
  await p2.waitForSelector('[data-ui=session-card]:not(.skeleton)');
  const wide = await p2.evaluate(() => document.documentElement.scrollWidth > innerWidth);
  assert.equal(wide, false, 'no sideways scroll at 390 px');
  await p2.screenshot({ path: `${shots}/home-390.png` });
  report.push(`phone: no sideways scroll; console errors: ${errors.length ? errors.join(' | ') : 'none'}`);
  console.log(report.map(l => '  ' + l).join('\n'));
} finally { await b.close(); await close(); }
