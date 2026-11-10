import {test} from 'node:test';import assert from 'node:assert/strict';import {createApp} from './test-app.ts';import {createStore} from '../../packages/coordinator/store.ts';import {volume} from './volume-fixture.ts';
// Live traffic shape (PDS18, Nov 2026): four hosts polling while two executor outboxes replay 100 events and 10 receipts.
// Runs on PGlite; set FLEET_TEST_DATABASE_URL to a disposable Postgres database for real lock and isolation behaviour.
import {PG,recreateFleetDatabase} from './pg-test.ts';
// FLEET_TEST_DATABASE_URL: one disposable database; else, with FLEET_TEST_PG, the owner URL of a fresh "fleet" database (pg-test.ts).
const url=process.env.FLEET_TEST_DATABASE_URL??(PG?await recreateFleetDatabase():undefined);
const EXECUTOR_HTTP_TIMEOUT_MS=5000; // crates/executor/src/outbox.rs: ureq AgentBuilder timeout(5 s)
const hosts=['owner-mac','owner-mac-program','pds18-native','pds18-program'];
test('four pollers and two outbox replays: p95 under the executor timeout, no lost or regressed write',async()=>{
 if(url){const s=createStore(url);await s.query('DROP SCHEMA public CASCADE');await s.query('CREATE SCHEMA public');await s.close()}
 const app=createApp({database:url??'memory://',browserSecret:'b',machineSecret:'m',hostId:'owner-mac',integrationHostId:'owner-mac-program',publicationHostId:'pds18-program',nativeHostIds:['pds18-native']});await app.ready();const store=(app as any).store;
 const {docs}=volume('owner-mac');
 // Ten replayed executions: 0-4 on the Mac, 5-9 on PDS18 native; each has a start command the host already accepted.
 const replay=Array.from({length:10},(_,i)=>({id:'replay-'+i,hostId:i<5?'owner-mac':'pds18-native'}));
 for(const r of replay){docs.push(['session',{id:'s-'+r.id,createdAt:1}]);docs.push(['execution',{id:r.id,executionId:r.id,sessionId:'s-'+r.id,hostId:r.hostId,state:'started',createdAt:1}]);docs.push(['command',{id:'c-'+r.id,executionId:r.id,sessionId:'s-'+r.id,hostId:r.hostId,kind:'start',status:'started',payload:{}}])}
 await store.tx(async()=>{for(const [k,d] of docs)await store.put(k,d)});if(url)await store.query('ANALYZE document');
 const times:number[]=[];const statuses:number[]=[];
 const send=async(path:string,payload:any)=>{const t=performance.now();const r=await app.inject({method:'POST',url:path,headers:{authorization:'Bearer m'},payload});times.push(performance.now()-t);statuses.push(r.statusCode);if(r.statusCode!==200)throw Error(path+' '+r.statusCode+' '+r.body);return r.json()};
 // An outbox delivers its rows in order, one request at a time; consecutive events of one execution go in one batch (outbox.rs).
 const outbox=async(host:string)=>{for(const r of replay.filter(r=>r.hostId===host)){
  for(let b=0;b<2;b++)await send('/api/hosts/events',{hostId:host,executionId:r.id,commandId:'c-'+r.id,events:Array.from({length:5},(_,k)=>({id:`e${b*5+k}`,seq:b*5+k,type:'item.completed',data:{n:b*5+k}}))});
  await send('/api/hosts/receipts',{hostId:host,commandId:'c-'+r.id,executionId:r.id,status:'completed'})}};
 store.stats.clear();let draining=true;const polls:number[]=hosts.map(()=>0);
 const poller=async(i:number)=>{while(draining){await send('/api/hosts/poll',{hostId:hosts[i],platform:i<2?'darwin':'linux'});polls[i]++;await new Promise(r=>setTimeout(r,20))}};
 const pollers=hosts.map((_,i)=>poller(i));
 await Promise.all([outbox('owner-mac'),outbox('pds18-native')]);draining=false;await Promise.all(pollers);
 const sorted=[...times].sort((a,b)=>a-b),p95=sorted[Math.floor(sorted.length*0.95)];
 console.log(`${times.length} requests (${polls.join('/')} polls), p95 ${p95.toFixed(0)} ms, max ${sorted.at(-1)!.toFixed(0)} ms, retries ${store.stats.get('RETRY')??0}`);
 assert.ok(statuses.every(s=>s===200));assert.ok(p95<EXECUTOR_HTTP_TIMEOUT_MS,`p95 ${p95} ms`);
 for(const r of replay){assert.equal((await store.get(r.id,'execution')).state,'completed');assert.equal((await store.get('c-'+r.id,'command')).status,'completed');
  const events=await store.all('event',{executionId:r.id});assert.deepEqual(events.map((e:any)=>e.seq).sort((a:number,b:number)=>a-b),[0,1,2,3,4,5,6,7,8,9])}
 for(const h of hosts)assert.ok((await store.get(h,'host-observation')).lastSeenAt>0);
 await app.close()});
