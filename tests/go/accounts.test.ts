// Account reads over the canonical route and operation. Historical typed rows are fixture seed data only.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator, lit} from '../harness.ts';

test('Account inspections retain legacy host work without widening actor/project access (requirement: fleet-people-access subscriptions and data-foundation typed execution history; risk: account reads 404 on retained tool_run or disclose a foreign execution)', {timeout: 30000}, async () => {
  const c = await startCoordinator({name:'account-read'});
  const owner='owner@trial.test', architect='architect@trial.test', mine='desiign', other='hello-world';
  const call=async(method:string,path:string,body?:unknown,person=owner)=>{
    const r=await fetch(c.base+path,{method,headers:{authorization:'Bearer '+c.secret,'x-fleet-person':person,...(body===undefined?{}:{'content-type':'application/json'})},body:body===undefined?undefined:JSON.stringify(body)});
    return {status:r.status,body:await r.json()};
  };
  const op=(name:string,b:object={},person=owner)=>call('POST','/api/ops/'+name,b,person);
  const ok=(r:any)=>{assert.equal(r.status,200,JSON.stringify(r.body));return r.body;};
  const read=(person:string)=>Promise.all([call('GET','/api/accounts',undefined,person),op('account_list',{},person)]);
  const seed=(accountId:string,project:string,kind:string,executionAccount=accountId,executionProject=project)=>{
    const id=randomUUID(), executionId=randomUUID();
    const data={id:executionId,executionId,accountId:executionAccount,actor:'program-worker',state:'succeeded',error:{message:'foreign-canary-'+executionId}};
    c.sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES(${lit(executionId)},${lit(kind)},'failed',${lit(executionProject)},${lit(data)});
      INSERT INTO document(id,kind,project_id,data) VALUES(${lit(id)},'account-inspection',${lit(project)},${lit({id,accountId,executionId,hostId:'invented-host',binding:null,createdAt:1})});`);
    return {id,executionId};
  };
  try {
    const people=ok(await op('people_list')).people;
    ok(await op('people_set',{people:[...people,{email:architect,name:'Scoped architect',roles:['architect'],grants:[{kind:'project',id:mine}]}]}));
    const account=ok(await op('account_create',{name:'inspection-own',harness:'codex',holder:architect}));
    const foreign=ok(await op('account_create',{name:'inspection-foreign',harness:'codex'}));
    const memberAccount=ok(await op('account_create',{name:'inspection-member',harness:'codex',holder:'member@trial.test'}));
    const memberInspection=seed(memberAccount.id,mine,'tool_run');
    const legacy=seed(account.id,mine,'tool_run'), current=seed(account.id,mine,'host_job');
    const hidden=seed(foreign.id,other,'tool_run');
    for(const person of [owner,architect]) for(const response of await read(person)) {
      const view=ok(response), own=view.accounts.find((a:any)=>a.id===account.id);
      assert.deepEqual(own.inspections.map((i:any)=>[i.id,i.state]).sort(),[[legacy.id,'failed'],[current.id,'failed']].sort(),'typed state wins over stale data.state');
      if(person===architect) assert.ok(!JSON.stringify(view).includes(hidden.executionId) && !JSON.stringify(view).includes(hidden.id));
    }
    const [memberRoute,memberOp]=await read('member@trial.test');
    assert.equal(memberRoute.status,403,'existing route architect gate is unchanged');
    const memberView=ok(memberOp);assert.deepEqual(memberView.accounts.map((a:any)=>a.id),[memberAccount.id]);
    assert.equal(memberView.accounts[0].inspections[0].id,memberInspection.id);
    const outsider=ok(await op('account_list',{},'outsider@trial.test'));assert.deepEqual(outsider.accounts,[]);
    // Even a caller granted both projects must not project a mismatched account/project execution into this inspection.
    for(const [executionAccount,executionProject] of [[foreign.id,mine],[account.id,other]]) {
      const bad=seed(account.id,mine,'tool_run',executionAccount,executionProject);
      for(const person of [owner,architect]) for(const response of await read(person)) {
        assert.equal(response.status,404);assert.ok(!JSON.stringify(response.body).includes('foreign-canary'));
      }
      c.sql(`DELETE FROM document WHERE id=${lit(bad.id)}`);
    }
  } finally {await c.stop();}
});

test('Selected execution hosts inspect their own account through authenticated commands (requirement: fleet.md host-owned accounts; risk: Mac-only routing blocks Windows/Linux or accepts another host receipt)', {timeout:30000}, async()=>{
 const c=await startCoordinator({name:'account-remote'});
 const call=async(path:string,body:object,secret=c.secret,person='owner@trial.test')=>{
  const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+secret,...(secret===c.secret?{'x-fleet-person':person}:{}),'content-type':'application/json'},body:JSON.stringify(body)});
  return {status:r.status,body:await r.json()};
 };
 const op=(name:string,b:object)=>call('/api/ops/'+name,b);
 const ok=(r:any)=>{assert.equal(r.status,200,JSON.stringify(r.body));return r.body;};
 try {
  const a=ok(await op('account_create',{name:'remote',harness:'codex'}));
  for(const platform of ['win32','linux']){
   const id='account-'+platform,secret=ok(await op('hosts_register',{id})).secret;
   const poll={platform,capabilities:['codex'],stores:[],problems:[]};
   ok(await call('/api/hosts/poll',poll,secret));
   const inspection=ok(await op('account_connect',{accountId:a.id,hostId:id,binding:'fixture-codex'}));
   const commands=ok(await call('/api/hosts/poll',poll,secret)).commands;
   const cmd=commands.find((x:any)=>x.executionId===inspection.executionId);
   assert.equal(cmd.kind,'account-inspect');assert.equal(cmd.payload.nativeHostId,id);
   const receipt={hostId:id,executionId:inspection.executionId,commandId:cmd.id,accountObservation:{accountId:a.id,binding:'fixture-codex',platform,readiness:'ready',reason:'ready',nativeLoginObserved:true,quota:'unknown'}};
   assert.equal((await call('/api/hosts/accounts/report',receipt)).status,401,'browser cannot forge host receipt');
   ok(await call('/api/hosts/accounts/report',receipt,secret));
  }
 }finally{await c.stop();}
});
