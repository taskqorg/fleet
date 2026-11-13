// Conversations browser check (docs/designs/fleet-conversations.md, "Browser check"). The Go coordinator (tests/harness.ts) holds the captured
// fixtures (codex 6a55ab8b, claude 127d6fd7, lost-receipt d89cc199) and one long thread built from the same block shapes, behind a
// proxy that injects the Bearer as the access guard does. Chromium opens the built app and asserts behaviour; timings are printed,
// never asserted. Exits 1 when an assertion fails.
// Run: pnpm run build && node tests/ui/conversations.check.ts
import { readFileSync } from 'node:fs';
import { chromium, type Page } from 'playwright';
import { checkServer } from './seed.ts';
import { checkProjects } from './projects.check.ts';
import { checkTaskStory } from './task-story.check.ts';
import { checkSpotlight } from './spotlight.check.ts';

const failures: string[] = [], timings: string[] = [];
const check = (ok: boolean, what: string) => { console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) failures.push(what); };
const time = (what: string, ms: number) => { timings.push(`${what}: ${ms.toFixed(0)} ms`); };

const { origin, seed, host, close } = await checkServer({ store: true });
const lit = (v: unknown) => v === null ? 'NULL' : typeof v === 'number' ? String(v) : "'" + String(typeof v === 'string' ? v : JSON.stringify(v)).replace(/'/g, "''") + "'";
// Captured fixtures, as stored.
for (const name of ['codex-6a55ab8b', 'claude-127d6fd7', 'lost-receipt-d89cc199']) {
  const fx = JSON.parse(readFileSync(new URL(`fixtures/conversations/${name}.json`, import.meta.url), 'utf8')), e = fx.execution;
  seed.bulk(() => {
    seed.sql(`INSERT INTO document(kind,id,data) VALUES('session',${lit(e.session_id)},${lit({ id: e.session_id, createdAt: Date.now() })}) ON CONFLICT DO NOTHING`);
    seed.sql(`INSERT INTO execution(id,kind,state,session_id,harness,model,config_ref,seq) VALUES(${lit(e.id)},'agent_run',${lit(e.state)},${lit(e.session_id)},${lit(e.harness)},${lit(e.model)},${lit({ effort: e.effort })},${e.seq})`);
    for (const d of fx.documents) seed.sql(`INSERT INTO document(kind,id,data,seq) VALUES(${lit(d.kind)},${lit(d.data.id)},${lit(d.data)},${d.seq})`);
  });
}
// A long thread: two running executions of one session, N turns of operator message, agent reply and tool call, one open question.
const N = Number(process.env.TURNS ?? 600), S = 'long-thread';
seed.put('session', { id: S, createdAt: Date.now() - 3600e3, agentConfig: { id: 'baseline', harness: 'claude' } });
for (const x of ['x1', 'x2']) seed.put('execution', { id: x, executionId: x, sessionId: S, hostId: 'pds18', state: 'started', startedAt: Date.now() - 600e3, model: 'claude-opus-5-5', effort: 'high', agentConfig: { harness: 'claude' } });
seed.bulk(() => {
  for (let i = 0; i < N; i++) {
    seed.put('message', { id: `m${i}`, sessionId: S, executionId: 'x1', text: `Question ${i}: check step ${i}` });
    const reply = i === 7 ? 'The migration 6 backfill finished.' : `Reply ${i}. ` + 'Long replies wrap. '.repeat(i % 5) + (i % 9 === 0 ? '\n\n```\npnpm test\n```' : '');
    seed.put('event', { id: `e${i}`, sessionId: S, executionId: 'x1', type: 'item.completed', seq: i, data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: reply }, { kind: 'tool', name: `tool-${i}`, input: { step: i }, output: 'ok\n'.repeat(1 + i % 4), state: 'completed', startedAt: 1, endedAt: 2001 }] } } });
  }
});
// A task thread over two sessions (Stage 3).
const T = seed.task({ title: 'Ship the hero copy' });
for (const [sid, x, word] of [['task-a', 'ta1', 'Alpha'], ['task-b', 'tb1', 'Bravo']] as const) {
  seed.put('session', { id: sid, createdAt: Date.now() });
  seed.put('execution', { id: x, executionId: x, sessionId: sid, hostId: 'pds18', state: 'completed', model: 'gpt-6.1-sol', effort: 'low' });
  seed.sql(`UPDATE execution SET task_id=${lit(T)} WHERE id=${lit(x)}`);
  seed.put('message', { id: 'm-' + x, sessionId: sid, executionId: x, text: `${word} question` });
  seed.put('event', { id: 'e-' + x, sessionId: sid, executionId: x, type: 'item.completed', seq: 0, data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: `${word} reply` }] } } });
}
// Results (Stage 4): rows in the artifact table for the list, legacy documents (bytes inline) for the viewer.
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==';
// An interactive result: its script runs, but on an opaque origin (no cookies, no parent access, no network).
const PAGE_HTML = `<h1>hi</h1><img id="logo" src="logo.svg"><script>document.body.dataset.ran='1';try{document.cookie;document.body.dataset.cookie='readable'}catch(e){document.body.dataset.cookie='blocked'}try{window.top.x=1}catch(e){document.body.dataset.top='blocked'}fetch('/api/ops/me',{method:'POST'}).then(()=>{document.body.dataset.net='open'},()=>{document.body.dataset.net='blocked'})</script>`;
const LOG = Array.from({ length: 6000 }, (_, i) => `line ${i} of the build log`).join('\n');
seed.bulk(() => {
  seed.artifact({ id: 'shot-1', executionId: 'x1', name: 'screenshot.png', mediaType: 'image/png', bytes: Buffer.from(PNG, 'base64') });
  seed.artifact({ id: 'log-1', executionId: 'x1', name: 'build.log', mediaType: 'text/plain', bytes: Buffer.from(LOG) });
  seed.artifact({ id: 'logo-1', executionId: 'x1', name: 'logo.svg', mediaType: 'image/svg+xml', bytes: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><circle cx="10" cy="10" r="9"/></svg>') });
  seed.artifact({ id: 'md-1', executionId: 'x1', name: 'notes.md', mediaType: 'text/markdown', bytes: Buffer.from('# Notes\n\n**bold** text') });
  seed.artifact({ id: 'json-1', executionId: 'x1', name: 'out.json', mediaType: 'application/json', bytes: Buffer.from('{"a":1,"b":[2,3]}') });
  seed.artifact({ id: 'csv-1', executionId: 'x1', name: 'rows.csv', mediaType: 'text/csv', bytes: Buffer.from('name,note\nx,"a, ""b"""\n') });
  seed.artifact({ id: 'page-1', executionId: 'x1', name: 'index.html', mediaType: 'text/html', bytes: Buffer.from(PAGE_HTML) });
  for (const [i, st] of ['stored', 'pending', 'failed'].entries()) seed.artifact({ id: 'row-' + i, executionId: 'x1', name: `result-${i}.${i ? 'txt' : 'png'}`, mediaType: i ? 'text/plain' : 'image/png', bytes: Buffer.alloc(10 + i, i), state: st });
});
seed.question({ kind: 'agent_question', text: 'Use Postgres 17?', options: ['yes', 'no'], executionId: 'x1', sessionId: S, askedBy: 'agent' });

const pds18 = await host('pds18');
const ingest = (id: string, seq: number, markdown: string) => pds18('/api/hosts/events', { executionId: 'x1', events: [{ id, seq, type: 'item.completed', timestamp: Date.now(), data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown }] } } }] });

