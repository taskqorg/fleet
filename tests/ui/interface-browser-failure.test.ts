import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createWriteStream,existsSync,mkdirSync,mkdtempSync,readFileSync,writeFileSync} from 'node:fs';
import {join} from 'node:path';
import {freePort,root} from '../harness.ts';

// Requirement: a real browser preserves available traces and an honestly incomplete image before closing.
// Risk: a fixture/status failure loses painted evidence or a partial browser artifact becomes acceptance.
test('browser endpoint failure preserves incomplete evidence and closes Chromium',{skip:!process.env.FLEET_CONTROL_BROWSER,timeout:60000},async t=>{
 mkdirSync(join(root,'.egm'),{recursive:true});const out=mkdtempSync(join(root,'.egm','i-')),browserOut=join(out,'browser'),port=await freePort(),pgport=await freePort();mkdirSync(browserOut);
 const placement=join(out,'placement.json');writeFileSync(placement,JSON.stringify({development:true}));const browserPlacement=join(browserOut,'placement.json');writeFileSync(browserPlacement,JSON.stringify({classification:'browser failure correctness test, not capacity'}));
 const log=createWriteStream(join(out,'server.log')),browserLog=createWriteStream(join(browserOut,'browser.log'));
 const server=spawn(process.execPath,[join(root,'tests/performance/interface.ts'),'serve','--development','--history=18','--projects=1','--out='+out,'--placement='+placement,'--port='+port],{cwd:root,env:{...process.env,FLEET_TRIAL_PGDATA:join(out,'pg'),FLEET_TRIAL_PGPORT:String(pgport)},stdio:['ignore','pipe','pipe']});server.stdout.pipe(log,{end:false});server.stderr.pipe(log,{end:false});
 let serverExit=false,browser:ReturnType<typeof spawn>|undefined;const serverGone=new Promise<number|null>(resolve=>server.once('exit',code=>{serverExit=true;resolve(code);}));
 const origin='http://127.0.0.1:'+port;
 try{
  for(;;){t.signal.throwIfAborted();try{if((await fetch(origin+'/__interface/config')).ok)break;}catch{}assert.equal(serverExit,false);await new Promise(resolve=>setTimeout(resolve,20));}
  browser=spawn(process.execPath,[join(root,'tests/performance/interface.ts'),'browse','--origin='+origin,'--out='+browserOut,'--placement='+browserPlacement,'--source-distribution=distinct','--readers=1','--rate=1','--warmup=2','--duration=3','--burst=1','--recovery=5'],{cwd:root,stdio:['ignore','pipe','pipe']});browser.stdout!.pipe(browserLog,{end:false});browser.stderr!.pipe(browserLog,{end:false});const browserGone=new Promise<number|null>(resolve=>browser!.once('exit',resolve));
  for(;;){t.signal.throwIfAborted();assert.equal(browser.exitCode,null,'browser failed before the mounted-reader barrier: '+readFileSync(join(browserOut,'browser.log'),'utf8'));if(readFileSync(join(out,'events.jsonl'),'utf8').includes('"type":"start"'))break;assert.equal(serverExit,false);await new Promise(resolve=>setTimeout(resolve,20));}
  const response=await fetch(origin+'/__interface/stop',{method:'POST',headers:{'content-type':'application/json'},body:'{}'});assert.equal(response.status,200);assert.equal(await serverGone,0);assert.equal(await browserGone,1);
  const evidence=JSON.parse(readFileSync(join(browserOut,'browser-incomplete.json'),'utf8'));assert.equal(evidence.incomplete,true);assert.equal(evidence.visualAcceptance,false);assert.ok(evidence.failure);assert.ok(evidence.navigation[0].initialVisible==='visible');
  const traces=JSON.parse(readFileSync(join(browserOut,'api-traces-incomplete.json'),'utf8'));assert.ok(traces[0].requests.some((r:any)=>r.version&&r.bodyParsedAt),'actual API receipts survive endpoint loss');
  assert.ok(readFileSync(join(browserOut,'rendered-incomplete.png')).length>1000);assert.equal(existsSync(join(browserOut,'rendered.png')),false,'incomplete capture cannot masquerade as completed acceptance');
  console.log('incomplete browser screenshot:',join(browserOut,'rendered-incomplete.png'));
 }finally{if(!serverExit){server.kill('SIGTERM');await serverGone;}if(browser?.exitCode===null)browser.kill('SIGTERM');log.end();browserLog.end();}
});
