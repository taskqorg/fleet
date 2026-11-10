import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';import {createApp} from './test-app.ts';
test('shared recovery preserves original conflict, authenticates owner and never changes outcome',async()=>{
 const config={database:mkdtempSync('/tmp/fleet-recovery-')+'/db',browserSecret:'browser',machineSecret:'machine',hostId:'mac',publicationHostId:'pds'};let app=createApp(config);
 const post=(url:string,b:any,machine=false)=>app.inject({method:'POST',url,headers:{authorization:'Bearer '+(machine?'machine':'browser')},payload:b});
 try{const sid=(await post('/api/sessions',{})).json().sessionId,e=(await post('/api/sessions/'+sid+'/messages',{requestId:'one',text:'fixture'})).json();
 const command=(await post('/api/hosts/poll',{hostId:'mac'},true)).json().commands[0];await post('/api/hosts/receipts',{hostId:'mac',executionId:e.id,commandId:command.id,status:'unknown'},true);
 const body={hostId:'mac',executionId:e.id,events:[{id:'resource',seq:1,type:'resource.observation',data:{cpuPercent:null}}]};assert.equal((await post('/api/hosts/events',body,true)).statusCode,200);
 const closure={...body,events:[{id:'closed',seq:1,type:'process.closed',data:{exitCode:0,stdoutClosed:true}}]};assert.equal((await post('/api/hosts/events',closure,true)).statusCode,409);
 assert.equal((await post('/api/hosts/events',{...closure,hostId:'pds'},true)).statusCode,403);
 const inspect=()=>post('/api/recovery/transport_inspect',{executionId:e.id});const initial=(await inspect()).json(),c=initial.conflicts[0];assert.equal(initial.state,'unknown');assert.equal(c.state,'blocked');assert.equal(c.original,undefined);
 const approval={executionId:e.id,conflictId:c.id,frameSha256:c.frameSha256,requestId:'operator-one'};
 assert.equal((await app.inject({method:'POST',url:'/api/recovery/transport_authorize',payload:approval})).statusCode,401);
 assert.equal((await post('/api/recovery/transport_authorize',{...approval,frameSha256:'changed'})).statusCode,409);
 const tool=await post('/api/recovery-tools',{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'transport_authorize',arguments:approval}});assert.equal(tool.statusCode,200);
 assert.equal((await post('/api/hosts/events',closure,true)).statusCode,200);assert.equal((await post('/api/hosts/events',closure,true)).statusCode,200);
 await app.close();app=createApp(config);const after=(await inspect()).json();assert.equal(after.state,'unknown');assert.equal(after.conflicts[0].state,'delivered');assert.equal(after.eventCount,2);assert.equal(after.audit.length,2);assert.deepEqual(after.commands.map((x:any)=>x.kind),['start']);
 const ev=(await app.inject({url:'/api/executions/'+e.id+'/events',headers:{authorization:'Bearer browser'}})).json().events;assert.equal(ev[0].type,'resource.observation');assert.equal(ev[1].seq,2);assert.deepEqual(ev[1].data,closure.events[0].data);assert.equal(ev[1].originalTransport.sequence,1);
 assert.equal((await post('/api/hosts/events',{...closure,events:[{...closure.events[0],type:'assistant'}]},true)).statusCode,409);const unsupported=(await inspect()).json().conflicts.find((x:any)=>!x.supported);assert.equal((await post('/api/recovery/transport_authorize',{...approval,conflictId:unsupported.id,frameSha256:unsupported.frameSha256,requestId:'unsupported'})).statusCode,409);
 }finally{await app.close()}
});

test('recovery tools and UI retain browser authority and exact proxy Origin',async()=>{
 const app=createApp({database:mkdtempSync('/tmp/fleet-recovery-auth-')+'/db',browserSecret:'browser',machineSecret:'machine',hostId:'mac',browserProxyAuth:true,browserOrigin:'https://fixture.invalid'});
 try{for(const url of ['/api/recovery/transport_inspect','/api/recovery-tools']){const payload=url.endsWith('tools')?{id:1,method:'tools/list'}:{executionId:'missing'};
 assert.equal((await app.inject({method:'POST',url,payload,headers:{authorization:'Bearer machine',origin:'https://fixture.invalid'}})).statusCode,401);
 assert.equal((await app.inject({method:'POST',url,payload,headers:{authorization:'Bearer browser'}})).statusCode,403);
 assert.equal((await app.inject({method:'POST',url,payload,headers:{authorization:'Bearer browser',origin:'https://other.invalid'}})).statusCode,403)}
 assert.equal((await app.inject({method:'POST',url:'/api/recovery-tools',payload:{id:1,method:'tools/list'},headers:{authorization:'Bearer browser',origin:'https://fixture.invalid'}})).statusCode,200);
 }finally{await app.close()}
});

test('stream chunks are stored as event-delta; a resent chunk is found, a changed one or a seq collision conflicts',async()=>{
 const app=createApp({database:mkdtempSync('/tmp/fleet-delta-')+'/db',browserSecret:'browser',machineSecret:'machine',hostId:'mac'});
 const post=(url:string,b:any,machine=false)=>app.inject({method:'POST',url,headers:{authorization:'Bearer '+(machine?'machine':'browser')},payload:b});
 try{const sid=(await post('/api/sessions',{})).json().sessionId,e=(await post('/api/sessions/'+sid+'/messages',{requestId:'one',text:'x'})).json();
  const chunk={id:'c1',seq:1,type:'acp.update',data:{fleetNormalized:{deltaOf:'text:1',opens:true,blocks:[{kind:'text',role:'agent',markdown:'Hel'}]}}};
  const send=(ev:any)=>post('/api/hosts/events',{hostId:'mac',executionId:e.id,events:[ev]},true);
  assert.equal((await send(chunk)).statusCode,200);assert.equal((await send(chunk)).statusCode,200);
  const kinds=await (app as any).store.query("SELECT kind FROM document WHERE id=$1",['event:'+e.id+':c1']);assert.deepEqual(kinds.map((k:any)=>k.kind),['event-delta']);
  assert.equal((await send({...chunk,data:{fleetNormalized:{...chunk.data.fleetNormalized,blocks:[]}}})).statusCode,409);
  assert.equal((await send({id:'other',seq:1,type:'resource.observation',data:{}})).statusCode,409);
 }finally{await app.close()}
});
