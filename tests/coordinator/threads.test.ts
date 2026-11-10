// Conversation threads (docs/designs/fleet-conversations.md, Stage 1 item 13).
import {test} from 'node:test';import assert from 'node:assert/strict';import {readFileSync} from 'node:fs';
import {createStore} from '../../packages/coordinator/store.ts';
import {threads} from '../../packages/coordinator/threads.ts';
import {execFileSync} from 'node:child_process';
import {executorBinary} from './executor-binary.ts';

const fresh=async(url='memory://')=>{const s=createStore(url);await s.migrate();
 const t=threads({query:s.query,artifacts:async(id)=>id==='with-result'?[{id:'a1',state:'stored'}]:[],cursor:async()=>(await s.changes.since(null)).cursor});
 const read=<T>(fn:()=>Promise<T>)=>s.read(fn);
 const op={entries:(b:any)=>read(()=>t.entries(b)),blocks:(b:any)=>read(()=>t.blocks(b)),search:(b:any)=>read(()=>t.search(b)),export:(b:any)=>read(()=>t.exportText(b,read))};
 const exec=async(id:string,o:{session:string,task?:string,harness?:string,model?:string,effort?:string,state?:string})=>(await s.query("INSERT INTO execution(id,kind,state,session_id,task_id,harness,model,config_ref,legacy_kind,legacy_id) VALUES($1,'agent_run',$2,$3,$4,$5,$6,$7,'execution',$1) RETURNING seq",[id,o.state??'running',o.session,o.task??null,o.harness??'codex',o.model??'gpt-6.1-sol',JSON.stringify({effort:o.effort??'low'})]))[0].seq;
 const doc=async(kind:string,id:string,data:any)=>Number((await s.query('INSERT INTO document(kind,id,data) VALUES($1,$2,$3) RETURNING seq',[kind,id,JSON.stringify({id,...data})]))[0].seq);
 const msg=(id:string,executionId:string,sessionId:string,text:string)=>doc('message',id,{executionId,sessionId,text});
 const ev=(id:string,executionId:string,sessionId:string,blocks:any[],extra:any={})=>doc(extra.kind??'event',id,{executionId,sessionId,type:extra.type??'item.completed',data:{fleetNormalized:{blocks,...extra.fn}}});
 const say=(t:string)=>({kind:'text',role:'agent',markdown:t});
 await s.query("INSERT INTO document(kind,id,data) VALUES('session','s1','{\"id\":\"s1\"}'),('session','s2','{\"id\":\"s2\"}')");
 return {s,op,exec,doc,msg,ev,say}};
const thr=(f:any)=>threads({query:f.s.query,artifacts:async()=>[],cursor:async()=>0});
const texts=(r:any)=>r.entries.map((b:any)=>b.kind==='text'?b.markdown:b.kind);

test('paging newest, before, after and around by block key; two blocks of one event never repeat or drop',async()=>{const f=await fresh();
 try{await f.exec('x1',{session:'s1'});await f.msg('m1','x1','s1','hi');
  for(let i=0;i<5;i++)await f.ev('e'+i,'x1','s1',[f.say('a'+i),{kind:'tool',name:'t'+i,input:null,output:'ok',state:'completed'}]);
  await f.ev('noise','x1','s1',[]);
  const all=await f.op.entries({key:{session:'s1'},limit:100});
  assert.deepEqual(texts(all),['boundary','hi','a0','tool','a1','tool','a2','tool','a3','tool','a4','tool']);
  assert.equal(all.total,12);assert.equal(all.hasBefore,false);assert.equal(typeof all.feedCursor,'number');
  const newest=await f.op.entries({key:{session:'s1'},limit:3});assert.deepEqual(texts(newest),['tool','a4','tool']);assert.equal(newest.hasBefore,true);
  // Walk up in pages of 3: every block once, in order.
  let seen=[...newest.entries],page=newest;while(page.hasBefore){page=await f.op.entries({key:{session:'s1'},limit:3,before:page.first});seen=[...page.entries,...seen]}
  assert.deepEqual(seen.map(b=>b.key),all.entries.map((b:any)=>b.key));
  const after=await f.op.entries({key:{session:'s1'},limit:4,after:all.entries[3].key});assert.deepEqual(after.entries.map((b:any)=>b.key),all.entries.slice(4,8).map((b:any)=>b.key));assert.equal(after.hasAfter,true);
  // Odd limit: floor(5/2)=2 before, the rest from the key on; a short side is not refilled.
  const around=await f.op.entries({key:{session:'s1'},limit:5,around:all.entries[6].key});assert.deepEqual(around.entries.map((b:any)=>b.key),all.entries.slice(4,9).map((b:any)=>b.key));
  const edge=await f.op.entries({key:{session:'s1'},limit:5,around:all.entries[1].key});assert.deepEqual(edge.entries.map((b:any)=>b.key),all.entries.slice(0,4).map((b:any)=>b.key));assert.equal(edge.hasBefore,false);
  const agent=all.entries.find((b:any)=>b.markdown==='a0');assert.deepEqual([agent.harness,agent.model,agent.effort,agent.plain],['codex','gpt-6.1-sol','low','a0']);
  await assert.rejects(f.op.entries({key:{session:'s1'},limit:0}),/limit/);await assert.rejects(f.op.entries({key:{session:'nope'},limit:1}),/Not found/);
 }finally{await f.s.close()}});

