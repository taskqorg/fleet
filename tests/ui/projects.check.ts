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
  assert.deepEqual(errors,[]);console.log('ok Projects create/edit/readback/readiness and explicit project PM selection');
 }finally{await context.close();}
}
