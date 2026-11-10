// Slice 4 Holder and director (docs/designs/fleet-people-access.md §4-5, CUJ 3-4): a subscription runs its holder's work, or
// work of a project its take rule lists; a take makes the taker the director; settings changes are setting-change rows.
import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';
import {createApp,peopleFile,OWNER} from './test-app.ts';

const ALINA='alina@example.test',SIMON='simon@example.test';
test('Alina-directed work never runs on Simon\'s subscription unless Simon takes it; task take; holder-only settings',async()=>{
 const people=peopleFile([{email:OWNER,name:'Owner',roles:['operator','architect'],owner:true},
  {email:ALINA,name:'Alina',roles:['operator'],grants:[{kind:'project',id:'hello-world'}]},{email:SIMON,name:'Simon',roles:['operator'],grants:[{kind:'project',id:'hello-world'}]}]);
 const app=createApp({database:mkdtempSync('/tmp/fleet-director-')+'/db',browserSecret:'browser',machineSecret:'machine',hostId:'mac',peopleFile:people});
 try{
  const call=(url:string,payload:any,person=OWNER)=>app.inject({method:'POST',url,headers:{authorization:'Bearer browser','x-fleet-person':person},payload});
  const ok=async(url:string,payload:any,person=OWNER)=>{const r=await call(url,payload,person);assert.equal(r.statusCode,200,url+' '+r.body);return r.json()};
  const host=(url:string,payload:any)=>app.inject({method:'POST',url,headers:{authorization:'Bearer machine'},payload});
  const sub=async(name:string,holder:string)=>{const a=await ok('/api/ops/account_create',{name,harness:'codex',holder});
   assert.equal((await host('/api/hosts/accounts',{hostId:'mac',accountId:a.id,binding:name,platform:'darwin',readiness:'ready'})).statusCode,200);return a};
  const alinaSub=await sub('alina-codex',ALINA),simonSub=await sub('simon-codex',SIMON);
  assert.equal(alinaSub.holder,ALINA);
  // A hello-world session directed by Alina.
  const session=async(account:string)=>{const s=(await call('/api/sessions',{},ALINA)).json();const store=(app as any).store;
   await store.tx(async()=>{const d=await store.get(s.sessionId,'session');d.projectId='hello-world';await store.put('session',d)});
   await ok('/api/sessions/'+s.sessionId+'/profile',{profileId:'baseline',account},ALINA);return s.sessionId};
  const send=(sid:string,requestId:string)=>call('/api/sessions/'+sid+'/messages',{requestId,text:'x'},ALINA);
  const own=(await send(await session('alina-codex'),'one'));assert.equal(own.statusCode,200,own.body);assert.equal(own.json().directedBy,ALINA);
  const foreign=await send(await session('simon-codex'),'two');assert.equal(foreign.statusCode,409);assert.match(foreign.body,/held by simon@example.test/);
  // Only the holder (or the owner) changes a subscription; the change is audited.
  assert.equal((await call('/api/ops/account_update',{accountId:simonSub.id,take:{projects:['hello-world']}},ALINA)).statusCode,403);
  await ok('/api/ops/account_update',{accountId:simonSub.id,take:{projects:['hello-world']}},SIMON);
  const taken=await send(await session('simon-codex'),'three');assert.equal(taken.statusCode,200,taken.body);
  assert.deepEqual([taken.json().directedBy,taken.json().takenFrom],[SIMON,ALINA]);
  const history=(await ok('/api/ops/account_list',{})).accounts.find((a:any)=>a.id===simonSub.id).history;
  assert.deepEqual(history.map((h:any)=>[h.field,h.actor]),[['take',SIMON]]);
  // Task take: the owner takes Alina's task; requested_by never changes.
  const t=await ok('/api/ops/task_create',{projectId:'hello-world',title:'logo'},ALINA);assert.deepEqual([t.directedBy,t.requestedBy],[ALINA,ALINA]);
  const after=await ok('/api/ops/task_take',{taskId:t.id});assert.deepEqual([after.directedBy,after.requestedBy,after.takenFrom],[OWNER,ALINA,ALINA]);
 }finally{await app.close()}
});
