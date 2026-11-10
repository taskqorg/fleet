// Lab Stage 1 (docs/designs/fleet-lab.md): use-case registry, drift check, examples and their import.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {existsSync,mkdirSync,mkdtempSync,readFileSync,readdirSync,writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {section,hash,check,run,loadRegistry} from '../../scripts/lab-drift.js';
import {checkExample,readExamples,runChecks,importExamples} from '../../scripts/lab-examples.js';
import {convert} from '../../scripts/lab-import-assetq.js';
import {useCaseIds} from '../../packages/coordinator/lab/examples.js';
import {createApp} from '../../packages/coordinator/app.js';
import {attachStores} from './artifact-helpers.js';

const doc='# Cases\n\n<a id="uc1"></a>\n\n## UC1. First\n\nText one.\n\n### Detail\n\nMore.\n\n## UC-02. Second case\n\nText two.\n\n## Rows\n\n| ID | Goal |\n|---|---|\n| L01 PILOTQ-R1 | Row one |\n| L010 | Not L01 |\n';

test('section: explicit anchor, heading slug, table row',()=>{
 assert.equal(section(doc,'uc1'),'## UC1. First\n\nText one.\n\n### Detail\n\nMore.');
 assert.equal(section(doc,'uc-02-second-case')?.split('\n')[0],'## UC-02. Second case');
 assert.equal(section(doc,'L01'),'| L01 PILOTQ-R1 | Row one |');
 assert.equal(section(doc,'nope'),null);
 assert.equal(section('## A\n\n```sh\n# not a heading\n```\n\nafter\n\n## B\n','a'),'## A\n\n```sh\n# not a heading\n```\n\nafter','a fenced # line does not end the section')});

test('drift check flags a changed source and leaves the others current',()=>{
 const base=mkdtempSync(join(tmpdir(),'lab-drift-')),repo=join(base,'other'),root=join(base,'fleet');
 const git=(...a:string[])=>execFileSync('git',['-C',repo,...a],{encoding:'utf8'}).trim();
 mkdirSync(repo);git('init','-q');git('config','user.email','t@t');git('config','user.name','t');
 writeFileSync(join(repo,'cases.md'),doc);git('add','.');git('commit','-qm','one');
 const src=(anchor:string)=>({repo:'other',path:'cases.md',anchor,commit:git('rev-parse','HEAD'),sha256:hash(section(doc,anchor)!)});
 mkdirSync(join(root,'config/lab/use-cases'),{recursive:true});
 const entries=[{id:'other:UC1',source:src('uc1'),source_status:'current'},{id:'other:UC-02',source:src('uc-02-second-case'),source_status:'current'},{id:'other:L01',source:src('L01'),source_status:'current'},{id:'gone:X',source:{...src('uc1'),repo:'gone'},source_status:'current'}];
 writeFileSync(join(root,'config/lab/use-cases/other.json'),JSON.stringify(entries));
 const log:string[]=[];
 assert.equal(run([],root,l=>log.push(l),base),0,'nothing changed yet; an absent repo is not a change');
 writeFileSync(join(repo,'cases.md'),doc.replace('Text one.','Text one, edited.').replace('| Row one |','| Row one, edited |'));git('commit','-qam','two');
 assert.equal(check(entries[0].source,root,base),'changed');assert.equal(check(entries[1].source,root,base),'current');
 assert.equal(run(['--write'],root,l=>log.push(l),base),2);
 const status=()=>Object.fromEntries(loadRegistry(root)[0].entries.map((e:any)=>[e.id,e.source_status]));
 assert.deepEqual(status(),{'other:UC1':'changed','other:UC-02':'current','other:L01':'changed','gone:X':'current'});
 assert.equal(run(['--pin','other:UC1'],root,()=>{},base),0);
 assert.equal(status()['other:UC1'],'current');assert.equal(check(loadRegistry(root)[0].entries[0].source,root,base),'current');
 writeFileSync(join(repo,'cases.md'),'# Cases\n');git('commit','-qam','three');
 assert.equal(check(entries[1].source,root,base),'missing')});

