// Composer attachments in the built app (docs/designs/fleet-conversations.md, Attachments; controls/composer.md).
// Called by conversations.check.ts; `node tests/ui/attachments.check.ts` is the focused entry (pnpm run build first).
// Requirement: a pasted screenshot and a file dropped over the conversation become chips with a thumbnail or icon, name,
// size and upload progress; Send waits for the uploads and message_send carries their artifact ids; the sent message in
// the transcript shows the image as a thumbnail and the file as a chip, each opening the result viewer; while the agent
// runs, the empty composer's one button is Stop and stops that execution.
// Risk: a pasted or dropped file is lost, a message goes out before its files are stored, the transcript hides what was
// sent, or Stop is missing while the agent runs.
// With COMPOSER_SHOTS=<dir> it saves dark screenshots at 1440 and 390 px.
import assert from 'node:assert/strict';
import { deflateSync } from 'node:zlib';
import { chromium, type Browser, type Page } from 'playwright';
import { checkServer, type seeder } from './seed.ts';

const until = async (f: () => Promise<boolean>, ms = 8000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 50)); } return false; };

/** A small solid PNG (one colour), so an agent and a reader can name its colour. */
export function solidPng(w: number, h: number, rgb: [number, number, number]): Buffer {
  const crcTable = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; return c >>> 0; });
  const crc = (b: Buffer) => { let c = 0xffffffff; for (const x of b) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
  const chunk = (type: string, data: Buffer) => { const t = Buffer.concat([Buffer.from(type), data]), len = Buffer.alloc(4), sum = Buffer.alloc(4); len.writeUInt32BE(data.length); sum.writeUInt32BE(crc(t)); return Buffer.concat([len, t, sum]); };
  const head = Buffer.alloc(13); head.writeUInt32BE(w, 0); head.writeUInt32BE(h, 4); head[8] = 8; head[9] = 2;
  const row = Buffer.concat([Buffer.from([0]), Buffer.from(Array.from({ length: w }, () => rgb).flat())]);
  return Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), chunk('IHDR', head), chunk('IDAT', deflateSync(Buffer.concat(Array.from({ length: h }, () => row)))), chunk('IEND', Buffer.alloc(0))]);
}

