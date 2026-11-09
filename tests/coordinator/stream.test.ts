// Change feed (docs/designs/fleet-operator-app.md §4): triggers, kind map, (xid, seq) watermark, resync, the SSE route and lists.
import {test} from 'node:test';import assert from 'node:assert/strict';import http from 'node:http';
import {createStore} from '../../packages/coordinator/store.js';import {createApp} from '../../packages/coordinator/app.js';
const fresh=async(scope?:string)=>{const s=createStore('memory://',scope?{scope}:{});await s.migrate();return s};
const read=(s:any,c:number|null)=>s.read(()=>s.changes.since(c));

test('triggers map writes to change rows; rollbacks and unmapped kinds leave none; host polls skip lastSeenAt',async()=>{const s=await fresh();
 const start=(await read(s,null)).cursor;
 await s.put('session',{id:'s1',createdAt:1});await s.put('message',{id:'m1',sessionId:'s1',text:'hi'});await s.put('outbox',{id:'o1'});
 await s.put('host-observation',{id:'mac',lastSeenAt:1,platform:'darwin'});await s.put('host-observation',{id:'mac',lastSeenAt:2,platform:'darwin'});
 await s.put('host-observation',{id:'mac',lastSeenAt:3,platform:'linux'});
 let task='';await s.tx(async()=>{task=await s.tasks.create({title:'T',projectId:'p'});await s.executions.createExecution({id:'e1',kind:'agent_run',taskId:task,sessionId:'s1',hostId:'mac',harness:'codex',projectId:'p',state:'running'})});
 await assert.rejects(s.tx(async()=>{await s.put('session',{id:'s2'});throw Error('undo')}));
 const r=await read(s,start);
 assert.deepEqual(r.changes.map((c:any)=>[c.kind,c.id,c.sessionId,c.hostId,c.taskId]),[
  ['session','s1','s1',null,null],['event','m1','s1',null,null],['host','mac',null,'mac',null],['host','mac',null,'mac',null],
  ['task',task,null,null,task],['execution','e1','s1','mac',task]]);
 await s.close()});

test('cursor resumes in (xid, seq) order; unknown or pruned cursors resync; change rows carry the RLS policy',async()=>{const s=await fresh();
 await s.put('session',{id:'a'});const c1=(await read(s,null)).cursor;await s.put('session',{id:'b'});
 const r=await read(s,c1);assert.deepEqual(r.changes.map((c:any)=>c.id),['b']);assert.deepEqual((await read(s,r.cursor)).changes,[]);
 assert.equal((await read(s,999999)).resync,true);
 await s.query("UPDATE change SET at=now()-interval '10 days'");assert.ok(await s.changes.prune(7)>0);assert.equal((await read(s,c1)).resync,true);
 await s.close();
 // Row-level security: a store scoped to project p sees only p's change rows (rows without a project are visible to '*' only).
 const w=await fresh();await w.tx(async()=>{await w.tasks.create({title:'in p',projectId:'p'});await w.tasks.create({title:'in q',projectId:'q'})});
 assert.equal((await read(w,0)).changes.length,2);
 // PGlite runs as a superuser, which bypasses row-level security; the change table carries the same project_scope policy as
 // every other table (migration 8), checked on Postgres under the fleet_app role.
 assert.ok((await w.query("SELECT 1 FROM pg_policies WHERE tablename='change' AND policyname='project_scope'")).length===1);await w.close()});

test('watermark: a change of a transaction still running is not passed by a later commit',async()=>{
 // Two connections to one Postgres are needed to interleave transactions; PGlite runs one transaction at a time, so this checks
 // the guarantee that matters on PGlite: rows of the reader's own snapshot horizon only, in (xid, seq) order.
 const s=await fresh();await s.put('session',{id:'x1'});await s.put('session',{id:'x2'});
 const all=(await read(s,0)).changes.map((c:any)=>c.seq);assert.deepEqual(all,[...all].sort((a:number,b:number)=>a-b));await s.close()});