test('parallel executions interleave by seq; a taskless session that later gets a task shows its early history',async()=>{const f=await fresh();
 try{const task=await f.s.tasks.create({title:'T'});
  await f.exec('early',{session:'s1'});await f.msg('m0','early','s1','before task');await f.ev('e0','early','s1',[f.say('early reply')]);
  await f.exec('a',{session:'s1',task});await f.exec('b',{session:'s2',task});
  await f.ev('ea','a','s1',[f.say('from a')]);await f.ev('eb','b','s2',[f.say('from b')]);await f.ev('ea2','a','s1',[f.say('a again')]);
  const r=await f.op.entries({key:{task},limit:100});
  assert.deepEqual(texts(r),['boundary','before task','early reply','boundary','boundary','from a','from b','a again']);
  assert.deepEqual(texts(await f.op.entries({key:{session:'s2'},limit:100})),['boundary','from b']);
  // A session shows the thread of the first task it got; the page lists the thread's sessions for the live feed.
  assert.deepEqual(await f.s.read(()=>thr(f).keyOf('s1')),{task});assert.deepEqual((await f.op.entries({key:{task},limit:1})).sessionIds.sort(),['s1','s2']);
 }finally{await f.s.close()}});

test('streams: an open or cut-off stream is one entry at its first chunk; a closed stream is the closing event block',async()=>{const f=await fresh();
 try{await f.exec('g',{session:'s1',harness:'antigravity'});
  const chunk=(id:string,t:string,opens=false)=>f.ev(id,'g','s1',[f.say(t)],{kind:'event-delta',type:'acp.update',fn:{deltaOf:'text:1',...(opens?{opens:true}:{})}});
  await chunk('c1','Hel',true);await chunk('c2','lo');
  let r=await f.op.entries({key:{session:'s1'},limit:10});assert.deepEqual(texts(r),['boundary','Hello']);assert.equal(r.entries[1].stream,'open');assert.equal(r.total,2);
  // A fed chunk asks for its stream's entry.
  assert.deepEqual((await f.op.blocks({key:{session:'s1'},rows:[{kind:'event',id:'c2'}]})).entries.map((b:any)=>b.markdown),['Hello']);
  await f.s.query("UPDATE execution SET state='failed' WHERE id='g'");
  r=await f.op.entries({key:{session:'s1'},limit:10});assert.equal(r.entries[1].stream,'cut-off');
  await f.ev('end','g','s1',[f.say('Hello')],{type:'turn.completed',fn:{closes:['text:1']}});
  const fed=await f.op.blocks({key:{session:'s1'},rows:[{kind:'event',id:'end'}]});assert.deepEqual(fed.removed,[(await f.op.entries({key:{session:'s1'},limit:10})).entries[0].key.replace(/^\d+/,String(await f.s.query("SELECT seq FROM document WHERE id='c1'").then(x=>x[0].seq)))]);assert.deepEqual(fed.entries.map((b:any)=>b.markdown),['Hello']);
  r=await f.op.entries({key:{session:'s1'},limit:10});assert.deepEqual(texts(r),['boundary','Hello']);assert.equal(r.entries[1].stream,undefined);assert.equal(r.entries[1].key.split(':')[0],String(await f.s.query("SELECT seq FROM document WHERE id='end'").then(x=>x[0].seq)));
 }finally{await f.s.close()}});

