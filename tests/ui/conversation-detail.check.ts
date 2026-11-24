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
  const names = ['mcp-lifecycle-5eb2ce91f04a4d3c8e1b7a2f6d9c0e13-session-transcript-with-an-extended-name-for-narrow-window-review.jsonl', 'mcp-lifecycle-5eb2ce91f04a4d3c8e1b7a2f6d9c0e13-stderr-and-stdout.log', 'summary.md'];
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
    // Title first, then the code (the profile chip lives in the composer below).
    const order = await p.locator('.detail-h').evaluate(h => [...h.children].map(c => c.classList.contains('conv-title') ? 'title' : c.matches('[data-ui="entity-code"], .ec-reference') ? 'code' : '').filter(Boolean).join(' '));
    check(order === 'title code', `${width}: header order title, code (${order})`);
    const overflow = await p.locator('.detail-h').evaluate(h => h.scrollWidth - h.clientWidth);
    check(overflow <= 1, `${width}: the header fits its width (${overflow} px over)`);
    if (width === 390) {
      const close = (await p.locator('.detail-h').getByRole('button', { name: 'Close', exact: true }).boundingBox())!;
      check(close.height === close.width && close.height === 28, '390: a narrow mouse window keeps compact square icon buttons');
      check(await p.locator('.conv-search-results').isHidden(), '390: inactive search stepping controls use no space');
    }
    // Result names: inside the row, each with its full name in the tooltip.
    const row = p.locator('.th-result');
    check(await until(async () => await row.count() === 1), `${width}: the result row shows`);
    const out = await row.evaluate(r => { const right = r.getBoundingClientRect().right; return [...r.querySelectorAll('a')].map(a => Math.round(a.getBoundingClientRect().right - right)); });
    check(out.length === 3 && out.every(d => d <= 0), `${width}: result names stay inside the column (${out.join(', ')})`);
    const resultTops = await row.getByRole('listitem').evaluateAll(items => items.map(item => item.getBoundingClientRect().top));
    check(resultTops.length === 3 && resultTops.every((top, i) => i === 0 || top > resultTops[i - 1]),
      `${width}: each result has a separate line`);
    const tips = await row.locator('a').evaluateAll(as => as.map(a => a.getAttribute('data-tip') ?? ''));
    check(names.every((n, i) => tips[i]?.startsWith(n + ' · ')), `${width}: each result name has its full name in the tooltip`);
    const clipped = await row.locator('a').first().evaluate(a => a.scrollWidth > a.clientWidth);
    if (width === 390) check(clipped, '390: a long result name ends in an ellipsis');
    const slots = await p.locator('.th-service').evaluateAll(rows => rows.map(row => ({
      start: Math.round(row.getBoundingClientRect().left),
      time: Math.round(row.querySelector('.th-time')!.getBoundingClientRect().left),
      action: Math.round(row.querySelector('.th-action')!.getBoundingClientRect().left),
    })));
    check(slots.length >= 2 && new Set(slots.map(slot => JSON.stringify(slot))).size === 1 &&
      slots.every(slot => slot.start === slot.action && slot.time > slot.action), `${width}: action and time share fixed slots at the left edge`);
    const grouped = await p.locator('.th-text.agent').first().evaluate(row => {
      const meta = row.querySelector('.th-service')!, content = row.querySelector('.th-main')!;
      return !!meta.querySelector('.agent-mark') && meta.getBoundingClientRect().top >= content.getBoundingClientRect().bottom;
    });
    check(grouped, `${width}: model, time and action are grouped below the message`);
    // Copy tools: hidden until the row is hovered or a tool is focused.
    const reply = p.locator('.th-text.agent').first(), copy = reply.locator('.th-copy');
    await p.mouse.move(0, 0);
    const shown = () => copy.evaluate(e => getComputedStyle(e).opacity);
    check(await shown() === '0', `${width}: message copy hidden while the row is not hovered`);
    check(await reply.locator('.code-copy').evaluate(e => getComputedStyle(e).opacity) === '0', `${width}: code copy hidden while its block is not hovered`);
    const codeGeometry = await reply.locator('.code').evaluate(block => ({
      content: block.querySelector('pre')!.getBoundingClientRect().right,
      copy: block.querySelector('.code-copy')!.getBoundingClientRect().left,
      width: block.getBoundingClientRect().width,
      available: block.parentElement!.getBoundingClientRect().width,
    }));
    check(codeGeometry.copy >= codeGeometry.content && codeGeometry.width <= codeGeometry.available,
      `${width}: copy is to the right of code within the column`);
    if (width === 1440) check(codeGeometry.width < codeGeometry.available, '1440: short code fits its contents');
    const box = (await reply.boundingBox())!;
    await reply.hover();
    check(await until(async () => await shown() === '1'), `${width}: hovering the row shows its copy`);
    check(Math.round((await reply.boundingBox())!.height) === Math.round(box.height), `${width}: showing the copy moves nothing`);
    if (shots) {
      await p.screenshot({ path: `${shots}/conv-detail-${width}.png` });
      if (width === 1440) await p.locator('.detail.conversation').screenshot({ path: `${shots}/conv-work-area.png` });
    }
    // The same long title keeps its natural tooltip width next to either viewport edge.
    const titleText = 'A long result title with enough words to fill a normal tooltip on a narrow window';
    await p.evaluate(text => {
      for (const edge of ['left', 'right']) {
        const anchor = document.createElement('button');
        anchor.textContent = edge;
        anchor.dataset.tooltipProbe = edge;
        anchor.dataset.tip = text;
        Object.assign(anchor.style, { position: 'fixed', top: '50%', [edge]: '0', zIndex: '9' });
        document.body.append(anchor);
      }
    }, titleText);
    const widths: number[] = [];
    for (const edge of ['left', 'right']) {
      const probe = p.locator(`[data-tooltip-probe="${edge}"]`);
      const before = await probe.boundingBox();
      await probe.hover();
      const tip = p.getByRole('tooltip').filter({ hasText: titleText });
      await tip.waitFor();
      const box = (await tip.boundingBox())!;
      widths.push(box.width);
      check(box.x >= 0 && box.x + box.width <= width && JSON.stringify(await probe.boundingBox()) === JSON.stringify(before),
        `${width}: tooltip fits at the ${edge} edge and keeps its anchor in place`);
    }
    check(widths[0] === widths[1] && widths[0] >= 200,
      `${width}: tooltip stays the same readable width at both edges (${widths.join(', ')} px)`);
    await p.locator('[data-tooltip-probe]').evaluateAll(probes => probes.forEach(probe => probe.remove()));
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
    const shell = (await p.locator('.shell-h').boundingBox())!, search = (await p.locator('.shell-search').boundingBox())!;
    check(search.width === 44 && search.height === 44 && search.y >= shell.y && search.y + search.height <= shell.y + shell.height,
      'touch: the full 44 px search target fits inside the shell header');
    const bar = (await p.locator('[data-ui=bottom-bar]').boundingBox())!, tab = (await p.locator('[data-ui=bottom-bar] .tab-i').first().boundingBox())!;
    check(tab.width >= 44 && tab.height >= 44 && tab.y >= bar.y && tab.y + tab.height <= bar.y + bar.height,
      'touch: the bottom bar contains the full navigation target');
    const detail = (await p.locator('.detail').boundingBox())!, composer = (await p.locator('.composer').boundingBox())!;
    const heading = (await p.locator('.detail-h').boundingBox())!;
    const message = (await p.locator('.th-text.agent .th-main').first().boundingBox())!;
    check(heading.x === composer.x && heading.x === message.x && Math.round(composer.x - detail.x) === 8,
      'touch: the heading, transcript and composer share one 8 px inset');
    const cdp = await context.newCDPSession(p);
    await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 2 });
    await p.waitForFunction(() => window.visualViewport!.width < 280);
    await p.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const zoomHint = 'A long tooltip remains readable beside the edge of the zoomed visible viewport';
    const viewportWidth = await p.evaluate(text => {
      const width = window.visualViewport!.width;
      const probe = document.createElement('button');
      probe.dataset.tip = text;
      Object.assign(probe.style, { position: 'fixed', left: `${width - 40}px`, top: '100px' });
      document.body.append(probe);
      probe.dispatchEvent(new PointerEvent('pointerover', { pointerType: 'mouse', bubbles: true }));
      return width;
    }, zoomHint);
    const hint = p.getByRole('tooltip').filter({ hasText: zoomHint });
    await hint.waitFor();
    const zoomed = (await hint.boundingBox())!;
    check(zoomed.x >= 0 && zoomed.x + zoomed.width <= viewportWidth && zoomed.width >= viewportWidth - 9,
      'zoom: an edge tooltip wraps to the visible viewport width');
    await context.close();
  }
  // Rename from the menu: the header and the list show the new title.
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } }), p = await context.newPage();
  await p.goto(`${origin}/conversations/${S}`);
  await rename(p, 'MCP startup exits');
  check(await until(async () => (await p.locator('.detail-h .conv-title').textContent()) === 'MCP startup exits'), 'Rename shows the new title in the header');
  // The list row follows the change feed; a fresh read of the list shows the stored title either way.
  const listed = () => until(async () => await p.locator('.vl-row .row-link', { hasText: 'MCP startup exits' }).count() === 1);
  check(await listed() || (await p.reload(), await listed()), 'Rename shows in the list row');
  const stored = seed.rows(`SELECT title, title_by AS by FROM session WHERE id='${S}'`)[0];
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