// The first row whose top is inside the list viewport, and its top; rows are found again by their text.
// Rows by their list key, not their text: a row's text carries a live relative time ("9s") that ticks between reads.
const anchor = (p: Page) => p.locator('.tr-body .vl').evaluate(e => { const top = e.getBoundingClientRect().top; const r = [...e.querySelectorAll<HTMLElement>('.vl-row')].sort((a, b) => a.getBoundingClientRect().top - b.getBoundingClientRect().top).find(x => x.getBoundingClientRect().top >= top); return r ? { key: r.dataset.key!, top: r.getBoundingClientRect().top } : null; });
const topOf = (p: Page, key: string) => p.locator('.tr-body .vl').evaluate((e, k) => [...e.querySelectorAll<HTMLElement>('.vl-row')].find(x => x.dataset.key === k)?.getBoundingClientRect().top ?? null, key);
const visible = (p: Page, text: string) => p.locator('.tr-body .vl').evaluate((e, t) => { const b = e.getBoundingClientRect(); return [...e.querySelectorAll('.vl-row')].some(x => x.textContent!.includes(t) && x.getBoundingClientRect().bottom > b.top && x.getBoundingClientRect().top < b.bottom); }, text);
const until = async (f: () => Promise<boolean>, ms = 5000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 50)); } return false; };

