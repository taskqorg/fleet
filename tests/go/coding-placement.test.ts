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
  for(const profiles of [[7],[''],['coding-worker','coding-worker'],['unknown-fixture-profile']]) {
   const invalid={...settings,id:'invalid-'+randomUUID(),execution:{...settings.execution,profiles}};
   const response=await op('project_create',{id:invalid.id,requestId:randomUUID(),content:{settings:invalid,contextFiles:[]}});assert.equal(response.status,400,JSON.stringify({profiles,...response}));
  }
  const start=ok(await op('session_start',{projectId:id,role:'worker',requestId:randomUUID(),text:'Fixture only.'}));
  assert.ok(!ok(await poll(['tool:node','tool:pnpm','tool:browser'])).commands.some((x:any)=>x.executionId===start.executionId),'tools alone do not grant forge');
  const command=ok(await poll(['tool:node','tool:pnpm','tool:browser','forge:invented/project'])).commands.find((x:any)=>x.executionId===start.executionId);
  assert.ok(command);assert.equal(command.payload.agentConfig.id,'coding-worker');assert.equal(command.payload.gitNetwork,true);
  assert.deepEqual(command.payload.project.execution,{tools:['node','pnpm','browser'],forge:true,profiles:[]});
  const windows='preferred-windows',windowsSecret=ok(await op('hosts_register',{id:windows})).secret;
  const winPoll=()=>call('/api/hosts/poll',{platform:'win32',capabilities:['codex','tool:node','tool:pnpm','tool:browser'],stores:[],problems:[]},windowsSecret);
  ok(await winPoll());
  const connect=async(name:string,h:string,token:string,platform:string,caps:string[])=>{
   const account=ok(await op('account_create',{name,harness:'codex'}));
   const inspection=ok(await op('account_connect',{accountId:account.id,hostId:h,binding:name}));
   const command=ok(await call('/api/hosts/poll',{platform,capabilities:caps,stores:[],problems:[]},token)).commands.find((x:any)=>x.executionId===inspection.executionId);
   ok(await call('/api/hosts/accounts/report',{hostId:h,executionId:inspection.executionId,commandId:command.id,accountObservation:{accountId:account.id,binding:name,platform,readiness:'ready',reason:'ready',nativeLoginObserved:true,quota:'unknown'}},token));
   return account;
  };
  const win=await connect('preferred-windows-account',windows,windowsSecret,'win32',['codex','tool:node','tool:pnpm','tool:browser']);
  const mac=await connect('eligible-mac-account',host,secret,'darwin',['codex','tool:node','tool:pnpm','tool:browser','forge:invented/project']);
  const autoSettings={...settings,id:'auto-'+randomUUID(),subscriptions:[win.name,mac.name],environments:[...settings.environments,{hostId:windows,checkoutPath:'/invented/project'}],execution:{...settings.execution,profiles:['coding-worker']}};
  ok(await op('project_create',{id:autoSettings.id,requestId:randomUUID(),content:{settings:autoSettings,contextFiles:[]}}));
  const auto=ok(await op('session_start',{projectId:autoSettings.id,role:'worker',requestId:randomUUID(),text:'Auto coding fixture.'}));
  assert.ok(!ok(await winPoll()).commands.some((x:any)=>x.executionId===auto.executionId));
  const autoCommand=ok(await poll(['codex','tool:node','tool:pnpm','tool:browser','forge:invented/project'])).commands.find((x:any)=>x.executionId===auto.executionId);
  assert.ok(autoCommand);assert.equal(autoCommand.payload.account,mac.name);
  const explicit=ok(await op('session_start',{projectId:autoSettings.id,role:'worker',account:win.id,hostId:windows,requestId:randomUUID(),text:'Explicit Windows fixture.'}));
  assert.ok(!ok(await winPoll()).commands.some((x:any)=>x.executionId===explicit.executionId));
  assert.ok(!ok(await poll(['codex','tool:node','tool:pnpm','tool:browser','forge:invented/project'])).commands.some((x:any)=>x.executionId===explicit.executionId));
  const excluded={...settings,id:'scoped-'+randomUUID(),roles:{pm:'pm',worker:'coding-worker'},execution:{...settings.execution,profiles:['coding-worker']}};
  ok(await op('project_create',{id:excluded.id,requestId:randomUUID(),content:{settings:excluded,contextFiles:[]}}));
  const pm=ok(await op('session_start',{projectId:excluded.id,role:'pm',hostId:host,requestId:randomUUID(),text:'Read and edit fixture.'}));
  const pmCommand=ok(await poll([])).commands.find((x:any)=>x.executionId===pm.executionId);
  assert.ok(pmCommand,'excluded native PM needs no coding prerequisites');
  assert.equal(pmCommand.payload.agentConfig.id,'pm');
  for(const status of ['started','completed']) ok(await call('/api/hosts/receipts',{executionId:pm.executionId,commandId:pmCommand.id,status,nativeSessionId:'invented-pds-pm'},secret));
  const resumed=ok(await call(`/api/sessions/${pm.sessionId}/messages`,{requestId:randomUUID(),text:'Continue native PM fixture.'}));
  const resumedCommand=ok(await poll([])).commands.find((x:any)=>x.executionId===resumed.id);
  assert.ok(resumedCommand);assert.equal(resumedCommand.payload.nativeSessionId,'invented-pds-pm');assert.equal(resumedCommand.payload.account,pmCommand.payload.account);
  const scoped=ok(await op('session_start',{projectId:excluded.id,role:'worker',hostId:host,requestId:randomUUID(),text:'Coding fixture.'}));
  assert.ok(!ok(await poll([])).commands.some((x:any)=>x.executionId===scoped.executionId));
  assert.ok(ok(await poll(['tool:node','tool:pnpm','tool:browser','forge:invented/project'])).commands.some((x:any)=>x.executionId===scoped.executionId));
 }finally{await c.stop();}
});
