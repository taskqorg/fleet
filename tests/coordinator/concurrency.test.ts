import {test} from 'node:test';import assert from 'node:assert/strict';import {createApp} from './test-app.ts';import {createStore} from '../../packages/coordinator/store.ts';import {volume} from './volume-fixture.ts';
// Runs on PGlite; set FLEET_TEST_DATABASE_URL to a disposable Postgres database to exercise real SERIALIZABLE conflicts.
import {PG,recreateFleetDatabase} from './pg-test.ts';
// FLEET_TEST_DATABASE_URL: one disposable database; else, with FLEET_TEST_PG, the owner URL of a fresh "fleet" database (pg-test.ts).
const url=process.env.FLEET_TEST_DATABASE_URL??(PG?await recreateFleetDatabase():undefined);
const fresh=async()=>{if(url){const s=createStore(url);await s.query('DROP SCHEMA public CASCADE');await s.query('CREATE SCHEMA public');await s.close()}
 const app=createApp({database:url??'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.ready();return app};
test('concurrent receipts and event batches: no 500, no lost write',async()=>{const app=await fresh(),store=(app as any).store;
 const {docs}=volume();await store.tx(async()=>{for(const [k,d] of docs)await store.put(k,d)});
 if(url)await store.query('ANALYZE document'); // as autovacuum leaves a live database
 // The 10 queued executions (190-199) each get receipts and event batches at once; two hosts' worth of traffic.
 const ids=Array.from({length:10},(_,i)=>190+i).filter(i=>i!==199);
 const send=(url:string,payload:any)=>app.inject({method:'POST',url,headers:{authorization:'Bearer m'},payload});
 store.stats.clear();const t=performance.now();
 const results=await Promise.all(ids.flatMap(i=>{const executionId='execution-'+i,commandId='command-'+i;
  const batches=Array.from({length:6},(_,b)=>send('/api/hosts/events',{hostId:'mac',executionId,commandId,events:Array.from({length:10},(_,k)=>({id:`b${b}-${k}`,seq:100+b*10+k,type:'item.completed',data:{n:k}}))}));
  const receipts=(async()=>{const out=[];for(const status of ['accepted','started','completed'])out.push(await send('/api/hosts/receipts',{hostId:'mac',commandId,executionId,status}));return out})();
  return [...batches,receipts]})).then(r=>r.flat());
 const ms=performance.now()-t;
 for(const r of results)assert.equal(r.statusCode,200,r.body);
 for(const i of ids){const e=await store.get('execution-'+i,'execution');assert.equal(e.state,'completed');assert.equal((await store.get('command-'+i,'command')).status,'completed');
  assert.equal((await store.all('event',{executionId:e.id})).filter((x:any)=>x.seq>=100).length,60)}
 console.log(`${results.length} requests in ${ms.toFixed(0)} ms, ${store.stats.get('RETRY')??0} serialization retries`);await app.close()});
test('a handler that swallows a serialization failure still succeeds by retry',async()=>{const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});const store=(app as any).store;let calls=0;
 app.post('/api/test-swallow',async()=>{calls++;await store.put('x',{id:'once',calls});
  if(calls===1)try{await store.query("DO $$ BEGIN RAISE EXCEPTION 'could not serialize access' USING ERRCODE='40001'; END $$")}catch{}
  await store.put('x',{id:'after',calls});return {calls}});
 const r=await app.inject({method:'POST',url:'/api/test-swallow',headers:{authorization:'Bearer b'},payload:{}});
 assert.equal(r.statusCode,200,r.body);assert.deepEqual(r.json(),{calls:2});assert.deepEqual((await store.all('x')).map((d:any)=>[d.id,d.calls]),[['once',2],['after',2]]);
 assert.equal(store.stats.get('RETRY'),1);await app.close()});
