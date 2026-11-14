import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// Requirement: indexed delta ingestion preserves full durable closing blocks and retry identity across restart.
// Risk: cleanup resurrects duplicate text, accepts conflicting sequence reuse, or loses the final transcript.
test('PR6 indexed chunks close, retry, reorder and restart without transcript loss', {timeout: 30_000}, async () => {
 const c=await startCoordinator({name:'pr6-deltas'});
 try {
  const call=async(path:string,body:unknown,token=c.secret,person:string|null='owner@trial.test')=>{
   const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+token,'content-type':'application/json',...(person?{'x-fleet-person':person}:{})},body:JSON.stringify(body)});
   return {status:r.status,body:await r.json() as any};
  };
  const host=await call('/api/ops/hosts_register',{id:'pr6-host'}); assert.equal(host.status,200);
  const session=await call('/api/ops/session_create',{}); assert.equal(session.status,200);
  const sid=session.body.sessionId;
  c.sql(`INSERT INTO execution(id,kind,state,observation,session_id,host_id,harness,directed_by,data) VALUES('pr6-ex','agent_run','running','confirmed','${sid}','pr6-host','claude','owner@trial.test',jsonb_build_object('id','pr6-ex','sessionId','${sid}','hostId','pr6-host'));`);
  const chunk=(i:number)=>({id:'d'+i,seq:i,type:'item.completed',timestamp:1,data:{fleetNormalized:{deltaOf:'text:1',...(i===0?{opens:true}:{}),blocks:[{kind:'text',markdown:String(i)+','}]}}});
  const ingest=(events:any[])=>call('/api/hosts/events',{executionId:'pr6-ex',events},host.body.secret,null);
  const chunks=Array.from({length:200},(_,i)=>chunk(i));
  assert.equal((await ingest([...chunks].reverse())).status,200);
  assert.equal((await ingest(chunks)).status,200);
  assert.equal(c.sql("SELECT count(*) FROM document WHERE kind='event-delta' AND data->>'executionId'='pr6-ex'").trim(),'200');
  const final=chunks.map((_,i)=>i+',').join('');
  const close={id:'close',seq:201,type:'item.completed',timestamp:2,data:{fleetNormalized:{closes:['text:1'],blocks:[{kind:'text',markdown:final}]}}};
  assert.equal((await ingest([close])).status,200);
  assert.equal(c.sql("SELECT count(*) FROM document WHERE kind='event-delta' AND data->>'executionId'='pr6-ex'").trim(),'0');
  assert.equal(c.sql("SELECT count(*) FROM closed_delta_identity WHERE execution_id='pr6-ex'").trim(),'200');
  assert.equal((await ingest(chunks)).status,200);
  assert.equal((await ingest([{...chunk(1),timestamp:999}])).status,409);
  assert.equal((await ingest([{...chunk(1),id:'collision'}])).status,409);
  // A late chunk for an already closed block is compacted in its own commit.
  assert.equal((await ingest([chunk(202)])).status,200);
  assert.equal(c.sql("SELECT count(*) FROM document WHERE kind='event-delta' AND data->>'executionId'='pr6-ex'").trim(),'0');
  await c.restart();
  assert.equal((await ingest(chunks)).status,200);
  const r=await fetch(c.base+'/api/executions/pr6-ex/events',{headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test'}});
  assert.equal(r.status,200);
  const durable=await r.json() as any;
  assert.equal(durable.events.find((e:any)=>e.id==='event:pr6-ex:close').data.fleetNormalized.blocks[0].markdown,final);
 } finally { await c.stop(); }
});

