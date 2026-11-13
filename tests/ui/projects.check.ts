// R2/R3: Projects and PM selection use real shared operations; risk: Save appears successful or role/context differs from the retained version.
// Called by the existing Conversations CI browser entry; do not launch locally on the owner's Mac.
import assert from 'node:assert/strict';
import type {Browser} from 'playwright';
export async function checkProjects(browser:Browser,origin:string,host:(id:string)=>Promise<(path:string,body:unknown)=>Promise<Response>>){
 const context=await browser.newContext();const p=await context.newPage();
 const errors:string[]=[];p.on('pageerror',e=>errors.push(e.message));
 const id='ui-project-'+Date.now();
 const call=async(name:string,body:object)=>{const r=await fetch(origin+'/api/ops/'+name,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)});assert.equal(r.status,200,await r.clone().text());return r.json() as Promise<any>;};
 const edit=async(label:string,text:string)=>{await p.getByLabel(label,{exact:true}).click();const input=p.getByLabel(label,{exact:true});await input.fill(text);await input.press('Enter');};
 try{
  await p.goto(origin+'/projects');await p.getByRole('button',{name:'Create project',exact:true}).click();
  await edit('Project ID',id);await edit('Name','UI retained project');
  await p.getByRole('button',{name:'Save project',exact:true}).click();await p.waitForURL('**/projects/'+id);
  const first=await call('project_context',{projectId:id});assert.equal(first.revision,1);assert.equal(first.name,'UI retained project');
  await edit('Name','UI edited project');await p.getByRole('heading',{name:'UI edited project',exact:true}).waitFor();
  const update=p.waitForResponse(r=>r.url().endsWith('/api/ops/project_update'));
  await p.getByRole('button',{name:'Save project',exact:true}).click();
  const updateResponse=await update;assert.equal(updateResponse.status(),200,await updateResponse.text());
  const second=await call('project_context',{projectId:id});assert.equal(second.revision,2);assert.equal(second.name,'UI edited project');
  await p.locator('.kv-row').filter({hasText:/Saved version2$/}).waitFor();
  await p.reload();await p.getByRole('heading',{name:'UI edited project',exact:true}).waitFor();
  await p.getByRole('button',{name:'Check readiness',exact:true}).click();await p.getByText('No environment is configured.',{exact:true}).waitFor();
  const sendHost=await host('project-readiness-ui');
  const content=structuredClone(second.content);content.settings.environments=[{hostId:'project-readiness-ui',checkoutPath:'/tmp/ui-readiness'}];
  await call('project_update',{projectId:id,requestId:crypto.randomUUID(),baseRevision:second.revision,content});
  await p.getByRole('button',{name:'Reload project',exact:true}).click();
  const queued=p.waitForResponse(r=>r.url().endsWith('/api/ops/project_readiness'));
  await p.getByRole('button',{name:'Check readiness',exact:true}).click();
  const check=(await (await queued).json()).hosts[0];assert.ok(check.executionId);
  const posted=await sendHost('/api/hosts/events',{executionId:check.executionId,events:[{id:'late-ready',seq:0,type:'readiness',timestamp:Date.now(),data:{hostId:'project-readiness-ui',ok:false,checks:[{check:'checkout',target:'/tmp/ui-readiness',status:'failed',reason:'checkout-missing'}]}}]});assert.equal(posted.status,200);
  await p.locator('.kv-row').filter({hasText:/Not ready$/}).waitFor();await p.getByText('/tmp/ui-readiness · checkout-missing',{exact:true}).waitFor();
  await p.getByRole('button',{name:'Open PM conversation',exact:true}).click();await p.getByRole('group',{name:'Role / configuration'}).getByRole('button',{name:'pm · pm',exact:true}).waitFor();
  assert.equal(await p.getByRole('group',{name:'Project',exact:true}).getByRole('button',{name:'UI edited project',exact:true}).getAttribute('aria-pressed'),'true');
  assert.equal(await p.getByRole('group',{name:'Role / configuration'}).getByRole('button',{name:'pm · pm',exact:true}).getAttribute('aria-pressed'),'true');
  // Analytics query & interactive AreaChart / multi-currency / unknown+hidden indicators on /projects/:id.
  const today = new Date().toISOString().slice(0, 10);
  const yesterday = new Date(Date.now() - 864e5).toISOString().slice(0, 10);
  const queries: any[] = [];
  const mkMeasures = (runs: number, tok: number) => ({
    executions: { value: runs, basis: 'measured' }, failed: { value: 0, basis: 'measured' },
    wall_ms: { value: runs * 12000, basis: 'measured' }, active_ms: { value: runs * 9000, basis: 'measured' }, cpu_ms: { value: runs * 1200, basis: 'measured' },
    peak_memory_bytes: { value: 268435456, basis: 'measured' }, tokens_input: { value: tok, basis: 'measured' }, tokens_output: { value: Math.round(tok / 4), basis: 'measured' },
    tokens_cache_read: { value: 0, basis: 'measured' }, tokens_cache_write: { value: 0, basis: 'measured' }, tokens_reasoning: { value: null, basis: 'unavailable' }, calls: { value: runs * 3, basis: 'measured' },
  });
  const costs = [
    { value: 3.4, currency: 'USD', basis: 'measured' },
    { value: 1.2, currency: 'EUR', basis: 'estimated', reason: 'tokens' },
    { value: 1.1, currency: 'USD', basis: 'subscription', note: 'Claude Max' },
  ];
  await p.route('**/api/ops/analytics_query', route => {
    const req = route.request().postDataJSON();
    queries.push(req);
    const dim = req.dimensions?.[0] ?? 'day';
    const isBucket = (req.window?.to - req.window?.from) <= 90000e3;
    const row = (val: string, runs: number, tok: number, c = costs) => ({ dimensions: { [dim]: val }, measures: mkMeasures(runs, tok), cost: c });
    const rows = (dim === 'day' || dim === 'hour' || dim === 'week')
      ? [row(yesterday, 4, 8000), row(today, 6, 12000)]
      : [row(isBucket ? `${dim}-bucket` : `${dim}-primary`, 6, 12000)];
    return route.fulfill({ json: { rows, totals: { measures: mkMeasures(10, 20000), cost: costs }, hidden: 1, unknown: 2 } });
  });
  await p.goto(origin + '/projects/' + id);
  const chart = p.locator('[data-ui="area-chart"]');
  await chart.waitFor();
  assert.equal(await chart.locator('svg').count(), 0, 'OR-8: AreaChart must use zero <svg>');
  assert.equal(await chart.locator('[role="img"] button').count(), 0, 'CA-09: interactive bucket buttons sit outside role="img"');
  const bucketBtns = chart.locator('.ac-buckets button.ac-bucket');
  assert.ok(await bucketBtns.count() >= 2, 'AreaChart renders bucket selector buttons');
  const detail = p.locator('[data-ui="detail"]');
  await detail.locator('.totals').getByText('25k', { exact: true }).waitFor();
  await detail.locator('.totals').getByText('$3.40', { exact: true }).waitFor();
  await detail.locator('.totals').getByText('≈ 1.20 EUR', { exact: true }).waitFor();
  await detail.locator('.totals').getByText('≈ $1.10', { exact: true }).waitFor();
  await detail.getByRole('button', { name: 'Cost', exact: true }).click();
  const costAria = await chart.locator('[role="img"]').getAttribute('aria-label');
  assert.ok(costAria && costAria.includes('USD metered') && costAria.includes('USD subscription equiv.'), `Cost chart keeps metered USD & subscription separate: ${costAria}`);
  assert.ok(await detail.getByText('unknown', { exact: false }).count() >= 1, 'unknown count badge shown when > 0');
  assert.ok(await detail.getByText('hidden', { exact: false }).count() >= 1, 'hidden count badge shown when > 0');
  await bucketBtns.first().click();
  assert.equal(await bucketBtns.first().getAttribute('aria-pressed'), 'true');
  await detail.getByText('model-bucket', { exact: true }).waitFor();
  await detail.getByRole('button', { name: 'Harness', exact: true }).click();
  await detail.getByText('harness-bucket', { exact: true }).waitFor();
  await detail.getByRole('button', { name: 'Pipeline rev', exact: true }).click();
  const filterReq = p.waitForRequest(r => r.url().includes('/api/ops/analytics_query') && Array.isArray(r.postDataJSON()?.filters?.pipeline_revision) && r.postDataJSON().filters.pipeline_revision.includes('pipeline_revision-bucket'));
  await detail.getByRole('button', { name: 'pipeline_revision-bucket', exact: true }).click();
  await filterReq;
  await p.unroute('**/api/ops/analytics_query');
  assert.deepEqual(errors,[]);console.log('ok Projects create/edit/readback/readiness, explicit PM selection, and analytics_query UI');
 }finally{await context.close();}
}
