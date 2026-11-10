// Slice 2 Host secrets and trust (docs/designs/fleet-people-access.md §3, CUJ 2): each host has its own secret, the coordinator
// derives the host from it, the old shared secret is gone, and a host held by another person gets only its holder's projects.
import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';
import {createApp,peopleFile,OWNER,sha256} from './test-app.ts';

const ALINA='alina@example.test';
test('per-host secrets: derived host, foreign hostId 403, old shared secret 401, rotation, held-host trust',async()=>{
 const people=peopleFile([{email:OWNER,name:'Owner',roles:['operator','architect'],owner:true},{email:ALINA,name:'Alina',roles:['operator'],grants:[{kind:'project',id:'desiign'}]}]);
 // No machineSecret: the app has no shared host secret at all; hosts exist only through hosts_register.
 const app=createApp({database:mkdtempSync('/tmp/fleet-hosts-')+'/db',browserSecret:'browser',hostId:'pds18-native',peopleFile:people});
 try{
  const op=async(name:string,payload:any,person=OWNER)=>app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer browser','x-fleet-person':person},payload});
  const host=(secret:string,route:string,payload:any)=>app.inject({method:'POST',url:'/api/hosts/'+route,headers:{authorization:'Bearer '+secret},payload});
  const pds=(await op('hosts_register',{id:'pds18-native'})).json();assert.match(pds.secret,/^[0-9a-f]{64}$/);assert.equal(pds.host.secretHash,undefined);
  // A secret made on the host itself: only its hash reaches Fleet.
  const alinaSecret='alina-mac-secret-made-on-her-mac-0123456789';
  const alina=(await op('hosts_register',{id:'alina-mac',holder:ALINA,secretSha256:sha256(alinaSecret)})).json();assert.equal(alina.secret,undefined);
  assert.equal((await op('hosts_register',{id:'x'},ALINA)).statusCode,403);
  assert.equal((await op('hosts_register',{id:'y',secretSha256:sha256('browser')})).statusCode,400);
  // The old shared secret (and any unknown one) is refused on every host route.
  for(const route of ['poll','receipts','events','stores','artifacts/get','agent-tools','accounts'])assert.equal((await host('machine',route,{hostId:'pds18-native'})).statusCode,401,route);
  // CUJ 2: Alina's secret claiming pds18-native is 403 on poll and store registration; without hostId it is her host.
  assert.equal((await host(alinaSecret,'poll',{hostId:'pds18-native'})).statusCode,403);
  assert.equal((await host(alinaSecret,'stores',{hostId:'pds18-native',store:{}})).statusCode,403);
  assert.equal((await host(alinaSecret,'poll',{platform:'darwin'})).statusCode,200);
  assert.equal((await host(pds.secret,'poll',{hostId:'pds18-native'})).statusCode,200);
  // Held-host trust: a command of another project is never delivered to Alina's host; one of desiign is.
  const store=(app as any).store;
  await store.tx(async()=>{for(const [id,projectId] of [['e-other','hello-world'],['e-desiign','desiign']]){
   await store.put('execution',{id,executionId:id,sessionId:'s-'+id,hostId:'alina-mac',projectId,state:'queued',createdAt:Date.now()});
   await store.put('command',{id:'c-'+id,executionId:id,sessionId:'s-'+id,hostId:'alina-mac',kind:'start',payload:{projectId},status:'pending'})}});
  const got=(await host(alinaSecret,'poll',{})).json().commands.map((c:any)=>c.id);
  assert.ok(!got.includes('c-e-other'),'other project command withheld');
  // Rotation: the old secret stops at once.
  const next=(await op('hosts_rotate_secret',{id:'pds18-native'})).json().secret;
  assert.equal((await host(pds.secret,'poll',{})).statusCode,401);assert.equal((await host(next,'poll',{})).statusCode,200);
 }finally{await app.close()}
});