test('execution.ended places the result block; it is written once',async()=>{const f=await fresh();
 try{await f.s.put('session',{id:'s3'});const e={id:'with-result',executionId:'with-result',sessionId:'s3',state:'started'};await f.s.put('execution',e);
  await f.s.put('execution',{...e,state:'completed',finishedAt:Date.now()});await f.s.put('execution',{...e,state:'completed',finishedAt:Date.now()});
  await f.s.executions.transition('with-result','succeeded').catch(()=>{});
  assert.equal((await f.s.query("SELECT count(*)::int AS n FROM document WHERE data->>'type'='execution.ended'"))[0].n,1);
  const r=await f.op.entries({key:{session:'s3'},limit:10});assert.deepEqual(texts(r),['boundary','result']);assert.deepEqual(r.entries[1].artifactIds,['a1']);
  await f.s.query("INSERT INTO document(kind,id,data) VALUES('artifact','legacy-a',$1)",[JSON.stringify({id:'legacy-a',executionId:'with-result'})]);
  assert.deepEqual((await f.op.blocks({key:{session:'s3'},rows:[{kind:'artifact',id:'legacy-a'}]})).entries.map((b:any)=>b.kind),['result']);
 }finally{await f.s.close()}});

test('search finds blocks newest first and a hit opens with around; thread_blocks by fed rows; export with and without a range',async()=>{const f=await fresh();
 try{await f.exec('x',{session:'s1'});await f.msg('m','x','s1','run migration 6 please');
  await f.ev('e1','x','s1',[f.say('Migration 6 is done'),{kind:'tool',name:'psql',input:{sql:'select 1'},output:'1',state:'completed'}]);await f.ev('e2','x','s1',[f.say('Something else, 100% sure')]);
  const s=await f.op.search({key:{session:'s1'},q:'migration 6',limit:10});assert.deepEqual(s.hits.map((h:any)=>h.snippet),['Migration 6 is done','run migration 6 please']);
  assert.equal((await f.op.search({key:{session:'s1'},q:'migration 6',limit:1})).hasMore,true);
  assert.deepEqual((await f.op.search({key:{session:'s1'},q:'migration 6',limit:10,before:s.hits[0].key})).hits.length,1);
  assert.equal((await f.op.search({key:{session:'s1'},q:'100%',limit:10})).hits.length,1);
  await f.ev('e3','x','s1',[f.say('run "make test"\nnext line')]);assert.equal((await f.op.search({key:{session:'s1'},q:'run "make',limit:10})).hits.length,1);assert.equal((await f.op.search({key:{session:'s1'},q:'_',limit:10})).hits.length,0);
  const around=await f.op.entries({key:{session:'s1'},limit:2,around:s.hits[1].key});assert.equal(around.entries[1].key,s.hits[1].key);
  assert.deepEqual((await f.op.blocks({key:{session:'s1'},rows:[{kind:'event',id:'e1'},{kind:'execution',id:'x'},{kind:'event',id:'elsewhere'}]})).entries.map((b:any)=>b.kind),['boundary','text','tool']);
  const whole=(await f.op.export({key:{session:'s1'}})).markdown;
  assert.match(whole,/\*\*You:\*\*\n\nrun migration 6 please/);assert.match(whole,/\*\*Agent \(gpt-6\.1-sol\):\*\*\n\nMigration 6 is done/);assert.match(whole,/```\n\{\n "sql": "select 1"\n\}\n```/);
  const [m,a]=[s.hits[1].key,s.hits[0].key];
  const part=(await f.op.export({key:{session:'s1'},from:{key:m,offset:4},to:{key:a,offset:9}})).markdown;
  assert.equal(part,'**You:**\n\nmigration 6 please\n\n**Agent (gpt-6.1-sol):**\n\nMigration\n\n');
 }finally{await f.s.close()}});