const b = await chromium.launch();
try {
  const ctx = await b.newContext({ viewport: { width: 1440, height: 900 } });
  await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
  const p = await ctx.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.stack || e.message));
  const rtt: number[] = []; const started = new Map<any, number>();
  p.on('request', r => { if (r.url().includes('/api/ops/thread_entries')) started.set(r, performance.now()); });
  p.on('requestfinished', r => { const s = started.get(r); if (s !== undefined) rtt.push(performance.now() - s); });

  // Captured fixtures: each reply shows with its agent mark.
  for (const [sid, reply, label] of [['c2c11096-a08e-4b86-88d9-eb6c46785b11', 'Hi! What would you like to work on?', 'gpt-6.1-sol · low'], ['05e696a1-f925-4d58-abe1-8073f74eb965', 'I’ll continue when Fleet wakes me', 'gpt-6.1-sol · low']] as const) {
    await p.goto(`${origin}/conversations/${sid}`);
    check(await until(() => visible(p, reply)), `fixture ${sid.slice(0, 8)}: reply visible`);
    check(await p.locator('.th-text.agent .agent-mark .lbl', { hasText: label }).count() > 0, `fixture ${sid.slice(0, 8)}: agent mark "${label}"`);
  }

  // Open at the newest entry.
  let t = performance.now();
  await p.goto(`${origin}/conversations/${S}`);
  check(await until(() => visible(p, `Reply ${N - 1}.`)), 'long thread opens at the newest entry');
  const h = (await p.locator('.tr-body .vl').boundingBox())!.height; check(h > 500, `thread uses the available height (${Math.round(h)} px of a 900 px window)`);
  // Markdown renders as structure, never as raw HTML.
  await ingest('md-1', N + 50, '## Plan\n\n- one\n- **two**\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n> note\n\n[site](https://example.com) [bad](javascript:alert(1)) <b>raw</b>');
  check(await until(async () => await p.locator('.md h4', { hasText: 'Plan' }).count() === 1), 'markdown heading renders');
  check(await p.locator('.md li strong', { hasText: 'two' }).count() === 1 && await p.locator('.md table td', { hasText: '2' }).count() >= 1 && await p.locator('.md blockquote').count() >= 1, 'markdown list, table and quote render');
  check(await p.locator('.md a[href="https://example.com"]').count() === 1 && await p.locator('.md a[href^="javascript"]').count() === 0 && await p.locator('.md b').count() === 0, 'only safe links; raw HTML stays text');
  time('first paint of the newest reply', performance.now() - t);

  // Scroll up: older pages load, the row being read moves 0 px.
  const vl = p.locator('.tr-body .vl'); let drift = 0, pages = 0;
  const loaded = async () => Number(((await p.locator('.detail-h .count').textContent()) ?? '0').split(' of ')[0].replace(/,/g, ''));
  for (let i = 0; i < 4; i++) {
    const n = await loaded();
    await vl.evaluate(e => { e.scrollTop = 0; e.dispatchEvent(new Event('scroll')); });
    await p.waitForTimeout(50);
    const a = await anchor(p);
    if (!await until(async () => await loaded() > n, 4000)) { console.log(`  loaded ${n}, requests ${rtt.length}, scrollTop ${await vl.evaluate(e => e.scrollTop)}, first ${await p.locator('.tr-body .vl-row').first().getAttribute('data-index')}`); break; }
    await p.waitForTimeout(150); pages++;
    const after = a ? await topOf(p, a.key) : null;
    if (a && after !== null) drift = Math.max(drift, Math.abs(after - a.top));
  }
  check(pages >= 2, `scroll up loads older pages (${pages})`);
  check(drift <= 1, `anchor drift on prepend ${drift.toFixed(1)} px`);
  timings.push(`anchor drift: ${drift.toFixed(1)} px`);

  // Expand a tool row: its header stays where it is.
  const box = (await vl.boundingBox())!, heads = p.locator('.th-fold .th-head');
  let pick = 0; for (let i = 0; i < await heads.count(); i++) { const y = (await heads.nth(i).boundingBox())?.y ?? -1; if (y > box.y + 10 && y < box.y + box.height - 80) { pick = i; break; } }
  const head = heads.nth(pick), name = await head.textContent(), ht = (await head.boundingBox())!.y;
  await head.click(); await p.waitForTimeout(150);
  check(Math.abs((await p.locator('.th-fold .th-head', { hasText: name! }).first().boundingBox())!.y - ht) <= 1, 'expand keeps the clicked header in place');

  // Search jumps to a hit from the start of the thread.
  t = performance.now();
  await p.click('button[aria-label="Search thread"]'); // an icon until used (OR-31)
  await p.fill('input[aria-label="Search thread"]', 'migration 6'); await p.press('input[aria-label="Search thread"]', 'Enter');
  check(await until(() => visible(p, 'The migration 6 backfill finished.')), 'search hit is in view after the jump');
  time('search and jump', performance.now() - t);
  check(await p.locator('.vl-row.marked').count() === 1, 'search hit row is marked');

  // Back to newest: a streamed entry appends and the view follows; scrolled up, the next one shows the pill.
  await p.goto(`${origin}/conversations/${S}`); await until(() => visible(p, `Reply ${N - 1}.`));
  await ingest('live-1', N, 'Live reply one'); check(await until(() => visible(p, 'Live reply one')), 'streamed entry appends at the bottom in view');
  await vl.evaluate(e => { e.scrollTop = e.scrollTop - 600; }); await p.waitForTimeout(150);
  const a2 = await anchor(p);
  await ingest('live-2', N + 1, 'Live reply two');
  check(await until(async () => /^\d+ new$/.test((await p.locator('.pill').first().textContent().catch(() => '')) ?? '')), 'scrolled up: the pill counts the new entry');
  check(a2 !== null && Math.abs((await topOf(p, a2.key))! - a2.top) <= 1, 'scrolled up: the row being read does not move on append');

  // Copy one reply.
  await vl.evaluate(e => { e.scrollTop = e.scrollHeight; }); await p.waitForTimeout(200);
  await p.locator('.th-text.agent', { hasText: 'Live reply two' }).getByRole('button', { name: 'Copy message' }).click();
  // formats.md "Copy": a bold header line with author and time, the body, and chat-safe HTML beside the text.
  const one = await until(async () => /^\*\*\S.* · \d{1,2} [A-Z][a-z]{2}( \d{4})? \d\d:\d\d\*\*\nLive reply two\n$/.test(await p.evaluate(() => navigator.clipboard.readText())));
  check(one, 'message copy puts a header line (author · time) and the body on the clipboard');
  const html = await p.evaluate(async () => { const [it] = await navigator.clipboard.read(); return it.types.includes('text/html') ? (await it.getType('text/html')).text() : ''; });
  check(/^<b>[^<]+<\/b><br>Live reply two$/.test(html), 'message copy also writes chat-safe HTML');

  // Answer the open question.
  await p.locator('.decision', { hasText: 'Use Postgres 17?' }).first().getByRole('button', { name: 'yes' }).click();
  check(await until(async () => seed.rows('SELECT text, state, answer FROM question').some((q: any) => q.text === 'Use Postgres 17?' && q.state === 'answered' && q.answer === 'yes')), 'question answered from the thread');

  // Stop one of two running executions from its boundary row.
  await vl.evaluate(e => { e.scrollTop = 0; }); await p.waitForTimeout(300);
  for (let i = 0; i < 20 && !(await p.locator('.th-boundary').count()); i++) { await vl.evaluate(e => { e.scrollTop = 0; }); await p.waitForTimeout(300); }
  const x2 = p.locator('.th-boundary', { has: p.locator('[data-tip*="Run x2 "]') });
  await x2.getByRole('button', { name: 'Stop' }).click();
  check(await until(async () => seed.rows("SELECT 1 FROM document WHERE kind='command' AND data->>'executionId'='x2' AND data->>'kind'='stop'").length === 1), 'Stop on one boundary sends a stop for that execution');
  check(seed.rows("SELECT 1 FROM document WHERE kind='command' AND data->>'executionId'='x1' AND data->>'kind'='stop'").length === 0, 'the other execution gets no stop');
  // Task thread: a session of a task shows the whole task; Shift+click selects across rows and Cmd+C copies their markdown.
  await p.goto(`${origin}/conversations/task-a`);
  check(await until(async () => await visible(p, 'Alpha reply') && await visible(p, 'Bravo reply')), 'a task session shows both sessions of the task');
  check(await p.locator('.th-boundary [data-tip^="Conversation task-b "]').count() === 1, 'boundary rows name their session');
  await pds18('/api/hosts/events', { executionId: 'tb1', events: [{ id: 'tb-live', seq: 1, type: 'item.completed', timestamp: Date.now(), data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: 'Bravo live' }] } } }] });
  check(await until(() => visible(p, 'Bravo live')), 'a task thread follows another session of the task live');
  await p.locator('.th-text', { hasText: 'Alpha question' }).click();
  await p.locator('.th-text', { hasText: 'Bravo reply' }).click({ modifiers: ['Shift'] });
  check(await p.locator('.vl-row.picked').count() >= 4, 'shift+click selects the rows in between');
  await p.keyboard.press('Meta+c');
  const clip = await until(async () => /Alpha question[\s\S]*Alpha reply[\s\S]*Bravo question[\s\S]*Bravo reply/.test(await p.evaluate(() => navigator.clipboard.readText())));
  check(clip, 'Cmd+C copies the selected rows as markdown');
  check(/\*\*You · \d{1,2} [A-Z][a-z]{2}|\*\*\S+ · \d{1,2} [A-Z][a-z]{2}/.test(await p.evaluate(() => navigator.clipboard.readText())), 'each copied message keeps its author and time');
  await p.evaluate(() => navigator.clipboard.writeText(''));
  await p.keyboard.press('Meta+a'); await p.keyboard.press('Meta+c');
  check(await until(async () => { const t = await p.evaluate(() => navigator.clipboard.readText()); return t.startsWith('---') && t.includes('Alpha question') && t.includes('Bravo reply'); }), 'Cmd+A then Cmd+C copies the whole thread');
  // Menu actions (Stage 5): one left-aligned menu with icons; each calls the existing operation.
  await p.goto(`${origin}/conversations/task-a`); await until(() => visible(p, 'Alpha reply'));
  await p.getByRole('button', { name: 'Actions' }).click();
  const items = (await p.locator('[role=menu] [role=menuitem]').allTextContents()).map(t => t.trim());
  check(items.join('|') === 'Retry last message|Switch profile or model|Hand off to another agent|Fork' && await p.locator('[role=menu] [role=menuitem] .icon').count() === 4, 'action menu: four items with icons');
  const before = seed.rows("SELECT data->>'text' AS text FROM document WHERE kind='message' AND data->>'sessionId'='task-a' ORDER BY seq").length;
  await p.getByRole('menuitem', { name: 'Retry last message' }).click();
  check(await until(async () => seed.rows("SELECT data->>'text' AS text FROM document WHERE kind='message' AND data->>'sessionId'='task-a' ORDER BY seq").length === before + 1 && seed.rows("SELECT data->>'text' AS text FROM document WHERE kind='message' AND data->>'sessionId'='task-a' ORDER BY seq").at(-1).text === 'Alpha question'), 'Retry sends the last operator message again');
  await p.getByRole('button', { name: 'Actions' }).click(); await p.getByRole('menuitem', { name: 'Switch profile or model' }).click();
  check(await until(async () => await p.locator('[role=group][aria-label=Profile] button').count() > 0), 'Switch opens an inline panel with profiles');
  await p.getByRole('button', { name: 'Actions' }).click(); await p.getByRole('menuitem', { name: 'Fork' }).click();
  check(await until(async () => !p.url().endsWith('/task-a') && /\/conversations\/[0-9a-f-]{36}/.test(p.url())), 'Fork opens the new session');
  // Requirement: handoff uses managed intake/harness fields. Risk: enabled is absent on real accounts, hiding valid bindings.
  await p.route('**/api/ops/profiles_list', route => route.fulfill({ json: { profiles: [{ id: 'handoff-codex', harness: 'codex' }, { id: 'handoff-claude', harness: 'claude' }] } }));
  await p.route('**/api/ops/account_list', route => route.fulfill({ json: { accounts: [
    { id: 'pds', name: 'pds-ready', harness: 'codex', intake: 'all' },
    { id: 'windows', name: 'windows-ready', harness: 'codex', intake: 'all' },
    { id: 'paused', name: 'paused-account', harness: 'codex', intake: 'paused' },
    { id: 'unknown', name: 'unknown-intake', harness: 'codex' },
    { id: 'claude', name: 'claude-ready', harness: 'claude', intake: 'all' },
  ] } }));
  let handoffBody: any;
  await p.route('**/api/ops/session_transition', route => { handoffBody = route.request().postDataJSON(); return route.fulfill({ json: { transition: 'reuse' } }); });
  await p.getByRole('button', { name: 'Actions' }).click(); await p.getByRole('menuitem', { name: 'Hand off to another agent' }).click();
  await p.getByRole('button', { name: 'handoff-codex · codex', exact: true }).click();
  const accountGroup = p.getByRole('group', { name: 'Account', exact: true });
  check(await until(async () => await accountGroup.getByRole('button').count() === 2), 'handoff exposes active PDS and Windows accounts without enabled, excludes paused/unknown intake and other harness');
  await accountGroup.getByRole('button', { name: 'pds-ready · codex', exact: true }).click();
  await p.getByRole('button', { name: 'handoff-claude · claude', exact: true }).click();
  check(await until(async () => await accountGroup.getByRole('button').count() === 1 && await accountGroup.getByRole('button', { name: 'claude-ready · claude', exact: true }).count() === 1), 'changing profile filters accounts by harness');
  await p.getByRole('button', { name: 'Hand off', exact: true }).click();
  check(await until(async () => handoffBody?.profileId === 'handoff-claude' && !('account' in handoffBody)), 'changing profile clears the incompatible account selection');
  await accountGroup.getByRole('button', { name: 'claude-ready · claude', exact: true }).click();
  await p.getByRole('button', { name: 'Hand off', exact: true }).click();
  check(await until(async () => handoffBody?.account === 'claude-ready' && handoffBody?.profileId === 'handoff-claude'), 'handoff submits selected managed account name through session_transition');
  await p.unroute('**/api/ops/profiles_list'); await p.unroute('**/api/ops/account_list'); await p.unroute('**/api/ops/session_transition');
  // Results page: state first, filters, and the viewer for an image, a long text read in pages, and HTML without scripts.
  await p.goto(`${origin}/results`);
  check(await until(async () => await p.locator('.vl-row', { hasText: 'result-0.png' }).count() === 1), 'results page lists results');
  check(await p.locator('.vl-row').first().locator('[data-ui=state-badge]').count() === 1, 'each result row starts with its state');
  await p.locator('[data-ui=state-counters] button[aria-label^="Failed"]').click();
  check(await until(async () => await p.locator('.vl-row').count() === 1 && await p.locator('.vl-row', { hasText: 'result-2.txt' }).count() === 1), 'state filter narrows the list');
  await p.goto(`${origin}/results/shot-1`);
  check(await until(async () => await p.locator('.av-frame img').evaluate((i: any) => i.complete && i.naturalWidth === 1).catch(() => false)), 'image result shows in the viewer');
  await p.goto(`${origin}/results/log-1`);
  check(await until(async () => (await p.locator('.av-text pre').textContent() ?? '').includes('line 10 of')), 'text result shows its first page');
  const firstPage = (await p.locator('.av-text pre').textContent())!.length;
  for (let i = 0; i < 6; i++) { await p.locator('.av-text').evaluate(e => { e.scrollTop = e.scrollHeight; }); await p.waitForTimeout(200); }
  check((await p.locator('.av-text pre').textContent())!.includes('line 5999 of'), `long text loads further pages on scroll (first page ${firstPage} chars)`);
  // Every renderable format opens rendered; the Source/Preview switch shows the source and is remembered per format.
  await p.goto(`${origin}/results/md-1`);
  check(await until(async () => await p.locator('.av-text .md .md-h', { hasText: 'Notes' }).count() === 1), 'Markdown opens rendered');
  await p.goto(`${origin}/results/json-1`);
  check(await until(async () => (await p.locator('.av-text pre').textContent() ?? '').includes('"a": 1,\n')), 'JSON opens indented');
  await p.goto(`${origin}/results/csv-1`);
  check(await until(async () => await p.locator('.av-table td', { hasText: 'a, "b"' }).count() === 1), 'CSV opens as a table');
  await p.getByRole('button', { name: 'Source', exact: true }).click();
  check(await until(async () => (await p.locator('.av-text pre').textContent() ?? '').includes('"a, ""b"""')), 'Source shows the raw CSV');
  await p.goto(`${origin}/results/logo-1`);
  check(await until(async () => await p.locator('.av-frame img').evaluate((i: any) => i.complete && i.naturalWidth > 0).catch(() => false)), 'SVG opens as an image');
  await p.goto(`${origin}/results/csv-1`);
  check(await until(async () => await p.getByRole('button', { name: 'Preview', exact: true }).count() === 1 && await p.locator('.av-table').count() === 0), 'the CSV choice of Source is remembered');
  await p.goto(`${origin}/results/page-1`);
  check(await until(async () => await p.locator('.av-frame iframe').count() === 1), 'HTML opens as a running preview');
  await p.getByRole('button', { name: 'Source', exact: true }).click();
  check(await until(async () => (await p.locator('.av-text pre').textContent() ?? '').includes('<script>')), 'HTML Source shows the source text');
  await p.getByRole('button', { name: 'Preview', exact: true }).click();
  check(await p.locator('.av-frame iframe').getAttribute('sandbox') === 'allow-scripts allow-forms allow-modals', 'HTML preview frame: scripts, no same-origin');
  const frame = p.frameLocator('.av-frame iframe');
  check(await until(async () => await frame.locator('body[data-ran="1"]').count() === 1), 'the interactive result runs its script');
  check(await until(async () => await frame.locator('body[data-cookie="blocked"][data-top="blocked"][data-net="blocked"]').count() === 1), 'its script cannot read cookies, reach the app page or call the network');
  check(await until(async () => await frame.locator('#logo').evaluate((i: any) => i.complete && i.naturalWidth > 0).catch(() => false)), 'the page loads its own image from the same run');
  check(await p.evaluate(() => (window as any).x === undefined), 'the app page is untouched');
  const direct = await p.evaluate(async () => (await fetch('/api/results/page-1')).headers.get('content-security-policy'));
  check(!!direct && !/allow-scripts/.test(direct), '/api/results itself stays script-free');
  check(!errors.length, `no page errors${errors.length ? ': ' + errors.join('; ') : ''}`);
  if (rtt.length) time('thread_entries page round trip (median)', [...rtt].sort((x, y) => x - y)[rtt.length >> 1]);
  await checkProjects(b, origin, host);
  await checkSpotlight(b, origin);
  await checkTaskStory(b, origin);
} finally { await b.close(); await close(); }

console.log('\nTimings (reported, not asserted):'); for (const l of timings) console.log('  ' + l);
if (process.env.GITHUB_STEP_SUMMARY) { const { appendFileSync } = await import('node:fs'); appendFileSync(process.env.GITHUB_STEP_SUMMARY, '### Conversations check\n' + timings.map(l => '- ' + l).join('\n') + '\n'); }
process.exit(failures.length ? 1 : 0);
