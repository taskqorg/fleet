import {test} from 'node:test';import assert from 'node:assert/strict';import {createApp} from './test-app.ts';import {volume} from './volume-fixture.ts';
// Budgets measured on the old SQLite build (1967769) with this fixture on the author's Mac: poll 380 ms, session read 18 ms.
// Postgres before the fix (PGlite, main 1ddecc1): poll 780 ms and 163 queries, session read 30 ms.
// 21 queries: 20 reads and writes plus the scope lock statement.
const POLL_MS=380,SESSION_MS=18,POLL_QUERIES=22; // 22: the host lookup by secret hash (people-access §3)
test('poll and a session read stay inside the SQLite-era budget at live volume',async()=>{
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.ready();const store=(app as any).store;
 const {docs,sessions}=volume();await store.tx(async()=>{for(const [k,d] of docs)await store.put(k,d)});
  // Fastest of ten warm runs: other test files run in parallel and add scheduling noise.
 const timed=async(o:any)=>{await app.inject(o);let best=Infinity,r:any,queries=0;for(let i=0;i<10;i++){store.stats.clear();const t=performance.now();r=await app.inject(o);best=Math.min(best,performance.now()-t);queries=[...store.stats.values()].reduce((a:number,b:number)=>a+b,0);assert.equal(r.statusCode,200)}return {ms:best,queries,body:r.json()}};
 const poll=await timed({method:'POST',url:'/api/hosts/poll',headers:{authorization:'Bearer m'},payload:{hostId:'mac'}});
 assert.deepEqual(poll.body.commands.map((c:any)=>c.id).sort(),Array.from({length:9},(_,i)=>'command-'+(190+i)).sort());
 assert.ok(poll.queries<=POLL_QUERIES,`poll ran ${poll.queries} queries`);assert.ok(poll.ms<POLL_MS,`poll took ${poll.ms.toFixed(0)} ms`);
 const session=await timed({method:'GET',url:'/api/sessions/'+sessions[3],headers:{authorization:'Bearer b'}});
 assert.equal(session.body.executions.length,2);assert.equal(session.body.events.length,30);
 assert.ok(session.ms<SESSION_MS,`session read took ${session.ms.toFixed(1)} ms`);
 console.log(`poll ${poll.ms.toFixed(0)} ms ${poll.queries} queries; session ${session.ms.toFixed(1)} ms`);await app.close()});
