import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from './test-app.ts';
import {decideTransition} from '../../packages/coordinator/agent-tools.ts';
import {resumeArgv,forkArgv} from '../../packages/coordinator/resume-argv.ts';
test('agent tools: queue, handoff, fork, resume, ask/reply, worker_done, stop outcome, reconcile (fake native)',async()=>{
 const config={database:mkdtempSync(tmpdir()+'/fleet-agent-')+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac'};let app=createApp(config);
 const call=async(url:string,payload?:any,machine=false)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload}));
 const op=async(name:string,body:any)=>(await call('/api/ops/'+name,body)).json();
 const poll=async(capabilities?:string[])=>(await call('/api/hosts/poll',{hostId:'mac',...(capabilities?{capabilities}:{})},true)).json().commands;
 const receipt=(c:any,status:string,nativeSessionId?:string)=>call('/api/hosts/receipts',{hostId:'mac',commandId:c.id,executionId:c.executionId,status,...(nativeSessionId?{nativeSessionId}:{})},true);
 // 3) resume table
 assert.deepEqual(resumeArgv('codex',{id:'x'}),['codex','resume','x']);assert.deepEqual(forkArgv('claude',{id:'x'}),['claude','--resume','x','--fork-session']);
 assert.equal(decideTransition({key:'codex:',model:'a',effort:'low',config:'{}'},{key:'codex:',model:'b',effort:'low',config:'{}'}),'switch_model_in_session');
 const s=(await call('/api/sessions',{})).json().sessionId,path='/api/sessions/'+s+'/messages';
 const first=(await call(path,{requestId:'one',text:'Build'})).json();
 // 5) busy session queues instead of 409
 const q=(await call(path,{requestId:'two',text:'Then this'})).json();assert.equal(q.queued,true);assert.equal(q.position,1);
 let [c]=await poll(['steer']);await receipt(c,'started','native-codex');
 await call('/api/hosts/events',{hostId:'mac',executionId:first.id,events:[{id:'a',seq:0,type:'assistant',data:{fleetNormalized:{blocks:[{kind:'text',role:'agent',markdown:'built it'}]}}}]},true);
 const steer=(await call(path,{requestId:'three',text:'steer me',delivery:'steer'})).json();assert.equal(steer.state,'steering');
 const sc=(await poll()).find((x:any)=>x.kind==='steer');await receipt(sc,'failed');
 // 4) ask/reply + worker_done
 const asked=(await call('/api/hosts/agent-tools',{hostId:'mac',commandId:c.id,executionId:c.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'ask',arguments:{requestId:'q1',question:'Colors?'}}}},true)).json();
 const question=JSON.parse(asked.result.content[0].text);assert.equal(question.state,'open');
 const other=(await call('/api/sessions',{})).json().sessionId;
 for(const [name,args] of [['message_send',{sessionId:other,requestId:'x',text:'x'}],['session_transition',{sessionId:other,profileId:'baseline'}],['session_fork',{sessionId:other,requestId:'x'}],['session_transition',{profileId:'baseline',account:'made-up-account'}],['session_transition',{profileId:'no-such-profile'}],['reconcile',{}],['execution_status',{sessionId:other}]] as const)
  assert.equal((await call('/api/hosts/agent-tools',{hostId:'mac',commandId:c.id,executionId:c.executionId,request:{jsonrpc:'2.0',id:2,method:'tools/call',params:{name,arguments:args}}},true)).statusCode,403,name);
 const ownQueue=JSON.parse((await call('/api/hosts/agent-tools',{hostId:'mac',commandId:c.id,executionId:c.executionId,request:{jsonrpc:'2.0',id:3,method:'tools/call',params:{name:'queue_list',arguments:{}}}},true)).json().result.content[0].text);
 assert.ok(ownQueue.queued.every((x:any)=>x.sessionId===s));
 const replied=await op('reply',{questionId:question.id,answer:'Blue'});assert.equal(replied.delivery.state,'queued');
 assert.equal((await op('worker_done',{executionId:first.id,outcome:'maybe'})).code,'invalid_outcome');
 assert.equal((await op('worker_done',{executionId:first.id,outcome:'succeeded',summary:'ok',nativeSessionId:'native-codex'})).action,'accepted');
 assert.equal((await call('/api/sessions/'+s)).json().nativeSessionId,'native-codex');
 await receipt(c,'completed');
 // queued messages promote in order; worker_done on superseded execution is stale
 let next=(await poll()).filter((x:any)=>x.kind==='start');assert.equal(next.length,1);assert.equal(next[0].payload.text,'Then this');assert.equal(next[0].payload.nativeSessionId,'native-codex');
 assert.equal((await op('worker_done',{executionId:first.id,outcome:'succeeded',summary:'ok'})).code,'stale_dispatch');
 // 6) stop outcome unknown until a host receipt proves it; reconcile after restart reports without promotion
 const stop=(await call('/api/executions/'+next[0].executionId+'/stop',{})).json();assert.equal(stop.stop.outcome,'unknown');
 await receipt(next[0],'unknown');await app.close();app=createApp(config);
 const rec=(await call('/api/reconciliation')).json().reconciliation;assert.ok(rec.items.some((i:any)=>i.classification==='unknown'&&i.next==='session_fork'));
 assert.equal((await op('execution_status',{executionId:next[0].executionId})).stop.outcome,'unknown');
 // 2) fork the stuck session: context transfer, original untouched
 const f=await op('session_fork',{sessionId:s,requestId:'f1',text:'continue'});assert.equal(f.strategy,'context_transfer');
 const fc=(await poll()).find((x:any)=>x.sessionId===f.sessionId);assert.match(fc.payload.text,/built it/);assert.match(fc.payload.text,/Current request:\ncontinue/);assert.equal(fc.payload.nativeSessionId,undefined);
 assert.equal((await call('/api/sessions/'+s)).json().executions.at(-1).state,'unknown');
 await receipt(fc,'started','native-fork');assert.equal((await op('session_handoffs',{sessionId:f.sessionId})).handoffs[0].state,'delivered');await receipt(fc,'completed');
 // 1) cross-harness handoff: new native session with full summary, then back with delta into the old one
 const t=await op('session_transition',{sessionId:f.sessionId,profileId:'claude-worker'});assert.equal(t.transition,'create_with_handoff');assert.equal(t.nativeSessionId,null);
 const back=await op('session_transition',{sessionId:f.sessionId,profileId:'baseline'});assert.equal(back.transition,'create_with_handoff');assert.equal(back.nativeSessionId,'native-fork');
 assert.equal((await op('session_resume',{sessionId:f.sessionId})).nativeSessions['codex:'].nativeSessionId,'native-fork');
 const tools=(await call('/api/tools',{jsonrpc:'2.0',id:1,method:'tools/list'})).json();assert.ok(tools.result.tools.some((x:any)=>x.name==='session_fork'));
 await app.close();
});

