// Requirement: independent Projects and universal access lifecycle in the mounted shipped app.
// Risks: summaries block edits; stale private replies/drafts/receipts survive revoke; old cursors replay.
// T-FLT-59: Bots stays discoverable for members, and the Humans sidebar link is restricted to the owner.
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdirSync, readFileSync } from 'node:fs';
import { chromium, type Browser, type Route } from 'playwright';
import { checkServer, type seeder } from './seed.ts';
import { lit, type Coordinator } from '../harness.ts';
const tick = () => new Promise<void>(resolve => setImmediate(resolve));
export async function checkAccessGate(browser: Browser, coordinator: Coordinator, seed: ReturnType<typeof seeder>) {
  let actor = 'member@trial.test';
  const target = new URL(coordinator.base);
  const proxy = http.createServer((req, res) => {
    const upstream = http.request({ hostname: target.hostname, port: target.port, path: req.url, method: req.method, headers: { ...req.headers, authorization: 'Bearer ' + coordinator.secret, 'x-fleet-person': actor } }, response => { res.writeHead(response.statusCode!, response.headers); response.pipe(res); });
    upstream.on('error', () => { res.writeHead(502); res.end(); }); req.pipe(upstream);
  });
  await new Promise<void>(resolve => proxy.listen(0, '127.0.0.1', resolve));
  const origin = 'http://127.0.0.1:' + (proxy.address() as { port: number }).port;
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.addInitScript(() => {
    const Native = window.EventSource;
    (window as any).__streams = [];
    window.EventSource = class extends Native { constructor(url: string | URL, config?: EventSourceInit) { super(url, config); (window as any).__streams.push(this); } };
  });
  const p = await context.newPage(), errors: string[] = [];
  const sidebar = p.getByRole('navigation', { name: 'Sections', exact: true });
  // The rail's seven sections (OR-46): Bots are in Library, People in System.
  const botsLink = sidebar.getByRole('link', { name: 'Library', exact: true });
  const humansLink = sidebar.getByRole('link', { name: 'System', exact: true });
  p.on('pageerror', error => errors.push(error.message));
  const call = async (name: string, body: object) => {
    const response = await fetch(coordinator.base + '/api/ops/' + name, { method: 'POST', headers: { authorization: 'Bearer ' + coordinator.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json' }, body: JSON.stringify(body) });
    assert.equal(response.status, 200, await response.clone().text()); return response.json() as Promise<any>;
  };
  const focus = () => p.evaluate(() => window.dispatchEvent(new Event('focus')));
  const resync = () => p.evaluate(() => (window as any).__streams.at(-1).dispatchEvent(new MessageEvent('resync', { data: JSON.stringify({ cursor: 0 }) })));
  const idleSave = () => p.waitForFunction(() => [...document.querySelectorAll('button')].some(button => button.textContent?.trim() === 'Save project' && !button.disabled && button.getAttribute('aria-disabled') !== 'true' && button.getAttribute('aria-busy') !== 'true'));
  const edit = async (label: string, value: string) => { await p.getByLabel(label, { exact: true }).click(); const input = p.getByLabel(label, { exact: true }); await input.fill(value); await input.press('Enter'); };
  if (process.env.FLEET_WINDOW_DIAGNOSTIC) {
    p.on('request', request => { if (request.url().endsWith('/api/ops/project_resource_items')) { const body=request.postDataJSON(); console.log('window request',JSON.stringify({anchor:body.anchor,cursor:!!body.cursor,around:body.around?.key?.kind,queryLength:body.query?.length})); } });
    p.on('response', async response => { if(response.url().endsWith('/api/ops/project_resource_items'))try{const body=await response.json();console.log('window response',JSON.stringify({count:body.items?.length,first:body.items?.[0]?.name?.slice(0,40),last:body.items?.at(-1)?.name?.slice(0,40),previous:!!body.previousCursor,next:!!body.nextCursor}));}catch{} });
  }
  const screenshots = process.env.FLEET_BROWSER_SHOTS;
  if (screenshots) mkdirSync(screenshots, { recursive: true });
  const shot = async (name: string) => { if (screenshots) await p.screenshot({ path: screenshots + '/' + name + '.png' }); };
  let releaseSummary!: () => void;
  const summaryWait = new Promise<void>(resolve => { releaseSummary = resolve; });
  let summaryHeld = true;
  await p.route('**/api/ops/project_resources', async route => { if (summaryHeld) await summaryWait; await route.continue().catch(() => {}); });
  try {
    seed.sql(`INSERT INTO execution(id,kind,state,project_id,data) SELECT 'browser-resource-'||lpad(i::text,4,'0'),'tool_run','succeeded','desiign',jsonb_build_object('tool','browser-tool-'||lpad(i::text,4,'0')) FROM generate_series(1,207) i;`);
    await p.goto(origin + '/projects/desiign');
    await p.getByRole('button', { name: 'Save project', exact: true }).waitFor();
    await botsLink.waitFor();
    assert.equal(await sidebar.getByRole('link', { name: 'People', exact: true }).count(), 0, 'People is in System, not on the rail');
    await edit('Name', 'Browser dirty project');
    await p.getByRole('heading', { name: 'Browser dirty project', exact: true }).waitFor();
    assert.equal(await p.locator('[data-resource-version]').first().getAttribute('data-resource-version'), '', 'editor works with genuinely delayed summary');
    const geometry = await p.locator('[data-ui="list-detail"]').boundingBox();
    await shot('editor-summary-delayed');
    summaryHeld = false; releaseSummary();
    await p.waitForFunction(() => !!document.querySelector('[data-resource-project]')?.getAttribute('data-resource-version'));
    assert.equal(await p.getByRole('heading', { name: 'Browser dirty project', exact: true }).count(), 1, 'summary preserves dirty fields');
    assert.deepEqual(await p.locator('[data-ui="list-detail"]').boundingBox(), geometry, 'summary does not move columns');
    // Returning to the window with the same person keeps the page: nothing unmounts, nothing shows "Validating access".
    await p.evaluate(() => { (window as any).__kept = document.querySelector('[data-ui="detail"]'); });
    const meChecked = p.waitForResponse(r => r.url().endsWith('/api/ops/me'));
    // A wrongful fence would start right after the reply (validate runs synchronously): two frames are enough to see it.
    await focus(); await meChecked; await p.evaluate(() => new Promise<void>(r => requestAnimationFrame(() => requestAnimationFrame(() => r()))));
    assert.equal(await p.evaluate(() => (window as any).__kept?.isConnected === true), true, 'focus with the same person unmounted the page');
    assert.equal(await p.getByText('Validating access…').count(), 0, 'focus with the same person showed Validating access');
    await focus();
    await p.getByRole('heading', { name: 'Browser dirty project', exact: true }).waitFor();
    assert.equal(await p.getByLabel('Name', { exact: true }).inputValue(), 'Browser dirty project', 'authored editor input survives a new authorized generation');
    await p.getByLabel('Name', { exact: true }).fill('Unsubmitted field input');
    await focus();
    await p.getByRole('heading', { name: 'Unsubmitted field input', exact: true }).waitFor();
    await edit('Name', 'Browser dirty project');
    const detail = await call('project_detail', { projectId: 'desiign' });
    const authoredBase=await p.evaluate(()=>{const key='fleet-input:'+JSON.stringify([1,'member@trial.test','project-editor','desiign',null]);return JSON.parse(JSON.parse(localStorage.getItem(key)!).text);});
    await p.evaluate(()=>{const original=Storage.prototype.setItem;(window as any).__storageSet=original;Storage.prototype.setItem=function(key,value){if(key.includes('project-editor'))throw Error('fixture quota');return original.call(this,key,value);};});
    await p.getByLabel('Name',{exact:true}).fill('Quota-protected editor input');
    await p.getByText('Draft storage unavailable; keep this page open.',{exact:true}).waitFor();await focus();
    await p.getByRole('heading',{name:'Quota-protected editor input',exact:true}).waitFor();
    await p.evaluate(()=>{Storage.prototype.setItem=(window as any).__storageSet;delete (window as any).__storageSet;});
    await edit('Name','Browser dirty project');
    const changed = structuredClone(detail.content); changed.settings.name = 'External browser project';
    await call('project_update', { projectId: 'desiign', requestId: crypto.randomUUID(), baseRevision: detail.revision, content: changed });
    await p.getByText(/definition changed while you were editing/).waitFor();
    assert.equal(await p.getByRole('heading', { name: 'Browser dirty project', exact: true }).count(), 1);
    await focus();await p.getByText(/definition changed while you were editing/).waitFor();
    const restoredBase=await p.evaluate(()=>{const key='fleet-input:'+JSON.stringify([1,'member@trial.test','project-editor','desiign',null]);return JSON.parse(JSON.parse(localStorage.getItem(key)!).text);});
    assert.equal(restoredBase.base.revision,authoredBase.base.revision);assert.equal(restoredBase.base.contentSha,authoredBase.base.contentSha);assert.equal(restoredBase.requestId,authoredBase.requestId,'revalidation never silently rebases the optimistic write identity');
    await p.evaluate(()=>{const original=Storage.prototype.setItem;(window as any).__storageSet=original;Storage.prototype.setItem=function(key,value){if(key.includes('project-editor'))throw Error('fixture quota');return original.call(this,key,value);};});
    await p.getByRole('button', { name: 'Discard edits and reload', exact: true }).click();
    await p.getByRole('heading', { name: 'External browser project', exact: true }).waitFor();
    await focus();await p.getByRole('heading',{name:'External browser project',exact:true}).waitFor();
    assert.equal(await p.getByRole('button',{name:'Discard edits and reload',exact:true}).count(),0,'discard survives quota failure and old draft does not return after revalidation');
    await p.evaluate(()=>{Storage.prototype.setItem=(window as any).__storageSet;delete (window as any).__storageSet;});
    await edit('Name', 'Verified browser project'); const readback = p.waitForResponse(response => response.url().endsWith('/api/ops/project_detail') && response.status() === 200); await p.getByRole('button', { name: 'Save project', exact: true }).click();
    await p.getByText(/Saved version/).waitFor();
    await readback;
    assert.equal((await call('project_detail', { projectId: 'desiign' })).name, 'Verified browser project');
    await shot('editor-confirmed');
    // Actual committed source -> processor -> prepared fetch -> rendered figure. This is a small fixture, not load acceptance.
    const resources = p.locator('[data-resource-project]');
    assert.equal(await resources.isVisible(), true);
    // Ready can still be updating: freeze an exact baseline only after this fixture's queued
    // source rows drain, otherwise an intermediate count can be skipped before it is rendered.
    let confirmed = await call('project_resources',{projectId:'desiign'});
    const baselineDeadline = Date.now() + 30_000;
    while (confirmed.kind !== 'ready' || confirmed.progress.pending || confirmed.progress.state !== 'current') {
      assert.ok(Date.now() < baselineDeadline, 'fixture source baseline did not drain: ' + JSON.stringify(confirmed));
      await new Promise(resolve => setTimeout(resolve, 20));
      confirmed = await call('project_resources',{projectId:'desiign'});
    }
    const previousCalls = confirmed.header.tools.totalCalls;
    await p.waitForFunction(expected=>{const dt=[...document.querySelectorAll('[data-resource-project] dt')].find(node=>node.textContent==='Tools');return Number(dt?.nextElementSibling?.textContent)===expected;},previousCalls);
    const oldVersion = await resources.getAttribute('data-resource-version');
    const started = Date.now();
    seed.sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES('browser-resource-new','tool_run','succeeded','desiign','{"tool":"new confirmed browser tool"}');`);
    await p.waitForFunction(version => document.querySelector('[data-resource-project]')?.getAttribute('data-resource-version') !== version, oldVersion);
    await p.waitForFunction(expected => { const region=document.querySelector('[data-resource-project]'); const dt=[...region!.querySelectorAll('dt')].find(node=>node.textContent==='Tools'); return Number(dt?.nextElementSibling?.textContent)===expected; }, previousCalls+1);
    assert.ok(Date.now() - started < 5000, 'isolated connected rendered value update');
    console.log('prepared source-to-render small fixture ms', Date.now() - started);
    await p.getByRole('button', { name: /^Tools ·/ }).click();
    const grid = p.getByRole('grid', { name: 'Project tools', exact: true }); await grid.waitFor();
    // A real preparing reply must retain the selected end transition until ready.
    seed.sql("UPDATE resource_project_summary SET ready=false WHERE project_id='desiign'");
    const preparingEnd=p.waitForResponse(async response=>response.url().endsWith('/api/ops/project_resource_items')&&response.request().postDataJSON()?.anchor==='end'&&(await response.json()).kind==='preparing');
    await p.getByRole('button', { name: 'Jump to end', exact: true }).click();await preparingEnd;
    await p.waitForFunction(()=>document.querySelector('[data-resource-window]')?.getAttribute('data-resource-version')==='');
    const readyEnd=p.waitForResponse(async response=>response.url().endsWith('/api/ops/project_resource_items')&&response.request().postDataJSON()?.anchor==='end'&&Array.isArray((await response.json()).items));
    seed.sql("UPDATE resource_project_summary SET ready=true WHERE project_id='desiign'");
    await readyEnd;await p.getByText('new confirmed browser tool', { exact: true }).waitFor();
    await p.getByRole('button', { name: 'Jump to start', exact: true }).click();
    await p.getByText('browser-tool-0001', { exact: true }).waitFor();
    // A temporary edge-read error must retry the identical cursor, then append it.
    let failedCursor:string|undefined;
    await p.route('**/api/ops/project_resource_items',async route=>{
      const body=route.request().postDataJSON();
      if(body.cursor&&!failedCursor){failedCursor=body.cursor;await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({message:'fixture temporary edge failure'})});}
      else {if(body.cursor&&failedCursor)assert.equal(body.cursor,failedCursor,'retry retains original edge cursor');await route.continue();}
    });
    const failedEdge=p.waitForResponse(response=>response.url().endsWith('/api/ops/project_resource_items')&&response.status()===503);
    await grid.evaluate(el=>{el.scrollTop=el.scrollHeight;el.dispatchEvent(new Event('scroll'));});await failedEdge;
    const returnStart=p.waitForResponse(response=>response.url().endsWith('/api/ops/project_resource_items')&&response.request().postDataJSON()?.anchor==='start');
    await p.getByRole('button',{name:'Jump to start',exact:true}).click();await returnStart;
    await p.getByText('browser-tool-0001',{exact:true}).waitFor();
    await p.waitForFunction(()=>document.querySelector('[role=grid][aria-label="Project tools"]')!.scrollTop===0);
    failedCursor=undefined;
    const failedAgain=p.waitForResponse(response=>response.url().endsWith('/api/ops/project_resource_items')&&response.status()===503);
    await grid.evaluate(el=>{el.scrollTop=el.scrollHeight;el.dispatchEvent(new Event('scroll'));});await failedAgain;
    await p.getByRole('button',{name:'Retry resources',exact:true}).click();
    await p.waitForFunction(()=>document.querySelector('[data-resource-window]')?.getAttribute('data-window-last')?.includes('browser-tool-0100'));
    await p.unroute('**/api/ops/project_resource_items');
    for (const lastTool of ['0150','0200']) {
      const nextPage=p.waitForResponse(response=>response.url().endsWith('/api/ops/project_resource_items')&&!!response.request().postDataJSON()?.cursor);
      await grid.evaluate(el=>{el.scrollTop=el.scrollHeight;el.dispatchEvent(new Event('scroll'));});await nextPage;
      await p.waitForFunction(tool=>document.querySelector('[data-resource-window]')?.getAttribute('data-window-last')?.includes('browser-tool-'+tool),lastTool);
    }
    const previousPage=p.waitForResponse(response=>response.url().endsWith('/api/ops/project_resource_items')&&!!response.request().postDataJSON()?.cursor);
    await grid.evaluate(el=>{el.scrollTop=0;el.dispatchEvent(new Event('scroll'));});await previousPage;
    await p.waitForFunction(()=>document.querySelector('[data-resource-window]')?.getAttribute('data-window-first')?.includes('browser-tool-0001'));
    await grid.evaluate(el=>{el.scrollTop=0;el.dispatchEvent(new Event('scroll'));});
    await p.getByText('browser-tool-0001',{exact:true}).waitFor();
    await grid.evaluate(el => { el.scrollTop = 650; el.dispatchEvent(new Event('scroll')); });
    const anchor = await grid.evaluate(element => { const top = element.getBoundingClientRect().top; return [...element.querySelectorAll('[role="row"]')].map(row => ({ key: row.getAttribute('data-key')!, top: row.getBoundingClientRect().top, bottom: row.getBoundingClientRect().bottom })).find(row => row.bottom > top); });
    assert.ok(anchor);
    await shot('resource-continuous-window');
    const deletedName = JSON.parse(anchor!.key)[1] as string;
    const oldWindowVersion = await p.locator('[data-resource-window]').getAttribute('data-resource-version');
    seed.sql(`DELETE FROM execution WHERE id='browser-resource-'||right(${lit(deletedName)},4);`);
    await p.waitForFunction(version => document.querySelector('[data-resource-window]')?.getAttribute('data-resource-version') !== version, oldWindowVersion);
    assert.equal(await grid.locator('[role="row"]').evaluateAll((rows, key) => rows.some(row => row.getAttribute('data-key') === key), anchor!.key), false, 'deleted anchor is not displayed');
    const nearest = await grid.evaluate(element => { const top=element.getBoundingClientRect().top; return [...element.querySelectorAll('[role="row"]')].map(row=>({key:row.getAttribute('data-key'),offset:row.getBoundingClientRect().top-top})).find(row=>row.offset>=-1); });
    assert.ok(nearest && Math.abs(nearest.offset) < 2, 'nearest surviving key is restored at the top');
    // Replacement epoch: no stale cursor loop, and a packed native position is used rather than raw display text.
    seed.sql("INSERT INTO execution(id,kind,state,project_id,data) VALUES('browser-packed-tool','tool_run','succeeded','desiign',jsonb_build_object('tool',repeat('z',40000)))");
    await p.getByRole('button', { name: 'Search tools', exact: true }).click();
    await p.getByRole('searchbox', { name: 'Search tools', exact: true }).fill('z'.repeat(200));
    await p.waitForFunction(() => document.querySelector('[data-resource-window] [role="grid"]')?.getAttribute('aria-rowcount') === '1');
    let packedRequests = 0;
    const packed = (request: import('playwright').Request) => { if (request.url().endsWith('/api/ops/project_resource_items') && request.postDataJSON()?.around?.key?.kind === 'tool-position') packedRequests++; };
    p.on('request', packed);
    const epochVersion = await p.locator('[data-resource-window]').getAttribute('data-resource-version');
    seed.sql("UPDATE resource_project_summary SET epoch=gen_random_uuid(),revision=revision+1 WHERE project_id='desiign'");
    await p.waitForFunction(version => document.querySelector('[data-resource-window]')?.getAttribute('data-resource-version') !== version, epochVersion);
    assert.ok(packedRequests > 0, 'packed server position reaches around unchanged');
    seed.sql("UPDATE resource_project_summary SET ready=false WHERE project_id='desiign'");
    await p.waitForFunction(()=>document.querySelector('[data-resource-window]')?.getAttribute('data-resource-version')==='');
    assert.equal(await grid.locator('[role="row"]').count(),0,'preparing removes stale local rows');
    assert.equal(await p.locator('[data-resource-window] .totals').count(),0,'preparing removes the old publication header');
    seed.sql("UPDATE resource_project_summary SET ready=true,epoch=gen_random_uuid(),revision=revision+1 WHERE project_id='desiign'");
    await p.waitForFunction(()=>!!document.querySelector('[data-resource-window]')?.getAttribute('data-resource-version'));
    assert.equal(await grid.getAttribute('aria-rowcount'),'1','new authorized publication re-anchors the retained packed position');
    p.off('request', packed);
    await shot('resource-packed-epoch');
    await p.getByRole('button', { name: 'Close resources', exact: true }).click();
    // Hold a committed write reply, close access, then release it: no readback or old completion UI.
    let releaseWrite!: () => void, wrote!: () => void;
    const writeWait = new Promise<void>(resolve => { releaseWrite = resolve; }), writeStarted = new Promise<void>(resolve => { wrote = resolve; });
    await p.route('**/api/ops/project_update', async route => { const response = await route.fetch(); wrote(); await writeWait; await route.fulfill({ response }).catch(() => {}); });
    await edit('Name', 'Committed before revoke'); await p.getByRole('button', { name: 'Save project', exact: true }).click(); await writeStarted;
    await p.getByLabel('Name',{exact:true}).fill('Newer input after submitted save');
    await p.keyboard.press('Meta+k');
    const palette=p.getByRole('dialog');await palette.waitFor();
    const search=palette.getByRole('combobox');await search.fill('desiign');
    await palette.getByText('Committed before revoke',{exact:true}).waitFor();
    let releaseSearch!:()=>void,searchStarted!:()=>void;
    const searchWait=new Promise<void>(resolve=>{releaseSearch=resolve}),searchPending=new Promise<void>(resolve=>{searchStarted=resolve});
    await p.route('**/api/ops/search',async route=>{const response=await route.fetch();searchStarted();await searchWait;await route.fulfill({response}).catch(()=>{});});
    await search.fill('Committed');await searchPending;
    let releaseMe!: () => void, meStarted!: () => void;
    const meWait = new Promise<void>(resolve => { releaseMe = resolve; }), mePending = new Promise<void>(resolve => { meStarted = resolve; });
    let holdMe = true;
    await p.route('**/api/ops/me', async route => { if (holdMe) { meStarted(); await meWait; } await route.continue().catch(() => {}); });
    await resync(); await mePending;
    assert.equal(await p.getByRole('button', { name: 'Save project', exact: true }).count(), 0, 'private editor unmounted during pending me');
    assert.equal(await p.getByRole('dialog').count(),0,'private Spotlight results unmount while identity validation is pending');
    assert.equal(await p.getByText('Member', { exact: true }).count(), 0, 'previous identity label hidden');
    let readbacks = 0; p.on('request', request => { if (request.url().endsWith('/api/ops/project_detail')) readbacks++; });
    releaseWrite(); await tick(); assert.equal(readbacks, 0, 'late write does not start a new-generation readback');
    actor='outsider@trial.test';releaseSearch();await p.unroute('**/api/ops/search');
    holdMe = false; releaseMe();await p.getByText('Outsider',{exact:true}).waitFor();
    assert.equal(await p.getByRole('dialog').getByText('Committed before revoke',{exact:true}).count(),0,'old search response cannot appear under the new identity');
    await p.keyboard.press('Escape');actor='member@trial.test';await focus();await p.getByRole('button', { name: 'Retry original save', exact: true }).waitFor();
    await p.getByRole('button', { name: 'Retry original save', exact: true }).click(); await idleSave();
    await p.keyboard.press('Escape');
    assert.equal((await call('project_detail', { projectId: 'desiign' })).name, 'Committed before revoke', 'durable write is neither undone nor resent');
    assert.equal(await p.getByLabel('Name',{exact:true}).inputValue(),'Newer input after submitted save','late success/readback cannot erase newer authored input');
    await p.unroute('**/api/ops/project_update'); await p.unroute('**/api/ops/me');
    let releaseReadback!: () => void, reading!: () => void;
    const readbackWait = new Promise<void>(resolve => { releaseReadback=resolve; }), readingStarted = new Promise<void>(resolve => { reading=resolve; });
    await p.route('**/api/ops/project_detail', async route => { const response=await route.fetch(); if(response.status()===200){reading();await readbackWait;}await route.fulfill({response}).catch(()=>{}); });
    await edit('Name','Readback generation fence');
    const savedReply=p.waitForResponse(response=>response.url().endsWith('/api/ops/project_update'));savedReply.catch(()=>{});
    await p.getByRole('button',{name:'Save project',exact:true}).click();assert.equal((await savedReply).status(),200);await readingStarted;
    actor='outsider@trial.test';await focus();await p.getByText('Outsider',{exact:true}).waitFor();releaseReadback();
    await p.unroute('**/api/ops/project_detail');
    assert.equal(await p.getByRole('heading',{name:'Readback generation fence',exact:true}).count(),0,'a pre-revoke readback cannot apply under a new identity');
    actor='member@trial.test';await focus();await p.getByRole('button',{name:'Retry original save',exact:true}).waitFor();await p.getByRole('button',{name:'Retry original save',exact:true}).click();await idleSave();

    // Mounted thread/Composer: preserve author text for the same freshly authorized target only.
    seed.put('session', { id: 'browser-access-session', title: 'Private list marker', titleBy: 'operator', projectId: 'desiign', createdAt: Date.now() });
    seed.execution({ id: 'browser-access-run', kind: 'agent_run', state: 'succeeded', projectId: 'desiign', sessionId: 'browser-access-session' });
    seed.put('message', { id: 'browser-private-message', projectId: 'desiign', sessionId: 'browser-access-session', executionId: 'browser-access-run', text: 'Private thread marker' });
    await p.goto(origin + '/conversations/browser-access-session?q=Private%20list%20marker');
    const composer = p.getByPlaceholder('Message', { exact: true }); await composer.waitFor(); await composer.fill('Unsent owner-specific browser text');
    await focus(); await composer.waitFor(); await p.waitForFunction(() => document.querySelector<HTMLTextAreaElement>('[data-ui="composer"] textarea')?.value === 'Unsent owner-specific browser text');
    actor = 'outsider@trial.test'; await focus();
    await p.getByText('Outsider', { exact: true }).waitFor();
    assert.equal(await p.getByText('Private thread marker', { exact: true }).count(), 0);
    assert.equal(await p.locator('textarea').evaluateAll(nodes => nodes.some(node => (node as HTMLTextAreaElement).value.includes('Unsent owner-specific'))), false);
    actor = 'member@trial.test'; await focus(); await composer.waitFor(); await p.waitForFunction(() => document.querySelector<HTMLTextAreaElement>('[data-ui="composer"] textarea')?.value === 'Unsent owner-specific browser text');
    // Genuine grant revoke while mounted, with an old authorized thread reply held past the fence.
    let releaseThread!: () => void, threadStarted!: () => void;
    const threadWait = new Promise<void>(resolve => { releaseThread = resolve; }), threadPending = new Promise<void>(resolve => { threadStarted = resolve; });
    await p.route('**/api/ops/thread_entries', async route => { const response = await route.fetch(); threadStarted(); await threadWait; await route.fulfill({ response }).catch(() => {}); });
    // A change-stream resync re-reads the mounted thread (focus with the same person no longer re-reads anything).
    await resync(); await threadPending;
    // Hold a confirmed old-scope membership reply across the same grant fence as the thread.
    const privateRow = p.locator('.split-list .vl-row[data-key="browser-access-session"]');
    await privateRow.getByRole('link', { name: 'Private list marker', exact: true }).waitFor();
    let releaseMembership!: () => void, membershipStarted!: () => void;
    const membershipWait = new Promise<void>(resolve => { releaseMembership = resolve; });
    const membershipPending = new Promise<void>(resolve => { membershipStarted = resolve; });
    let authorizedTitle: string | undefined;
    const holdMembership = async (route: Route) => {
      if (!route.request().postDataJSON().ids.includes('browser-access-session')) { await route.continue(); return; }
      try {
        const response = await route.fetch({ timeout: 8000 });
        const result = await response.json();
        authorizedTitle = result.lists[0].rows.find((row: { id: string }) => row.id === 'browser-access-session')?.title;
        membershipStarted();
        await membershipWait;
        await route.fulfill({ response }).catch(() => {});
      } finally { membershipStarted(); }
    };
    await p.route('**/api/ops/list_membership', holdMembership);
    const membershipRequest = p.waitForRequest(r => r.url().endsWith('/api/ops/list_membership')
      && r.postDataJSON().ids.includes('browser-access-session'), { timeout: 8000 });
    seed.sql("UPDATE session SET title='Late private list marker' WHERE id='browser-access-session';");
    await membershipRequest;
    await membershipPending;
    assert.equal(authorizedTitle, 'Late private list marker', 'the held response contains the old authorized row');
    const people = [ { email: 'owner@trial.test', name: 'Owner', owner: true, roles: ['operator', 'architect'] }, { email: 'member@trial.test', name: 'Member', roles: ['operator'], grants: [] }, { email: 'outsider@trial.test', name: 'Outsider', roles: ['operator'], grants: [] } ];
    try { await call('people_set', { people }); await resync(); }
    finally { releaseThread(); releaseMembership(); }
    await p.waitForFunction(() => !document.body.textContent?.includes('Private thread marker'));
    await p.unroute('**/api/ops/thread_entries');
    await p.unroute('**/api/ops/list_membership', holdMembership);
    await privateRow.waitFor({ state: 'detached' });
    assert.equal(await p.getByText('Late private list marker', { exact: true }).count(), 0, 'the old membership reply did not apply');
    assert.equal(await p.locator('textarea').evaluateAll(nodes => nodes.some(node => (node as HTMLTextAreaElement).value.includes('Unsent owner-specific'))), false);
    await shot('access-revoked');
    // Restore the fixture's grant for subsequent checks; no source facts are changed by access.
    await call('people_set', { people: people.map(person => person.email === 'member@trial.test' ? { ...person, grants: [{ kind: 'project', id: 'desiign' }] } : person) });
    // Results owns local rows outside useDoc. Its late authorized response must die with the private subtree.
    actor = 'member@trial.test';
    seed.artifact({ id: 'browser-access-artifact', executionId: 'browser-access-run', name: 'Private result marker.txt', mediaType: 'text/plain', bytes: Buffer.from('private browser fixture') });
    seed.sql("UPDATE artifact SET project_id='desiign' WHERE id='browser-access-artifact'");
    await p.goto(origin + '/results'); await p.getByText('Private result marker.txt', { exact: true }).waitFor();
    let releaseResults!: () => void, resultsStarted!: () => void;
    const resultsWait = new Promise<void>(resolve => { releaseResults = resolve; }), resultsPending = new Promise<void>(resolve => { resultsStarted = resolve; });
    await p.route('**/api/ops/artifact_list', async route => { const response = await route.fetch(); resultsStarted(); await resultsWait; await route.fulfill({ response }).catch(() => {}); });
    // Re-read once under the allowed actor (a change-stream resync), then change identity during that exact read.
    await resync(); await resultsPending; actor = 'outsider@trial.test'; await focus(); releaseResults();
    await p.getByText('Outsider', { exact: true }).waitFor();
    await p.unroute('**/api/ops/artifact_list');
    await p.waitForFunction(() => !document.body.textContent?.includes('Private result marker.txt'));
    // A real preference write may commit before identity changes; its late reply must not overwrite the new actor.
    actor='member@trial.test';await focus();await p.getByText('Member',{exact:true}).waitFor();
    let releasePrefs!:()=>void, prefsStarted!:()=>void;
    const prefsWait=new Promise<void>(resolve=>{releasePrefs=resolve}), prefsPending=new Promise<void>(resolve=>{prefsStarted=resolve});
    await p.route('**/api/ops/person_settings_set',async route=>{const response=await route.fetch();prefsStarted();await prefsWait;await route.fulfill({response}).catch(()=>{});});
    await p.getByRole('separator',{name:'Resize list',exact:true}).press('ArrowRight');await prefsPending;
    actor='outsider@trial.test';const newPrefs=p.waitForResponse(response=>response.url().endsWith('/api/ops/person_settings_get'));
    await focus();await newPrefs;await p.getByText('Outsider',{exact:true}).waitFor();
    const newWidth=await p.locator('.shell').evaluate(element=>(element as HTMLElement).style.getPropertyValue('--size-ctl'));
    releasePrefs();await p.unroute('**/api/ops/person_settings_set');await p.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>resolve())));
    assert.equal(await p.locator('.shell').evaluate(element=>(element as HTMLElement).style.getPropertyValue('--size-ctl')),newWidth,'old preference reply cannot overwrite current actor geometry');
    // Creation input is actor-scoped, and its authored ID is never treated as an existing authorization grant.
    actor = 'owner@trial.test'; await p.goto(origin + '/projects');
    const createProject = p.getByRole('button', { name: 'Create project', exact: true });
    await createProject.waitFor();
    // Bots and People open from their sections, and the rail marks the section that owns them (OR-57).
    assert.equal(await p.getByRole('button', { name: 'Bots', exact: true }).count(), 0);
    await botsLink.click();
    await p.waitForURL(origin + '/library/agents');
    await p.goto(origin + '/bots'); await p.waitForURL(origin + '/library/bots');
    await p.waitForSelector('[data-ui="nav-rail"] [data-id="library"] a[aria-current="page"]');
    await humansLink.click();
    await p.waitForURL(origin + '/system/hosts');
    await p.goto(origin + '/humans'); await p.waitForURL(origin + '/system/people');
    await p.waitForSelector('[data-ui="nav-rail"] [data-id="system"] a[aria-current="page"]');
    await p.goto(origin + '/projects'); await createProject.click();
    await p.getByLabel('Project ID',{exact:true}).fill('browser-create-authored-only');await p.getByLabel('Name',{exact:true}).fill('Unsent creation actor');
    const creationRecord=await p.evaluate(()=>{const key='fleet-input:'+JSON.stringify([1,'owner@trial.test','project-create','',null]);return JSON.parse(JSON.parse(localStorage.getItem(key)!).text);});
    assert.equal(creationRecord.bundle.settings.name,'Unsent creation actor','creation input is retained before the validation fence');
    let draftIdReads=0;const createdIdRead=(request:import('playwright').Request)=>{if(request.url().endsWith('/api/ops/project_detail')&&request.postDataJSON()?.projectId==='browser-create-authored-only')draftIdReads++;};p.on('request',createdIdRead);
    await focus();await p.getByRole('button',{name:'Create project',exact:true}).click();await p.getByRole('heading',{name:'Create project',exact:true}).waitFor();
    assert.equal(await p.getByLabel('Name',{exact:true}).inputValue(),'Unsent creation actor');
    const restoredCreation=await p.evaluate(()=>{const key='fleet-input:'+JSON.stringify([1,'owner@trial.test','project-create','',null]);return JSON.parse(JSON.parse(localStorage.getItem(key)!).text);});
    assert.equal(restoredCreation.requestId,creationRecord.requestId);assert.equal(draftIdReads,0);
    actor='outsider@trial.test';await focus();await p.getByRole('button',{name:'Create project',exact:true}).click();
    assert.equal(await p.getByLabel('Project ID',{exact:true}).inputValue(),'','another actor never restores creation input');p.off('request',createdIdRead);
    // Lab authored input belongs to the original actor, while restored server receipt fields are quarantined.
    // Give the second Lab actor architect role (still grants: []) so /lab/new reads do not return 403 and trip signedOut.
    await call('people_set', { people: people.map(person => person.email === 'member@trial.test' ? { ...person, grants: [{ kind: 'project', id: 'desiign' }] } : person.email === 'outsider@trial.test' ? { ...person, roles: ['operator', 'architect'] } : person) });
    actor = 'owner@trial.test'; await p.goto(origin + '/lab/new');
    const goal = p.getByRole('textbox', { name: 'Goal', exact: true }); await goal.waitFor();
    await goal.fill('Private Lab authored input');
    await focus(); await p.waitForFunction(() => [...document.querySelectorAll('textarea')].some(input => input.value === 'Private Lab authored input'));
    actor = 'outsider@trial.test'; await focus(); await p.getByText('Outsider', { exact: true }).waitFor();
    assert.equal(await p.locator('textarea').evaluateAll(nodes => nodes.some(node => (node as HTMLTextAreaElement).value.includes('Private Lab authored input'))), false);
    actor = 'owner@trial.test'; await focus(); await p.getByText('Owner', { exact: true }).waitFor(); await goal.waitFor();
    await p.waitForFunction(() => [...document.querySelectorAll('textarea')].some(input => input.value === 'Private Lab authored input'));
    assert.equal(await p.getByText('Session expired', { exact: true }).count(), 0);
    // Unknown/created server state is not restored as a current receipt on the next authorized mount.
    await p.evaluate(() => { const key = 'fleet-lab-create:' + JSON.stringify([1, 'owner@trial.test']); const saved = JSON.parse(sessionStorage.getItem(key)!); saved.state = 'created'; saved.id = 'old-private-server-receipt'; sessionStorage.setItem(key, JSON.stringify(saved)); });
    await p.reload(); await goal.waitFor();
    await p.getByText('Creation unconfirmed', { exact: true }).waitFor();
    assert.equal(await p.getByRole('button', { name: 'Read details again', exact: true }).count(), 0);
    assert.equal(await p.evaluate(() => { const key='fleet-lab-create:'+JSON.stringify([1,'owner@trial.test']); return JSON.parse(sessionStorage.getItem(key)!).id; }), undefined);
    await shot('lab-authored-quarantine');
    const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
    const imported = await call('lab_examples_add', { examples: [example], approved: true });
    await p.evaluate(exampleId => {
      const key='fleet-lab-create:'+JSON.stringify([1,'owner@trial.test']);
      const saved=JSON.parse(sessionStorage.getItem(key)!); saved.state='editing'; delete saved.id; delete saved.message;
      Object.assign(saved.draft,{projectId:'hello-world',pipelineId:'typed-decision',kind:'single',examples:[exampleId],repeats:'1',goal:'Late real creation reply',hypothesis:'No stale receipt',variants:[{label:'A',pipelineId:'',overrides:'{}'}],evaluators:[],primary:''});
      sessionStorage.setItem(key,JSON.stringify(saved));
    }, imported.examples[0].artifactId);
    await p.reload();
    let releaseCreate!: () => void, createStarted!: () => void, createPosts=0;
    const createWait=new Promise<void>(resolve=>{releaseCreate=resolve}), createdPending=new Promise<void>(resolve=>{createStarted=resolve});
    await p.route('**/api/ops/lab_experiment_create',async route=>{createPosts++; const response=await route.fetch(); assert.equal(response.status(),200,await response.text());createStarted();await createWait;await route.fulfill({response}).catch(()=>{});});
    await p.getByRole('button',{name:'Create draft',exact:true}).click();await createdPending;
    actor='outsider@trial.test';await focus();await p.getByText('Outsider',{exact:true}).waitFor();releaseCreate();
    await p.unroute('**/api/ops/lab_experiment_create');
    assert.equal(new URL(p.url()).pathname,'/lab/new','late creation cannot navigate a new actor');
    assert.equal(await p.evaluate(()=>{const key='fleet-lab-create:'+JSON.stringify([1,'owner@trial.test']);return JSON.parse(sessionStorage.getItem(key)!).id;}),undefined,'late result cannot repopulate quarantined receipt');
    actor='owner@trial.test';await focus();await p.getByText('Owner',{exact:true}).waitFor();await p.getByText('Creation unconfirmed',{exact:true}).waitFor();
    assert.equal(createPosts,1,'reopening never resends an uncertain creation');
    await call('people_set', { people: people.map(person => person.email === 'member@trial.test' ? { ...person, grants: [{ kind: 'project', id: 'desiign' }] } : person) });

    assert.deepEqual(errors, [], 'mounted browser must have no runtime errors');
    console.log('PASS mounted Projects/access: delayed-summary, dirty conflict/save, real source update, continuous list, pending-me/late-write, identity drafts, actual thread and list membership revoke, Results late reply, blocked IDB and Lab quarantine');
  } catch (error) { await shot('browser-failure'); throw error; } finally { releaseSummary(); await context.close(); await new Promise<void>(resolve => { proxy.closeAllConnections(); proxy.close(() => resolve()); }); }
}
if (process.argv[1]?.endsWith('/access-gate.check.ts')) {
  const server = await checkServer({ env: { FLEET_RESOURCE_READS: 'on', FLEET_RESOURCE_PROCESSOR: 'on' }, store: true });
  const browser = await chromium.launch({ headless: true });
  try { await checkAccessGate(browser, server.c, server.seed); } finally { await browser.close(); await server.close(); }
}
