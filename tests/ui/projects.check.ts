// R2/R3: Projects and PM selection use real shared operations; risk: Save appears successful or role/context differs from the retained version.
// Called by the existing Conversations CI browser entry; do not launch locally on the owner's Mac.
import assert from 'node:assert/strict';
import type {Browser} from 'playwright';
export async function checkProjects(browser:Browser,origin:string){
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
  await edit('Name','UI edited project');await p.getByRole('button',{name:'Save project',exact:true}).click();
  await p.getByText('2',{exact:true}).waitFor();const second=await call('project_context',{projectId:id});assert.equal(second.revision,2);assert.equal(second.name,'UI edited project');
  await p.reload();await p.getByRole('heading',{name:'UI edited project',exact:true}).waitFor();
  await p.getByRole('button',{name:'Check readiness',exact:true}).click();await p.getByText('No environment is configured.',{exact:true}).waitFor();
  await p.getByRole('button',{name:'Open PM conversation',exact:true}).click();await p.getByRole('group',{name:'Role / configuration'}).getByRole('button',{name:'pm · pm',exact:true}).waitFor();
  assert.equal(await p.getByRole('group',{name:'Project',exact:true}).getByRole('button',{name:'UI edited project',exact:true}).getAttribute('aria-pressed'),'true');
  assert.equal(await p.getByRole('group',{name:'Role / configuration'}).getByRole('button',{name:'pm · pm',exact:true}).getAttribute('aria-pressed'),'true');
  assert.deepEqual(errors,[]);console.log('ok Projects create/edit/readback/readiness and explicit project PM selection');
 }finally{await context.close();}
}
