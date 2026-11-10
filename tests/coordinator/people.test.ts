// Slice 1 Identity (docs/designs/fleet-people-access.md §1, CUJ 1): the guard's X-Fleet-Person names the person; host and
// browser credentials never mix; people.json keeps its last valid load.
import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync,writeFileSync} from 'node:fs';
import {createApp,peopleFile,OWNER} from './test-app.ts';
import {parsePeople} from '../../packages/coordinator/people.ts';

const ALINA={email:'Alina@Example.test',name:'Alina',roles:['operator'],grants:[{kind:'project',id:'desiign'}]};
const owner={email:OWNER,name:'Owner',roles:['operator','architect'],owner:true};

test('person header: verified e-mail maps to a person; none is 401, unknown 403, host credential with a person 403',async()=>{
 const file=peopleFile([owner,ALINA]);
 const app=createApp({database:mkdtempSync('/tmp/fleet-people-')+'/db',browserSecret:'browser',machineSecret:'machine',hostId:'mac',peopleFile:file});
 try{
  const me=(person:any,auth='Bearer browser')=>app.inject({method:'POST',url:'/api/ops/me',headers:{authorization:auth,'x-fleet-person':person},payload:{}});
  assert.equal((await me(null)).statusCode,401);
  assert.equal((await me('stranger@example.test')).statusCode,403);
  const a=(await me('alina@example.test')).json();
  assert.deepEqual([a.email,a.roles,a.owner],['alina@example.test',['operator'],false]);assert.match(a.grantsVersion,/^[0-9a-f]{16}$/);
  assert.equal((await me(OWNER)).json().owner,true);
  assert.equal((await me('alina@example.test','Bearer machine')).statusCode,401);
  const poll=(headers:any)=>app.inject({method:'POST',url:'/api/hosts/poll',headers:{authorization:'Bearer machine',...headers},payload:{hostId:'mac'}});
  assert.equal((await poll({'x-fleet-person':OWNER})).statusCode,403);
  assert.equal((await poll({})).statusCode,200);
  // Audit actors are the person's e-mail.
  const t=(await app.inject({method:'POST',url:'/api/ops/task_create',headers:{authorization:'Bearer browser','x-fleet-person':'alina@example.test'},payload:{title:'x'}})).json();
  assert.equal(t.createdBy??t.created_by,'alina@example.test');
  // A malformed file keeps the last valid load and asks once.
  writeFileSync(file,'{not json');await new Promise(r=>setTimeout(r,20));
  assert.equal((await me('alina@example.test')).statusCode,200);assert.equal((await me('alina@example.test')).statusCode,200);
  const qs=(await app.inject({method:'POST',url:'/api/ops/questions_list',headers:{authorization:'Bearer browser'},payload:{}})).json();
  assert.equal((qs.questions??qs).filter((q:any)=>/people\.json/.test(q.text)).length,1);
  writeFileSync(file,JSON.stringify([owner]));await new Promise(r=>setTimeout(r,20));
  assert.equal((await me('alina@example.test')).statusCode,403);
 }finally{await app.close()}
});

test('people.json validation: one owner, known roles, e-mails lowercased',()=>{
 assert.throws(()=>parsePeople(JSON.stringify([ALINA])),/Exactly one person/);
 assert.throws(()=>parsePeople(JSON.stringify([owner,{...ALINA,roles:['admin']}])),/Roles/);
 assert.throws(()=>parsePeople(JSON.stringify([owner,{...owner,email:OWNER.toUpperCase(),owner:false}])),/Duplicate/);
 assert.ok(parsePeople(JSON.stringify([owner,ALINA])).has('alina@example.test'));
});
