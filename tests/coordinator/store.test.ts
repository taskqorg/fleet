import {test} from 'node:test';import assert from 'node:assert/strict';import {createStore} from '../../packages/coordinator/store.js';
const fresh=async()=>{const s=createStore('memory://');await s.migrate();return s};
test('all() keeps insertion order; updates keep their place; put never changes kind',async()=>{const s=await fresh();
 for(const id of ['c','a','b'])await s.put('x',{id,n:1});await s.put('x',{id:'c',n:2});await s.put('other',{id:'a',stolen:true});
 assert.deepEqual((await s.all('x')).map(d=>d.id+d.n),['c2','a1','b1']);assert.deepEqual(await s.all('other'),[]);assert.deepEqual(await s.get('a','x'),{id:'a',n:1});
 await assert.rejects(s.get('a','other'),(e:any)=>e.statusCode===404);assert.deepEqual((await s.all('x',{n:1})).map(d=>d.id),['a','b']);
 await s.migrate();assert.equal((await s.query('SELECT count(*)::int AS n FROM schema_migration'))[0].n,1);await s.close()});
test('tx commits together, rolls back on error, joins nested calls, and retries serialization failures',async()=>{const s=await fresh();
 await assert.rejects(s.tx(async()=>{await s.put('x',{id:'gone'});throw Error('boom')}),/boom/);await assert.rejects(s.get('gone','x'));
 let runs=0;const r=await s.tx(async()=>{runs++;await s.put('x',{id:'r'+runs});await s.tx(()=>s.put('x',{id:'inner'+runs}));if(runs<3)throw Object.assign(Error('could not serialize access'),{code:'40001'});return runs});
 assert.equal(r,3);assert.deepEqual((await s.all('x')).map(d=>d.id),['r3','inner3']);
 // Work started inside a transaction and still running after it ends falls back to plain queries.
 let late:Promise<void>|undefined;await s.tx(async()=>{late=new Promise<void>(ok=>setTimeout(ok,10)).then(()=>s.put('x',{id:'late'}))});await late;assert.equal((await s.get('late','x')).id,'late');await s.close()});
test('atomic keeps writes made before an error; a nested tx undoes only its own writes',async()=>{const s=await fresh();
 await assert.rejects(s.atomic(async()=>{await s.put('x',{id:'kept'});await assert.rejects(s.tx(async()=>{await s.put('x',{id:'undone'});throw Error('inner')}));await s.put('x',{id:'after'});throw Object.assign(Error('conflict'),{statusCode:409})}),/conflict/);
 assert.deepEqual((await s.all('x')).map(d=>d.id),['kept','after']);
 let runs=0;await s.atomic(async()=>{runs++;await s.put('x',{id:'retry'+runs});if(runs<2)throw Object.assign(Error('serialize'),{code:'40001'})});
 assert.deepEqual((await s.all('x')).map(d=>d.id),['kept','after','retry2']);await s.close()});
