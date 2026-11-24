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
  await p.getByRole('button',{name:'Save project',exact:true}).click();await p.waitForURL(u=>/^\/projects\/[^/]+$/.test(new URL(u).pathname));
  // A project opens on Sessions (its address names it by code once known); its editor is the Rules & methods facet
  // (docs/design-system/pages/projects.md).
  await p.getByRole('tab',{name:'Rules & methods',exact:true}).click();await p.waitForURL(u=>/^\/projects\/[^/]+\/rules$/.test(new URL(u).pathname));
  const first=await call('project_context',{projectId:id});assert.equal(first.revision,1);assert.equal(first.name,'UI retained project');
  // Creation changes the actor's project grants. Establish the new authorized feed baseline before the next write.
  await p.reload();await p.getByRole('heading',{name:'UI retained project',exact:true}).waitFor();
  await edit('Name','UI edited project');
  const update=p.waitForResponse(r=>r.url().endsWith('/api/ops/project_update'));
  await p.getByRole('button',{name:'Save project',exact:true}).click();
  const updateResponse=await update;if(updateResponse.status()!==200)assert.fail(await updateResponse.text().catch(()=>'project_update '+updateResponse.status())); // the body only when it explains a failure: reading it eagerly can throw on its own
  const second=await call('project_context',{projectId:id});assert.equal(second.revision,2);assert.equal(second.name,'UI edited project');
  await p.locator('.kv-row').filter({hasText:/Saved version2$/}).waitFor();
  await p.reload();await p.getByRole('heading',{name:'UI edited project',exact:true}).waitFor();
  await p.getByRole('button',{name:'Check readiness',exact:true}).click();await p.getByText('No environment is configured.',{exact:true}).waitFor();
  const sendHost=await host('project-readiness-ui');
  const content=structuredClone(second.content);content.settings.environments=[{hostId:'project-readiness-ui',checkoutPath:'/tmp/ui-readiness'}];
  const configured=await call('project_update',{projectId:id,requestId:crypto.randomUUID(),baseRevision:second.revision,content});
  await p.getByRole('button',{name:'Reload project',exact:true}).click();
  // Readiness belongs to the configured source, not the still-pending previous editor baseline.
  await p.locator('.kv-row').filter({hasText:new RegExp(`Saved version${configured.revision}$`)}).waitFor();
  const queued=p.waitForResponse(r=>r.url().endsWith('/api/ops/project_readiness'));
  await p.getByRole('button',{name:'Check readiness',exact:true}).click();
  const check=(await (await queued).json()).hosts[0];assert.ok(check.executionId);
  const posted=await sendHost('/api/hosts/events',{executionId:check.executionId,events:[{id:'late-ready',seq:0,type:'readiness',timestamp:Date.now(),data:{hostId:'project-readiness-ui',ok:false,checks:[{check:'checkout',target:'/tmp/ui-readiness',status:'failed',reason:'checkout-missing'}]}}]});assert.equal(posted.status,200);
  await p.locator('.kv-row').filter({hasText:/Not ready$/}).waitFor();await p.getByText('/tmp/ui-readiness · checkout-missing',{exact:true}).waitFor();
  // The PM conversation opens the new-conversation page with this project and the Discuss preset (its pm role).
  await p.getByRole('button',{name:'Open PM conversation',exact:true}).click();await p.waitForURL(u=>new URL(u).pathname==='/conversations/new');
  await p.getByRole('group',{name:'Preset',exact:true}).locator('.preset-card[aria-pressed=true]',{hasText:'Discuss'}).waitFor();
  assert.equal(await p.getByRole('group',{name:'Project',exact:true}).getByRole('button',{name:'UI edited project',exact:true}).getAttribute('aria-pressed'),'true');
  assert.match(await p.locator('[data-ui=composer] [data-fact=agent]').getAttribute('data-tip')??'',/the project's pm role/);
  assert.deepEqual(errors,[]);console.log('ok Projects create/edit/readback/readiness and explicit PM selection');
 }finally{await context.close();}
}