// Requirement: bounded 200-client SSE fanout coexists with HTTP reads and preserves viewer isolation.
// Risk: per-tab database amplification starves ordinary reads or exposes another viewer's rows.
test('PR6 200 SSE clients receive authorized fanout with concurrent API reads', {timeout:30_000}, async()=>{
 const c=await startCoordinator({name:'pr6-fanout',env:{FLEET_STREAM_PING_MS:'100'}});
 const clients:Array<{abort:AbortController,events:any[],ready:Promise<void>}>=[];
 const headers=(person:string)=>({authorization:'Bearer '+c.secret,'x-fleet-person':person});
 try {
  const begin=performance.now();
  await Promise.all(Array.from({length:200},async(_,i)=>{
   const abort=new AbortController(),events:any[]=[];
   const response=await fetch(c.base+'/api/stream',{headers:headers(i%2?'outsider@trial.test':'owner@trial.test'),signal:abort.signal});
   assert.equal(response.status,200);
   let readyResolve!:()=>void;
   const ready=new Promise<void>(resolve=>{readyResolve=resolve;});
   clients[i]={abort,events,ready};
   void (async()=>{let buffer='';const dec=new TextDecoder();try{for await(const bytes of response.body as any){buffer+=dec.decode(bytes,{stream:true});let end;while((end=buffer.indexOf('\n\n'))>=0){const frame=buffer.slice(0,end);buffer=buffer.slice(end+2);const data=frame.split('\n').find(l=>l.startsWith('data: '));if(!data)continue;const value=JSON.parse(data.slice(6));if(frame.includes('event: ready'))readyResolve();else if(!frame.includes('event:'))events.push(value);}}}catch{}})();
   await ready;
  }));
  const res=await fetch(c.base+'/api/ops/task_create',{method:'POST',headers:{...headers('owner@trial.test'),'content-type':'application/json'},body:JSON.stringify({title:'PR6 fanout',projectId:'hello-world'})});
  assert.equal(res.status,200);const task=await res.json() as any;
  const until=Date.now()+8000;
  while(clients.some((s,i)=>i%2===0&&!s.events.some(e=>e.id===task.id))&&Date.now()<until)await new Promise(r=>setTimeout(r,10));
  assert.equal(clients.filter((s,i)=>i%2===0&&s.events.some(e=>e.id===task.id)).length,100);
  assert.ok(clients.filter((_,i)=>i%2===1).every(s=>s.events.length===0),'outsiders receive no hidden payload or metadata');
  const reads=await Promise.all(Array.from({length:20},()=>fetch(c.base+'/api/ops/hosts_list',{method:'POST',headers:{...headers('owner@trial.test'),'content-type':'application/json'},body:'{}'}).then(r=>r.status)));
  assert.ok(reads.every(s=>s===200));
  console.log(`PR6 local fanout: clients=200 identities=2 owner-deliveries=100 hidden-deliveries=0 reads=20 elapsed_ms=${Math.round(performance.now()-begin)}`);
 } finally {for(const s of clients)s?.abort.abort();await c.stop();}
});

// Requirement: already open streams and reconnects must use committed current grants.
// Risk: a shared RLS batch survives revocation, or a regrant resurrects an old stream's authorization.
test('PR6 open SSE revokes and reconnects under current grants', {timeout:30_000}, async()=>{
 const c=await startCoordinator({name:'pr6-revoke',env:{FLEET_STREAM_PING_MS:'100'}});
 const aborts:AbortController[]=[];
 const headers=(person:string)=>({authorization:'Bearer '+c.secret,'x-fleet-person':person});
 const op=async(name:string,body:unknown,person='owner@trial.test')=>{const r=await fetch(c.base+'/api/ops/'+name,{method:'POST',headers:{...headers(person),'content-type':'application/json'},body:JSON.stringify(body)});assert.equal(r.status,200,await r.clone().text());return r.json() as Promise<any>;};
 const wait=async(check:()=>boolean)=>{const end=Date.now()+5000;while(!check()&&Date.now()<end)await new Promise(r=>setTimeout(r,10));assert.ok(check(),'stream condition timeout');};
 const open=async(cursor?:number)=>{
  const abort=new AbortController();aborts.push(abort);
  const frames:Array<{event:string,data:any}>=[];
  const r=await fetch(c.base+'/api/stream'+(cursor===undefined?'':'?cursor='+cursor),{headers:headers('member@trial.test'),signal:abort.signal});assert.equal(r.status,200);
  void(async()=>{let buffer='';const dec=new TextDecoder();try{for await(const bytes of r.body as any){buffer+=dec.decode(bytes,{stream:true});let end;while((end=buffer.indexOf('\n\n'))>=0){const frame=buffer.slice(0,end);buffer=buffer.slice(end+2);const data=frame.split('\n').find(l=>l.startsWith('data: '));if(data)frames.push({event:frame.split('\n').find(l=>l.startsWith('event: '))?.slice(7)??'',data:JSON.parse(data.slice(6))});}}}catch{}})();
  await wait(()=>frames.some(f=>f.event==='ready'));return frames;
 };
 try {
  const people=JSON.parse(readFileSync('tests/trial/people.json','utf8'));
  const live=await open();
  const before=await op('task_create',{title:'Before revoke',projectId:'desiign'});
  await wait(()=>live.some(f=>f.data.id===before.id));
  const cursor=live.find(f=>f.data.id===before.id)!.data.seq;
  await op('people_set',{people:people.map((p:any)=>p.email==='member@trial.test'?{...p,grants:[]}:p)});
  await wait(()=>live.some(f=>f.event==='resync'));
  const hidden=await op('task_create',{title:'After revoke',projectId:'desiign'});
  const resumed=await open(cursor);
  await new Promise(r=>setTimeout(r,200));
  assert.ok(!resumed.some(f=>f.data.id===hidden.id||f.data.id===before.id));
  await op('people_set',{people});
  const regranted=await open();
  const visible=await op('task_create',{title:'After regrant',projectId:'desiign'});
  await wait(()=>regranted.some(f=>f.data.id===visible.id));
  assert.ok(!live.some(f=>f.data.id===visible.id),'revoked connection remains closed after regrant');
 } finally {for(const a of aborts)a.abort();await c.stop();}
});