test('worker_done never sets an unconfirmed native session id',async()=>{
 const config={database:mkdtempSync(tmpdir()+'/fleet-agent-')+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac'};const app=createApp(config);try{
 const call=async(url:string,payload?:any,machine=false)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload}));
 const s=(await call('/api/sessions',{})).json().sessionId,e=(await call('/api/sessions/'+s+'/messages',{requestId:'one',text:'x'})).json();
 const r=(await call('/api/ops/worker_done',{executionId:e.id,outcome:'succeeded',nativeSessionId:'--evil'})).json();assert.equal(r.action,'accepted');
 assert.equal((await call('/api/sessions/'+s)).json().nativeSessionId,undefined);
 }finally{await app.close()}});
test('readiness per environment host; start carries setup; archive sends teardown',async()=>{
 const dir=mkdtempSync(tmpdir()+'/fleet-ready-');mkdirSync(dir+'/projects/p1',{recursive:true});
 writeFileSync(dir+'/projects/p1/project.json',JSON.stringify({id:'p1',name:'P1',contextFiles:[],repos:[{id:'main',remote:'git@example.com:a/b.git',defaultBranch:'main'}],environments:[{hostId:'mac',checkoutPath:'/x'},{hostId:'pds',checkoutPath:'/y'}]}));
 const app=createApp({database:dir+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac',projectConfigDirectory:dir});
 const call=async(url:string,payload?:any,machine=false)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload}));
 const op=async(name:string,body:any)=>(await call('/api/ops/'+name,body)).json();
 const poll=async(hostId='mac')=>(await call('/api/hosts/poll',{hostId},true)).json().commands;
 const r=await op('project_readiness',{projectId:'p1'});assert.deepEqual(r.hosts.map((h:any)=>h.hostId),['mac','pds']);
 // 'pds' is neither configured nor ever polled: no run is created for it, the answer says why.
 assert.deepEqual(r.hosts[1],{hostId:'pds',executionId:null,problem:'unregistered host pds'});assert.ok(r.hosts[0].executionId);
 const [rc]=await poll();assert.equal(rc.kind,'readiness');assert.equal(rc.payload.project.repos[0].id,'main');
 await call('/api/hosts/events',{hostId:'mac',executionId:rc.executionId,events:[{id:'r',seq:0,type:'readiness',data:{hostId:'mac',ok:false,checks:[{check:'remote',status:'failed',reason:'ls-remote-exit-128'}]}}]},true);
 assert.equal((await op('execution_status',{executionId:rc.executionId})).readiness[0].checks[0].reason,'ls-remote-exit-128');
 const s=(await op('session_create',{})).sessionId;
 await op('message_send',{sessionId:s,requestId:'one',text:'Go',setup:'skip'});
 const start=(await poll()).find((x:any)=>x.kind==='start');assert.equal(start.payload.setup,'skip');
 assert.equal((await call('/api/ops/session_archive',{sessionId:s})).statusCode,409);
 await call('/api/hosts/receipts',{hostId:'mac',commandId:start.id,executionId:start.executionId,status:'completed'},true);
 const t=await op('session_archive',{sessionId:s});assert.equal(t.kind,'teardown');assert.equal(t.executionId,start.executionId);
 assert.equal((await op('session_archive',{sessionId:s})).id,t.id);
 await app.close();
});
test('session_start binds a project session; finished child wakes its creator; session_deliver opens a PR step',async()=>{
 const dir=mkdtempSync(tmpdir()+'/fleet-start-');mkdirSync(dir+'/projects/p1',{recursive:true});mkdirSync(dir+'/agents');
 writeFileSync(dir+'/agents/w.json',JSON.stringify({id:'w',model:'gpt-6.1-sol',effort:'low',instructions:'x',mcpServers:[]}));
 writeFileSync(dir+'/projects/p1/project.json',JSON.stringify({id:'p1',name:'P1',contextFiles:[],roles:{pm:'w',worker:'w'},repos:[{id:'main',remote:'git@example.com:a/b.git'}],environments:[{hostId:'mac',checkoutPath:'/x'}]}));
 const app=createApp({database:dir+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac',integrationHostId:'prog',projectConfigDirectory:dir,profilesDirectory:dir+'/agents'});
 const call=async(url:string,payload?:any,machine=false)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload}));
 const op=async(name:string,body:any)=>(await call('/api/ops/'+name,body)).json();
 const poll=async(hostId='mac')=>(await call('/api/hosts/poll',{hostId},true)).json().commands;
 const done=(c:any)=>call('/api/hosts/receipts',{hostId:'mac',commandId:c.id,executionId:c.executionId,status:'completed'},true);
 const tool=async(c:any,name:string,args:any)=>JSON.parse((await call('/api/hosts/agent-tools',{hostId:'mac',commandId:c.id,executionId:c.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name,arguments:args}}},true)).json().result.content[0].text);
 const pm=await op('session_start',{projectId:'p1',role:'pm',requestId:'pm1',text:'Plan'});assert.equal(pm.profileId,'w');
 assert.equal((await op('session_start',{projectId:'p1',role:'pm',requestId:'pm1'})).sessionId,pm.sessionId);
 const [pc]=await poll();assert.equal(pc.payload.projectId,'p1');assert.equal(pc.payload.gitNetwork,true);
 const child=await tool(pc,'session_start',{role:'worker',requestId:'w1',text:'Do it'});
 assert.equal((await call('/api/hosts/agent-tools',{hostId:'mac',commandId:pc.id,executionId:pc.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'session_start',arguments:{projectId:'other',role:'worker',requestId:'w2'}}}},true)).statusCode,403);
 // Readiness from a host agent: the caller reads its own readiness runs.
 const rd=(args:any)=>call('/api/hosts/agent-tools',{hostId:'mac',commandId:pc.id,executionId:pc.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'project_readiness',arguments:args}}},true);
 const ready=JSON.parse((await rd({projectId:'p1'})).json().result.content[0].text);assert.equal(ready.projectId,'p1');
 const rs=(await op('execution_status',{executionId:ready.hosts[0].executionId})).sessionId;
 assert.equal(JSON.parse((await call('/api/hosts/agent-tools',{hostId:'mac',commandId:pc.id,executionId:pc.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'execution_status',arguments:{sessionId:rs}}}},true)).json().result.content[0].text).executionId,ready.hosts[0].executionId,'the caller reads its readiness run');
 assert.equal((await call('/api/hosts/agent-tools',{hostId:'mac',commandId:pc.id,executionId:pc.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'message_send',arguments:{sessionId:rs,requestId:'x',text:'hi'}}}},true)).statusCode,403,'reading grants no ownership');
 await done(pc);const cc=(await poll()).find((x:any)=>x.sessionId===child.sessionId);await done(cc);
 const wake=(await poll()).find((x:any)=>x.sessionId===pm.sessionId);assert.match(wake.payload.text,new RegExp('your session '+child.sessionId));
 const d=await tool(wake,'session_deliver',{sessionId:child.sessionId});assert.equal(d.branch,'fleet/'+child.sessionId);
 const [ic]=await poll('prog');assert.equal(ic.payload.branch,d.branch);assert.equal((await tool(wake,'session_deliver',{sessionId:child.sessionId})).id,d.id);
 assert.ok((await op('session_get',{sessionId:pm.sessionId})).executions.length===2);
 await app.close();
});

