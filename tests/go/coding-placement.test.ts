import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator} from '../harness.ts';

test('Coding requirements select an eligible host and retain the ordinary coding profile (requirement: fleet.md environment compatibility and project grants; risk: a coding worker starts without its tools or uses an ungranted forge)', {timeout:30000},async()=>{
 const c=await startCoordinator({name:'coding-placement'});
 const call=async(path:string,body:object,secret=c.secret)=>{const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+secret,...(secret===c.secret?{'x-fleet-person':'owner@trial.test'}:{}),'content-type':'application/json'},body:JSON.stringify(body)});return {status:r.status,body:await r.json()};};
 const ok=(r:any)=>{assert.equal(r.status,200,JSON.stringify(r.body));return r.body;};
 const op=(name:string,b:object)=>call('/api/ops/'+name,b);
 try{
  const host='coding-fixture',secret=ok(await op('hosts_register',{id:host})).secret;
  const poll=(capabilities:string[])=>call('/api/hosts/poll',{platform:'darwin',capabilities,stores:[],problems:[]},secret);
  ok(await poll([]));
  const id='coding-'+randomUUID(),settings={id,name:'Coding fixture',contextFiles:[],repos:[{id:'main',remote:'https://github.com/invented/project.git',forge:'github',defaultBranch:'main'}],environments:[{hostId:host,checkoutPath:'/invented/project'}],roles:{worker:'coding-worker'},execution:{tools:['node','pnpm','browser'],forge:true}};
  ok(await op('project_create',{id,requestId:randomUUID(),content:{settings,contextFiles:[]}}));
  const start=ok(await op('session_start',{projectId:id,role:'worker',requestId:randomUUID(),text:'Fixture only.'}));
  assert.ok(!ok(await poll(['tool:node','tool:pnpm','tool:browser'])).commands.some((x:any)=>x.executionId===start.executionId),'tools alone do not grant forge');
  const command=ok(await poll(['tool:node','tool:pnpm','tool:browser','forge:invented/project'])).commands.find((x:any)=>x.executionId===start.executionId);
  assert.ok(command);assert.equal(command.payload.agentConfig.id,'coding-worker');assert.equal(command.payload.gitNetwork,true);
  assert.deepEqual(command.payload.project.execution,{tools:['node','pnpm','browser'],forge:true});
 }finally{await c.stop();}
});
