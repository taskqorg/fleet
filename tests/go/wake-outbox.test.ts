import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {join} from 'node:path';
import {homedir} from 'node:os';
import {startCoordinator,root} from '../harness.ts';

// Requirement: committed notification intents survive failures/restarts and retain source XIDs.
// Risk: removing NOTIFY from source commits silently loses SSE/resource updates or widens RLS.
test('durable wake dispatch qualifies against actual PostgreSQL failures, restart and SSE',{timeout:90000},async()=>{
 const env={FLEET_RESOURCE_PROCESSOR:'off',FLEET_RESOURCE_READS:'on',FLEET_LOST_AFTER_MS:'864000000'};
 const c=await startCoordinator({name:'wake-outbox',env});
 const wait=async(check:()=>boolean|Promise<boolean>,ms=15000)=>{const end=Date.now()+ms;while(!await check()){assert.ok(Date.now()<end,'wake fixture checkpoint timeout');await new Promise(r=>setTimeout(r,10));}};
 const abort=new AbortController();let stream:Promise<void>|undefined;
 try{
  c.sql(`INSERT INTO definition(kind,id,path,head,project_id) VALUES('project','WAKE','fixture',0,'WAKE');
   CREATE TABLE wake_fixture_fault(kind text PRIMARY KEY);
   CREATE FUNCTION wake_fixture_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
    IF EXISTS(SELECT 1 FROM wake_fixture_fault WHERE kind='error') THEN RAISE EXCEPTION 'fixture wake deletion failure'; END IF;
    IF EXISTS(SELECT 1 FROM wake_fixture_fault WHERE kind='gate') THEN PERFORM pg_advisory_xact_lock(hashtextextended('fixture:wake-delete',0)); END IF;
    RETURN OLD; END $$;
   CREATE TRIGGER wake_fixture_delete BEFORE DELETE ON wake_outbox FOR EACH ROW EXECUTE FUNCTION wake_fixture_delete()`);
  // Stop only this fixture's real coordinator, retaining its private database for deterministic claims.
  process.kill(-c.pid,'SIGTERM');await wait(()=>c.exited(),6000);
  const pg=process.env.FLEET_TRIAL_PGDATA||join(homedir(),'.cache/fleet-trial/pg');
  const db=c.sql('SELECT current_database()').trim();
  const url=`postgresql://fleet_app@/${db}?host=${encodeURIComponent(pg)}&port=${process.env.FLEET_TRIAL_PGPORT||'54329'}`;
  const code=await new Promise<number|null>((resolve,reject)=>{
   const child=spawn('go',['test','./coordinator','-run','^TestWakeOutboxPostgres$','-count=1','-v'],{cwd:join(root,'go'),env:{...process.env,FLEET_WAKE_TEST_URL:url},stdio:'inherit'});
   child.once('error',reject);child.once('exit',resolve);
  });
  assert.equal(code,0,'actual wake claim/rollback/backend-loss/RLS proof');
  const retained=c.sql("SELECT wake_id::text||':'||source_xid::text FROM wake_outbox WHERE payload='restart-owned'").trim();assert.ok(retained);
  assert.equal(c.sql("SELECT count(*) FROM resource_work WHERE execution_id='wake-source'").trim(),'1');
  env.FLEET_RESOURCE_PROCESSOR='on';await c.restart();
  const op=async(name:string,body:object)=>{const r=await fetch(c.base+'/api/ops/'+name,{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify(body)});assert.equal(r.status,200);return r.json() as Promise<any>;};
  await wait(async()=>{const h=await op('project_resources',{projectId:'WAKE'});return h.kind==='ready'&&h.header.executions===1&&!h.progress.pending;});
  await wait(()=>c.sql('SELECT count(*) FROM wake_outbox').trim()==='0');
  const response=await fetch(c.base+'/api/stream',{headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test'},signal:abort.signal});assert.equal(response.status,200);
  const frames:any[]=[];
  stream=(async()=>{let buffer='';try{for await(const bytes of response.body as any){buffer+=new TextDecoder().decode(bytes);let end;while((end=buffer.indexOf('\n\n'))>=0){const frame=buffer.slice(0,end);buffer=buffer.slice(end+2);const data=frame.split('\n').find(l=>l.startsWith('data: '));if(data)frames.push({event:frame.split('\n').find(l=>l.startsWith('event: '))?.slice(7),data:JSON.parse(data.slice(6))});}}}catch(e){if(!abort.signal.aborted)throw e;}})();
  await wait(()=>frames.some(f=>f.event==='ready'));
  c.sql("INSERT INTO execution(id,kind,state,project_id,data) VALUES('wake-after-restart','tool_run','succeeded','WAKE','{\"tool\":\"wake-proof\"}')");
  await wait(()=>frames.some(f=>f.data.kind==='resource_summary'&&f.data.id==='WAKE'),5000);
  const h=await op('project_resources',{projectId:'WAKE'});assert.equal(h.header.executions,2);
  await wait(()=>c.sql('SELECT count(*) FROM wake_outbox').trim()==='0');
  assert.equal(c.sql("SELECT count(*) FROM resource_work WHERE execution_id IN ('wake-source','wake-after-restart')").trim(),'0');
 }finally{abort.abort();await stream;await c.stop();}
});
