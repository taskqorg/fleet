import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';import {createApp} from '../../packages/coordinator/app.js';
const setup=(extra:any={})=>{const config={database:mkdtempSync('/tmp/fleet-accounts-')+'/db',browserSecret:'browser',machineSecret:'machine',hostId:'mac',integrationHostId:'mac-program',publicationHostId:'pds-program',...extra};let app=createApp(config);
 const call=(url:string,payload?:any,headers:any={authorization:'Bearer browser'})=>app.inject({method:payload?'POST':'GET',url,headers,payload});
 const op=async(name:string,b:any={},code=200)=>{const r=await call('/api/project-operations/'+name,b);assert.equal(r.statusCode,code,r.body);return r.json()};
 const host=async(route:string,b:any,code=200)=>{const r=await call('/api/hosts/'+route,b,{authorization:'Bearer machine'});assert.equal(r.statusCode,code,r.body);return r.json()};
 return {config,call,op,host,get app(){return app},reload:async()=>{await app.close();app=createApp(config)}}};

test('account API/MCP operations require bearer, same Origin and owned host receipts',async()=>{const f=setup();try{
 assert.equal((await f.call('/api/project-operations/account_list',{},{})).statusCode,401);
 assert.equal((await f.call('/api/project-operations/account_list',{},{authorization:'Bearer machine'})).statusCode,401);
 assert.equal((await f.call('/api/hosts/poll',{hostId:'mac-program'},{authorization:'Bearer browser'})).statusCode,401);
 assert.equal((await f.call('/api/project-operations/account_list',{},{authorization:'Bearer browser',origin:'https://evil.example'})).statusCode,403);
 await f.host('poll',{hostId:'stranger',platform:'darwin'},403);
 const a=await f.op('account_create',{name:'own-test',harness:'codex'});await f.host('poll',{hostId:'mac-program',platform:'darwin'});
 const i=await f.op('account_connect',{accountId:a.id,hostId:'mac',binding:'fleet-codex'});const cmd=(await f.host('poll',{hostId:'mac-program',platform:'darwin'})).commands.find((c:any)=>c.kind==='account-inspect');assert.equal(cmd.executionId,i.executionId);
 const obs={accountId:a.id,binding:'fleet-codex',platform:'darwin',readiness:'ready',reason:'ready'};
 await f.host('receipts',{hostId:'pds-program',executionId:i.executionId,commandId:cmd.id,accountObservation:obs},403);
 await f.host('receipts',{hostId:'mac-program',executionId:i.executionId,commandId:cmd.id,accountObservation:{...obs,binding:'other-binding'}},403);
 assert.deepEqual((await f.op('account_list')).accounts[0].bindings,[]);
}finally{await f.app.close()}});

test('proxy mode requires configured Origin for account mutations',async()=>{const f=setup({browserProxyAuth:true,browserOrigin:'https://fleet.example'});try{
 assert.equal((await f.call('/api/project-operations/account_create',{name:'x-test',harness:'codex'},{authorization:'Bearer browser'})).statusCode,403);
 assert.equal((await f.call('/api/project-operations/account_create',{name:'x-test',harness:'codex'},{authorization:'Bearer browser',origin:'https://fleet.example'})).statusCode,200);
}finally{await f.app.close()}});

test('host selection: Claude never PDS, unknown/offline rejected, Windows unprovisioned',async()=>{const f=setup();try{
 const c=await f.op('account_create',{name:'claude-test',harness:'claude'});
 await f.op('account_discover',{accountId:c.id,hostId:'mac'},403);// no heartbeat: platform unknown, Claude ineligible
 await f.op('account_discover',{accountId:c.id,hostId:'pds-program'},409);
 await f.op('account_discover',{accountId:c.id,hostId:'nowhere'},409);
 await f.host('poll',{hostId:'pds-program',platform:'linux'});
 const hs=await f.op('account_hosts',{accountId:c.id});const pds=hs.hosts.find((h:any)=>h.id==='pds-program');assert.equal(pds.eligible,false);assert.match(pds.reason,/never PDS/);assert.match(hs.windows,/no Windows executor/);
 await f.host('accounts',{hostId:'pds-program',accountId:c.id,binding:'fleet-claude',platform:'linux',readiness:'ready'},403);
 await f.host('accounts',{hostId:'pds-program',accountId:c.id,binding:'fleet-claude',platform:'darwin',readiness:'ready'},403);
 await f.op('account_login_handoff',{accountId:c.id,hostId:'pds-program',binding:'fleet-claude'},403);
 await f.host('poll',{hostId:'mac-program',platform:'linux'});
 await f.op('account_discover',{accountId:c.id,hostId:'mac'},403);// Mac inspector reporting non-Mac platform
 await f.host('poll',{hostId:'mac-program',platform:'darwin'});
 assert.ok((await f.op('account_discover',{accountId:c.id,hostId:'mac'})).executionId);
 const x=await f.op('account_create',{name:'codex-test',harness:'codex'});
 await f.op('account_discover',{accountId:x.id,hostId:'pds-program'},409);
 await f.op('account_discover',{accountId:x.id,hostId:'win-host'},409);
 const now=Date.now;Date.now=()=>now()+31000;try{assert.equal((await f.op('account_hosts',{accountId:x.id})).hosts[0].connectivity,'offline');await f.op('account_connect',{accountId:x.id,hostId:'mac',binding:'fleet-codex'},409)}finally{Date.now=now}
}finally{await f.app.close()}});

