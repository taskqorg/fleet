import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,existsSync,statSync} from 'node:fs';import {join} from 'node:path';
import {createApp,peopleFile,OWNER,sha256} from './test-app.ts';
const VALUE='tvly-test-0123456789abcdef',FP=sha256(VALUE).slice(0,12);
const setup=(extra:any={})=>{const secretDir=mkdtempSync('/tmp/fleet-secret-'),gatewayDir=mkdtempSync('/tmp/fleet-gateway-');
 const config={database:mkdtempSync('/tmp/fleet-connections-')+'/db',browserSecret:'browser',hostId:'mac',secretDir,gatewayDir,...extra};const app=createApp(config);
 const op=async(name:string,b:any={},code=200,person?:string)=>{const r=await app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer browser',...(person?{'x-fleet-person':person}:{})},payload:b});assert.equal(r.statusCode,code,r.body);return r.json()};
 const snapshot=()=>JSON.parse(readFileSync(join(gatewayDir,'snapshot.json'),'utf8'));
 return {app,op,secretDir,gatewayDir,snapshot}};
const settle=()=>new Promise(r=>setTimeout(r,20));

test('import: a key file in the secret directory registers the tavily connection held by the owner',async()=>{
 const secretDir=mkdtempSync('/tmp/fleet-secret-');mkdirSync(join(secretDir,'connections','tavily'),{recursive:true});
 writeFileSync(join(secretDir,'connections','tavily',FP),VALUE+'\n',{mode:0o600});writeFileSync(join(secretDir,'connections','tavily','0123456789ab'),'not this key\n');
 const f=setup({secretDir});try{await f.app.ready();
  const [c]=(await f.op('connections_list')).connections;
  assert.equal(c.name,'tavily');assert.equal(c.holder,OWNER);assert.deepEqual(c.access.projects,['*']);assert.deepEqual(c.tools,['web_search','web_extract']);
  assert.deepEqual(c.keys.map((k:any)=>[k.fingerprint,k.state]),[[FP,'active']]);// the mismatched file is ignored
  assert.ok(!JSON.stringify(c).includes(VALUE));
  const s=f.snapshot();assert.equal(s.schema,2);assert.deepEqual(s.connections.tavily.keys,[{id:FP,state:'active'}]);assert.equal(s.providers.tavily.tools.web_search.path,'/search');
  assert.ok(!readFileSync(join(f.gatewayDir,'snapshot.json'),'utf8').includes(VALUE));
 }finally{await f.app.close()}});

test('key add returns only the fingerprint, writes a 0600 file; rotation and retire; snapshot follows',async()=>{const f=setup();try{
 await f.op('connections_create',{name:'tavily',provider:'tavily'});
 await f.op('connections_create',{name:'tavily',provider:'tavily'},409);await f.op('connections_create',{name:'x',provider:'nope'},400);
 const k=await f.op('connections_key_add',{connection:'tavily',value:VALUE});
 assert.deepEqual([k.fingerprint,k.state],[FP,'active']);assert.ok(!JSON.stringify(k).includes(VALUE));
 const file=join(f.secretDir,'connections','tavily',FP);assert.equal(readFileSync(file,'utf8').trim(),VALUE);assert.equal(statSync(file).mode&0o777,0o600);
 await f.op('connections_key_add',{connection:'tavily',value:VALUE},409);await f.op('connections_key_add',{connection:'tavily',value:'has space here'},400);
 await f.op('connections_key_state',{keyId:FP,state:'draining'});await settle();assert.deepEqual(f.snapshot().connections.tavily.keys,[{id:FP,state:'draining'}]);
 await f.op('connections_key_state',{keyId:FP,state:'disabled'},400);
 await f.op('connections_key_state',{keyId:FP,state:'retired'});await settle();assert.ok(!existsSync(file));assert.deepEqual(f.snapshot().connections.tavily.keys,[]);
 await f.op('connections_key_state',{keyId:FP,state:'active'},409);
 const [c]=(await f.op('connections_list')).connections;assert.equal(c.keys[0].state,'retired');assert.deepEqual(c.history.map((h:any)=>h.value),['draining','retired']);
 await f.op('connections_set_access',{connection:'tavily',access:{projects:['p1'],people:['A@x.test']}});
 await f.op('connections_set_intake',{connection:'tavily',intake:'paused'});await settle();assert.equal(f.snapshot().connections.tavily.intake,'paused');
 assert.deepEqual((await f.op('connections_list')).connections[0].access,{people:['a@x.test'],projects:['p1'],experiments:[]});
}finally{await f.app.close()}});

test('key values are owner only; API keys are architect views',async()=>{
 const people=peopleFile([{email:OWNER,name:'Owner',roles:['operator','architect'],owner:true},{email:'arch@fleet.test',roles:['architect']},{email:'op@fleet.test',roles:['operator']}]);
 const f=setup({peopleFile:people});try{await f.op('connections_create',{name:'tavily',provider:'tavily'});
  await f.op('connections_key_add',{connection:'tavily',value:VALUE},403,'arch@fleet.test');
  await f.op('connections_list',{},403,'op@fleet.test');
  assert.equal((await f.op('connections_list',{},200,'arch@fleet.test')).connections.length,1);
  await f.op('connections_set_intake',{connection:'tavily',intake:'paused'},403,'arch@fleet.test');// not the holder
  // A key the gateway disabled comes back only through the owner, also by way of draining.
  await f.op('connections_key_add',{connection:'tavily',value:VALUE});await f.op('connections_set_access',{connection:'tavily',access:{}});
  const store=(f.app as any).store;await store.tx(async()=>store.put('connection-key',{...(await store.get('key:'+FP,'connection-key')),state:'disabled',disabledReason:'401'}));
  // holder change: give the architect the connection
  await store.tx(async()=>store.put('api-connection',{...(await store.get('connection:tavily','api-connection')),holder:'arch@fleet.test'}));
  await f.op('connections_key_state',{keyId:FP,state:'draining'},403,'arch@fleet.test');await f.op('connections_key_state',{keyId:FP,state:'active'},403,'arch@fleet.test');
  await f.op('connections_set_intake',{connection:'tavily',intake:'paused'},200,'arch@fleet.test');// the holder may
  await f.op('connections_key_state',{keyId:FP,state:'active'});
  await settle();
 }finally{await f.app.close()}});
