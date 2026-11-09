import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from '../../packages/coordinator/app.js';
import {decideTransition} from '../../packages/coordinator/agent-tools.js';
import {resumeArgv,forkArgv} from '../../packages/coordinator/resume-argv.js';
test('agent tools: queue, handoff, fork, resume, ask/reply, worker_done, stop outcome, reconcile (fake native)',async()=>{
 const config={database:mkdtempSync(tmpdir()+'/fleet-agent-')+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac'};let app=createApp(config);
 const call=async(url:string,payload?:any,machine=false)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload}));
 const op=async(name:string,body:any)=>(await call('/api/agent-operations/'+name,body)).json();
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
 await call('/api/hosts/events',{hostId:'mac',executionId:first.id,events:[{id:'a',seq:0,type:'assistant',text:'built it'}]},true);
 const steer=(await call(path,{requestId:'three',text:'steer me',delivery:'steer'})).json();assert.equal(steer.state,'steering');
 const sc=(await poll()).find((x:any)=>x.kind==='steer');await receipt(sc,'failed');
 // 4) ask/reply + worker_done
 const asked=(await call('/api/hosts/agent-tools',{hostId:'mac',commandId:c.id,executionId:c.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'ask',arguments:{requestId:'q1',question:'Colors?'}}}},true)).json();
 const question=JSON.parse(asked.result.content[0].text);assert.equal(question.state,'open');
 const other=(await call('/api/sessions',{})).json().sessionId;
 for(const [name,args] of [['message_send',{sessionId:other,requestId:'x',text:'x'}],['session_transition',{sessionId:other,profileId:'baseline'}],['session_fork',{sessionId:other,requestId:'x'}],['session_transition',{profileId:'baseline',account:'made-up-account'}],['session_transition',{profileId:'no-such-profile'}],['reconcile',{}]] as const)
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
 const tools=(await call('/api/agent-tools',{jsonrpc:'2.0',id:1,method:'tools/list'})).json();assert.ok(tools.result.tools.some((x:any)=>x.name==='session_fork'));
 await app.close();
});

test('worker_done never sets an unconfirmed native session id',async()=>{
 const config={database:mkdtempSync(tmpdir()+'/fleet-agent-')+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac'};const app=createApp(config);try{
 const call=async(url:string,payload?:any,machine=false)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload}));
 const s=(await call('/api/sessions',{})).json().sessionId,e=(await call('/api/sessions/'+s+'/messages',{requestId:'one',text:'x'})).json();
 const r=(await call('/api/agent-operations/worker_done',{executionId:e.id,outcome:'succeeded',nativeSessionId:'--evil'})).json();assert.equal(r.action,'accepted');
 assert.equal((await call('/api/sessions/'+s)).json().nativeSessionId,undefined);
 }finally{await app.close()}});
