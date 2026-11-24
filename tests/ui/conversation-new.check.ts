// Start a conversation in the built app (docs/designs/fleet-conversations.md "Start a conversation", owner 2026-11-17;
// docs/design-system/pages/conversations.md). Called by conversations.check.ts; also runs alone:
//   pnpm run build && node tests/ui/conversation-new.check.ts
// Requirement: the New conversation button is in the shell header next to the title; it opens /conversations/new in the work
// area with the list kept, and Back returns; a project is picked from chips; a preset picks the profile, access and branch the
// chips under the composer show; Send starts the conversation with session_start, also in a project without roles (cs).
// Risk: the form returns to the list column, a preset shows raw ids or picks nothing, or a project without roles cannot start.
// With CONV_NEW_SHOTS=<dir> it saves dark screenshots at 1440x900 and 390 px: the new page with presets (new-presets), the
// header button (header-button) and the conversation just started in cs (cs-started).
import assert from 'node:assert/strict';
import type { Browser } from 'playwright';

const until = async (f: () => Promise<boolean>, ms = 8000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 50)); } return false; };

export async function checkNewConversation(browser: Browser, origin: string) {
  const call = async (name: string, body: object) => {
    const r = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    const d: any = await r.json(); assert.equal(r.status, 200, JSON.stringify(d)); return d;
  };
  const shots = process.env.CONV_NEW_SHOTS;
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ colorScheme: 'dark', viewport: { width, height: width > 500 ? 900 : 844 } }), p = await context.newPage();
    const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
    const shot = async (name: string, clip?: boolean) => { if (shots) await p.screenshot({ path: `${shots}/${name}-${width}.png`, ...(clip ? { clip: { x: 0, y: 0, width, height: 120 } } : {}) }); };
    try {
      await p.goto(origin + '/conversations');
      const head = p.locator('[data-ui=shell-header]'), button = head.getByRole('button', { name: 'New conversation' });
      await button.waitFor();
      // Next to the section title: right after it, before the status slot and the focus chip.
      assert.ok(await head.evaluate(h => { const t = h.querySelector('.shell-title')!, b = h.querySelector('.shell-actions')!; return t.nextElementSibling === b && !!b.querySelector('button'); }), 'the button sits right after the section title');
      assert.equal(await p.locator('.split-list .page-h button', { hasText: 'New conversation' }).count(), 0, 'no button left in the list column');
      await shot('header-button', true);
      await button.click();
      await p.waitForURL(u => new URL(u).pathname === '/conversations/new');
      const form = p.locator('[data-ui=detail][aria-label="New conversation"]');
      await form.waitFor();
      assert.ok(await p.locator('.split-list [data-ui=list-view], .split-list .list-view').count() > 0, 'the list stays in the control column');
      assert.equal(await p.locator('.split-list [aria-label="New conversation"]').count(), 0, 'the form is not in the list column');
      // Project: chips, "No project" is one of them (no separate Projectless button).
      const projects = form.getByRole('group', { name: 'Project' });
      await projects.getByRole('button', { name: 'No project' }).waitFor();
      assert.equal(await projects.getByRole('button', { name: 'No project' }).count(), 1, '"No project" is one project choice');
      assert.equal(await p.getByRole('button', { name: 'Projectless conversation' }).count(), 0);
      await projects.getByRole('button', { name: 'cyprus.bz' }).click();
      const cards = form.locator('[data-ui=preset-pick] .preset-card'), facts = form.locator('[data-ui=composer] .composer-row');
      assert.ok(await until(async () => await cards.count() === 5), 'cs (no roles) shows the five system presets');
      assert.deepEqual(await cards.locator('b').allTextContents(), ['Discuss', 'Design', 'Fix / build', 'Review', 'Investigate a problem']);
      assert.equal(await cards.first().getAttribute('aria-pressed'), 'true', 'the first preset is picked');
      const fact = (k: string) => facts.locator(`[data-fact=${k}]`), said = async (k: string) => (await fact(k).locator(':scope > span:last-child').textContent())?.trim();
      assert.ok(await until(async () => await said('access') === 'Code read only'), 'Discuss is read only');
      assert.equal(await said('branch'), 'No branch');
      assert.match((await cards.first().locator('.pc-f').textContent())!, /Code read only · No branch/, 'the card says what it sets');
      assert.ok(!/coding-worker|claude-reviewer/.test((await cards.allTextContents()).join(' ')), 'cards show what a preset sets, not raw profile ids');
      await shot('new-presets');
      // A preset picks the profile: Fix / build is the coding worker (a system profile, cs maps no role), with its branch.
      await cards.filter({ hasText: 'Fix / build' }).click();
      assert.ok(await until(async () => (await fact('agent').getAttribute('data-tip')) === 'Profile coding-worker, a system profile'), 'Fix / build picks the coding worker');
      assert.equal(await said('access'), 'Can edit');
      assert.equal(await said('branch'), 'fleet-test/new');
      assert.equal(await fact('branch').evaluate(e => e.tagName), 'SPAN', 'edits always get a branch: the chip does not flip');
      await fact('access').click();
      assert.equal(await said('access'), 'Code read only', 'the access chip flips');
      await fact('branch').click();
      assert.equal(await said('branch'), 'No branch', 'read only may go without a branch');
      await fact('access').click();
      assert.deepEqual([await said('access'), await said('branch')], ['Can edit', 'fleet-test/new'], 'back to edits brings the branch back');
      // Start: session_start in cs, then the conversation opens; Back returns to the new page.
      const text = `Fix the hero on ${width}`;
      await form.locator('[data-ui=composer] textarea').fill(text);
      await form.locator('[data-ui=composer] textarea').press('Enter');
      await p.waitForURL(u => /^\/conversations\/[0-9a-f-]{36}$/.test(new URL(u).pathname), { timeout: 10000 });
      const sessionId = new URL(p.url()).pathname.split('/').pop()!;
      const s = await call('session_get', { sessionId });
      assert.deepEqual([s.projectId, s.agentConfig?.id, s.preset, s.access, s.branch], ['cs', 'coding-worker', 'fix', 'write', true], 'the conversation starts with the preset');
      await p.locator('[data-ui=detail].session-view').waitFor();
      if (width > 500) assert.ok(await until(async () => await p.locator('.split-list .vl-row', { hasText: text }).count() === 1, 15000), 'the new conversation shows in the list: ' + await p.locator('.split-list').innerText());
      await shot('cs-started');
      await p.goBack(); await p.waitForURL(u => new URL(u).pathname === '/conversations/new');
      await form.waitFor();
      // The old link opens the page with its project; a project id in ?in= is picked.
      await p.goto(origin + '/conversations?new=desiign');
      await p.waitForURL(u => new URL(u).pathname === '/conversations/new' && new URL(u).searchParams.get('in') === 'desiign');
      assert.ok(await until(async () => await projects.getByRole('button', { name: 'desiign' }).getAttribute('aria-pressed') === 'true'), 'the linked project is picked');
      assert.ok(await until(async () => (await fact('agent').getAttribute('data-tip') ?? '').includes("the project's pm role")), 'desiign maps its own pm role');
      assert.ok(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'nothing scrolls sideways');
      assert.deepEqual(errors, []);
    } finally { await context.close(); }
  }
  console.log('ok New conversation: header button next to the title, /conversations/new in the work area with Back, project chips with No project, presets pick profile, access and branch, start in cs without roles');
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const { chromium } = await import('playwright');
  const { checkServer } = await import('./seed.ts');
  const { origin, close } = await checkServer();
  const b = await chromium.launch();
  try { await checkNewConversation(b, origin); } finally { await b.close(); await close(); }
}
