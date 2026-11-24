// FLT-10, Conversations CUJ 4. A drag must cut both endpoint replies by source offset, in either direction.
// The server must supply intervening replies even when the client replaces its page during the drag.
import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { seeder } from './seed.ts';
import type { Coordinator } from '../harness.ts';

export const selectionSession = 'thread-selection';
export const replyCount = 524;
export const firstPrefix = '> **start😀 ';
export const firstSuffix = 'KEEP first**';
export const localPrefix = 'Skip 😀 ';
export const localSuffix = 'KEEP local';
export const lastPrefix = 'end 😀 [repeated](https://example.test/repeated) KEEP last';
export const lastSuffix = ' OUTSIDE';
const projectId = 'hello-world';
const timestamp = Date.UTC(2020, 0, 15, 12);

export function selectionReply(i: number) {
  if (i === 0) return firstPrefix + firstSuffix;
  if (i === replyCount - 3) return localPrefix + localSuffix;
  if (i === replyCount - 1) return lastPrefix + lastSuffix;
  return `Fixture reply ${i}.`;
}

export function seedThreadSelection(seed: ReturnType<typeof seeder>) {
  const sessionId = selectionSession, executionId = 'thread-selection-run';
  seed.bulk(() => {
    seed.put('session', { id: sessionId, projectId, title: 'Range export fixture', createdAt: Date.now() });
    seed.execution({ id: executionId, sessionId, projectId, state: 'completed', harness: 'codex' });
    for (let i = -1; i <= replyCount; i++) {
      seed.put('event', { id: `range-reply-${i}`, sessionId, executionId, projectId, type: 'item.completed', seq: i + 1, timestamp,
        data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: selectionReply(i) }] } } });
    }
    // Thread export uses the event's stored update time in its author line.
    seed.sql("UPDATE event SET updated_at = at WHERE session_id = 'thread-selection'");
  });
  return seed.rows("SELECT seq || ':0' AS key FROM event WHERE session_id = 'thread-selection' ORDER BY event_seq")
    .slice(1, -1).map(row => row.key as string);
}

export async function checkThreadSelectionAuthority(coordinator: Coordinator, keys: string[]) {
  const response = await fetch(coordinator.base + '/api/ops/thread_export', {
    method: 'POST',
    headers: { authorization: 'Bearer ' + coordinator.secret, 'x-fleet-person': 'outsider@trial.test', 'content-type': 'application/json' },
    body: JSON.stringify({ key: { session: selectionSession }, from: { key: keys[0], offset: Array.from(firstPrefix).length }, to: { key: keys.at(-1), offset: Array.from(lastPrefix).length } }),
  });
  assert.equal(response.status, 404, 'A range cannot reveal a thread outside the reader’s project grant');
  assert.doesNotMatch(await response.text(), /KEEP first|KEEP last|Fixture reply/);
}

export function expectedSelection(local: boolean, timeZone: string) {
  const time = new Intl.DateTimeFormat('en-GB', {
    day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone,
  }).format(timestamp).replace(',', '');
  const start = local ? replyCount - 3 : 0;
  return Array.from({ length: replyCount - start }, (_, index) => {
    const i = start + index;
    const body = i === start ? local ? localSuffix : firstSuffix : i === replyCount - 1 ? lastPrefix : selectionReply(i);
    return `**codex · ${time}**\n${body}\n\n`;
  }).join('');
}

/** Get the viewport position of a caret in the visible text, independent of the app's source-offset calculation. */
export function locateCaret({ text, offset }: { text: string; offset: number }) {
  const walker = document.createTreeWalker(document.querySelector('.transcript')!, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.textContent !== text) continue;
    const range = document.createRange();
    range.setStart(node, offset);
    range.collapse(true);
    const rect = range.getBoundingClientRect();
    return { x: rect.left, y: rect.top + rect.height / 2 };
  }
  throw Error(`Missing reply fragment: ${text}`);
}

