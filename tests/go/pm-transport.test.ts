// Real native MCP handler + executor outbox + coordinator transaction; no model/provider is launched.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {spawn} from 'node:child_process';
const base=process.env.FLEET_TRIAL_URL!,secret=process.env.FLEET_TRIAL_BROWSER_SECRET!,person='member@trial.test';
async function op(name:string,args:object={},who=person){const r=await fetch(base+'/api/ops/'+name,{method:'POST',headers:{authorization:'Bearer '+secret,'x-fleet-person':who,'content-type':'application/json'},body:JSON.stringify(args)});assert.equal(r.status,200,await r.clone().text());return r.json() as Promise<any>;}
// R1, R3; risk: an unknown committed create duplicates a task, leaks a cached acknowledgment, or loses its session link.
test('PM transport: lost committed create, concurrent replay, native uncertainty readback and explicit Held attachment',{timeout:90_000},async()=>{
 const hostId='pm-transport-host',host=await op('hosts_register',{id:hostId,holder:person},'owner@trial.test');
 const poll=async()=>{const r=await fetch(base+'/api/hosts/poll',{method:'POST',headers:{authorization:'Bearer '+host.secret,'content-type':'application/json'},body:JSON.stringify({platform:'darwin',capabilities:[],stores:[],problems:[]})});assert.equal(r.status,200);return r.json() as Promise<any>;};
 await poll();const startId=randomUUID();const session=await op('session_start',{projectId:'desiign',role:'pm',requestId:startId,text:'Propose a Held task only'});
 // A client retry after a cut reply (app writeOnce) sends the same requestId: the same session, no second one.
 const again=await op('session_start',{projectId:'desiign',role:'pm',requestId:startId,text:'Propose a Held task only'});assert.equal(again.sessionId,session.sessionId);assert.equal(again.existing,true);
 const command=(await poll()).commands.find((c:any)=>c.executionId===session.executionId&&c.kind==='start');assert.ok(command);
 const create=command.payload.fleetOperations.find((o:any)=>o.name==='task_create');assert.ok(create.inputSchema.properties.title,'canonical mutation schema is retained');
 const attach=command.payload.fleetOperations.find((o:any)=>o.name==='session_task_attach');assert.ok(attach.inputSchema.properties.taskId);
 const title='PM lost reply '+randomUUID();
 const code=await new Promise<number>(resolve=>spawn('go',['test','./executor','-run','^TestPMTransportAgainstCoordinator$','-count=1','-timeout=75s'],{cwd:new URL('../../go',import.meta.url),stdio:'inherit',env:{...process.env,FLEET_PM_TEST_URL:base,FLEET_PM_TEST_TOKEN:host.secret,FLEET_PM_TEST_TITLE:title,FLEET_PM_TEST_CONTEXT:JSON.stringify({hostId,commandId:command.id,executionId:session.executionId,projectId:'desiign',operations:command.payload.fleetOperations})}}).on('exit',c=>resolve(c??1)));
 assert.equal(code,0);
 const tasks=(await op('task_list',{projectId:'desiign'})).tasks.filter((t:any)=>t.title===title);assert.equal(tasks.length,1,'one task after a lost response and concurrent retransmissions');
 assert.equal(tasks[0].state,'paused');assert.equal(tasks[0].pauseReason,'held');
 assert.equal((await op('session_get',{sessionId:session.sessionId})).taskId,tasks[0].id,'explicit native attachment retained');
 const target='receipt-'+randomUUID(),createId=randomUUID(),created={id:target,requestId:createId,content:{settings:{id:target,name:'Receipt scope',contextFiles:[]},contextFiles:[]}};const made=await op('project_create',created);
 assert.deepEqual(await op('project_create',created),made,'a repeated project_create requestId replays its reply');
 const task=await op('task_create',{projectId:target,title:'Revocable receipt'});
 const requestKey=command.id+':agent:'+randomUUID();
 const rpc=async(key:string,name:string,args:object)=>{const r=await fetch(base+'/api/hosts/agent-tools',{method:'POST',headers:{authorization:'Bearer '+host.secret,'content-type':'application/json'},body:JSON.stringify({hostId,commandId:command.id,executionId:session.executionId,agentToolKey:key,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name,arguments:args}}})});return {status:r.status,body:await r.json()};};
 assert.equal((await rpc(requestKey,'task_update',{taskId:task.id,title:'Committed title'})).status,200);
 assert.equal((await rpc(requestKey,'task_update',{taskId:task.id,title:'Different title'})).status,409,'same identity cannot change its payload');
 const outsider='outsider@trial.test',foreignProject='foreign-'+randomUUID();
 await op('project_create',{id:foreignProject,requestId:randomUUID(),content:{settings:{id:foreignProject,name:'Other director',roles:{pm:'pm'},contextFiles:[]},contextFiles:[]}},outsider);
 const foreignTask=await op('task_create',{projectId:foreignProject,title:'Other director task'},outsider);
 const foreignHost=await op('hosts_register',{id:'pm-foreign-host',holder:outsider},'owner@trial.test');
 const foreignPoll=async()=>{const r=await fetch(base+'/api/hosts/poll',{method:'POST',headers:{authorization:'Bearer '+foreignHost.secret,'content-type':'application/json'},body:JSON.stringify({platform:'darwin',capabilities:[],stores:[],problems:[]})});assert.equal(r.status,200);return r.json() as Promise<any>;};
 await foreignPoll();const foreignSession=await op('session_start',{projectId:foreignProject,role:'pm',requestId:randomUUID(),text:'No model launch'},outsider);
 const foreignCommand=(await foreignPoll()).commands.find((c:any)=>c.executionId===foreignSession.executionId);assert.ok(foreignCommand);
 const forged=await fetch(base+'/api/hosts/agent-tools',{method:'POST',headers:{authorization:'Bearer '+foreignHost.secret,'content-type':'application/json'},body:JSON.stringify({hostId:'pm-foreign-host',commandId:foreignCommand.id,executionId:foreignSession.executionId,agentToolKey:requestKey,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'task_update',arguments:{taskId:foreignTask.id,title:'Forged receipt'}}}})});
 assert.equal(forged.status,409,'another valid ordinary director cannot reuse the receipt identity');
 assert.equal((await op('task_get',{taskId:foreignTask.id},outsider)).title,'Other director task');

 await op('project_initial_access_revoke',{projectId:target,baseAccessVersion:1},'owner@trial.test');
 assert.equal((await rpc(requestKey,'task_update',{taskId:task.id,title:'Committed title'})).status,403,'implicit target cannot return old data after grants shrink');
 assert.equal((await op('task_get',{taskId:task.id},'owner@trial.test')).title,'Committed title');

});
