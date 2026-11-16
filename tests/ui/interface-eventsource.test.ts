import {test} from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {mkdtempSync,readFileSync,rmSync,existsSync,mkdirSync,writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {chromium} from 'playwright';
import {browse,workload} from '../performance/interface.ts';
import {installEventSourceCapture,EventSourceRecorder} from '../performance/eventsource-interface.ts';

// Requirement: actual native SSE constructor/credentials/events/reconnect/duplicates/cancel remain unchanged.
// Risk: a replacement transport invents notification arrival, loses messages, changes callbacks or hides a full/broken sink.
test('native SSE evidence equals actual messages and fails closed on reconnect/fullness/sink errors',{skip:!process.env.FLEET_CONTROL_BROWSER,timeout:60000},async()=>{
 const requests=new Map<string,{last:string;cookie:string}[]>(),closed=new Set<string>(),responses=new Set<http.ServerResponse>();
 const payload=(seq:number)=>JSON.stringify({kind:'resource_summary',id:'owned',seq,op:'U'});
 let earlyNavigations=0;
 const server=http.createServer((req,res)=>{
  const u=new URL(req.url!,'http://localhost');
  if(u.pathname==='/__interface/config'){res.setHeader('content-type','application/json');res.end(JSON.stringify({development:true,projects:['owned'],marker:'unused-fixture-marker'}));return;}
  if(u.pathname==='/projects/owned'){earlyNavigations++;res.setHeader('content-type','text/html; charset=utf-8');res.end(`<button>Save project</button><div data-resource-project="owned"></div><button id="tools" disabled>Tools · 1</button>${earlyNavigations>1?'<button>Tools · 2</button>':''}<script>const s=new EventSource('/api/stream?case=clean-early-${earlyNavigations}');s.onmessage=()=>{document.querySelector('#tools').disabled=false;document.querySelector('[data-resource-project]').setAttribute('data-resource-version','1')};</script>`);return;}
  if(u.pathname!=='/api/stream'){res.setHeader('content-type','text/html; charset=utf-8');res.end('<html><body>native protocol</body></html>');return;}
  const key=u.searchParams.get('case')!,seen=requests.get(key)??[];seen.push({last:String(req.headers['last-event-id']??''),cookie:String(req.headers.cookie??'')});requests.set(key,seen);
  res.writeHead(200,{'content-type':'text/event-stream','cache-control':'no-store'});responses.add(res);res.on('close',()=>{responses.delete(res);if(seen.length===2)closed.add(key);});
  if(key.startsWith('clean'))res.write(`id: one\ndata: ${payload(11)}\n\nid: two\ndata: ${payload(12)}\n\nid: two\ndata: ${payload(12)}\n\nid: three\ndata: ${payload(13)}\n\nevent: custom\ndata: second\n\n`);
  else if(seen.length===1)res.end(`retry: 10\nid: one\ndata: ${payload(11)}\n\nid: two\ndata: ${payload(12)}\n\nid: two\ndata: ${payload(12)}\n\nevent: custom\ndata: first\n\n`);
  else res.write(`id: three\ndata: ${payload(13)}\n\nevent: custom\ndata: second\n\n`);
 });
 await new Promise<void>(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+(server.address() as any).port,dir=mkdtempSync(join(tmpdir(),'fleet-native-sse-')),browser=await chromium.launch({headless:true});
 const run=async(key:string,instrumented:boolean,maxBytes?:number)=>{
  const context=await browser.newContext();await context.addCookies([{name:'protocol',value:'owned-fixture',url:origin}]);const page=await context.newPage();await page.goto(origin);
  await page.evaluate(()=>{(window as any).__nativeES=EventSource;});
  if(instrumented){await page.evaluate(installEventSourceCapture,{projects:['owned'],maxBytes});const session=await context.newCDPSession(page);await session.send('Network.emulateNetworkConditions',{offline:false,latency:80,downloadThroughput:10_000_000/8,uploadThroughput:10_000_000/8});}
  await page.evaluate(({url})=>{
   const w=window as any;w.events=[];let reads=0;class Source extends EventSource{};const options={get withCredentials(){reads++;return true;}};const source=w.source=new Source(url,options);
   w.semantics={reads,credentials:source.withCredentials,native:source instanceof w.__nativeES,subclass:source instanceof Source,prototype:EventSource.prototype===w.__nativeES.prototype,close:source.close===w.__nativeES.prototype.close,constant:EventSource.CLOSED};
   try{(EventSource as any)(url);w.semantics.withoutNew=false;}catch(e){w.semantics.withoutNew=e instanceof TypeError;}
   const started=performance.now();source.onopen=()=>{w.firstOpenMs??=performance.now()-started;w.events.push({type:'open'});};source.onerror=()=>w.events.push({type:'error',state:source.readyState});source.onmessage=e=>w.events.push({type:'message',data:e.data,id:e.lastEventId});source.addEventListener('custom',e=>{w.events.push({type:'custom',data:(e as MessageEvent).data});if((e as MessageEvent).data==='second'){source.close();w.done=true;}});
  },{url:origin+'/api/stream?case='+key});
  await page.waitForFunction(()=>(window as any).done===true);
  const result=await page.evaluate(()=>({events:(window as any).events,semantics:(window as any).semantics,state:(window as any).source.readyState,firstOpenMs:(window as any).firstOpenMs}));
  assert.deepEqual(result.semantics,{reads:1,credentials:true,native:true,subclass:true,prototype:true,close:true,constant:2,withoutNew:true});assert.equal(result.state,2);if(instrumented)assert.ok(result.firstOpenMs>=60,'80ms CDP shaping still acts without own Network.enable');
  assert.equal(requests.get(key)!.length,key.startsWith('clean')?1:2,'recorder creates no extra EventSource/HTTP');if(!key.startsWith('clean'))assert.equal(requests.get(key)![1].last,'two');assert.ok(requests.get(key)!.every(r=>r.cookie.includes('protocol=owned-fixture')));
  return {context,page,result};
 };
 try{
  // A preparation failure after the warm document's real SSE arrives must export that page too.
  const early=join(dir,'early');mkdirSync(early);const placement=join(early,'placement.json');writeFileSync(placement,'{}');
  await assert.rejects(browse({origin,out:early,placement,spec:workload({warmup:0,duration:0,burst:0,recovery:0},true)}),/strict mode violation/);
  assert.equal(earlyNavigations,2);const earlyEvidence=JSON.parse(readFileSync(join(early,'browser-incomplete.json'),'utf8'));assert.equal(earlyEvidence.notificationCapture.documents.length,2);assert.ok(earlyEvidence.notificationCapture.documents.every((d:any)=>d.frozen&&d.observed>0));assert.equal(earlyEvidence.notificationCapture.complete,false);assert.ok(readFileSync(join(early,'notifications-received.jsonl.partial'),'utf8').includes('reader-0'));assert.ok(existsSync(join(early,'rendered-incomplete.png')));
  const before=await run('native',false),after=await run('observed',true);assert.deepEqual(after.result.events,before.result.events);
  const recorder=new EventSourceRecorder(dir);await recorder.capture({id:'reader',page:after.page},true);await assert.rejects(recorder.finish(true),/native EventSource error/,'native reconnect error retained, never an error-free qualification');
  const rows=readFileSync(join(dir,'notifications-received.jsonl.partial'),'utf8').trim().split('\n').map(l=>JSON.parse(l));
  assert.deepEqual(rows.map(r=>[r.lastEventId,r.data]),before.result.events.filter((e:any)=>e.type==='message').map((e:any)=>[e.id,e.data]));assert.deepEqual(rows.map(r=>r.change.sequence),['11','12','12','13']);assert.ok(rows.every(r=>r.provenance==='native-eventsource-callback'&&Number.isFinite(r.browserMonotonic)&&!('cdpMonotonicMs'in r)));
  assert.ok(rows.every(r=>r.reader==='reader'));assert.equal(new Set(rows.map(r=>r.documentId)).size,1);assert.equal(new Set(rows.map(r=>r.sourceId)).size,1);assert.deepEqual(rows.map(r=>r.openGeneration),[1,1,1,2]);assert.equal(new Set(rows.map(r=>r.ordinal)).size,4,'duplicates retained by distinct ordinals');
  assert.ok(!existsSync(join(dir,'notifications-received.jsonl')));assert.equal(recorder.snapshot().documents[0].acknowledged,recorder.snapshot().documents[0].observed);
  const clean=await run('clean',true),cleanDir=join(dir,'clean');mkdirSync(cleanDir);const complete=new EventSourceRecorder(cleanDir);
  await complete.capture({id:'same-reader',page:clean.page});await complete.capture({id:'same-reader',page:clean.page},true);
  await clean.page.addInitScript(installEventSourceCapture,{projects:['owned']});await clean.page.goto(origin);
  await clean.page.evaluate(url=>{const w=window as any;w.done=false;const es=new EventSource(url);es.onmessage=e=>{if(JSON.parse(e.data).seq===13){es.close();w.done=true;}};},origin+'/api/stream?case=clean-nav');await clean.page.waitForFunction(()=>(window as any).done);
  await complete.capture({id:'same-reader',page:clean.page},true);await complete.finish(true);
  const completeRows=readFileSync(join(cleanDir,'notifications-received.jsonl'),'utf8').trim().split('\n').map(l=>JSON.parse(l));assert.equal(completeRows.length,8);assert.ok(completeRows.every(r=>r.reader==='same-reader'));assert.equal(new Set(completeRows.map(r=>r.documentId)).size,2);assert.equal(complete.snapshot().documents.length,2);assert.equal(complete.snapshot().documents[1].sources[0].withCredentials,false);assert.equal(complete.snapshot().complete,true);assert.ok(complete.snapshot().documents.every(d=>d.frozen&&d.acknowledged===d.observed));await clean.context.close();
  const publication=await run('clean-publication',true),publicationDir=join(dir,'publication');mkdirSync(publicationDir);mkdirSync(join(publicationDir,'notifications-received.jsonl'));const publishing=new EventSourceRecorder(publicationDir);
  await publishing.capture({id:'publication',page:publication.page},true);await assert.rejects(publishing.finish(true),/EISDIR/);assert.equal(publishing.snapshot().complete,false);assert.ok(existsSync(join(publicationDir,'eventsource-events.jsonl.partial')));assert.ok(existsSync(join(publicationDir,'notifications-received.jsonl.partial')));assert.ok(!existsSync(join(publicationDir,'eventsource-events.jsonl')));await publication.context.close();
  const overflow=await run('overflow',true,128),out=join(dir,'overflow');mkdirSync(out);const full=new EventSourceRecorder(out);await full.capture({id:'full',page:overflow.page},true);await assert.rejects(full.finish(true),/capture incomplete/);assert.ok(full.snapshot().documents[0].lost>0);assert.ok(full.snapshot().documents[0].firstLost);assert.ok(!existsSync(join(out,'notifications-received.jsonl')));
  const untrusted=await run('untrusted',true);await untrusted.page.evaluate(()=>{(window as any).source.dispatchEvent(new MessageEvent('message',{data:JSON.stringify({kind:'resource_summary',id:'owned',seq:999,op:'U'}),lastEventId:'fake'}));});
  const fake=await untrusted.page.evaluate(()=>(window as any).__interfaceSSE.drain(0,true));assert.ok(fake.errors.some((e:string)=>e.includes('untrusted')));assert.ok(!fake.notifications.trim().split('\n').filter(Boolean).map((s:string)=>JSON.parse(s)).some((r:any)=>r.change.sequence==='999'));assert.ok(fake.events.includes('untrusted-message'));await untrusted.context.close();
  const badPage=await run('broken',true);const bad=join(dir,'bad');mkdirSync(bad);mkdirSync(join(bad,'eventsource-events.jsonl.partial'));const broken=new EventSourceRecorder(bad);await assert.rejects(broken.capture({id:'broken',page:badPage.page},true));await broken.finish(false);assert.ok(broken.snapshot().errors.length);
  await before.context.close();await after.context.close();await overflow.context.close();await badPage.context.close();
  for(const key of ['native','observed','overflow','broken','untrusted'])assert.ok(closed.has(key),'native cancellation closes server response '+key);
 }finally{await browser.close();for(const res of responses)res.end();await new Promise<void>(r=>server.close(()=>r()));rmSync(dir,{recursive:true,force:true});}
});

// Requirement: a large native SSE backlog drains completely in few page round trips with exact ordinals.
// Risk: per-row shift and 256-row drains let a busy reader's 64 MiB capture overflow before the run ends.
test('native SSE capture drains a 30000-event backlog in few round trips',{skip:!process.env.FLEET_CONTROL_BROWSER,timeout:60000},async()=>{
 const total=30000,streams=new Set<http.ServerResponse>();
 const server=http.createServer((req,res)=>{
  if(req.url!.startsWith('/api/stream')){res.writeHead(200,{'content-type':'text/event-stream'});let body='';for(let i=1;i<=total;i++)body+=`id: ${i}\ndata: ${JSON.stringify(i%10?{kind:'execution',id:'x'+i,seq:i}:{kind:'resource_summary',id:'owned',seq:i,op:'U'})}\n\n`;res.write(body);streams.add(res);return;} // left open: an ended stream would reconnect
  res.setHeader('content-type','text/html');res.end('<html><body>bulk</body></html>');
 });
 await new Promise<void>(r=>server.listen(0,'127.0.0.1',r));const origin='http://127.0.0.1:'+(server.address() as any).port,dir=mkdtempSync(join(tmpdir(),'fleet-bulk-sse-')),browser=await chromium.launch({headless:true});
 try{
  const page=await (await browser.newContext()).newPage();await page.goto(origin);await page.evaluate(installEventSourceCapture,{projects:['owned']});
  await page.evaluate((url)=>new Promise<void>(ok=>{let n=0;const es=new EventSource(url);es.onmessage=()=>{if(++n===30000){es.close();ok();}};}),origin+'/api/stream');
  let calls=0;const evaluate=page.evaluate.bind(page);(page as any).evaluate=(...a:any[])=>{calls++;return (evaluate as any)(...a);};
  const recorder=new EventSourceRecorder(dir);await recorder.capture({id:'bulk',page},true);
  const snapshot=recorder.snapshot();assert.equal(snapshot.eventCount,total+1,'every message plus the open event');assert.deepEqual(snapshot.errors,[]);
  assert.ok(calls<=20,`drained in ${calls} page round trips`);
  const lines=readFileSync(join(dir,'eventsource-events.jsonl.partial'),'utf8').trim().split('\n').map(l=>JSON.parse(l).ordinal);
  assert.deepEqual(lines,Array.from({length:total+1},(_,i)=>i+1),'exact contiguous ordinals');
  assert.equal(snapshot.count,total/10,'every project notification is in the notification evidence');
 }finally{for(const s of streams)s.destroy();await browser.close();server.close();rmSync(dir,{recursive:true,force:true});}
});
