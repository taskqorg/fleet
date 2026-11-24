// The session view in the Conversations work area (docs/design-system/pages/session.md, OR-51, OR-54): the head shows the
// session's name first, then its state, with no model badge, cost or ids, and Rename stores a new name; long result names
// stay inside the transcript column (an ellipsis, the full name in the tooltip); code copy shows only on hover or focus.
// Called by conversations.check.ts.
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
    const title = p.locator('.sv-head .sv-name');
    check(await until(async () => (await title.textContent()) === TITLE), `${width}: the head shows the session name`);
    // The name, then the state; no code, model badge or cost in the head (OR-54).
    const order = await p.locator('.sv-head').evaluate(h => [...h.children].map(c => c.classList.contains('sv-name') ? 'name' : c.matches('[data-ui="state-dot"]') ? 'state' : c.matches('[data-ui="entity-code"], .agent-mark, [data-ui="resource-bar"]') ? 'extra' : '').filter(Boolean).join(' '));
    check(order === 'name state', `${width}: head order name, state (${order})`);
    const overflow = await p.locator('.sv-head').evaluate(h => h.scrollWidth - h.clientWidth);
    check(overflow <= 1, `${width}: the head fits its width (${overflow} px over)`);
    if (width === 390) {
      const close = (await p.locator('.sv-head').getByRole('button', { name: 'Close', exact: true }).boundingBox())!;
      check(close.height === close.width && close.height === 28, '390: a narrow mouse window keeps compact square icon buttons');
      check(await p.locator('.sv-hits').count() === 0, '390: inactive search stepping controls use no space');
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
    // Light chat (OR-54): a message is its words, with no service row of model, time and actions.
    check(await p.locator('.th-text .th-service, .th-text .agent-mark').count() === 0, `${width}: messages carry no model badge or service row`);
    // Code copy: hidden until its block is hovered or the copy is focused.
    const reply = p.locator('.th-text.agent').first(), copy = reply.locator('.code-copy');
    await p.mouse.move(0, 0);
    const shown = () => copy.evaluate(e => getComputedStyle(e).opacity);
    check(await shown() === '0', `${width}: code copy hidden while its block is not hovered`);
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
    await reply.locator('.code').hover();
    check(await until(async () => await shown() === '1'), `${width}: hovering the code block shows its copy`);
    check(Math.round((await reply.boundingBox())!.height) === Math.round(box.height), `${width}: showing the copy moves nothing`);
    if (shots) {
      await p.screenshot({ path: `${shots}/conv-detail-${width}.png` });
      if (width === 1440) await p.locator('.detail.session-view').screenshot({ path: `${shots}/conv-work-area.png` });
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
    await copy.getByRole('button').first().focus();
    check(await shown() === '1', `${width}: focusing the copy shows it`);
    check(!errors.length, `${width}: no page errors${errors.length ? ': ' + errors.join('; ') : ''}`);
    await context.close();
  }
  // Touch: always visible.
  {
    const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 390, height: 900 }, hasTouch: true, isMobile: true }), p = await context.newPage();
    await p.goto(`${origin}/conversations/${S}`);
    const copy = p.locator('.th-text.agent .code-copy').first();
    check(await until(async () => await copy.count() === 1 && await copy.evaluate(e => getComputedStyle(e).opacity) === '1'), 'touch: code copy always shows');
    const shell = (await p.locator('.shell-h').boundingBox())!, search = (await p.locator('.shell-search').boundingBox())!;
    check(search.width === 44 && search.height === 44 && search.y >= shell.y && search.y + search.height <= shell.y + shell.height,
      'touch: the full 44 px search target fits inside the shell header');
    const bar = (await p.locator('[data-ui=bottom-bar]').boundingBox())!, tab = (await p.locator('[data-ui=bottom-bar] .tab-i').first().boundingBox())!;
    check(tab.width >= 44 && tab.height >= 44 && tab.y >= bar.y && tab.y + tab.height <= bar.y + bar.height,
      'touch: the bottom bar contains the full navigation target');
    const detail = (await p.locator('.detail').boundingBox())!, composer = (await p.locator('.composer').boundingBox())!;
    const heading = (await p.locator('.sv-head').boundingBox())!;
    const message = (await p.locator('.th-text.agent .th-main').first().boundingBox())!;
    check(heading.x === composer.x && heading.x === message.x && Math.round(composer.x - detail.x) === 8,
      `touch: the head, transcript and composer share one 8 px inset (${heading.x}, ${message.x}, ${composer.x}, ${detail.x})`);
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
  check(await until(async () => (await p.locator('.sv-head .sv-name').textContent()) === 'MCP startup exits'), 'Rename shows the new name in the head');
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