test('registry loaders skip dotfiles such as macOS ._* copies',()=>{
 const root=mkdtempSync(join(tmpdir(),'lab-reg-'));mkdirSync(join(root,'use-cases'),{recursive:true});
 writeFileSync(join(root,'use-cases/x.json'),JSON.stringify([{id:'x:1'}]));writeFileSync(join(root,'use-cases/._x.json'),Buffer.from([0,5,22,7]));
 assert.deepEqual([...useCaseIds(root)],['x:1'])});

test('registry: unique IDs, required fields, every org repo indexed',()=>{
 const all=loadRegistry(process.cwd()).flatMap(f=>f.entries),ids=all.map((e:any)=>e.id);
 assert.equal(new Set(ids).size,ids.length,'IDs are unique');
 for(const e of all){assert.match(e.id,/^(fleet|taskq|pilotq|agentq|dashq|assetq):\S+$/);assert.ok(['work','behavior','none'].includes(e.lab_fit),e.id);
  assert.ok(['current','changed','missing'].includes(e.source_status),e.id);for(const k of ['repo','path','anchor'])assert.ok(e.source[k],e.id);
  assert.match(e.source.commit,/^[0-9a-f]{40}$/);assert.match(e.source.sha256,/^[0-9a-f]{64}$/);assert.ok(e.goal&&e.actor&&Array.isArray(e.checks)&&Array.isArray(e.tags)&&e.maturity,e.id)}
 assert.deepEqual([...new Set(all.map((e:any)=>e.id.split(':')[0]))].sort(),['agentq','assetq','dashq','fleet','pilotq','taskq'])});

test('examples: at least five work examples, each passes its script checks with no agent',()=>{
 const dir='config/lab/examples',examples=readExamples(readdirSync(dir).map(f=>join(dir,f))),useCases=useCaseIds('config/lab');
 const fit=new Map(loadRegistry(process.cwd()).flatMap(f=>f.entries).map((e:any)=>[e.id,e.lab_fit]));
 const work=examples.filter(x=>fit.get(x.use_case_id)==='work');assert.ok(work.length>=5,'five work examples');
 for(const x of examples)assert.deepEqual(checkExample(x,useCases),[],x.id);
 // A check that passes on untouched input is reported.
 const vacuous={...examples[0],checks:[{id:'true',run:'true'}]};assert.deepEqual(checkExample(vacuous,useCases),['every check passes on the inputs alone']);
 const broken={...examples[0],references:[]};assert.ok(checkExample(broken,useCases).some(p=>p.startsWith('check ')))});

test('assetq dataset cases convert to examples without loss',()=>{
 const rev={id:'crate',version:2,sha256:'ab',document:{operation:'synthetic.generate',cases:[{id:'blue',text:'invented blue crate',attachments:['obj-1']}]}};
 const [e]=convert([rev]);
 assert.equal(e.id,'assetq-crate-blue');assert.equal(e.use_case_id,'assetq:datasets');assert.equal(e.version,2);assert.equal(e.prompt,'invented blue crate');
 assert.deepEqual(JSON.parse(e.inputs[0].text),{dataset:'crate@2',datasetSha256:'ab',operation:'synthetic.generate',case:rev.document.cases[0]});
 assert.deepEqual(checkExample(e,useCaseIds('config/lab')),[])});

