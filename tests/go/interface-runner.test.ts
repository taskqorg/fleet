import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdirSync,mkdtempSync,writeFileSync,readFileSync,createWriteStream} from 'node:fs';
import {join} from 'node:path';
import {freePort,root} from '../harness.ts';

// Requirement: observation receipt survives retries while unknown/no actual render cannot pass.
// Risk: delayed retries forge a successful render or erase missing samples.
// This is a negative wire regression, not a browser or performance acceptance result.
test('fixture rejects unknown and changed observations and fails without actual render samples',{timeout:30000},async()=>{
 mkdirSync(join(root,'.egm'),{recursive:true});const out=mkdtempSync(join(root,'.egm','i-'));
 const port=await freePort(),pgport=await freePort(),origin='http://127.0.0.1:'+port;
 const placement=join(out,'placement.json');writeFileSync(placement,JSON.stringify({development:true}));
 const log=createWriteStream(join(out,'server.log'));
 const child=spawn(process.execPath,[join(root,'tests/performance/interface.ts'),'serve','--development','--history=18','--projects=1','--out='+out,'--placement='+placement,'--port='+port],{cwd:root,env:{...process.env,FLEET_TRIAL_PGDATA:join(out,'pg'),FLEET_TRIAL_PGPORT:String(pgport)},stdio:['ignore','pipe','pipe']});
 child.stdout.pipe(log,{end:false});child.stderr.pipe(log,{end:false});
 let exited=false;const gone=new Promise<void>(r=>child.once('exit',()=>{exited=true;r();}));
 const request=async(path:string,body?:object)=>{const r=await fetch(origin+'/__interface/'+path,body?{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(body)}:{});const b=await r.json() as any;assert.equal(r.status,200,JSON.stringify(b));return b;};
 const wait=async(check:()=>Promise<boolean>,ms=15000)=>{const end=Date.now()+ms;while(!await check()){assert.ok(!exited&&Date.now()<end,'private runner did not reach checkpoint: '+readFileSync(join(out,'server.log'),'utf8'));await new Promise(r=>setTimeout(r,20));}};
 try{
  let config:any;await wait(async()=>{try{config=await request('config');return true}catch{return false}});
  const {id:run}=await request('start',{spec:{sourceDistribution:'distinct',readers:1,rate:1,warmup:0,duration:1,burst:1,recovery:1,mode:'connected',network:'unshaped'},readers:[{id:'negative-reader',project:config.projects[0]}],browser:{classification:'negative protocol test, no actual DOM'}});
  const body={observation:'negative-1',reader:'negative-reader',run,project:config.projects[0],version:'ffffffff-ffff-ffff-ffff-ffffffffffff:1',value:0,errors:0,text:'0 · 0 failed',visible:true,browserMonotonic:1,browserWall:1,domNodes:0,longTasks:[]};
  assert.equal((await request('render',body)).accepted,false);
  await new Promise(r=>setTimeout(r,50));assert.equal((await request('render',body)).accepted,false);
  assert.equal((await request('render',{...body,value:1,text:'1 · 0 failed'})).reason,'observation-changed');
  let result:any;await wait(async()=>{result=await request('status');return !!result.status;});
  assert.ok(['failed','incomplete'].includes(result.status),'no-render run cannot pass even if source timing is unqualified');assert.equal(result.renderVerdict,'failed');assert.equal(result.samples.count,0);assert.equal(result.expectedSamples,2);assert.equal(result.missing.length,2);
  assert.ok(result.errors.includes('observation-changed'));
  await request('stop',{});await gone;
  const events=readFileSync(join(out,'events.jsonl'),'utf8').trim().split('\n').map(l=>JSON.parse(l));
  const observations=events.filter(x=>x.type==='render-observation');assert.equal(observations.length,3);
  assert.equal(new Set(observations.map(x=>x.firstReceipt)).size,1);assert.ok(observations[1].receipt>observations[0].receipt);
  assert.equal(events.filter(x=>x.type==='render-sample').length,0,'a synthetic unknown observation must never become an actual-render sample');
  assert.ok(readFileSync(join(out,'publication-journal.jsonl'),'utf8').trim(),'committed journal evidence retained even for failed/no-render runs');
 }finally{
  if(!exited){try{await request('stop',{})}catch{child.kill('SIGTERM')}await gone;}
  log.end();
 }
});