export async function checkAttachments(browser: Browser, origin: string, seed: ReturnType<typeof seeder>) {
  // The store the uploads go to (default location pds18): the seeder registers it with its first artifact.
  seed.artifact({ id: 'att-store-probe', executionId: 'att-none', name: 'probe.txt', mediaType: 'text/plain', bytes: Buffer.from('probe') });
  const S = 'att-session';
  seed.put('session', { id: S, createdAt: Date.now() - 60e3, title: 'Attachments', agentConfig: { id: 'claude-worker', harness: 'claude', model: 'claude-opus-5-5', effort: 'high' } });
  const shots = process.env.COMPOSER_SHOTS;
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } }), p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  const shoot = async (name: string) => {
    if (!shots) return;
    for (const width of [1440, 390]) { await p.setViewportSize({ width, height: width > 400 ? 900 : 844 }); await p.waitForTimeout(300); await p.screenshot({ path: `${shots}/${name}-${width}.png` }); }
    await p.setViewportSize({ width: 1440, height: 900 });
  };
  // Hold the upload of the second file (the dropped one), to see one file ready, one uploading, and that Send waits.
  let release = () => {}; const held = new Promise<void>(r => { release = r; });
  let puts = 0;
  await p.route('**/api/uploads/*', async route => { if (++puts >= 2) await held; await route.continue(); });
  try {
    await p.goto(`${origin}/conversations/${S}`);
    const composer = p.locator('[data-ui="composer"]'), box = composer.getByPlaceholder('Message', { exact: true });
    await box.waitFor();
    assert.equal(await composer.getByRole('button', { name: 'Attach files' }).count(), 1, 'the + button');
    assert.ok(await composer.locator('.composer-chip', { hasText: 'opus-5.5 · high' }).count() === 1, 'the profile chip names the model and effort');
    assert.ok(await composer.locator('.composer-chip', { hasText: 'Workspace' }).count() === 1, 'the permissions chip');
    await shoot('composer-idle');

    const png = solidPng(24, 24, [220, 30, 30]).toString('base64');
    // Paste a screenshot into the text area.
    await box.click();
    await p.evaluate(b64 => {
      const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0)), dt = new DataTransfer();
      dt.items.add(new File([bytes], 'screenshot.png', { type: 'image/png' }));
      document.querySelector('[data-ui="composer"] textarea')!.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    }, png);
    // Drop a text file anywhere over the conversation: the drop highlight shows while it is dragged over.
    await p.evaluate(() => {
      const dt = new DataTransfer(); dt.items.add(new File(['the word is heliotrope\n'], 'note.txt', { type: 'text/plain' }));
      const zone = document.querySelector('.tr-body')!;
      zone.dispatchEvent(new DragEvent('dragenter', { dataTransfer: dt, bubbles: true, cancelable: true }));
      zone.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }));
    });
    assert.ok(await until(async () => await p.locator('.drop-over').count() === 1), 'drop highlight while dragging files');
    await shoot('composer-drop');
    await p.evaluate(() => {
      const dt = new DataTransfer(); dt.items.add(new File(['the word is heliotrope\n'], 'note.txt', { type: 'text/plain' }));
      document.querySelector('.tr-body')!.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
    });
    assert.equal(await p.locator('.drop-over').count(), 0, 'the highlight goes on drop');
    const chips = composer.locator('.composer-files .att');
    assert.ok(await until(async () => await chips.count() === 2), 'two chips: the pasted image and the dropped file');
    assert.equal(await chips.nth(0).locator('img.att-thumb').count(), 1, 'the image chip has a thumbnail');
    assert.ok(await chips.nth(1).locator('.att-name').textContent() === 'note.txt');
    assert.ok(await until(async () => (await chips.nth(1).locator('.att-size').textContent())?.endsWith('%') === true), 'upload progress shows while uploading');
    assert.ok(await until(async () => (await chips.nth(0).locator('.att-size').textContent())?.endsWith('B') === true), 'the pasted image is stored first');
    await box.fill('Quote the word in the note and name the colour of the image.');
    await shoot('composer-uploading');
    await composer.getByRole('button', { name: 'Send' }).click();
    await p.waitForTimeout(300);
    assert.equal(seed.rows(`SELECT 1 FROM message WHERE session_id='${S}'`).length, 0, 'Send waits for the uploads');
    release();
    assert.ok(await until(async () => seed.rows(`SELECT 1 FROM message WHERE session_id='${S}'`).length === 1), 'the message is sent once the files are stored');
    const [message] = seed.rows(`SELECT payload AS data FROM message WHERE session_id='${S}'`);
    assert.deepEqual(message.data.attachments.map((a: any) => [a.name, a.mediaType]), [['screenshot.png', 'image/png'], ['note.txt', 'text/plain']]);
    assert.ok(await until(async () => await chips.count() === 0), 'the chips leave the composer after the send');
    assert.equal(await composer.locator('textarea').inputValue(), '', 'the text leaves with the send');

    // The transcript shows the files of the message; the execution it started is running: Stop in the composer.
    const row = p.locator('.th-text.operator', { hasText: 'Quote the word' });
    assert.ok(await until(async () => await row.locator('.msg-files .att').count() === 2), 'the sent message shows its two files: ' + await p.locator('.transcript, [data-ui=detail]').first().innerText().catch(e => String(e)));
    assert.equal(await row.locator('.msg-files img.att-thumb').count(), 1, 'the image as a thumbnail');
    assert.ok(await until(async () => await row.locator('img.att-thumb').evaluate(i => (i as HTMLImageElement).naturalWidth > 0)), 'the thumbnail loads from the store');
    const execution = message.data.executionId;
    seed.sql(`UPDATE execution SET state='running', started_at=now() WHERE id='${execution}'`);
    seed.put('event', { id: 'att-reply', sessionId: S, executionId: execution, type: 'item.completed', seq: 1, data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: 'Reading the files.' }] } } });
    await p.reload(); await row.waitFor();
    const stop = composer.getByRole('button', { name: 'Stop' });
    assert.ok(await until(async () => await stop.count() === 1), 'Stop while the agent runs and the composer is empty');
    await p.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await shoot('composer-running');
    await stop.click();
    assert.ok(await until(async () => seed.rows(`SELECT 1 FROM command WHERE execution_id='${execution}' AND kind='stop'`).length === 1), 'Stop sends a stop for the running execution');
    await composer.getByPlaceholder('Steer').fill('and one more thing');
    assert.equal(await composer.getByRole('button', { name: 'Send' }).count(), 1, 'typed text turns Stop into Send');
    await composer.getByPlaceholder('Steer').fill('');
    await shoot('transcript-attachments');
    // A click on a file opens the result viewer.
    await row.locator('.msg-files .att', { hasText: 'note.txt' }).click();
    assert.ok(await until(async () => new URL(p.url()).pathname.startsWith('/results/')), 'a message file opens in the result viewer');

    // The new-conversation page takes files the same way, and session_start carries them into the first message.
    await p.goto(`${origin}/conversations/new?in=cs`);
    const form = p.locator('[data-ui=detail][aria-label="New conversation"]'), fbox = form.locator('[data-ui=composer] textarea');
    await form.locator('[data-ui=preset-pick] .preset-card').first().waitFor();
    // The header + on the new page goes to its composer (never a dead click).
    await p.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await p.locator('[data-ui=shell-header]').getByRole('button', { name: 'New conversation' }).click();
    assert.ok(await until(async () => await fbox.evaluate(e => e === document.activeElement)), 'the header + focuses the new composer');
    // The same composer as in a conversation: + opens the file picker, and the start facts are its chips.
    assert.ok(await form.locator('[data-ui=composer] .composer-row [data-fact=agent]').count() === 1, 'the start facts are chips inside the composer');
    const chooser = p.waitForEvent('filechooser');
    await form.locator('[data-ui=composer]').getByRole('button', { name: 'Attach files' }).click();
    await (await chooser).setFiles({ name: 'chosen.txt', mimeType: 'text/plain', buffer: Buffer.from('chosen by the picker\n') });
    await fbox.click();
    await p.evaluate(b64 => {
      const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0)), dt = new DataTransfer();
      dt.items.add(new File([bytes], 'start.png', { type: 'image/png' }));
      document.querySelector('[aria-label="New conversation"] [data-ui="composer"] textarea')!.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
      const drop = new DataTransfer(); drop.items.add(new File(['start note\n'], 'start.txt', { type: 'text/plain' }));
      const zone = document.querySelector('[aria-label="New conversation"] .drop-zone')!;
      for (const t of ['dragenter', 'dragover', 'drop']) zone.dispatchEvent(new DragEvent(t, { dataTransfer: drop, bubbles: true, cancelable: true }));
    }, png);
    const fchips = form.locator('.composer-files .att');
    assert.ok(await until(async () => await fchips.count() === 3), 'the new page takes a picked, a pasted and a dropped file');
    assert.ok(await until(async () => (await fchips.locator('.att-size').allTextContents()).every(t => !t.endsWith('%'))), 'both stored');
    await fbox.fill('Start with these files.');
    await shoot('new-conversation-files');
    await form.locator('[data-ui=composer]').getByRole('button', { name: 'Send' }).click();
    await p.waitForURL(u => /^\/conversations\/[0-9a-f-]{36}$/.test(new URL(u).pathname), { timeout: 15000 });
    const started = new URL(p.url()).pathname.split('/').pop()!;
    const [first] = seed.rows(`SELECT payload AS data FROM message WHERE session_id='${started}'`);
    assert.deepEqual(first?.data.attachments?.map((x: any) => x.name), ['chosen.txt', 'start.png', 'start.txt'], 'session_start carries the files into the first message');
    assert.ok(await until(async () => await p.locator('.th-text.operator .msg-files .att').count() === 3, 15000), 'the started conversation shows them');
    await shoot('new-conversation-started');
    assert.deepEqual(errors, []);
    console.log('ok Composer attachments: paste and drop become chips with progress, Send waits for uploads, the transcript shows the files, Stop while running; the new-conversation page starts with files');
  } finally { release(); await context.close(); }
}

if (process.argv[1]?.endsWith('/attachments.check.ts')) {
  const server = await checkServer({ store: true });
  const browser = await chromium.launch({ headless: true });
  try { await checkAttachments(browser, server.origin, server.seed); } finally { await browser.close(); await server.close(); }
}

export type { Page };
