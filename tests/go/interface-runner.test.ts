import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdirSync,mkdtempSync,writeFileSync,readFileSync,createWriteStream,existsSync} from 'node:fs';
import {join} from 'node:path';
import {freePort,root} from '../harness.ts';

// Requirement: observation receipt survives retries while unknown/no actual render cannot pass.
// Risk: delayed retries forge a successful render or erase missing samples.
// These are protocol finalization regressions, not browser or performance acceptance results.
for (const scenario of ['statistics exception', 'large export parallel stop', 'journal export exception'] as const) {
 const positive = scenario !== 'statistics exception';
 // Requirement: one owner retains complete source/render evidence and every mandatory export outcome.
 // Risk: a failed journal/links artifact is reported complete or changes a known render verdict.
 test('fixture finalization: '+scenario,{timeout:60000},async()=>{
 mkdirSync(join(root,'.egm'),{recursive:true});const out=mkdtempSync(join(root,'.egm','i-'));
 const port=await freePort(),pgport=await freePort(),origin='http://127.0.0.1:'+port;
 const placement=join(out,'placement.json');writeFileSync(placement,JSON.stringify({development:true}));
 const log=createWriteStream(join(out,'server.log'));
 const child=spawn(process.execPath,[join(root,'tests/performance/interface.ts'),'serve','--development','--history=18','--projects=1','--out='+out,'--placement='+placement,'--port='+port],{cwd:root,env:{...process.env,FLEET_TRIAL_PGDATA:join(out,'pg'),FLEET_TRIAL_PGPORT:String(pgport)},stdio:['ignore','pipe','pipe']});
 child.stdout.pipe(log,{end:false});child.stderr.pipe(log,{end:false});
 let stopped=false,journalDropped=false,gate:ReturnType<typeof spawn>|undefined;
 let exited=false;const gone=new Promise<void>(r=>child.once('exit',()=>{exited=true;r();}));
 const request=async(path:string,body?:object)=>{const r=await fetch(origin+'/__interface/'+path,body?{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)}:{});const b=await r.json() as any;assert.equal(r.status,200,JSON.stringify(b));return b;};
 const wait=async(check:()=>Promise<boolean>,ms=15000)=>{const end=Date.now()+ms;while(!await check()){assert.ok(!exited&&Date.now()<end,'private runner did not reach checkpoint: '+readFileSync(join(out,'server.log'),'utf8'));await new Promise(r=>setTimeout(r,20));}};
 try{
  let config:any;await wait(async()=>{try{config=await request('config');return true}catch{return false}});
  const db=JSON.parse(readFileSync(join(out,'pg-before.json'),'utf8')).database;
  const sql=(input:string)=>execFileSync('psql',['-X','-qAt','-v','ON_ERROR_STOP=1','-h',join(out,'pg'),'-p',String(pgport),'-U','postgres','-d',db],{input,encoding:'utf8'}).trim();
  if(scenario==='large export parallel stop'){
   sql('CREATE TEMP TABLE interface_large_stats(x int) /*'+ 'x'.repeat(1200000) +'*/;');
   assert.equal(sql("SELECT octet_length(json_agg(s)::text)>1048576 FROM pg_stat_statements s WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database())"),'t','actual SQL statistics exceed the old spawnSync buffer');
   gate=spawn('psql',['-X','-qAt','-v','ON_ERROR_STOP=1','-h',join(out,'pg'),'-p',String(pgport),'-U','postgres','-d',db],{stdio:['pipe','pipe','pipe']});
   const locked=new Promise<void>((resolve,reject)=>{let text='';gate!.stdout!.on('data',b=>{text+=String(b);if(text.includes('locked'))resolve();});gate!.once('error',reject);});
   gate.stdin!.write("BEGIN;SELECT pg_advisory_lock(733631);SELECT 'locked';\n");await locked;
   sql('ALTER VIEW pg_stat_statements RENAME TO interface_real_statistics; CREATE VIEW pg_stat_statements AS WITH held AS MATERIALIZED (SELECT pg_advisory_xact_lock(733631)) SELECT s.* FROM interface_real_statistics s CROSS JOIN held; GRANT SELECT ON pg_stat_statements TO fleet;');
  }else if(scenario==='statistics exception') sql('DROP EXTENSION pg_stat_statements;');

  const {id:run}=await request('start',{spec:{sourceDistribution:'distinct',readers:1,rate:1,warmup:0,duration:1,burst:1,recovery:1,mode:'connected',network:'unshaped'},readers:[{id:'negative-reader',project:config.projects[0]}],browser:{classification:positive?'positive protocol finalization test, no actual DOM':'negative protocol test, no actual DOM'}});
  let result:any;
  if(!positive){
  const body={observation:'negative-1',reader:'negative-reader',run,project:config.projects[0],version:'ffffffff-ffff-ffff-ffff-ffffffffffff:1',value:0,errors:0,text:'0 · 0 failed',visible:true,browserMonotonic:1,browserWall:1,domNodes:0,longTasks:[]};
  assert.equal((await request('render',body)).accepted,false);
  await new Promise(r=>setTimeout(r,50));assert.equal((await request('render',body)).accepted,false);
  assert.equal((await request('render',{...body,value:1,text:'1 · 0 failed'})).reason,'observation-changed');
  await wait(async()=>{result=await request('status');return !!result.status;});
  assert.ok(['failed','incomplete'].includes(result.status),'no-render run cannot pass even if source timing is unqualified');assert.equal(result.renderVerdict,'failed');assert.equal(result.samples.count,0);assert.equal(result.expectedSamples,2);assert.equal(result.missing.length,2);
  assert.ok(result.errors.includes('observation-changed'));assert.equal(result.artifactsComplete,false);assert.ok(result.auxiliaryErrors.some((e:string)=>e.includes('query-statistics psql exit')));assert.ok(!result.errors.some((e:string)=>e.includes('query-statistics')),'auxiliary failure does not overwrite render errors');assert.equal(exited,false,'export failure keeps final status available');

  }else{
   // Deliberately synthetic protocol observations: exercise REAL journal/proof/finish,
   // never label these messages as a measured browser/render acceptance result.
   const db=JSON.parse(readFileSync(join(out,'pg-before.json'),'utf8')).database;
   const bodies=new Map<string,any>();
   await wait(async()=>{
    result=await request('status');if(result.status)return true;
    if(result.finalizing&&scenario==='large export parallel stop'){
     await wait(async()=>Number(sql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%json_agg(s)%'"))===1);
     const parallel=await Promise.all([request('status'),request('stop',{}),request('status')]);
     assert.equal(parallel[0].finalizing,true);assert.equal(parallel[2].finalizing,true);assert.equal(exited,false,'stop waits for the real finalization owner');
     gate!.stdin!.end('SELECT pg_advisory_unlock(733631);COMMIT;\n');await gone;stopped=true;
     result=JSON.parse(readFileSync(join(out,run+'-result.json'),'utf8'));return true;
    }
    if(journalDropped)return false;
    const raw=execFileSync('psql',['-X','-qAt','-v','ON_ERROR_STOP=1','-h',join(out,'pg'),'-p',String(pgport),'-U','postgres','-d',db],{input:"SET fleet.project_ids='*';SELECT json_build_object('version',epoch||':'||revision,'calls',calls,'errors',errors) FROM interface_publication ORDER BY revision DESC LIMIT 1",encoding:'utf8'}).trim();
    const row=JSON.parse(raw);let body=bodies.get(row.version);
    if(!body){body={observation:'protocol-'+bodies.size,reader:'negative-reader',run,project:config.projects[0],version:row.version,value:row.calls,errors:row.errors,text:`${row.calls} · ${row.errors} failed`,visible:true,browserMonotonic:1,browserWall:1,domNodes:0,longTasks:[]};bodies.set(row.version,body);}
    const ack=await request('render',body);await request('render',body);if(scenario==='journal export exception'&&row.calls===2&&ack.accepted){sql('DROP TABLE interface_publication;');journalDropped=true;}return false;
   });
   assert.equal(result.renderVerdict,'passed');assert.equal(result.samples.count,2);assert.equal(result.expectedSamples,2);assert.equal(result.missing.length,0);assert.deepEqual(result.errors,[]);assert.deepEqual(result.auxiliaryErrors,[]);const statistics=readFileSync(join(out,run+'-query-statistics.json'),'utf8');assert.ok(JSON.parse(statistics).length>0,'other statistics export succeeds');
   if(scenario==='journal export exception'){assert.equal(result.artifactsComplete,false);assert.equal(result.status,'incomplete');assert.equal(result.reason,'artifact-export-failed');assert.ok(result.journalErrors.some((e:string)=>e.startsWith('journal-export:')));assert.equal(result.journalStats,null);assert.ok(existsSync(join(out,run+'-pg-after.json')),'snapshot succeeds independently');assert.ok(existsSync(join(out,'publication-journal.jsonl.partial')));assert.ok(existsSync(join(out,'publication-notifications.jsonl.partial')));}
   else{assert.equal(result.artifactsComplete,true);assert.deepEqual(result.journalErrors,[]);assert.ok(Buffer.byteLength(statistics)>1048576);}
   assert.ok(result.samples.min>=0&&result.samples.max<=5000);assert.ok(['passed','incomplete'].includes(result.status));
  }
  assert.deepEqual(result.spec.fixture,config.fixture,'result retains actual fixture dimensions, not a diagnostic default');
  assert.deepEqual(JSON.parse(readFileSync(join(out,'source-manifest.json'),'utf8')).spec.fixture,config.fixture);
  assert.equal(result.candidate.sha,config.candidate,'result binds the candidate actually served');
  if(!stopped)await request('stop',{});await gone;
  const events=readFileSync(join(out,'events.jsonl'),'utf8').trim().split('\n').map(l=>JSON.parse(l));assert.equal(events.filter(e=>e.type==='finish-start').length,1);assert.equal(events.filter(e=>e.type==='finish').length,1,'one owner publishes exactly one result');
  if(!positive){
  const observations=events.filter(x=>x.type==='render-observation');assert.equal(observations.length,3);
  assert.equal(new Set(observations.map(x=>x.firstReceipt)).size,1);assert.ok(observations[1].receipt>observations[0].receipt);
  assert.equal(events.filter(x=>x.type==='render-sample').length,0,'a synthetic unknown observation must never become an actual-render sample');

  }else{
   const latest=new Map<string,number>();
   for(const event of events.filter(x=>x.type==='render-sample'||x.type==='render-sample-correction'))latest.set(event.reader+'/'+event.fact,event.upperMs);
   const values=[...latest.values()].sort((a,b)=>a-b);assert.equal(values.length,2);
   assert.equal(result.samples.count,latest.size);assert.equal(result.samples.min,values[0]);assert.equal(result.samples.max,values.at(-1));
   const observations=events.filter(x=>x.type==='render-observation');assert.ok(observations.length>2);
   for(const observation of observations)assert.equal(observation.firstReceipt,observations.find(x=>x.observation.observation===observation.observation.observation)!.receipt);
  }
  if(scenario!=='journal export exception')assert.ok(readFileSync(join(out,'publication-notifications.jsonl'),'utf8').trim(),'committed notification/XID mapping exported for the actual protocol');
  if(scenario!=='journal export exception')assert.ok(readFileSync(join(out,'publication-journal.jsonl'),'utf8').trim(),'committed journal evidence retained even for failed/no-render runs');
 }finally{
  gate?.stdin?.end();gate?.kill('SIGTERM');
  if(!exited){try{await request('stop',{})}catch{child.kill('SIGTERM')}await gone;}
  log.end();
 }
 });
}

// Requirement: cancellation stops offering, retains incomplete evidence and finalizes once.
// Risk: stop races the producer into two results, hangs timers, or turns missing facts into PASS.
test('fixture cancellation retains one incomplete result and cleans up its actual private backend',{timeout:60000},async t=>{
 const out=mkdtempSync(join(root,'.egm','i-')),port=await freePort(),pgport=await freePort(),origin='http://127.0.0.1:'+port;
 const placement=join(out,'placement.json');writeFileSync(placement,JSON.stringify({development:true}));const log=createWriteStream(join(out,'server.log'));
 const child=spawn(process.execPath,[join(root,'tests/performance/interface.ts'),'serve','--development','--history=18','--projects=1','--out='+out,'--placement='+placement,'--port='+port],{cwd:root,env:{...process.env,FLEET_TRIAL_PGDATA:join(out,'pg'),FLEET_TRIAL_PGPORT:String(pgport)},stdio:['ignore','pipe','pipe']});child.stdout.pipe(log,{end:false});child.stderr.pipe(log,{end:false});
 let exited=false;const gone=new Promise<void>(resolve=>child.once('exit',code=>{exited=true;assert.equal(code,0);resolve();}));
 const request=async(path:string,body?:object)=>{const response=await fetch(origin+'/__interface/'+path,body?{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)}:{});assert.equal(response.status,200);return response.json() as Promise<any>;};
 try{
  let config:any;for(;;){t.signal.throwIfAborted();try{config=await request('config');break;}catch{assert.equal(exited,false);await new Promise(resolve=>setTimeout(resolve,20));}}
  const {id}=await request('start',{spec:{sourceDistribution:'distinct',readers:1,rate:1,warmup:2,duration:3,burst:1,recovery:5,mode:'connected',network:'unshaped'},readers:[{id:'cancel-reader',project:config.projects[0]}],browser:{classification:'cancellation protocol test, no actual DOM'}});
  await Promise.all([request('stop',{}),request('stop',{})]);await gone;
  const result=JSON.parse(readFileSync(join(out,id+'-result.json'),'utf8'));assert.equal(result.status,'incomplete');assert.equal(result.sourceRate.status,'invalid-source-rate');assert.ok(result.errors.includes('run-cancelled'));assert.notEqual(result.renderVerdict,'passed');
  const events=readFileSync(join(out,'events.jsonl'),'utf8').trim().split('\n').map(line=>JSON.parse(line));assert.equal(events.filter(e=>e.type==='finish-start').length,1);assert.equal(events.filter(e=>e.type==='finish').length,1);
  assert.ok(readFileSync(join(out,'source-manifest.json'),'utf8').length>0);assert.match(readFileSync(join(out,'pg','server.log'),'utf8'),/database system is shut down/);
 }finally{if(!exited){child.kill('SIGTERM');await gone;}log.end();}
});
