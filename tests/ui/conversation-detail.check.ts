// Conversation detail (docs/designs/fleet-conversations.md, Titles; docs/design-system/controls/transcript-viewer.md): the
// header shows the conversation's title first, then its code and profile muted, and Rename stores a new one; long result
// names stay inside the transcript column (an ellipsis, the full name in the tooltip); copy tools show only on hover or
// focus, and on a touch screen always. Called by conversations.check.ts.
// With CONV_SHOTS=<dir> it also saves dark screenshots of the detail at 1440 and 390 px (conv-detail-1440.png,
// conv-detail-390.png) and of the list with titles (conv-titles.png).
import type { Browser, Page } from 'playwright';
import type { seeder } from './seed.ts';

const until = async (f: () => Promise<boolean>, ms = 8000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 50)); } return false; };

export async function checkConversationDetail(browser: Browser, origin: string, seed: ReturnType<typeof seeder>, check: (ok: boolean, what: string) => void) {
  const S = 'cd-titled', TITLE = 'Trace the MCP lifecycle failures';
  const names = ['mcp-lifecycle-5eb2ce91f04a4d3c8e1b7a2f6d9c0e13-session-transcript.jsonl', 'mcp-lifecycle-5eb2ce91f04a4d3c8e1b7a2f6d9c0e13-stderr-and-stdout.log', 'summary.md'];
  seed.bulk(() => {
    seed.put('session', { id: S, createdAt: Date.now() - 600e3, title: TITLE, titleBy: 'auto', agentConfig: { id: 'claude-worker', harness: 'claude' } });
    seed.put('message', { id: 'cd-m', sessionId: S, executionId: 'cd-x', text: '# STANDING RULES FOR EVERY TASK\nFind why the MCP servers exit during startup' });
    seed.put('execution', { id: 'cd-x', executionId: 'cd-x', sessionId: S, hostId: 'pds18', state: 'completed', startedAt: Date.now() - 500e3, finishedAt: Date.now() - 400e3, model: 'claude-opus-5-5', effort: 'high', agentConfig: { harness: 'claude' } });
    seed.put('event', { id: 'cd-e', sessionId: S, executionId: 'cd-x', type: 'item.completed', seq: 0, data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: 'The servers exit on a closed stdin.\n\n```\nfleet-executor mcp connection github\n```' }] } } });
    seed.put('event', { id: 'cd-end', sessionId: S, executionId: 'cd-x', type: 'execution.ended', seq: 1, data: {} });
    names.forEach((name, i) => seed.artifact({ id: 'cd-a' + i, executionId: 'cd-x', name, mediaType: name.endsWith('.md') ? 'text/markdown' : 'text/plain', bytes: Buffer.from('x'.repeat(100 + i)) }));
  });
  const shots = process.env.CONV_SHOTS;
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ colorScheme: 'dark', viewport: { width, height: 900 } }), p = await context.newPage();
    const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
    await p.goto(`${origin}/conversations/${S}`);
    const title = p.locator('.detail-h .conv-title');
    check(await until(async () => (await title.textContent()) === TITLE), `${width}: the header shows the conversation title`);
    // Title first, then the code and the profile, muted (the code is a copy icon: a conversation has no human code here).
    const order = await p.locator('.detail-h').evaluate(h => [...h.children].map(c => c.classList.contains('conv-title') ? 'title' : c.matches('[data-ui="entity-code"]') ? 'code' : c.classList.contains('conv-profile') ? 'profile' : '').filter(Boolean).join(' '));
    check(order === 'title code profile', `${width}: header order title, code, profile (${order})`);
    const overflow = await p.locator('.detail-h').evaluate(h => h.scrollWidth - h.clientWidth);
    check(overflow <= 1, `${width}: the header fits its width (${overflow} px over)`);
    // Result names: inside the row, each with its full name in the tooltip.
    const row = p.locator('.th-result');
    check(await until(async () => await row.count() === 1), `${width}: the result row shows`);
    const out = await row.evaluate(r => { const right = r.getBoundingClientRect().right; return [...r.querySelectorAll('a')].map(a => Math.round(a.getBoundingClientRect().right - right)); });
    check(out.length === 3 && out.every(d => d <= 0), `${width}: result names stay inside the column (${out.join(', ')})`);
    const tips = await row.locator('a').evaluateAll(as => as.map(a => a.getAttribute('data-tip') ?? ''));
    check(names.every((n, i) => tips[i]?.startsWith(n + ' · ')), `${width}: each result name has its full name in the tooltip`);
    const clipped = await row.locator('a').first().evaluate(a => a.scrollWidth > a.clientWidth);
    if (width === 390) check(clipped, '390: a long result name ends in an ellipsis');
    // Copy tools: hidden until the row is hovered or a tool is focused.
    const reply = p.locator('.th-text.agent').first(), copy = reply.locator('.th-copy');
    await p.mouse.move(0, 0);
    const shown = () => copy.evaluate(e => getComputedStyle(e).opacity);
    check(await shown() === '0', `${width}: message copy hidden while the row is not hovered`);
    check(await reply.locator('.code-copy').evaluate(e => getComputedStyle(e).opacity) === '0', `${width}: code copy hidden while its block is not hovered`);
    const box = (await reply.boundingBox())!;
    await reply.hover();
    check(await until(async () => await shown() === '1'), `${width}: hovering the row shows its copy`);
    check(Math.round((await reply.boundingBox())!.height) === Math.round(box.height), `${width}: showing the copy moves nothing`);
    if (shots) await p.screenshot({ path: `${shots}/conv-detail-${width}.png` });
    await p.mouse.move(0, 0);
    await p.keyboard.press('Escape');
    await reply.getByRole('button', { name: 'Copy message' }).focus();
    check(await shown() === '1', `${width}: focusing the copy shows it`);
    check(!errors.length, `${width}: no page errors${errors.length ? ': ' + errors.join('; ') : ''}`);
    await context.close();
  }
  // Touch: always visible.
  {
    const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 390, height: 900 }, hasTouch: true, isMobile: true }), p = await context.newPage();
    await p.goto(`${origin}/conversations/${S}`);
    const copy = p.locator('.th-text.agent .th-copy').first();
    check(await until(async () => await copy.count() === 1 && await copy.evaluate(e => getComputedStyle(e).opacity) === '1'), 'touch: message copy always shows');
    await context.close();
  }
  // Rename from the menu: the header and the list show the new title.
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } }), p = await context.newPage();
  await p.goto(`${origin}/conversations/${S}`);
  await rename(p, 'MCP startup exits');
  check(await until(async () => (await p.locator('.detail-h .conv-title').textContent()) === 'MCP startup exits'), 'Rename shows the new title in the header');
  check(await until(async () => await p.locator('.vl-row .row-link', { hasText: 'MCP startup exits' }).count() === 1), 'Rename updates the list row');
  const stored = seed.rows(`SELECT data->>'title' AS title, data->>'titleBy' AS by FROM document WHERE id='${S}'`)[0];
  check(stored?.title === 'MCP startup exits' && stored?.by === 'owner@trial.test', `Rename stores the title by the person (${JSON.stringify(stored)})`);
  if (shots) { await p.goto(`${origin}/conversations`); await p.waitForTimeout(500); await p.screenshot({ path: `${shots}/conv-titles.png` }); }
  await context.close();
}

async function rename(p: Page, title: string) {
  await p.getByRole('button', { name: 'Actions' }).click();
  await p.getByRole('menuitem', { name: 'Rename' }).click();
  const field = p.getByRole('textbox', { name: 'Title' });
  await field.fill(title);
  await p.getByRole('button', { name: 'Save', exact: true }).click();
}
