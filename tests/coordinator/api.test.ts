import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';import {tmpdir} from 'node:os';import {createApp} from '../../packages/coordinator/app.ts';
test('durable dedup, auth, host scope, results and follow-up',async()=>{const config={database:mkdtempSync(tmpdir()+'/fleet-')+'/db',browserSecret:'browser-test',machineSecret:'machine-test',hostId:'mac'};let app=createApp(config);const call=async(url:string,payload?:any,machine=false)=>app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?config.machineSecret:config.browserSecret)},payload});assert.equal((await app.inject('/api/sessions/no')).statusCode,401);const s=(await call('/api/sessions',{})).json().sessionId;const path='/api/sessions/'+s+'/messages';const first=(await call(path,{requestId:'one',text:'Build'})).json();assert.equal((await call(path,{requestId:'one',text:'Build'})).json().id,first.id);assert.equal((await call(path,{requestId:'one',text:'Changed'})).statusCode,409);assert.equal((await call('/api/hosts/poll',{hostId:'other'},true)).statusCode,403);const command=(await call('/api/hosts/poll',{hostId:'mac'},true)).json().commands[0];await call('/api/hosts/receipts',{hostId:'mac',commandId:command.id,executionId:first.id,status:'completed',nativeSessionId:'native-one'},true);await app.close();app=createApp(config);const next=(await call(path,{requestId:'two',text:'Follow up'})).json();assert.ok(next.id);const commands=(await call('/api/hosts/poll',{hostId:'mac'},true)).json().commands;assert.equal(commands[0].payload.nativeSessionId,'native-one');assert.equal((await call('/api/sessions/'+s)).json().messages.length,2);assert.equal((await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:next.id,name:'../bad',size:1,sha256:'0'.repeat(64)},true)).statusCode,400);await app.close()});
test('one operation surface: web ops and MCP tools share names',async()=>{const app=createApp({database:mkdtempSync(tmpdir()+'/fleet-')+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac'});const h={authorization:'Bearer b'};
 const tools=(await app.inject({method:'POST',url:'/api/tools',headers:h,payload:{jsonrpc:'2.0',id:1,method:'tools/list'}})).json().result.tools.map((t:any)=>t.name);
 for(const n of ['session_create','session_profile','message_send','execution_stop','session_fork','session_transition','ask','reply','transport_inspect','project_context'])assert.ok(tools.includes(n),n);
 const s=(await app.inject({method:'POST',url:'/api/ops/session_create',headers:h,payload:{}})).json();assert.ok(s.sessionId);
 const v=(await app.inject({method:'POST',url:'/api/tools',headers:h,payload:{jsonrpc:'2.0',id:2,method:'tools/call',params:{name:'queue_list',arguments:{sessionId:s.sessionId}}}})).json();assert.deepEqual(JSON.parse(v.result.content[0].text),{queued:[]});
 assert.equal((await app.inject({method:'POST',url:'/api/ops/nope',headers:h,payload:{}})).statusCode,404);await app.close()});
// Phase 3 of docs/designs/fleet-view-urls-and-list-path.md: view_link for the browser and agents, task_derived_check, groups.
test('view_link builds and parses operator-app links; task_derived_check; list_membership returns groups',async()=>{
 const origin='https://fleet.example';
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',browserProxyAuth:true,browserOrigin:origin});await app.ready();
 const op=(name:string,payload:any)=>app.inject({method:'POST',url:'/api/ops/'+name,payload,headers:{authorization:'Bearer b',origin}});
 const built=(await op('view_link',{view:{view:'tasks',state:['failed'],project:['fleet']}})).json();
 assert.deepEqual(built,{url:origin+'/tasks?project=fleet&state=failed',path:'/tasks?project=fleet&state=failed'});
 assert.deepEqual((await op('view_link',{url:built.url})).json(),{view:{view:'tasks',project:['fleet'],state:['failed']}});
 assert.deepEqual((await op('view_link',{url:'/tasks/a?sel=c,b'})).json(),{view:{view:'tasks',focus:'a',sel:['b','c']}});
 for(const bad of [{url:'https://evil.example/tasks'},{url:'/legacy/x'},{},{view:{view:'tasks'},url:'/tasks'},{view:{view:'nope'}}])assert.equal((await op('view_link',bad)).statusCode,400,JSON.stringify(bad));
 const agentTools=(await app.inject({method:'POST',url:'/api/agent-tools',headers:{authorization:'Bearer b',origin},payload:{jsonrpc:'2.0',id:1,method:'tools/list'}})).json().result.tools;
 assert.ok(agentTools.some((t:any)=>t.name==='view_link'&&t.description));
 const s=(app as any).store;let id='';await s.tx(async()=>{id=await s.tasks.create({title:'T',projectId:'p',priority:'urgent'})});
 assert.deepEqual((await op('task_derived_check',{})).json(),{checked:1,drifted:[]});
 const m=(await op('list_membership',{ids:[id],lists:[{name:'tasks',filter:{},group:'priority'}]})).json();
 assert.deepEqual(m.lists[0].groups,[{key:'1',label:'urgent',count:1}]);
 await app.close();
 // Without a configured origin the path is still returned.
 const bare=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await bare.ready();
 assert.deepEqual((await bare.inject({method:'POST',url:'/api/ops/view_link',payload:{view:{view:'hosts',focus:'mac'}},headers:{authorization:'Bearer b'}})).json(),{url:null,path:'/hosts/mac'});
 // review #34-9: without a configured origin, a full URL on any origin parses.
 assert.deepEqual((await bare.inject({method:'POST',url:'/api/ops/view_link',payload:{url:'https://anywhere.example/tasks?state=failed'},headers:{authorization:'Bearer b'}})).json(),{view:{view:'tasks',state:['failed']}});
 await bare.close()});
test('review #31-1: view_link rejects every invalid view field instead of dropping it',async()=>{
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.ready();
 const op=(payload:any)=>app.inject({method:'POST',url:'/api/ops/view_link',payload,headers:{authorization:'Bearer b'}});
 for(const view of [{view:'tasks',project:'fleet'},{view:'tasks',state:'failed'},{view:'tasks',state:['faild']},{view:'tasks',group:'agents'},{view:'tasks',sel:[1]},
  {view:'tasks',q:5},{view:'conversations',group:'agent'},{view:'tasks',nope:1},{view:'tasks',focus:''},{view:'conversations',archived:'1'},'tasks',null])
  assert.equal((await op({view})).statusCode,400,JSON.stringify(view));
 assert.equal((await op({view:{view:'tasks',state:['failed'],project:[null,'fleet'],group:'agent',sel:['a'],q:'x',focus:'t'}})).statusCode,200);
 await app.close()});