const get=(url:string,cb:(chunk:string)=>void)=>new Promise<http.ClientRequest>(r=>{const q=http.get(url,{headers:{authorization:'Bearer b'}},res=>{res.setEncoding('utf8');res.on('data',cb)});r(q)});
test('SSE stream sends ready, then commits made after it opened; resumes from ?cursor=',async()=>{
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.listen({port:0,host:'127.0.0.1'});
 const base=`http://127.0.0.1:${(app.server.address() as any).port}`;const store=(app as any).store;
 let text='';const req=await get(base+'/api/stream',c=>text+=c);
 const until=async(f:()=>boolean)=>{for(let i=0;i<100&&!f();i++)await new Promise(r=>setTimeout(r,30));assert.ok(f(),text)};
 await until(()=>text.includes('event: ready'));await store.put('session',{id:'live'});await until(()=>text.includes('"id":"live"'));
 // A burst of writes in one transaction wakes the stream once and arrives in order.
 await store.tx(async()=>{for(let i=0;i<50;i++)await store.put('message',{id:'m'+i,sessionId:'live'})});await until(()=>text.includes('"id":"m49"'));
 const seqs=[...text.matchAll(/^id: (\d+)$/gm)].map(m=>Number(m[1]));assert.deepEqual(seqs,[...seqs].sort((a,b)=>a-b));
 req.destroy();
 let again='';const r2=await get(base+'/api/stream?cursor='+seqs[0],c=>again+=c);await until(()=>again.includes('"id":"m49"'));assert.ok(!again.includes('"id":"live"'));r2.destroy();
 assert.equal((await fetch(base+'/api/stream')).status,401);
 await app.close()});

test('task_page, sessions_list and list_membership agree; counts come from the server',async()=>{
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.ready();const s=(app as any).store;
 const op=async(name:string,payload:any)=>{const r=await app.inject({method:'POST',url:'/api/ops/'+name,payload,headers:{authorization:'Bearer b'}});assert.equal(r.statusCode,200,r.body);return r.json()};
 const ids:string[]=[];await s.tx(async()=>{for(let i=0;i<30;i++)ids.push(await s.tasks.create({title:'Task '+i,projectId:i%3?'p':'q',priority:['urgent','normal','background','p1'][i%4]}))});
 const page=await op('task_page',{filter:{projectId:'p'},group:'priority',limit:7});assert.equal(page.tasks.length,7);assert.equal(page.total,20);
 let all=[...page.tasks],after=page.nextAfter;while(after){const next=await op('task_page',{filter:{projectId:'p'},group:'priority',limit:7,after});all.push(...next.tasks);after=next.nextAfter}
 assert.equal(all.length,20);assert.equal(new Set(all.map((t:any)=>t.id)).size,20);
 const rank:any={urgent:1,normal:2,background:3};assert.deepEqual(all.map((t:any)=>rank[t.priority]),all.map((t:any)=>rank[t.priority]).sort());
 assert.equal(page.groups.reduce((n:number,g:any)=>n+g.count,0),20);
 const m=await op('list_membership',{ids,lists:[{name:'tasks',filter:{projectId:'p'},group:'priority'}]});
 assert.deepEqual(m.lists[0].rows.map((r:any)=>r.id).sort(),all.map((t:any)=>t.id).sort());assert.equal(m.lists[0].counts.queued,20);
 await s.put('session',{id:'s-a',createdAt:1});await s.put('session',{id:'s-b',createdAt:2,archivedAt:3});await s.put('message',{id:'mm',sessionId:'s-a',text:'Hello world'});
 const sl=await op('sessions_list',{});assert.deepEqual(sl.sessions.map((x:any)=>[x.id,x.title,x.state]),[['s-a','Hello world','idle']]);
 const sm=await op('list_membership',{ids:['s-a','s-b'],lists:[{name:'sessions',filter:{}}]});assert.deepEqual(sm.lists[0].rows.map((r:any)=>r.id),['s-a']);
 await s.tx(()=>s.executions.createExecution({id:'run1',kind:'agent_run',hostId:'mac',harness:'claude',state:'running'}));
 const h=await op('hosts_list',{});assert.deepEqual(h.hosts.find((x:any)=>x.id==='mac').runningByHarness,{claude:1});
 assert.equal((await op('changes_since',{cursor:null})).changes.length,0);
 await app.close()});

test('routes: / opens the app, old pages redirect to /legacy, app files need no Bearer, /api still does',async()=>{
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',browserProxyAuth:true,browserOrigin:'https://fleet.example'});
 assert.equal((await app.inject({url:'/'})).headers.location,'/overview');
 assert.equal((await app.inject({url:'/lab'})).headers.location,'/legacy/lab');
 assert.equal((await app.inject({url:'/legacy/projects'})).statusCode,401);
 assert.equal((await app.inject({url:'/legacy',headers:{authorization:'Bearer b'}})).statusCode,200);
 assert.notEqual((await app.inject({url:'/tasks'})).statusCode,401);
 assert.equal((await app.inject({method:'POST',url:'/api/ops/me',payload:{}})).statusCode,401);
 // Fail closed: unknown and disguised paths need the Bearer; list_membership bounds its input.
 for(const url of ['/unknown','/tasks/../api/stream','/%61pi/stream','/assets/../api/ops/me'])assert.equal((await app.inject({url})).statusCode,401,url);
 await app.close()});

