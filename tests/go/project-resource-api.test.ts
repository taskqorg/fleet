import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {join} from 'node:path';
import {homedir} from 'node:os';
import {deflateSync,inflateSync} from 'node:zlib';
import {startCoordinator,root} from '../harness.ts';

const owner='owner@trial.test',member='member@trial.test',outsider='outsider@trial.test';
const operations=['project_detail','project_resources','project_resource_items'];
const sleep=(ms=20)=>new Promise(r=>setTimeout(r,ms));
const wait=async(check:()=>boolean|Promise<boolean>)=>{const end=Date.now()+15000;while(!await check()){assert.ok(Date.now()<end,'condition timeout');await sleep();}};
const quote=(v:unknown)=>"'"+JSON.stringify(v).replaceAll("'","''")+"'::jsonb";

// Requirement: additive prepared reads share project authority, atomic versions/progress and bounded windows.
// Risk: hidden counts, history-dependent checks, lost scroll positions, revision starvation or a partial snapshot.
test('prepared operations: auth, progress, configuration independence, C-order windows and epochs',{timeout:90000},async()=>{
 const env={FLEET_RESOURCE_READS:'off',FLEET_RESOURCE_PROCESSOR:'off',FLEET_LOST_AFTER_MS:'3600000'};
 const c=await startCoordinator({name:'prepared-api',env});
 const request=async(path:string,body:object,person=owner,secret=c.secret)=>{const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+secret,'content-type':'application/json',...(secret===c.secret?{'x-fleet-person':person}:{})},body:JSON.stringify(body)});return {status:r.status,body:await r.json() as any};};
 const op=async(name:string,body:object,person=owner,expected=200)=>{const r=await request('/api/ops/'+name,body,person);assert.equal(r.status,expected,JSON.stringify(r.body));return r.body;};
 const tools=async()=>{const r=await request('/api/project-tools',{id:1,method:'tools/list'});assert.equal(r.status,200);return r.body.result.tools;};
 const page=(args:object={})=>op('project_resource_items',{projectId:'desiign',category:'tools',...args});
 try{
  for(const name of operations)await op(name,{projectId:'desiign'},owner,503);
  assert.ok(!(await tools()).some((t:any)=>operations.includes(t.name)));
  env.FLEET_RESOURCE_READS='on';await c.restart();
  for(const name of operations){const entry=(await tools()).find((t:any)=>t.name===name);assert.ok(entry);assert.deepEqual(entry.inputSchema.required.includes('projectId'),true);}
  const detail=await op('project_detail',{projectId:'desiign'},member);
  assert.deepEqual(Object.keys(detail).sort(),['id','name','content','settings','unknownKeys','context','revision','contentSha','repoSha','sourceState'].sort());
  assert.equal(detail.id,'desiign');assert.equal('resources' in detail,false);
  const preparing=await op('project_resources',{projectId:'desiign'},member);
  assert.equal(preparing.kind,'preparing');assert.equal(preparing.version,null);assert.equal(preparing.progress.state,'initializing');assert.ok(Date.parse(preparing.progress.validatedAt)>0);
  assert.equal((await page()).kind,'preparing');
  for(const name of operations){
   for(const person of [owner,member])await op(name,{projectId:''},person,400);
   for(const projectId of ['hello-world','nonexistent'])await op(name,{projectId,category:'bogus',cursor:'invalid'},outsider,404);
  }
  c.sql(`INSERT INTO execution(id,kind,state,project_id,data) SELECT 'api-tool-'||lpad(i::text,4,'0'),'tool_run','succeeded','desiign',jsonb_build_object('tool','tool-'||lpad(i::text,4,'0')) FROM generate_series(1,205) i;
   INSERT INTO execution(id,kind,state,project_id,data) VALUES('api-tool-z','tool_run','succeeded','desiign','{"tool":"Z%_literal"}'),('api-tool-a','tool_run','succeeded','desiign','{"tool":"a"}'),('api-foreign','tool_run','succeeded','hello-world','{"tool":"foreign-secret"}');`);
  env.FLEET_RESOURCE_PROCESSOR='on';await c.restart();
  await wait(()=>c.sql("SELECT ready FROM resource_project_summary WHERE project_id='desiign'").trim()==='t');
  await wait(()=>c.sql("SELECT count(*) FROM resource_work_scope WHERE project_id='desiign'").trim()==='0');
  let ready=await op('project_resources',{projectId:'desiign'},member);
  assert.equal(ready.kind,'ready');assert.equal(ready.header.executions,207);assert.match(ready.version,/^[a-f0-9-]{36}:\d+$/);assert.equal(ready.progress.state,'current');
  assert.deepEqual((await op('project_resources',{projectId:'desiign'},owner)).header,ready.header);
  assert.equal((await op('project_resources',{projectId:'desiign',knownVersion:ready.version},member)).kind,'unchanged');
  assert.equal((await op('project_resources',{projectId:'desiign',knownVersion:'foreign:version'},member)).kind,'ready');
  // Use the qualified processor to construct support; pause only scheduling while testing pending/error reads.
  env.FLEET_RESOURCE_PROCESSOR='off';await c.restart();
  c.sql("UPDATE resource_project_summary SET ready=false WHERE project_id='desiign'; UPDATE resource_build SET last_error='build_failed' WHERE project_id='desiign'");
  const preparingError=await op('project_resources',{projectId:'desiign',knownVersion:ready.version},member);assert.equal(preparingError.kind,'preparing');assert.equal(preparingError.version,null);assert.equal(preparingError.progress.errorClass,'build_failed');
  c.sql("UPDATE resource_project_summary SET ready=true WHERE project_id='desiign'; UPDATE resource_build SET last_error=NULL WHERE project_id='desiign'");
  c.sql("INSERT INTO execution(id,kind,state,project_id,data) VALUES('api-pending','tool_run','succeeded','desiign','{\"tool\":\"pending\"}')");
  let unchanged=await op('project_resources',{projectId:'desiign',knownVersion:ready.version},member);
  assert.equal(unchanged.kind,'unchanged');assert.equal(unchanged.progress.state,'updating');assert.ok(unchanged.progress.oldestPendingAt);assert.equal(unchanged.progress.pending,true);
  c.sql("UPDATE resource_work SET last_error='ownership_mismatch' WHERE execution_id='api-pending'");
  unchanged=await op('project_resources',{projectId:'desiign',knownVersion:ready.version},member);
  assert.equal(unchanged.progress.state,'error');assert.equal(unchanged.progress.errorClass,'ownership_mismatch');
  assert.equal((await op('project_detail',{projectId:'desiign'},member)).contentSha,detail.contentSha);
  // Invalid config is independent of the already prepared projection and its progress.
  const revision=c.sql("SELECT content FROM definition_revision WHERE kind='project' AND id='desiign' AND revision=(SELECT head FROM definition WHERE kind='project' AND id='desiign')").trim();
  c.sql("UPDATE definition_revision SET content=jsonb_set(content::jsonb,'{settings,roles}','42')::text WHERE kind='project' AND id='desiign'");
  await op('project_detail',{projectId:'desiign'},member,500);
  assert.equal((await op('project_resources',{projectId:'desiign'},member)).header.executions,207);
  c.sql(`UPDATE definition_revision SET content=${quote(JSON.parse(revision))} WHERE kind='project' AND id='desiign'`);
  let first=await page({limit:50});assert.equal(first.total,207);assert.equal(first.items.length,50);assert.equal(first.items[0].name,'Z%_literal');assert.equal(first.items[1].name,'a');
  const seen=[...first.items.map((x:any)=>x.name)];let current=first;
  while(current.nextCursor){current=await page({limit:50,cursor:current.nextCursor});seen.push(...current.items.map((x:any)=>x.name));}
  assert.equal(seen.length,207);assert.equal(new Set(seen).size,207);assert.equal(seen.includes('foreign-secret'),false);
  const last=await page({anchor:'end',limit:3});assert.deepEqual(last.items.map((x:any)=>x.name),seen.slice(-3));assert.ok(last.previousCursor);
  const prev=await page({cursor:last.previousCursor,limit:3});assert.deepEqual(prev.items.map((x:any)=>x.name),seen.slice(-6,-3));
  const literal=await page({query:'%_',limit:3});assert.deepEqual(literal.items.map((x:any)=>x.name),['Z%_literal']);assert.equal(literal.total,1);
  assert.equal((await page({query:'TOOL-0001'})).total,1);
  for(const args of [{limit:0},{limit:101},{query:'x'.repeat(201)},{category:'costs',query:''},{group:{currency:'USD',basis:'measured'}},{category:'prices'},{category:'prices',group:{basis:'measured'}},{cursor:'bad'},{anchor:'end',around:{key:{kind:'tool',name:'a',connection:null},direction:'atOrAfter'}},{around:{key:{kind:'price',priceId:'x',observedAt:0},direction:'atOrAfter'}},{around:{key:{kind:'tool',name:'x'},direction:'atOrAfter'}},{around:{key:{kind:'tool',name:null,connection:null},direction:'atOrAfter'}},{category:'prices',group:{currency:'USD',basis:'measured'},around:{key:{kind:'price',priceId:'x',observedAt:null},direction:'atOrAfter'}}])await op('project_resource_items',{projectId:'desiign',category:'tools',...args},owner,400);
  await op('project_resource_items',{projectId:'hello-world',category:'tools',cursor:first.nextCursor},owner,400);
  await op('project_resource_items',{projectId:'desiign',category:'tools',cursor:first.nextCursor,query:'different'},owner,400);
  // Revisions may advance on every request; existing cursors remain usable and each page/header is atomic.
  for(let i=0;i<5;i++){
   c.sql(`BEGIN; UPDATE resource_tool SET calls=calls+1 WHERE project_id='desiign' AND name='Z%_literal'; UPDATE resource_project_summary SET revision=revision+1,header=jsonb_set(header,'{tools,totalCalls}',to_jsonb(208+${i}::bigint)) WHERE project_id='desiign'; COMMIT;`);
   const p=await page({limit:3});assert.equal(p.header.tools.totalCalls,p.items[0].calls+206);
   assert.equal((await page({cursor:first.nextCursor})).items.length,50);
  }
  const key={kind:'tool',name:'tool-0100',connection:null};
  c.sql("DELETE FROM resource_tool WHERE project_id='desiign' AND name='tool-0100'; UPDATE resource_project_summary SET revision=revision+1,header=jsonb_set(header,'{itemCounts,tools}','206') WHERE project_id='desiign'");
  assert.equal((await page({around:{key,direction:'atOrAfter'},limit:2})).resolvedAnchor.name,'tool-0101');
  assert.equal((await page({around:{key,direction:'atOrBefore'},limit:2})).resolvedAnchor.name,'tool-0099');
  assert.equal((await page({around:{key:{...key,name:'zzzz'},direction:'atOrAfter'},limit:2})).resolvedAnchor.name,'tool-0205');
  assert.equal((await page({around:{key:{...key,name:''},direction:'atOrBefore'},limit:2})).resolvedAnchor.name,'Z%_literal');
  assert.deepEqual((await page({query:'does-not-exist',around:{key,direction:'atOrAfter'}})).items,[]);
  c.sql("UPDATE resource_project_summary SET revision=9007199254740993 WHERE project_id='desiign'");
  assert.ok((await op('project_resources',{projectId:'desiign'},member)).version.endsWith(':9007199254740993'));
  c.sql("UPDATE resource_project_summary SET epoch=gen_random_uuid(),revision=0 WHERE project_id='desiign'");
  await op('project_resource_items',{projectId:'desiign',category:'tools',cursor:first.nextCursor},member,409);
  assert.equal((await page({around:{key,direction:'atOrAfter'},limit:2})).resolvedAnchor.name,'tool-0101');
  // Grouped monetary and provenance windows remain separated and typed; no header provenance expansion.
  c.sql(`DELETE FROM resource_cost WHERE project_id='desiign'; DELETE FROM resource_price_ref WHERE project_id='desiign'; INSERT INTO resource_cost(project_id,currency,basis,value,usages,references_count) VALUES('desiign','USD','measured',0,1,1),('desiign','EUR','estimated',2.5,1,1),('desiign','','unavailable',NULL,1,1);
   INSERT INTO resource_price_ref(project_id,currency,basis,price_id,observed_at,references_count) SELECT 'desiign','EUR','estimated','price-'||lpad(i::text,4,'0'),i,1 FROM generate_series(1,125) i;
   UPDATE resource_project_summary SET revision=revision+1,header=jsonb_set(jsonb_set(header,'{itemCounts,costs}','3'),'{itemCounts,prices}','125') WHERE project_id='desiign';`);
  const costs=await page({category:'costs'});assert.deepEqual(costs.items.map((x:any)=>x.currency),[null,'EUR','USD']);assert.equal(costs.items[2].value,0);assert.equal(costs.items[0].value,null);assert.equal(costs.items[1].priceCount,125);
  const group={currency:'EUR',basis:'estimated'};const prices=await page({category:'prices',group,limit:100});assert.equal(prices.total,125);assert.equal(prices.items.length,100);assert.deepEqual(prices.items[0].group,group);
  assert.equal((await page({category:'prices',group,cursor:prices.nextCursor})).items.length,25);
  await op('project_resource_items',{projectId:'desiign',category:'prices',group:{...group,currency:'USD'},cursor:prices.nextCursor},member,400);
  // Prove reads do not touch source history, not merely that their response is small.
  const db=c.sql('SELECT current_database()').trim();const pg=process.env.FLEET_TRIAL_PGDATA||join(homedir(),'.cache/fleet-trial/pg');
  const lock=spawn('psql',['-h',pg,'-p',process.env.FLEET_TRIAL_PGPORT||'54329','-U','fleet','-d',db,'-Atq'],{stdio:['pipe','pipe','inherit']});const closed=once(lock,'close');
  let output='';lock.stdout.on('data',x=>output+=x);lock.stdin.write("BEGIN; LOCK execution,execution_totals IN ACCESS EXCLUSIVE MODE; SELECT 'source-locked';\n");
  try{await wait(()=>output.includes('source-locked'));for(const name of operations)await op(name,{projectId:'desiign',category:'tools'},member);}finally{lock.stdin.end('COMMIT;\n');await closed;}
  // Hold the page table after its header snapshot starts, then publish a replacement before
  // releasing the page. The response must contain the old header AND old items together.
  const beforeAtomic=await page({limit:1});
  const publishing=spawn('psql',['-h',pg,'-p',process.env.FLEET_TRIAL_PGPORT||'54329','-U','fleet','-d',db,'-Atq'],{stdio:['pipe','pipe','inherit']});const published=once(publishing,'close');let publicationOutput='';publishing.stdout.on('data',x=>publicationOutput+=x);
  publishing.stdin.write("BEGIN; SET fleet.project_ids='*'; LOCK resource_tool IN ACCESS EXCLUSIVE MODE; SELECT 'page-held';\n");
  let delayed:Promise<any>|undefined;
  try{
   await wait(()=>publicationOutput.includes('page-held'));
   delayed=page({limit:1});
   await wait(()=>c.sql("SELECT count(*) FROM pg_locks WHERE relation='resource_tool'::regclass AND NOT granted").trim()==='1');
   publishing.stdin.end("UPDATE resource_tool SET calls=calls+1 WHERE project_id='desiign' AND name='Z%_literal'; UPDATE resource_project_summary SET revision=revision+1,header=jsonb_set(header,'{tools,totalCalls}',to_jsonb((header#>>'{tools,totalCalls}')::bigint+1)) WHERE project_id='desiign'; COMMIT;\n");
   await published;
   const atomic=await delayed;assert.deepEqual(atomic.header,beforeAtomic.header);assert.deepEqual(atomic.items,beforeAtomic.items);assert.equal(atomic.version,beforeAtomic.version);
   const newer=await page({limit:1});assert.equal(newer.items[0].calls,atomic.items[0].calls+1);assert.equal(newer.header.tools.totalCalls,atomic.header.tools.totalCalls+1);
  }finally{if(publishing.exitCode===null)publishing.stdin.end('ROLLBACK;\n');await published;await delayed;}
  // Native usage has no 200-character tool-name cap. Keep the full admitted key usable
  // in both issued cursors (also larger than 8192 bytes) and deletion-safe around reads.
  const nativeID='api-long-native',longName='z'.repeat(9000);
  await op('project_create',{id:nativeID,requestId:'prepared-long-native',content:{settings:{id:nativeID,name:'Long native tool fixture',contextFiles:[],roles:{}},contextFiles:[]}},member);
  c.sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES('api-long-tool','tool_run','succeeded','${nativeID}',jsonb_build_object('tool',repeat('z',9000))),('api-long-tail','tool_run','succeeded','${nativeID}','{"tool":"zzzzz{"}');`);
  env.FLEET_RESOURCE_PROCESSOR='on';await c.restart();
  await wait(()=>c.sql(`SELECT ready AND header->>'executions'='2' FROM resource_project_summary WHERE project_id='${nativeID}'`).trim()==='t');
  const longPage=await page({projectId:nativeID,limit:1});assert.equal(longPage.items[0].name,longName);assert.ok(longPage.nextCursor.length>8192);
  assert.equal((await page({projectId:nativeID,limit:1,cursor:longPage.nextCursor})).items[0].name,'zzzzz{');
  const longKey={kind:'tool',name:longName,connection:null};assert.equal((await page({projectId:nativeID,around:{key:longKey,direction:'atOrAfter'}})).resolvedAnchor.name,longName);
  c.sql("DELETE FROM execution WHERE id='api-long-tool'");
  await wait(()=>c.sql(`SELECT header->>'executions' FROM resource_project_summary WHERE project_id='${nativeID}'`).trim()==='1');
  assert.equal((await page({projectId:nativeID,around:{key:longKey,direction:'atOrAfter'}})).resolvedAnchor.name,'zzzzz{');
  env.FLEET_RESOURCE_PROCESSOR='off';await c.restart();
  // Both generic and host-agent paths must bind before interpreting foreign cursors/anchors.
  const host=(await op('hosts_register',{id:'api-host',holder:member}));
  const sid='api-session';c.sql(`INSERT INTO session(id,project_id) VALUES('${sid}','desiign');
   INSERT INTO execution(id,kind,state,project_id,session_id,host_id,directed_by,data) VALUES('api-agent','agent_run','running','desiign','${sid}','api-host','${member}',jsonb_build_object('id','api-agent','hostId','api-host','sessionId','${sid}','projectId','desiign','directedBy','${member}'));
   INSERT INTO command(id,kind,project_id,execution_id,host_id) VALUES('api-command','','desiign','api-agent','api-host');`);
  const agent=async(name:string,args:object,expected=200)=>{const r=await request('/api/hosts/agent-tools',{hostId:'api-host',executionId:'api-agent',commandId:'api-command',request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name,arguments:args}}},member,host.secret);assert.equal(r.status,expected,JSON.stringify(r.body));return r.status===200?JSON.parse(r.body.result.content[0].text):r.body;};
  c.sql(`INSERT INTO session(id,directed_by) VALUES('api-unbound-session','${member}');
   INSERT INTO command(id,kind,execution_id,host_id) VALUES('api-unbound-command','','api-unbound-agent','api-host');
   INSERT INTO execution(id,kind,state,session_id,host_id,directed_by,data) VALUES('api-unbound-agent','agent_run','running','api-unbound-session','api-host','${member}',jsonb_build_object('id','api-unbound-agent','hostId','api-host','sessionId','api-unbound-session','directedBy','${member}'));`);
  for(const name of operations){const r=await request('/api/hosts/agent-tools',{hostId:'api-host',executionId:'api-unbound-agent',commandId:'api-unbound-command',request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name,arguments:{category:'tools'}}}},member,host.secret);assert.equal(r.status,400,JSON.stringify(r.body));}
  for(const name of operations){
   const b=await agent(name,{category:'tools'});assert.equal(b.projectId??b.id,'desiign');
   await agent(name,{projectId:'hello-world',category:'bogus',cursor:'bad'},404);
  }
  c.sql("UPDATE session SET payload=payload||'{\"focus\":{\"projectIds\":[\"desiign\"]}}'::jsonb WHERE id='api-session'; UPDATE definition_revision SET content=jsonb_set(content::jsonb,'{settings,roles}','42')::text WHERE kind='project' AND id='desiign'");
  assert.equal((await agent('project_resources',{})).kind,'ready');
  assert.ok((await agent('project_resource_items',{category:'tools'})).items.length>0);
  await agent('project_detail',{},500);
  c.sql(`UPDATE definition_revision SET content=${quote(JSON.parse(revision))} WHERE kind='project' AND id='desiign'`);
  assert.equal((await agent('project_resources',{knownVersion:'foreign:version'})).kind,'ready');
  await agent('project_resource_items',{category:'tools',cursor:prices.nextCursor},400);
  const foreignCursor=Buffer.from(JSON.stringify({...JSON.parse(Buffer.from(first.nextCursor,'base64url').toString()),projectId:'hello-world'})).toString('base64url');
  await agent('project_resource_items',{category:'tools',cursor:foreignCursor},400);
  const foreignAnchor=await agent('project_resource_items',{category:'tools',around:{key:{kind:'tool',name:'foreign-secret',connection:null},direction:'atOrAfter'}});
  assert.ok(foreignAnchor.items.every((x:any)=>x.name!=='foreign-secret'));

  const bindingURL=`postgresql://fleet_app@/${db}?host=${encodeURIComponent(pg)}&port=${process.env.FLEET_TRIAL_PGPORT||'54329'}`;
  const binding=spawn('go',['test','./coordinator','-run','^TestResourceAPIBoundPaths$','-count=1'],{cwd:join(root,'go'),env:{...process.env,FLEET_RESOURCE_TEST_URL:bindingURL},stdio:'inherit'});
  assert.equal((await once(binding,'exit'))[0],0,'generic host binding proof');
  env.FLEET_RESOURCE_READS='off';await c.restart();
  for(const name of operations)await agent(name,{projectId:'desiign',category:'tools'},503);
  const discovery=await request('/api/hosts/agent-tools',{hostId:'api-host',executionId:'api-agent',commandId:'api-command',request:{jsonrpc:'2.0',id:1,method:'tools/list'}},member,host.secret);
  assert.equal(discovery.status,200);assert.ok(!discovery.body.result.tools.some((t:any)=>operations.includes(t.name)));
  const disabledMCP=await request('/api/project-tools',{id:1,method:'tools/call',params:{name:'project_resources',arguments:{projectId:'desiign'}}});assert.equal(disabledMCP.status,503);
  env.FLEET_RESOURCE_READS='on';await c.restart();
  // HTTP and agent revocation use the same current project visibility; supplied version cannot bypass it.
  const people=(await op('people_list',{})).people;const changed=people.map((p:any)=>p.email===member?{...p,grants:[]}:p);await op('people_set',{people:changed});
  for(const name of operations){await op(name,{projectId:'desiign',category:'bogus',cursor:'bad',knownVersion:ready.version},member,404);await op(name,{projectId:'nonexistent'},member,404);}
  await agent('project_resources',{knownVersion:ready.version},404);
 }finally{await c.stop();}
});

