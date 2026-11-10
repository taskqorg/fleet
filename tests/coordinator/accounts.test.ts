import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';import {createApp} from './test-app.ts';
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

test('host selection: harness placement is a host setting, unknown/offline rejected',async()=>{const f=setup();try{
 const c=await f.op('account_create',{name:'claude-test',harness:'claude'});
 await f.op('account_discover',{accountId:c.id,hostId:'mac'},409);// no inspector heartbeat yet
 await f.op('account_discover',{accountId:c.id,hostId:'pds-program'},409);
 await f.op('account_discover',{accountId:c.id,hostId:'nowhere'},409);
 // Where Claude may run is the host's setting (decision 10): PDS sets it to not allowed; nothing is hard-coded.
 assert.equal((await f.call('/api/ops/hosts_update',{id:'pds-program',harnesses:{claude:{allowed:false}}})).statusCode,200);
 await f.host('poll',{hostId:'pds-program',platform:'linux'});
 const hs=await f.op('account_hosts',{accountId:c.id});const pds=hs.hosts.find((h:any)=>h.id==='pds-program');assert.equal(pds.eligible,false);assert.match(pds.reason,/not allowed on this host/);
 assert.equal(hs.hosts.find((h:any)=>h.id==='mac').eligible,true);
 await f.host('accounts',{hostId:'pds-program',accountId:c.id,binding:'fleet-claude',platform:'linux',readiness:'ready'},409);
 await f.op('account_login_handoff',{accountId:c.id,hostId:'pds-program',binding:'fleet-claude'},409);
 assert.ok((await f.op('account_list')).accounts.find((x:any)=>x.id===c.id).holder);
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
 await f.op('account_update',{accountId:a.id,intake:'paused'});await f.op('account_update',{accountId:a.id,intake:'all'});
 const before=(await f.op('account_list')).accounts[0];
 await f.reload();const after=(await f.op('account_list')).accounts[0];
 assert.equal(after.id,a.id);assert.deepEqual(after.inspections.map((i:any)=>i.id),before.inspections.map((i:any)=>i.id));assert.equal(after.checks.length,3);assert.equal(after.history.length,2);assert.equal(after.bindings[0].readiness,'ready');
 assert.equal((await f.op('account_list')).catalogues[0].bindings[0].name,'fleet-codex');
}finally{await f.app.close()}});

test('extra native host: accepted, account binding selects host, Claude refused where the host does not allow it',async()=>{const f=setup({nativeHostIds:['pds-native']});try{
 await f.host('poll',{hostId:'pds-native',platform:'linux'});
 assert.equal((await f.call('/api/ops/hosts_update',{id:'pds-native',harnesses:{claude:{allowed:false}}})).statusCode,200);
 const hs=await f.op('account_hosts',{harness:'codex'});assert.equal(hs.hosts.find((h:any)=>h.id==='pds-native').eligible,true);
 assert.equal((await f.op('account_hosts',{harness:'claude'})).hosts.find((h:any)=>h.id==='pds-native').eligible,false);
 const a=await f.op('account_create',{name:'fleet-codex-pds',harness:'codex'});
 await f.host('accounts',{hostId:'pds-native',accountId:a.id,binding:'fleet-codex-pds',platform:'linux',readiness:'ready',reason:'ready'});
 const c=await f.op('account_create',{name:'claude-pds',harness:'claude'});
 await f.host('accounts',{hostId:'pds-native',accountId:c.id,binding:'fleet-claude-pds',platform:'linux',readiness:'ready'},409);
 const s=(await f.call('/api/sessions',{})).json();
 assert.equal((await f.call(`/api/sessions/${s.sessionId}/profile`,{profileId:'baseline',account:'fleet-codex-pds'})).statusCode,200);
 const m=await f.call(`/api/sessions/${s.sessionId}/messages`,{requestId:'r1',text:'hi'});assert.equal(m.statusCode,200,m.body);
 assert.equal(m.json().hostId,null);// late choice: the host is picked when a host polls (§6)
 const cmd=(await f.host('poll',{hostId:'pds-native',platform:'linux'})).commands.find((x:any)=>x.kind==='start');assert.equal(cmd.executionId,m.json().id);
 assert.equal((await f.host('poll',{hostId:'mac',platform:'darwin'})).commands.filter((x:any)=>x.kind==='start').length,0);
}finally{await f.app.close()}});
