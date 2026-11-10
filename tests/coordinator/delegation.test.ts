import {test} from 'node:test';import assert from 'node:assert/strict';
import {mkdtempSync,mkdirSync,readFileSync,writeFileSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from '../../packages/coordinator/app.ts';
import {attachStores,upload} from './artifact-helpers.ts';
test('durable scoped delegation, sequential dispatch, actual results and stop suppression',async()=>{
 const dir=mkdtempSync(tmpdir()+'/fleet-delegate-');mkdirSync(dir+'/profiles');
 for(const id of ['baseline','design-rules']){const p=JSON.parse(readFileSync('config/agents/'+id+'.json','utf8'));if(id==='design-rules')p.delegationProfiles=['baseline'];writeFileSync(dir+'/profiles/'+id+'.json',JSON.stringify(p))}
 const config={database:dir+'/db',profilesDirectory:dir+'/profiles',browserSecret:'browser',machineSecret:'machine',hostId:'mac',storeEndpointHosts:['127.0.0.1']};let app=createApp(config);await attachStores(app,'machine',['mac']);
 const call=(url:string,payload?:any,machine=false)=>app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'machine':'browser')},payload});
 const poll=async()=> (await call('/api/hosts/poll',{hostId:'mac'},true)).json().commands;
 const receipt=(c:any,status:string)=>call('/api/hosts/receipts',{hostId:'mac',commandId:c.id,executionId:c.executionId,status,nativeSessionId:'native-'+c.sessionId},true);
 // Two sessions: the parent's profile grants delegation to baseline; the other session's execution must be refused.
 const start=async(profileId:string,requestId:string)=>{const s=(await call('/api/sessions',{})).json();await call('/api/sessions/'+s.id+'/profile',{profileId});const m=(await call('/api/sessions/'+s.id+'/messages',{requestId,text:'work'})).json();assert.ok(m.executionId,JSON.stringify(m));return m};
 const first=await start('design-rules','parent'),other=await start('baseline','other');const parent=(await poll()).find((c:any)=>c.executionId===first.executionId);await receipt(parent,'started');
 const request={hostId:'mac',commandId:parent.id,executionId:parent.executionId,delegation:{requestId:'child',profileId:'baseline',input:'review'}};
 assert.equal((await call('/api/hosts/receipts',request)).statusCode,401);
 assert.equal((await call('/api/hosts/receipts',{...request,hostId:'other'},true)).statusCode,403);
 assert.equal((await call('/api/hosts/receipts',{...request,executionId:other.executionId},true)).statusCode,403);
 const d=(await call('/api/hosts/receipts',request,true)).json();assert.ok(d.childExecutionId);
 assert.equal((await call('/api/hosts/receipts',request,true)).json().id,d.id);
 assert.equal((await call('/api/hosts/receipts',{...request,delegation:{...request.delegation,input:'changed'}},true)).statusCode,409);
 assert.ok(!(await poll()).some((c:any)=>c.executionId===d.childExecutionId));
 await receipt(parent,'unknown');assert.ok(!(await poll()).some((c:any)=>c.executionId===d.childExecutionId));await upload(app,'machine',{hostId:'mac',executionId:parent.executionId,path:'output/index.html',mediaType:'text/html',content:'<h1>Actual site</h1>'});await receipt(parent,'completed');
 // Website sources are read outside the poll transaction: a later poll binds them.
 let child:any;for(let i=0;i<50&&!child;i++){child=(await poll()).find((c:any)=>c.executionId===d.childExecutionId);if(!child)await new Promise(r=>setTimeout(r,20))}assert.ok(child);await receipt(child,'started');
 assert.equal((await call('/api/hosts/receipts',{hostId:'mac',commandId:child.id,executionId:child.executionId,delegation:{requestId:'recursive',profileId:'baseline',input:'x'}},true)).statusCode,403);
 await call('/api/hosts/events',{hostId:'mac',executionId:child.executionId,events:[{id:'answer',seq:0,type:'assistant',text:'actual review result'}]},true);
 await receipt(child,'completed');await app.close();app=createApp(config);
 const next=(await poll()).find((c:any)=>c.sessionId===parent.sessionId);assert.ok(next);assert.equal(next.payload.nativeSessionId,'native-'+parent.sessionId);assert.match(next.payload.text,/actual review result/);
 assert.equal((await poll()).filter((c:any)=>c.sessionId===parent.sessionId).length,1);
 await call('/api/executions/'+parent.executionId+'/stop',{});
 assert.ok(!(await poll()).some((c:any)=>c.id===next.id));
 const history=(await call('/api/executions/'+parent.executionId+'/delegations')).json();assert.equal(history.delegations[0].result.events[0].text,'actual review result');
 await app.close();
});
