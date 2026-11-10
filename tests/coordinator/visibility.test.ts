// Slice 3 Visibility (docs/designs/fleet-people-access.md §2, CUJ 1): a person other than the owner sees only granted projects
// and the rows they hold; architect operations are 403 for an operator; the owner sees everything, unchanged.
import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdirSync,mkdtempSync,writeFileSync} from 'node:fs';
import {createApp,peopleFile,OWNER,sha256} from './test-app.ts';
import {createStore} from '../../packages/coordinator/store.ts';
import {PG,freshDb} from './pg-test.ts';

const ALINA='alina@example.test';
test('Alina sees only desiign and her own host; architect operations 403; the owner sees all',async()=>{
 // Row-level security needs a NOBYPASSRLS role: on PGlite the store switches to one in scoped transactions; on Postgres
 // (FLEET_TEST_PG) the store already runs as fleet_app.
 let s:any;
 if(PG)s=(await freshDb()).s;
 else{s=createStore('memory://',{role:'fleet_app_test'});await s.migrate();
  await s.query("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='fleet_app_test') THEN CREATE ROLE fleet_app_test NOLOGIN NOBYPASSRLS; END IF; END $$");
  await s.query('GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO fleet_app_test');await s.query('GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO fleet_app_test')}
 const dir=mkdtempSync('/tmp/fleet-visibility-');
 for(const id of ['desiign','hello-world']){mkdirSync(dir+'/projects/'+id,{recursive:true});writeFileSync(dir+'/projects/'+id+'/project.json',JSON.stringify({id,name:id,contextFiles:[]}))}
 const people=peopleFile([{email:OWNER,name:'Owner',roles:['operator','architect'],owner:true},{email:ALINA,name:'Alina',roles:['operator'],grants:[{kind:'project',id:'desiign'}]}]);
 const app=createApp({database:'memory://',store:s,browserSecret:'browser',hostId:'pds18-native',peopleFile:people,projectConfigDirectory:dir});
 try{
  const op=async(name:string,payload:any={},person=OWNER)=>app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer browser','x-fleet-person':person},payload});
  const json=async(name:string,payload:any={},person=OWNER)=>{const r=await op(name,payload,person);assert.equal(r.statusCode,200,name+' '+r.body);return r.json()};
  for(const projectId of ['desiign','hello-world'])await json('task_create',{projectId,title:'task of '+projectId});
  await json('hosts_register',{id:'pds18-native',secretSha256:sha256('pds-secret-0123456789abcdef0123456789')});
  await json('hosts_register',{id:'alina-mac',holder:ALINA,secretSha256:sha256('alina-secret-0123456789abcdef0123456789')});
  // Owner: unchanged, everything.
  assert.deepEqual((await json('task_list')).tasks.map((t:any)=>t.projectId).sort(),['desiign','hello-world']);
  assert.equal((await json('project_list')).projects.length,2);
  // Alina: her project and her host only.
  assert.deepEqual((await json('task_list',{},ALINA)).tasks.map((t:any)=>t.projectId),['desiign']);
  assert.deepEqual((await json('project_list',{},ALINA)).projects.map((p:any)=>p.id),['desiign']);
  assert.deepEqual((await json('hosts_list',{},ALINA)).hosts.map((h:any)=>h.id),['alina-mac']);
  assert.equal((await op('project_context',{projectId:'hello-world'},ALINA)).statusCode,404);
  assert.equal((await op('task_create',{projectId:'hello-world',title:'x'},ALINA)).statusCode,404);
  assert.equal((await json('task_create',{projectId:'desiign',title:'hers'},ALINA)).createdBy,ALINA);
  // Architect views: 403 for an operator.
  for(const name of ['account_list','lab_experiments_list'])assert.equal((await op(name,{},ALINA)).statusCode,403,name);
  assert.equal((await app.inject({method:'GET',url:'/api/accounts',headers:{authorization:'Bearer browser','x-fleet-person':ALINA}})).statusCode,403);
 }finally{await app.close();await s.close()}
});