test('readiness transitions keep prior ready binding, IDs and history across reload',async()=>{const f=setup();try{
 const a=await f.op('account_create',{name:'flow-test',harness:'codex'});const poll=async()=>(await f.host('poll',{hostId:'mac-program',platform:'darwin'})).commands.filter((c:any)=>c.kind==='account-inspect');await poll();
 const done=(i:any,c:any,status='completed')=>f.host('receipts',{hostId:'mac-program',executionId:i.executionId,commandId:c.id,status});
 const d=await f.op('account_discover',{accountId:a.id,hostId:'mac'});assert.equal((await f.op('account_discover',{accountId:a.id,hostId:'mac'})).id,d.id);
 const dc=(await poll()).find((c:any)=>c.executionId===d.executionId);
 await f.host('receipts',{hostId:'mac-program',executionId:d.executionId,commandId:dc.id,accountCatalogue:{accountId:a.id,bindings:[{name:'fleet-codex',executable:'/bin/codex',environment:{HOME:'/Users/x',TOKEN:'/x'}}]}},400);
 await f.host('receipts',{hostId:'mac-program',executionId:d.executionId,commandId:dc.id,accountCatalogue:{accountId:a.id,bindings:[{name:'fleet-codex',executable:'/bin/codex',environment:{HOME:'/Users/x',CODEX_HOME:'/Users/x/c'},behaviorApproved:true}]}});await done(d,dc);
 const step=async(readiness:string,reason:string,status='completed')=>{const i=await f.op('account_connect',{accountId:a.id,hostId:'mac',binding:'fleet-codex'});const c=(await poll()).find((c:any)=>c.executionId===i.executionId);assert.ok(c,'new inspection queued');await f.host('receipts',{hostId:'mac-program',executionId:i.executionId,commandId:c.id,accountObservation:{accountId:a.id,binding:'fleet-codex',platform:'darwin',readiness,reason}});await done(i,c,status);return (await f.op('account_list')).accounts[0]};
 let s=await step('unavailable','login-absent');assert.equal(s.bindings[0].readiness,'unavailable');
 const h=await f.op('account_login_handoff',{accountId:a.id,hostId:'mac',binding:'fleet-codex'});assert.deepEqual(h.command,['login','--device-auth']);assert.equal(h.authChanged,false);assert.doesNotMatch(JSON.stringify(h),/token|secret/i);
 await f.op('account_login_handoff',{accountId:a.id,hostId:'mac',binding:'undiscovered'},409);
 s=await step('ready','ready','stopped');assert.equal(s.bindings[0].readiness,'ready');
 assert.equal((await f.op('account_login_handoff',{accountId:a.id,hostId:'mac',binding:'fleet-codex'})).command,null);
 s=await step('unknown','catalogue-only');assert.equal(s.bindings[0].readiness,'ready');assert.equal(s.bindings[0].lastCheck.readiness,'unknown');
 await f.op('account_enabled',{accountId:a.id,enabled:false});await f.op('account_enabled',{accountId:a.id,enabled:true});
 const before=(await f.op('account_list')).accounts[0];
 await f.reload();const after=(await f.op('account_list')).accounts[0];
 assert.equal(after.id,a.id);assert.deepEqual(after.inspections.map((i:any)=>i.id),before.inspections.map((i:any)=>i.id));assert.equal(after.checks.length,3);assert.equal(after.history.length,2);assert.equal(after.bindings[0].readiness,'ready');
 assert.equal((await f.op('account_list')).catalogues[0].bindings[0].name,'fleet-codex');
}finally{await f.app.close()}});