test('POST /api/lab/examples stores approved lab artifacts; same content is idempotent, other content needs a new version',async()=>{
 const app:any=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',storeEndpointHosts:['127.0.0.1']});
 await app.ready();await attachStores(app,'m',['mac']);
 const call=(method:string,url:string,payload?:any)=>app.inject({method,url,headers:{authorization:'Bearer b'},payload});
 const ex=JSON.parse(readFileSync('config/lab/examples/fleet-uc1-about-page.json','utf8'));
 const r=await call('POST','/api/lab/examples',{examples:[ex],approved:true});assert.equal(r.statusCode,200,r.body);
 const [stored]=r.json().examples;assert.equal(stored.created,true);
 const row=(await app.store.query('SELECT scope,role,name,content_hash,approved_at FROM artifact WHERE id=$1',[stored.artifactId]))[0];
 assert.deepEqual({scope:row.scope,role:row.role,name:row.name,hash:row.content_hash},{scope:'lab',role:'example',name:'examples/fleet-uc1-about-page@1.json',hash:stored.hash});assert.ok(row.approved_at);
 const again=(await call('POST','/api/lab/examples',{examples:[ex],approved:true})).json().examples[0];
 assert.deepEqual([again.created,again.artifactId],[false,stored.artifactId]);
 assert.equal((await call('POST','/api/lab/examples',{examples:[{...ex,prompt:'other'}]})).statusCode,409);
 assert.equal((await call('POST','/api/lab/examples',{examples:[{...ex,prompt:'other',version:2}]})).json().examples[0].created,true);
 assert.equal((await call('POST','/api/lab/examples',{examples:[{...ex,use_case_id:'fleet:UC99'}]})).statusCode,400);
 // A refused batch stores nothing: the new example before the conflicting one is not written.
 assert.equal((await call('POST','/api/lab/examples',{examples:[{...ex,id:'batch-new'},{...ex,prompt:'other'}]})).statusCode,409);
 assert.equal((await app.store.query("SELECT 1 FROM artifact WHERE name='examples/batch-new@1.json'")).length,0);
 assert.equal((await call('POST','/api/lab/examples',{examples:[{...ex,inputs:[{path:'../x',text:''}]}]})).statusCode,400);
 const list=(await call('GET','/api/lab/examples?state=approved')).json().examples;assert.deepEqual(list.map((x:any)=>x.name),['examples/fleet-uc1-about-page@1.json']);
 assert.equal((await call('GET','/api/lab/examples?state=proposed')).json().examples.length,1);
 const got=(await call('GET','/api/lab/examples/'+stored.artifactId)).json();assert.equal(got.state,'approved');assert.deepEqual(got.example.checks,ex.checks);
 assert.equal((await app.inject({method:'GET',url:'/api/lab/examples'})).statusCode,401);
 // An agent of the example's project cannot list or read it (references are known-good answers).
 const own=(await call('POST','/api/lab/examples',{examples:[{...ex,id:'own-project'}],projectId:'fleet-hello-world',approved:true})).json().examples[0];
 await app.store.executions.insertIfAbsent({id:'agent-1',kind:'agent_run',state:'running',projectId:'fleet-hello-world',hostId:'mac'});
 const host=(url:string,payload:any)=>app.inject({method:'POST',url,headers:{authorization:'Bearer m'},payload});
 const seen=(await host('/api/hosts/artifacts/list',{hostId:'mac',executionId:'agent-1'})).json().artifacts.map((a:any)=>a.id);
 assert.ok(!seen.includes(own.artifactId),'examples are not in an agent artifact list');
 assert.equal((await host('/api/hosts/artifacts/get',{hostId:'mac',executionId:'agent-1',artifactId:own.artifactId})).statusCode,403);
 await app.close()});

test('check runner puts its own node on PATH',(t)=>{const saved=process.env.PATH;
 // PATH holds only the directory of sh, so the check's node can come only from the runner.
 if(existsSync('/bin/node'))return t.skip('node is in /bin');process.env.PATH='/bin';
 try{assert.deepEqual(runChecks({checks:[{id:'node',run:'node -e 0'}]} as any,[]),[])}finally{process.env.PATH=saved}});

test('import sends FLEET_ORIGIN, so it works behind browser proxy auth',async()=>{
 const app:any=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',storeEndpointHosts:['127.0.0.1'],browserProxyAuth:true,browserOrigin:'https://fleet.example'});
 await app.ready();await attachStores(app,'m',['mac']);const url=await app.listen({host:'127.0.0.1',port:0});
 const ex=JSON.parse(readFileSync('config/lab/examples/fleet-uc1-about-page.json','utf8'));
 try{await assert.rejects(importExamples({url,token:'b',examples:[ex]}),/403/);
  assert.equal((await importExamples({url,token:'b',origin:'https://fleet.example',examples:[ex]})).examples[0].created,true)}finally{await app.close()}});