test('captured fixtures: stored projections equal the executor output and give blocks for codex, claude and the lost-receipt case',async()=>{const f=await fresh(),bin=executorBinary();
 try{for(const name of ['codex-6a55ab8b','claude-127d6fd7','lost-receipt-d89cc199']){const fx=JSON.parse(readFileSync(new URL(`./fixtures/conversations/${name}.json`,import.meta.url),'utf8')),e=fx.execution;
   await f.s.query("INSERT INTO document(kind,id,data) VALUES('session',$1,$2) ON CONFLICT DO NOTHING",[e.session_id,JSON.stringify({id:e.session_id})]);
   await f.s.query("INSERT INTO execution(id,kind,state,session_id,harness,model,config_ref,legacy_kind,legacy_id,seq) VALUES($1,'agent_run',$2,$3,$4,$5,$6,'execution',$1,$7)",[e.id,e.state,e.session_id,e.harness,e.model,JSON.stringify({effort:e.effort}),e.seq]);
   // Shape check: the stored projection is exactly what the executor writes for the native record.
   const native=fx.documents.filter((d:any)=>d.data.data?.fleetNormalized);
   const out=execFileSync(bin,['reproject'],{input:native.map((d:any)=>{const {fleetNormalized,...record}=d.data.data;return JSON.stringify({executionId:e.id,harness:e.harness,timestamp:d.data.timestamp,record})}).join('\n')+'\n',encoding:'utf8'}).trim().split('\n').map(l=>JSON.parse(l));
   native.forEach((d:any,i:number)=>assert.deepEqual(d.data.data.fleetNormalized,out[i],`${name} ${d.data.id}`));
   for(const d of fx.documents)await f.s.query('INSERT INTO document(kind,id,data,seq) VALUES($1,$2,$3,$4)',[d.kind,d.data.id,JSON.stringify(d.data),d.seq])}
  const reply=async(session:string)=>(await f.op.entries({key:{session},limit:100})).entries;
  const codex=await reply('c2c11096-a08e-4b86-88d9-eb6c46785b11');assert.deepEqual(codex.filter((b:any)=>b.kind==='text').map((b:any)=>[b.role,b.markdown]),[['operator','hi'],['agent','Hi! What would you like to work on?']]);
  const claude=await reply((await f.s.query("SELECT session_id FROM execution WHERE id LIKE '127d6fd7%'"))[0].session_id);
  assert.ok(claude.some((b:any)=>b.kind==='text'&&b.role==='agent'&&b.markdown==='Shalom! How can I help?'&&b.harness==='claude'));
  const lost=await reply('05e696a1-f925-4d58-abe1-8073f74eb965');
  assert.deepEqual(lost.filter((b:any)=>b.kind==='tool').map((b:any)=>[b.name,b.state]),[['session_start','failed'],['execution_status','failed']]);
  assert.ok(lost.filter((b:any)=>b.kind==='tool').every((b:any)=>b.endedAt>b.startedAt));
  assert.equal(lost.filter((b:any)=>b.kind==='text'&&b.role==='agent').length,2);
 }finally{await f.s.close()}});

test('a row committed after a page read is fed from the page feedCursor',async()=>{const f=await fresh();
 try{await f.exec('x',{session:'s1'});const page=await f.op.entries({key:{session:'s1'},limit:10});
  await f.ev('late','x','s1',[f.say('late')]);
  const fed=await f.s.changes.since(page.feedCursor);assert.ok(fed.changes.some(c=>c.kind==='event'&&c.id==='late'));
  const delta=await f.ev('d','x','s1',[f.say('x')],{kind:'event-delta',fn:{deltaOf:'text:1',opens:true}});assert.ok(delta);
  assert.ok((await f.s.changes.since(page.feedCursor)).changes.some(c=>c.kind==='event'&&c.id==='d'));
 }finally{await f.s.close()}});
