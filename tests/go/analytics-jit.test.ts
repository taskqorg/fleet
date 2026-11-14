import {test} from 'node:test';
import assert from 'node:assert/strict';
import {homedir} from 'node:os';
import {join} from 'node:path';
import {execFileSync} from 'node:child_process';
import {startCoordinator} from '../harness.ts';

test('resource lineage preserves JIT settings and accounting with realistic history (requirement: fleet-analytics.md; risk: JIT blocks PM acknowledgments and settings leak)', async () => {
 const c = await startCoordinator({name:'analytics-jit'});
 try {
  c.sql(`INSERT INTO execution(id,kind,state,project_id) SELECT 'jit-history-'||n,'agent_run','succeeded','desiign' FROM generate_series(1,23000) n;`);
  const db=c.sql('SELECT current_database()').trim();
  const socket=process.env.FLEET_TRIAL_PGDATA || join(homedir(),'.cache/fleet-trial/pg');
  const port=process.env.FLEET_TRIAL_PGPORT || '54329';
  const url=`postgresql://fleet@/${db}?host=${encodeURIComponent(socket)}&port=${port}`;
  let output:string; try { output=execFileSync('go',['test','./coordinator','-run','^TestTaskResourceJITPostgres$','-count=1','-v'],{cwd:'go',env:{...process.env,FLEET_JIT_TEST_DATABASE_URL:url},encoding:'utf8'}); } catch (error:any) { console.error(error.stdout?.toString(),error.stderr?.toString()); throw error; }
  assert.match(output,/--- PASS: TestTaskResourceJITPostgres/);
  const op=async (name:string,body:object,token=c.secret,host=false) => {
   const r=await fetch(`${c.base}${host?'/api/hosts/':'/api/ops/'}${name}`,{method:'POST',headers:{authorization:`Bearer ${token}`,...(host?{}:{'x-fleet-person':'owner@trial.test'}),'content-type':'application/json'},body:JSON.stringify(body)});
   const data=await r.json(); assert.equal(r.status,200,JSON.stringify(data)); return data as any;
  };
  const h=await op('hosts_register',{id:'jit-host',holder:'owner@trial.test'});
  const poll={hostId:'jit-host',platform:'darwin',capabilities:['workspace-key-v1'],stores:[],problems:[]};
  await op('poll',poll,h.secret,true);
  const start=await op('session_start',{projectId:'desiign',profileId:'pm',role:'pm',hostId:'jit-host',requestId:'jit-pm',text:'qualification'});
  const claimed=await op('poll',poll,h.secret,true);
  const command=claimed.commands.find((x:any)=>x.executionId===start.executionId && x.kind==='start');
  assert.ok(command);
  const detail=await op('session_get',{sessionId:start.sessionId});
  assert.equal(detail.sessionId,start.sessionId);
  assert.equal(detail.resources.own.executions,1);

  const envelope={hostId:'jit-host',commandId:command.id,executionId:start.executionId,agentToolKey:'jit-task-create-once',request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'task_create',arguments:{projectId:'desiign',title:'JIT replay qualification'}}}};
  const first=await op('agent-tools',envelope,h.secret,true);
  const replay=await op('agent-tools',envelope,h.secret,true);
  assert.deepEqual(JSON.parse(replay.result.content[0].text),JSON.parse(first.result.content[0].text),'same native request returns committed acknowledgment');
  assert.equal(c.sql("SELECT count(*) FROM task WHERE title='JIT replay qualification'").trim(),'1');
  assert.equal(c.sql("SELECT count(*) FROM agent_tool_receipt WHERE id='jit-task-create-once'").trim(),'1');

 } finally {await c.stop();}
});