test('session_start with hostId runs its turns only on that host; an unknown host is refused',async()=>{
 const dir=mkdtempSync(tmpdir()+'/fleet-start-host-');mkdirSync(dir+'/projects/p1',{recursive:true});mkdirSync(dir+'/agents');
 writeFileSync(dir+'/agents/w.json',JSON.stringify({id:'w',model:'gpt-6.1-sol',effort:'low',instructions:'x',mcpServers:[]}));
 writeFileSync(dir+'/projects/p1/project.json',JSON.stringify({id:'p1',name:'P1',contextFiles:[],roles:{worker:'w'},repos:[],environments:[{hostId:'mac',checkoutPath:'/x'},{hostId:'win',checkoutPath:'/y'}]}));
 const app=createApp({database:dir+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac',nativeHostIds:['win'],projectConfigDirectory:dir,profilesDirectory:dir+'/agents'});
 const op=async(name:string,body:any)=>(await app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer b'},payload:body}));
 try{
  assert.equal((await op('session_start',{projectId:'p1',role:'worker',requestId:'h1',hostId:'win',text:'Hi'})).json().hostId,'win');
  assert.equal((await op('session_start',{projectId:'p1',role:'worker',requestId:'h2',hostId:'nope',text:'Hi'})).statusCode,400);
 }finally{await app.close()}
});

test('a session starts for a task or a taskless session attaches to one; its thread is the task thread, nothing copied',async()=>{
 const dir=mkdtempSync(tmpdir()+'/fleet-task-session-');mkdirSync(dir+'/projects/p1',{recursive:true});mkdirSync(dir+'/projects/p2',{recursive:true});mkdirSync(dir+'/agents');
 writeFileSync(dir+'/agents/w.json',JSON.stringify({id:'w',model:'gpt-6.1-sol',effort:'low',instructions:'x',mcpServers:[]}));
 for(const p of ['p1','p2'])writeFileSync(dir+`/projects/${p}/project.json`,JSON.stringify({id:p,name:p,contextFiles:[],roles:{pm:'w'},repos:[],environments:[{hostId:'mac',checkoutPath:'/x'}]}));
 const app=createApp({database:dir+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac',projectConfigDirectory:dir,profilesDirectory:dir+'/agents'});
 const op=async(name:string,body:any)=>{const r=await app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer b'},payload:body});return {status:r.statusCode,body:r.json()}};
 try{const task=(await op('task_create',{title:'T',projectId:'p1'})).body.id,other=(await op('task_create',{title:'U',projectId:'p1'})).body.id,elsewhere=(await op('task_create',{title:'V',projectId:'p2'})).body.id;
  // Started for the task: its execution carries the task.
  const a=(await op('session_start',{projectId:'p1',role:'pm',requestId:'a',taskId:task,text:'hi'})).body;assert.equal(a.taskId,task);
  assert.equal((await (app as any).store.query('SELECT task_id FROM execution WHERE id=$1',[a.executionId]))[0].task_id,task);
  assert.equal((await op('session_start',{projectId:'p1',role:'pm',requestId:'x',taskId:elsewhere})).status,409);
  // A taskless session with history attaches: its earlier execution joins the task thread unchanged (task_id stays null).
  const b=(await op('session_start',{projectId:'p1',role:'pm',requestId:'b',text:'early'})).body;
  assert.equal((await op('session_task_attach',{sessionId:b.sessionId,taskId:elsewhere})).status,409);
  // As a host agent sees it (bound sets boundProject): a task outside exactly its project, a projectless one included, is refused.
  const loose=(await op('task_create',{title:'L'})).body.id;assert.equal((await op('session_task_attach',{sessionId:b.sessionId,taskId:loose,boundProject:'p1'})).status,409);
  assert.deepEqual((await op('session_task_attach',{sessionId:b.sessionId,taskId:task})).body,{sessionId:b.sessionId,taskId:task});
  assert.equal((await op('session_task_attach',{sessionId:b.sessionId,taskId:task})).body.existing,true);
  assert.equal((await op('session_task_attach',{sessionId:b.sessionId,taskId:other})).status,409);
  assert.equal((await (app as any).store.query('SELECT task_id FROM execution WHERE id=$1',[b.executionId]))[0].task_id,null);
  const thread=(await op('thread_entries',{key:{session:b.sessionId},resolve:true,limit:50})).body;
  assert.deepEqual(thread.key,{task});assert.deepEqual(thread.sessionIds.sort(),[a.sessionId,b.sessionId].sort());
  assert.deepEqual(thread.entries.filter((x:any)=>x.kind==='text').map((x:any)=>x.markdown),['hi','early']);
 }finally{await app.close()}
});