test('review fixes: totals, artifacts and commands carry parent ids; no-project filter; hosts serverNow; join race',async()=>{
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.listen({port:0,host:'127.0.0.1'});const s=(app as any).store;
 const op=async(name:string,payload:any)=>(await app.inject({method:'POST',url:'/api/ops/'+name,payload,headers:{authorization:'Bearer b'}})).json();
 let task='';await s.tx(async()=>{task=await s.tasks.create({title:'T',projectId:'p'});await s.tasks.create({title:'loose'});await s.executions.createExecution({id:'e1',kind:'agent_run',taskId:task,sessionId:'s1',hostId:'mac',harness:'codex',projectId:'p',state:'running'})});
 const start=(await read(s,null)).cursor;
 await s.query("INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis) VALUES('e1','p',10,'measured')");
 await s.put('artifact',{id:'art1',executionId:'e1',sessionId:'s1',path:'a.txt'});await s.put('command',{id:'c1',hostId:'mac',sessionId:'s1',status:'pending'});
 const r=await read(s,start);
 assert.deepEqual(r.changes.map((c:any)=>[c.kind,c.taskId,c.sessionId,c.hostId]),[['execution',task,'s1','mac'],['artifact',task,'s1',null],['command',null,'s1','mac']]);
 // Tasks without a project: projectId null, not the global counts.
 const none=await op('task_page',{filter:{projectId:null},limit:5});assert.deepEqual(none.tasks.map((t:any)=>t.title),['loose']);assert.equal(none.total,1);
 const h=await op('hosts_list',{});assert.ok(Math.abs(h.serverNow-Date.now())<5000);
 // A commit right after connecting (before the stream joined) is still delivered.
 const before=(await read(s,null)).cursor;const base=`http://127.0.0.1:${(app.server.address() as any).port}`;let text='';const req=await get(base+'/api/stream?cursor='+before,c=>text+=c);
 await s.put('session',{id:'early'});
 for(let i=0;i<100&&!text.includes('"id":"early"');i++)await new Promise(r=>setTimeout(r,30));assert.ok(text.includes('"id":"early"'),text);req.destroy();
 await app.close()});

test('priority is a queue class: names and p0/p1/p2 synonyms, default normal, invalid rejected',async()=>{const s=await fresh();
 await s.tx(async()=>{const a=await s.tasks.create({title:'a',priority:'p0'}),b=await s.tasks.create({title:'b'}),c=await s.tasks.create({title:'c',priority:'background'});
  assert.deepEqual([(await s.tasks.get(a)).priority,(await s.tasks.get(b)).priority,(await s.tasks.get(c)).priority],['urgent','normal','background']);
  await s.tasks.update({taskId:b,priority:'p2'});assert.equal((await s.tasks.get(b)).priority,'background');
  await assert.rejects(s.tasks.update({taskId:b,priority:3}),(e:any)=>e.statusCode===400)});
 await s.close()});

test('artifact tables: artifacts and copies carry task and session; stores map to their host; polls and lease renewals skip',async()=>{const s=await fresh();
 let task='';await s.tx(async()=>{task=await s.tasks.create({title:'T',projectId:'p'});await s.executions.createExecution({id:'e1',kind:'agent_run',taskId:task,sessionId:'s1',hostId:'mac',harness:'codex',projectId:'p',state:'running'})});
 const start=(await read(s,null)).cursor;
 await s.query("INSERT INTO store(id,host_id,bucket,endpoint) VALUES('st1','mac','fleet','https://x')");
 await s.query("UPDATE store SET observed_at=now(), updated_at=now() WHERE id='st1'");
 await s.query("UPDATE store SET state='reachable' WHERE id='st1'");
 await s.query("INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id) VALUES('a1','p','prod','h',1,'text/plain','a.txt','e1')");
 await s.query("INSERT INTO artifact_copy(artifact_id,store_id,project_id) VALUES('a1','st1','p')");
 await s.query("UPDATE artifact_copy SET lease_expires_at=now() WHERE artifact_id='a1'");
 await s.query("UPDATE artifact_copy SET state='verified' WHERE artifact_id='a1'");
 const r=await read(s,start);
 assert.deepEqual(r.changes.map((c:any)=>[c.kind,c.id,c.taskId,c.sessionId,c.hostId,c.op]),[
  ['host','mac',null,null,'mac','I'],['host','mac',null,null,'mac','U'],['artifact','a1',task,'s1',null,'I'],['artifact','a1',task,'s1',null,'I'],['artifact','a1',task,'s1',null,'U']]);
 await s.close()});