export async function checkThreadSelection(page: Page, origin: string, keys: string[]) {
  await page.goto(`${origin}/conversations/${selectionSession}`);
  const first = page.getByText('start😀 KEEP first', { exact: true });
  const local = page.getByText(localPrefix + localSuffix, { exact: true });
  const middle = page.getByText(selectionReply(replyCount - 2), { exact: true });
  const last = page.locator('.th-text').filter({ hasText: 'KEEP last OUTSIDE' });
  await last.waitFor();
  await local.scrollIntoViewIfNeeded();
  await last.scrollIntoViewIfNeeded();

  const copy = async (localRange: boolean) => {
    const request = page.waitForRequest(r => new URL(r.url()).pathname === '/api/ops/thread_export');
    await page.keyboard.press('ControlOrMeta+c');
    const payload = (await request).postDataJSON();
    assert.deepEqual(payload.from, { key: keys[localRange ? replyCount - 3 : 0], offset: Array.from(localRange ? localPrefix : firstPrefix).length });
    assert.deepEqual(payload.to, { key: keys.at(-1), offset: Array.from(lastPrefix).length });
    assert.deepEqual(payload.key, { session: selectionSession });
    const expected = expectedSelection(localRange, payload.tz);
    await page.waitForFunction(async text => (await navigator.clipboard.readText()) === text, expected);
  };

  // Native same-row selection still copies just the visible text.
  const loc = (offset: number) => page.evaluate(locateCaret, { text: localPrefix + localSuffix, offset });
  const clickFrom = await loc(localPrefix.length), clickTo = await loc(localPrefix.length + 4);
  await page.mouse.move(clickFrom.x, clickFrom.y);
  await page.mouse.down();
  await page.mouse.move(clickTo.x, clickTo.y, { steps: 4 });
  await page.mouse.up();
  await page.keyboard.press('ControlOrMeta+c');
  await page.waitForFunction(async () => await navigator.clipboard.readText() === 'KEEP');
  await page.evaluate(() => getSelection()?.removeAllRanges());

  const localPoint = await loc(localPrefix.length);
  const lastPoint = await page.evaluate(locateCaret, { text: ' KEEP last OUTSIDE', offset: ' KEEP last'.length });
  await page.mouse.move(localPoint.x, localPoint.y);
  await page.mouse.down();
  await page.mouse.move(lastPoint.x, lastPoint.y, { steps: 8 });
  await page.mouse.up();
  assert.ok(await middle.locator('xpath=ancestor::*[contains(@class,"vl-row")]').evaluate(row => row.classList.contains('picked')));
  assert.deepEqual(await page.evaluate(() => [...CSS.highlights.get('thread-selection') ?? []].map(range =>
    (range as Range).toString()).sort()), [localSuffix, 'end 😀 ', 'repeated', ' KEEP last'].sort());
  await copy(true);

  // Delay the search jump until the reverse drag has its anchor. The old reply and the middle pages are absent
  // from the replacement window. Only the server can join the two partial replies across that gap.
  let releaseJump!: () => void;
  const jump = new Promise<void>(resolve => { releaseJump = resolve; });
  const entries = '**/api/ops/thread_entries';
  await page.route(entries, async route => {
    if (route.request().postDataJSON().around) await jump;
    await route.continue();
  });
  try {
    await page.getByRole('button', { name: 'Search thread', exact: true }).click();
    const search = page.getByRole('searchbox', { name: 'Search thread' });
    await search.fill('KEEP first');
    const jumping = page.waitForRequest(r => new URL(r.url()).pathname === '/api/ops/thread_entries' && !!r.postDataJSON().around);
    const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/ops/thread_entries' && !!r.request().postDataJSON().around);
    await search.press('Enter');
    await jumping;
    await page.getByRole('button', { name: 'Previous hit' }).waitFor();
    await page.evaluate(() => new Promise<void>(r => requestAnimationFrame(() => requestAnimationFrame(() => r()))));
    const anchor = await page.evaluate(locateCaret, { text: ' KEEP last OUTSIDE', offset: ' KEEP last'.length });
    await page.mouse.move(anchor.x, anchor.y);
    await page.mouse.down();
    releaseJump();
    const window = await (await response).json() as { entries: { key: string }[] };
    assert.ok(window.entries.every(entry => entry.key !== keys[250] && entry.key !== keys.at(-1)));
    await first.scrollIntoViewIfNeeded();
    assert.equal(await last.count(), 0);
    assert.equal(await page.getByText(selectionReply(250), { exact: true }).count(), 0);
    const focus = await page.evaluate(locateCaret, { text: 'start😀 KEEP first', offset: 'start😀 '.length });
    await page.mouse.move(focus.x, focus.y, { steps: 8 });
    await page.mouse.up();
    await copy(false);
  } finally {
    releaseJump();
    await page.unroute(entries);
  }
}