// Requirement: public summary changes commit with publication, honor project RLS/XID barriers,
// and recover over reconnect. Risk: rolled-back or foreign summaries escape through live/catch-up paths.
test('resource_summary commit, rollback, XID barrier, scoped SSE and reconnect',{timeout:60000},async()=>{
 const env={FLEET_RESOURCE_READS:'on',FLEET_RESOURCE_PROCESSOR:'on',FLEET_STREAM_PING_MS:'100',FLEET_LOST_AFTER_MS:'3600000'};
 const c=await startCoordinator({name:'prepared-stream',env});const aborts:AbortController[]=[];
 const headers=(person:string)=>({authorization:'Bearer '+c.secret,'x-fleet-person':person});
 const op=async(name:string,body:object,person=owner)=>{const r=await fetch(c.base+'/api/ops/'+name,{method:'POST',headers:{...headers(person),'content-type':'application/json'},body:JSON.stringify(body)});assert.equal(r.status,200,await r.clone().text());return r.json() as Promise<any>;};
 const open=async(person:string,cursor?:number)=>{
  const abort=new AbortController();aborts.push(abort);const frames:any[]=[];
  const response=await fetch(c.base+'/api/stream'+(cursor===undefined?'':'?cursor='+cursor),{headers:headers(person),signal:abort.signal});assert.equal(response.status,200);
  const done=(async()=>{let buffer='';try{for await(const bytes of response.body as any){buffer+=new TextDecoder().decode(bytes);let end;while((end=buffer.indexOf('\n\n'))>=0){const frame=buffer.slice(0,end);buffer=buffer.slice(end+2);const data=frame.split('\n').find(l=>l.startsWith('data: '));if(data)frames.push({event:frame.split('\n').find(l=>l.startsWith('event: '))?.slice(7)??'',data:JSON.parse(data.slice(6))});}}}catch(e){if(!abort.signal.aborted)throw e;}})();
  await wait(()=>frames.some(f=>f.event==='ready'));return {frames,abort,done};
 };
 try{
  await wait(()=>c.sql("SELECT ready FROM resource_project_summary WHERE project_id='desiign'").trim()==='t');
  await wait(()=>c.sql("SELECT ready FROM resource_project_summary WHERE project_id='hello-world'").trim()==='t');
  const sourceStream=await open(member);
  c.sql("INSERT INTO execution(id,kind,state,project_id,data) VALUES('stream-source','tool_run','succeeded','desiign','{\"tool\":\"source-visible\"}')");
  await wait(async()=>{const h=await op('project_resources',{projectId:'desiign'},member);return h.kind==='ready'&&h.header.executions===1&&!h.progress.pending;});
  await wait(()=>sourceStream.frames.some(f=>f.data.kind==='resource_summary'&&f.data.id==='desiign'));
  sourceStream.abort.abort();await sourceStream.done;
  env.FLEET_RESOURCE_PROCESSOR='off';await c.restart();
  const own=await open(owner),allowed=await open(member),hidden=await open(outsider);
  const baseline=allowed.frames.find(f=>f.event==='ready').data.cursor;
  const before=c.sql("SELECT count(*) FROM change WHERE kind='resource_summary'").trim();
  c.sql("BEGIN; UPDATE resource_project_summary SET revision=revision+1 WHERE project_id='desiign'; ROLLBACK;");
  assert.equal(c.sql("SELECT count(*) FROM change WHERE kind='resource_summary'").trim(),before);
  const db=c.sql('SELECT current_database()').trim(),pg=process.env.FLEET_TRIAL_PGDATA||join(homedir(),'.cache/fleet-trial/pg');
  const blocker=spawn('psql',['-h',pg,'-p',process.env.FLEET_TRIAL_PGPORT||'54329','-U','fleet','-d',db,'-Atq'],{stdio:['pipe','pipe','inherit']});const blockedExit=once(blocker,'close');let output='';blocker.stdout.on('data',b=>output+=b);blocker.stdin.write("BEGIN; SELECT pg_current_xact_id(); SELECT 'barrier-held';\n");
  let seq:number;
  try{
   await wait(()=>output.includes('barrier-held'));
   c.sql("UPDATE resource_project_summary SET revision=revision+1 WHERE project_id IN ('desiign','hello-world')");
   seq=Number(c.sql("SELECT max(seq) FROM change WHERE kind='resource_summary' AND project_id='desiign'").trim());
   assert.equal(c.sql(`SELECT count(*) FROM change WHERE seq=${seq} AND xid < pg_snapshot_xmin(pg_current_snapshot())`).trim(),'0','committed publication waits behind older transaction');
   assert.ok(!allowed.frames.some(f=>f.data.seq===seq));
  }finally{blocker.stdin.end('ROLLBACK;\n');await blockedExit;}
  await wait(()=>allowed.frames.some(f=>f.data.seq===seq));
  await wait(()=>own.frames.some(f=>f.data.kind==='resource_summary'&&f.data.id==='hello-world'));
  assert.ok(allowed.frames.filter(f=>f.data.kind==='resource_summary').every(f=>f.data.id==='desiign'));
  assert.equal(hidden.frames.filter(f=>f.data.kind==='resource_summary').length,0);
  allowed.abort.abort();await allowed.done;
  for(let i=0;i<3;i++)c.sql("UPDATE resource_project_summary SET revision=revision+1 WHERE project_id IN ('desiign','hello-world')");
  const reconnect=await open(member,baseline);
  const expected=Number(c.sql(`SELECT count(*) FROM change WHERE kind='resource_summary' AND project_id='desiign' AND seq>${baseline}`).trim());
  await wait(()=>reconnect.frames.filter(f=>f.data.kind==='resource_summary').length===expected);
  assert.ok(reconnect.frames.filter(f=>f.data.kind==='resource_summary').every(f=>f.data.id==='desiign'));
  const caught=await op('changes_since',{cursor:baseline},member);assert.ok(caught.changes.filter((x:any)=>x.kind==='resource_summary').every((x:any)=>x.id==='desiign'));
  assert.equal((await op('changes_since',{cursor:baseline},outsider)).changes.length,0);
  // Revoke while connected; subsequent publication must resync instead of reusing a shared allowed batch.
  const people=(await op('people_list',{})).people;
  await op('people_set',{people:people.map((p:any)=>p.email===member?{...p,grants:[]}:p)});
  await wait(()=>reconnect.frames.some(f=>f.event==='resync'));
  const after=c.sql("UPDATE resource_project_summary SET revision=revision+1 WHERE project_id='desiign'; SELECT max(seq) FROM change WHERE kind='resource_summary' AND project_id='desiign'").trim();
  const newScope=await open(member,baseline);await wait(()=>newScope.frames.some(f=>f.event==='ready'));
  assert.equal((await op('changes_since',{cursor:baseline},member)).changes.length,0);
  assert.ok(!newScope.frames.some(f=>String(f.data.seq)===after));
 }finally{for(const a of aborts)a.abort();await c.stop();}
});

