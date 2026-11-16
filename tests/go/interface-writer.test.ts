import {test} from 'node:test';
import assert from 'node:assert/strict';
import {PassThrough} from 'node:stream';
import {homedir} from 'node:os';
import {join} from 'node:path';
import {startCoordinator} from '../harness.ts';
import {Writer} from '../performance/sql-writer-interface.ts';
import {sourceTransaction,type Fact} from '../performance/interface.ts';

// Requirement: eight FIFO sessions pipeline separate durable canonical transactions with exact receipts/parity.
// Risk: receipt gating serializes the producer, batching merges XIDs, or partial failure/cancel invents confirmation.
test('fixture SQL lanes preserve FIFO independent commits, error prefixes, cancellation and native parity',{timeout:60000},async()=>{
 const c=await startCoordinator({name:'interface-writer',env:{FLEET_RESOURCE_PROCESSOR:'on',FLEET_RESOURCE_READS:'on',FLEET_LOST_AFTER_MS:'864000000'}});
 // The harness supports its documented default socket when no override is exported; Writer receives that same owned socket.
 const previousSocket=process.env.FLEET_TRIAL_PGDATA;process.env.FLEET_TRIAL_PGDATA=previousSocket||join(homedir(),'.cache/fleet-trial/pg');
 const writers:Writer[]=[],log=new PassThrough();log.resume();
 const make=()=>{const w=new Writer(c.sql('SELECT current_database()').trim(),log);writers.push(w);return w;};
 const fact=(i:number,id='pipe-'+i):Fact=>({id,execution:id,project:'pipe-project',n:i+1,calls:1,errors:i%7===0?1:0,lower:performance.now(),upper:null,phase:'burst',due:0});
 const until=async(check:()=>boolean)=>{const end=Date.now()+12000;while(!check()){assert.ok(Date.now()<end,'native protocol checkpoint timeout');await new Promise(r=>setTimeout(r,20));}};
 const submit=(w:Writer,f:Fact,padding='')=>w.run(sourceTransaction(f,'distinct')+padding,`json_build_object('transaction',:'source_tx','beforeCommit',:'before_commit')`);
 try{
  c.sql("INSERT INTO definition(kind,id,path,head,project_id) VALUES('project','pipe-project','fixture',0,'pipe-project'); INSERT INTO execution(id,kind,state,project_id) VALUES('pipe-baseline','agent_run','running','pipe-project')");
  await until(()=>c.sql("SELECT ready FROM resource_project_summary WHERE project_id='pipe-project'").trim()==='t');
  assert.equal(c.sql('SHOW synchronous_commit').trim(),'on');assert.equal(c.sql('SHOW fsync').trim(),'on');
  const lanes=Array.from({length:8},make),queued=Array.from({length:40},(_,i)=>submit(lanes[i%8],fact(i)));
  const receipts=await Promise.all(queued);assert.equal(new Set(receipts.map(r=>r.backend)).size,8);assert.equal(new Set(receipts.map(r=>r.payload.transaction)).size,40);
  for(let lane=0;lane<8;lane++){const rs=receipts.filter((_,i)=>i%8===lane);assert.equal(new Set(rs.map(r=>r.backend)).size,1);for(let i=1;i<rs.length;i++)assert.ok(BigInt(rs[i].payload.transaction)>BigInt(rs[i-1].payload.transaction),'native per-session FIFO');}
  await until(()=>c.sql("SELECT count(*) FROM resource_work_scope WHERE project_id='pipe-project'").trim()==='0');
  const parity=JSON.parse(c.sql("SELECT json_build_object('calls',calls,'errors',errors) FROM resource_tool WHERE project_id='pipe-project' AND name='!qualification-progress'"));assert.deepEqual(parity,{calls:40,errors:6});
  assert.equal(c.sql("SELECT count(*) FROM execution_totals WHERE execution_id LIKE 'pipe-%' AND tool_calls_basis='measured' AND wall_ms IS NULL AND cpu_ms IS NULL AND api_ms IS NULL").trim(),'40');
  await Promise.all(lanes.map(w=>w.close()));
  const broken=make();
  c.sql("CREATE FUNCTION pipe_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='pipe-reject' THEN RAISE EXCEPTION 'owned protocol rejection'; END IF; IF NEW.id='pipe-cancel' THEN PERFORM pg_sleep(1); END IF; RETURN NEW; END $$;CREATE TRIGGER pipe_reject BEFORE INSERT ON execution FOR EACH ROW EXECUTE FUNCTION pipe_reject()");
  const prefix=await Promise.allSettled([submit(broken,fact(40,'pipe-prefix')),submit(broken,fact(41,'pipe-reject')),submit(broken,fact(42,'pipe-never'))]);
  assert.equal(prefix[0].status,'fulfilled');assert.deepEqual(prefix.slice(1).map(r=>r.status),['rejected','rejected']);await broken.stopped;
  assert.equal(c.sql("SELECT count(*) FROM execution WHERE id='pipe-prefix'").trim(),'1');assert.equal(c.sql("SELECT count(*) FROM execution WHERE id IN ('pipe-reject','pipe-never')").trim(),'0');await assert.rejects(submit(broken,fact(43,'pipe-no-replay')));
  const cancelled=make(),attempts=[submit(cancelled,fact(44,'pipe-cancel'),'\n/*'+'.'.repeat(2*1024*1024)+'*/\n'),submit(cancelled,fact(45,'pipe-after-cancel'))];
  const settled=Promise.allSettled(attempts);
  await until(()=>c.sql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='PgSleep' AND query LIKE '%pipe-cancel%'").trim()==='1');
  await cancelled.cancel();assert.deepEqual((await settled).map(r=>r.status),['rejected','rejected']);
  await until(()=>c.sql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND query LIKE '%pipe-cancel%' AND pid<>pg_backend_pid()").trim()==='0');
  assert.equal(c.sql("SELECT count(*) FROM execution WHERE id IN ('pipe-cancel','pipe-after-cancel')").trim(),'0','cancelled uncommitted prefix rolled back; no false receipt');
  await Promise.all(lanes.map(w=>w.close()));
  assert.equal(c.sql(`SELECT count(*) FROM pg_stat_activity WHERE pid=ANY(ARRAY[${receipts.map(r=>r.backend).join(',')}])`).trim(),'0','owned source sessions closed');
 }finally{await Promise.all(writers.map(w=>w.close()));await c.stop();log.end();if(previousSocket===undefined)delete process.env.FLEET_TRIAL_PGDATA;else process.env.FLEET_TRIAL_PGDATA=previousSocket;}
});