// Requirement: every returned large native position round-trips inside the unchanged HTTP
// body envelope with exact identity. Risk: JSON/base64 escaping overflows admission, decoder
// expansion/truncation is accepted, or packed keys bypass scope/category/epoch validation.
test('admitted large native tool keys round-trip compressed cursors and packed anchors over HTTP',{timeout:60000},async()=>{
 const c=await startCoordinator({name:'large-key-proof',env:{FLEET_RESOURCE_READS:'on',FLEET_RESOURCE_PROCESSOR:'on',FLEET_LOST_AFTER_MS:'3600000'}});
 const request=async(path:string,body:any,secret=c.secret)=>{const bytes=JSON.stringify(body);const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+secret,'content-type':'application/json',...(secret===c.secret?{'x-fleet-person':owner}:{})},body:bytes});return {status:r.status,body:await r.json() as any,bytes:Buffer.byteLength(bytes)};};
 const p='large-native',name='\u0001'.repeat(174650),near='\u0001'.repeat(200)+'nearby',query='\u0001'.repeat(200);
 const page=async(args:any={},status=200,person=owner)=>{
  const bytes=JSON.stringify({projectId:p,category:'tools',limit:1,...args});const r=await fetch(c.base+'/api/ops/project_resource_items',{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':person,'content-type':'application/json'},body:bytes});
  const body=await r.json() as any;assert.equal(r.status,status,status===200?body.message:undefined);return {body,bytes:Buffer.byteLength(bytes)};
 };
 try{
  assert.equal((await request('/api/ops/project_create',{id:p,requestId:p,content:{settings:{id:p,name:p,roles:{},contextFiles:[]},contextFiles:[]}})).status,200);
  const host=(await request('/api/ops/hosts_register',{id:'large-native-host'})).body;
  c.sql(`INSERT INTO execution(id,kind,state,project_id,host_id,started_at,data) VALUES('large-native-agent','agent_run','running','${p}','large-native-host',now(),'{"id":"large-native-agent","hostId":"large-native-host"}'),('large-native-neighbor','agent_run','running','${p}','large-native-host',now(),'{"id":"large-native-neighbor","hostId":"large-native-host"}');`);
  const sourceBody={hostId:'large-native-host',executionId:'large-native-agent',events:[{id:'large-native-close',seq:1,type:'process.closed',timestamp:Date.now(),data:{exitCode:0,resources:{toolCalls:{[name]:{calls:1,errors:0},tail:{calls:1,errors:0}}}}}]};
  const source=await request('/api/hosts/events',sourceBody,host.secret);assert.equal(source.status,200);assert.equal(source.bytes,1048168);assert.ok(source.bytes<(1<<20));
  const neighbor=await request('/api/hosts/events',{hostId:'large-native-host',executionId:'large-native-neighbor',events:[{id:'large-neighbor-close',seq:1,type:'process.closed',timestamp:Date.now(),data:{exitCode:0,resources:{toolCalls:{[near]:{calls:1,errors:0}}}}}]},host.secret);assert.equal(neighbor.status,200);
  await wait(()=>c.sql(`SELECT ready AND header#>>'{itemCounts,tools}'='3' FROM resource_project_summary WHERE project_id='${p}'`).trim()==='t');
  assert.equal(c.sql(`SELECT max(length(name)) FROM resource_tool WHERE project_id='${p}'`).trim(),'174650','real admitted source persisted without truncation');
  const first=await page();assert.equal(first.body.items[0].name,name);assert.match(first.body.nextCursor,/^r2\./);assert.equal(first.body.items[0].position.kind,'tool-position');assert.deepEqual(first.body.resolvedAnchor,first.body.items[0].position);
  const next=await page({cursor:first.body.nextCursor});assert.equal(next.body.items[0].name,near);assert.ok(next.bytes<(1<<20));
  // Ordinary small cursor schema1 remains the emitted format and is still readable.
  assert.equal(JSON.parse(Buffer.from(next.body.previousCursor,'base64url').toString()).schemaVersion,1);
  const previous=await page({cursor:next.body.previousCursor});assert.equal(previous.body.items[0].name,name);
  const position=first.body.items[0].position;
  const around=await page({query,around:{key:position,direction:'atOrAfter'}});assert.equal(around.body.items[0].name,name);assert.deepEqual(around.body.resolvedAnchor,position);assert.ok(around.bytes<(1<<20));
  const compressed=Buffer.from(first.body.nextCursor.slice(3),'base64url');const cursor=JSON.parse(inflateSync(compressed).toString());assert.equal(cursor.schemaVersion,2);
  const encode=(value:any)=>'r2.'+deflateSync(Buffer.from(JSON.stringify(value))).toString('base64url');
  await page({projectId:'hello-world',cursor:first.body.nextCursor},400);
  await page({category:'costs',cursor:first.body.nextCursor},400);
  await page({query:'different',cursor:first.body.nextCursor},400);
  await page({cursor:encode({...cursor,buildEpoch:'00000000-0000-0000-0000-000000000000'})},409);
  await page({cursor:encode({...cursor,schemaVersion:1})},400);
  await page({cursor:first.body.nextCursor},404,outsider);
  await page({cursor:'r2.not_base64'},400);
  await page({cursor:'r2.'+compressed.subarray(0,-1).toString('base64url')},400);
  await page({cursor:'r2.'+Buffer.concat([compressed,Buffer.from([0])]).toString('base64url')},400);
  const bomb='tp1.'+deflateSync(Buffer.alloc(12*(1<<20)+4097,97)).toString('base64url');
  await page({around:{key:{kind:'tool-position',encoded:bomb},direction:'atOrAfter'}},400);
  const nonnull='tp1.'+deflateSync(Buffer.from(JSON.stringify({schemaVersion:1,key:{kind:'tool',name,connection:'\\\"><&'}}))).toString('base64url');
  assert.equal((await page({around:{key:{kind:'tool-position',encoded:nonnull},direction:'atOrAfter'}})).body.items[0].name,near,'packed connection remains part of the exact sort tuple');
  for(const key of [{kind:'tool',name:null,connection:null},{kind:'tool',name:'x',connection:42},{kind:'tool',connection:null},null]){
   const encoded='tp1.'+deflateSync(Buffer.from(JSON.stringify({schemaVersion:1,key}))).toString('base64url');
   const packed={kind:'tool-position',encoded};
   await page({around:{key:packed,direction:'atOrAfter'}},400);
   await page({cursor:encode({...cursor,key:packed})},400);
  }
  const broken={...position,encoded:position.encoded.slice(0,-3)};
  await page({around:{key:broken,direction:'atOrAfter'}},400);
  await page({category:'costs',around:{key:position,direction:'atOrAfter'}},400);
  const foreign=await page({projectId:'hello-world',around:{key:position,direction:'atOrAfter'}});assert.ok(foreign.body.items.every((x:any)=>x.name!==name));
  c.sql("BEGIN; SELECT id FROM execution WHERE id='large-native-agent' FOR UPDATE; DELETE FROM execution_totals WHERE execution_id='large-native-agent'; DELETE FROM execution WHERE id='large-native-agent'; COMMIT;");
  await wait(()=>c.sql(`SELECT header->>'executions' FROM resource_project_summary WHERE project_id='${p}'`).trim()==='1');
  const survived=await page({query,around:{key:position,direction:'atOrAfter'}});assert.equal(survived.body.resolvedAnchor.name,near);assert.equal(survived.body.items[0].name,near);
  console.log(JSON.stringify({largePositionEvidence:{sourceBytes:source.bytes,persistedNameCharacters:name.length,nextCursorCharacters:first.body.nextCursor.length,nextRequestBytes:next.bytes,previousRequestBytes:previous.bytes,aroundQuery200RequestBytes:around.bytes,positionCharacters:position.encoded.length}}));
 }finally{await c.stop();}
});
