// Lab experiments, scores and ratings (docs/designs/fleet-lab.md Stage 3, item 31).
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync,mkdirSync,writeFileSync,chmodSync,readFileSync,cpSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createApp} from '../../packages/coordinator/app.ts';
import {shuffle,parseRating,summarize,agreement} from '../../packages/coordinator/lab/scores.ts';
import {attachStores} from './artifact-helpers.ts';

const fake=fileURLToPath(new URL('./fixtures/fake_connection.py',import.meta.url));
const search=(conn:string,extra={})=>({version:2,id:'x',steps:[{id:'search',kind:'api',connection:conn,tool:'web_search',arguments:{query:'${example.prompt}'},...extra}]});
function setup(){
 const dir=mkdtempSync('/tmp/fleet-lab-');mkdirSync(dir+'/pipelines');mkdirSync(dir+'/projects');cpSync('config/agents',dir+'/agents',{recursive:true});cpSync('config/projects/hello-world',dir+'/projects/hello-world',{recursive:true});cpSync('config/catalogue.json',dir+'/catalogue.json');
 for(const [id,conn,extra] of [['lab-ok','ok',{}],['lab-full','full',{}],['lab-job','job',{mode:'job'}]] as const)writeFileSync(`${dir}/pipelines/${id}.json`,JSON.stringify({...search(conn,extra),id}));
 for(const [conn,answer] of [['ok','ok'],['full','queue-full'],['job','job']]){mkdirSync(`${dir}/connections/${conn}`,{recursive:true});writeFileSync(`${dir}/connections/${conn}/connection.json`,JSON.stringify({answer,log:dir+'/calls.log',jobState:dir+'/job.state'}));chmodSync(`${dir}/connections/${conn}/connection.json`,0o600)}
 writeFileSync(dir+'/calls.log','');writeFileSync(dir+'/job.state','running');
 const config={database:dir+'/db',browserSecret:'b',machineSecret:'m',hostId:'host',projectConfigDirectory:dir,profilesDirectory:dir+'/agents',connectionsDir:dir+'/connections',connectionClient:['python3',fake],storeEndpointHosts:['127.0.0.1'],pipelineTickMs:100};
 return {dir,config,calls:()=>readFileSync(dir+'/calls.log','utf8').split('\n').filter(Boolean).length}}
const boot=async(config:any)=>{const app:any=createApp(config);await app.ready();await attachStores(app,'m',['host']);return app};
const call=async(app:any,method:string,url:string,payload?:any,status=200)=>{const r=await app.inject({method,url,headers:{authorization:'Bearer b'},...(payload!==undefined?{payload}:{})});assert.equal(r.statusCode,status,r.body);return r.json()};
const op=(app:any,name:string,b:any,status=200)=>call(app,'POST','/api/ops/'+name,b,status);
const until=async(app:any,id:string,ok:(e:any)=>boolean)=>{for(let i=0;i<300;i++){const e=await call(app,'GET','/api/lab/experiments/'+id);if(ok(e))return e;await new Promise(x=>setTimeout(x,50))}throw Error("timeout "+JSON.stringify(await call(app,"GET","/api/lab/experiments/"+id)).slice(0,3000))};
const example={id:'t-search',use_case_id:'fleet:UC5',version:1,origin:'synthetic',fixture:null,prompt:'hello',inputs:[],references:[],checks:[{id:'url',run:'grep -q example.com output.json'}],split:'tune'};
const seed=async(app:any)=>(await call(app,'POST','/api/lab/examples',{examples:[example],approved:true})).examples[0].artifactId;
const ab=(ex:string,pipelineId='lab-ok',more:any={})=>({kind:'ab',projectId:'hello-world',pipelineId,examples:[ex],variants:[{label:'baseline'},{label:'skill',overrides:{skills:['x']}}],repeats:2,evaluators:[{kind:'script',ref:'example.checks',form:'pass_fail'},{kind:'human',ref:'owner',form:'pick_best'}],...more});

test('seeded shuffle is stable; rating answers parse per form; forms never combine',()=>{
 const ids=['a','b','c','d','e'];assert.deepEqual(shuffle(ids,'s1'),shuffle(ids,'s1'));assert.deepEqual([...shuffle(ids,'s1')].sort(),ids);
 assert.notDeepEqual(['s1','s2','s3','s4'].map(s=>shuffle(ids,s).join('')),Array(4).fill(ids.join('')));
 assert.deepEqual(parseRating('pass_fail','fail: wrong source',[]),{value:false,rationale:'wrong source'});
 assert.deepEqual(parseRating('scale','4',[]),{value:4,rationale:null});assert.throws(()=>parseRating('scale','6',[]),/Invalid scale/);
 assert.throws(()=>parseRating('pick_best','z',['a','b']),/Invalid pick_best/);
 const s=summarize([{trialIds:['t1'],evaluator:'script:example.checks:pass_fail',form:'pass_fail',value:true},{trialIds:['t1'],evaluator:'human:owner:scale',form:'scale',value:4},{trialIds:['t2'],evaluator:'human:owner:scale',form:'scale',value:2}],['t1','t2']);
 assert.deepEqual(s,{'script:example.checks:pass_fail':{form:'pass_fail',n:1,passed:1},'human:owner:scale':{form:'scale',n:2,mean:3,min:2,max:4}});
 assert.deepEqual(agreement([{trialIds:['t1'],evaluator:'judge:j:pass_fail',form:'pass_fail',value:true},{trialIds:['t1'],evaluator:'human:owner:pass_fail',form:'pass_fail',value:false}]),{compared:1,matched:0,percent:0});
});

test('A/B: concurrent reconciles launch one trial per cell, script scores land once, spend counted once, restart relaunches nothing',async()=>{
 const s=setup();let app=await boot(s.config);
 try{const ex=await seed(app);
  await call(app,'POST','/api/lab/experiments',{...ab(ex),kind:'loop'},400);await call(app,'POST','/api/lab/experiments',{...ab(ex),projectId:'nope'},400);
  const e=await call(app,'POST','/api/lab/experiments',ab(ex));assert.equal(e.state,'draft');
  assert.notEqual(e.variants[0].config_ref,e.variants[1].config_ref);
  // Two launches at once: one wins, the other is a wrong-state 409; reconcile is serial, so each cell has one trial.
  const both=await Promise.all([app.inject({method:'POST',url:`/api/lab/experiments/${e.id}/launch`,headers:{authorization:'Bearer b'}}),app.inject({method:'POST',url:`/api/lab/experiments/${e.id}/launch`,headers:{authorization:'Bearer b'}})]);
  assert.deepEqual(both.map(r=>r.statusCode).sort(),[200,409]);
  const done=await until(app,e.id,x=>x.state==='done');
  assert.equal(done.trials.length,4);assert.equal(s.calls(),4);assert.equal(done.stopReason,'complete');
  assert.ok(done.trials.every((t:any)=>t.comparable&&t.spend.credits===2&&t.spend.basis==='measured'));
  assert.equal(done.spend.pipe_credits.value,8);assert.equal(done.spend.trials.value,4);
  assert.equal(done.scores.filter((x:any)=>x.evaluator==='script:example.checks:pass_fail').length,4);
  // Spread: each variant's summary covers both repeats.
  assert.deepEqual(done.variants[0].summary['script:example.checks:pass_fail'],{form:'pass_fail',n:2,passed:2});
  assert.equal(done.cells.length,2);assert.equal(done.cells[0].trials.length,2);
  await app.close();app=await boot(s.config);await new Promise(x=>setTimeout(x,500));
  const again=await call(app,'GET','/api/lab/experiments/'+e.id);
  assert.equal(again.trials.length,4);assert.equal(s.calls(),4);assert.equal(again.spend.pipe_credits.value,8);assert.equal(again.scores.length,4);
  // Promote: a catalogue patch for the owner; no method has this config_ref yet, so no recommendation.
  const p=await call(app,'POST',`/api/lab/experiments/${e.id}/promote`,{variant:'skill'});
  assert.equal(p.cataloguePatch[0].value.configRef,e.variants[1].config_ref);assert.equal(p.recommendation,null);
 }finally{await app.close()}
});

test('budgets: pause asks once and resumes; stop ends with its reason; warn only marks',async()=>{
 const s=setup();const app=await boot(s.config);
 try{const ex=await seed(app);
  const e=await call(app,'POST','/api/lab/experiments',ab(ex,'lab-ok',{budgets:[{unit:'trials',limit:2}]}));assert.equal(e.budgets[0].action,'pause');
  // The trials budget caps launches in one pass: the first two cells launch, then the experiment pauses with one question.
  await call(app,'POST',`/api/lab/experiments/${e.id}/launch`);
  const paused=await until(app,e.id,x=>x.state==='paused');assert.equal(paused.trials.length,2);
  const qs=(await op(app,'questions_list',{kind:'operator_decision'})).questions.filter((q:any)=>q.state==='open');assert.equal(qs.length,1);
  await op(app,'questions_answer',{questionId:qs[0].id,answer:'maybe'},400);
  await op(app,'questions_answer',{questionId:qs[0].id,answer:'resume'});
  const done=await until(app,e.id,x=>x.state==='done');assert.equal(done.trials.length,4);
  assert.equal((await op(app,'questions_list',{kind:'operator_decision'})).questions.length,1);
  const st=await call(app,'POST','/api/lab/experiments',{...ab(ex),budgets:[{unit:'trials',limit:2,action:'stop'},{unit:'pipe_calls',limit:1,action:'warn'}]});
  await call(app,'POST',`/api/lab/experiments/${st.id}/launch`);
  const stopped=await until(app,st.id,x=>x.state==='stopped');assert.equal(stopped.stopReason,'budget:trials');assert.equal(stopped.trials.length,2);
  assert.ok(stopped.budgets[0].firedAt);assert.equal((await op(app,'questions_list',{kind:'operator_decision'})).questions.length,1);
  await call(app,'POST',`/api/lab/experiments/${st.id}/resume`,undefined,409);
 }finally{await app.close()}
});

test('infra-failed and cancelled trials are excluded; retry raises the attempt',async()=>{
 const s=setup();const app=await boot(s.config);
 try{const ex=await seed(app);
  const f=await call(app,'POST','/api/lab/experiments',{kind:'single',projectId:'hello-world',pipelineId:'lab-full',examples:[ex],variants:[{label:'baseline'}],evaluators:[{kind:'script',ref:'example.checks',form:'pass_fail'}]});
  await call(app,'POST',`/api/lab/experiments/${f.id}/launch`);const fd=await until(app,f.id,x=>x.state==='done');
  assert.equal(fd.trials[0].problem,'infra:queue-full');assert.equal(fd.trials[0].comparable,false);assert.equal(fd.scores.length,0);
  const j=await call(app,'POST','/api/lab/experiments',{kind:'single',projectId:'hello-world',pipelineId:'lab-job',examples:[ex],variants:[{label:'baseline'}]});
  await call(app,'POST',`/api/lab/experiments/${j.id}/launch`);const running=await until(app,j.id,x=>x.trials.length===1&&x.trials[0].state==='running');
  const cell=encodeURIComponent('t-search@1|baseline|0');
  await call(app,'POST',`/api/lab/experiments/${j.id}/cells/${cell}/retry`,undefined,409);
  await call(app,'POST',`/api/lab/experiments/${j.id}/cells/${cell}/cancel`);
  const c=await until(app,j.id,x=>x.state==='done');assert.equal(c.trials[0].state,'cancelled');assert.equal(c.trials[0].comparable,false);
  writeFileSync(s.dir+'/job.state','succeeded');
  await call(app,'POST',`/api/lab/experiments/${j.id}/cells/${cell}/retry`);
  const r=await until(app,j.id,x=>x.state==='done'&&x.trials.length===2);assert.deepEqual(r.trials.map((t:any)=>t.cell.attempt),[0,1]);assert.equal(r.trials[1].state,'completed');
  assert.ok(running);
 }finally{await app.close()}
});

test('owner ratings: blind seeded order, answers become scores, a correction replaces, agreement shown',async()=>{
 const s=setup();const app=await boot(s.config);
 try{const ex=await seed(app);const e=await call(app,'POST','/api/lab/experiments',ab(ex));await call(app,'POST',`/api/lab/experiments/${e.id}/launch`);
  const done=await until(app,e.id,x=>x.state==='done');const [t1,t2]=[done.trials[0].id,done.trials[2].id];
  await call(app,'POST','/api/lab/ratings',{experimentId:e.id,form:'pick_best',trialIds:[t1]},400);
  const r=await call(app,'POST','/api/lab/ratings',{experimentId:e.id,form:'pick_best',trialIds:[t1,t2]});
  assert.deepEqual(r.items.map((i:any)=>i.label),['A','B']);assert.ok(!JSON.stringify(r.items).includes('baseline'));
  assert.deepEqual((await call(app,'GET','/api/lab/ratings/'+r.id)).items.map((i:any)=>i.trialId),r.items.map((i:any)=>i.trialId));
  await op(app,'questions_answer',{questionId:r.id,answer:'nobody'},400);
  await op(app,'questions_answer',{questionId:r.id,answer:t2});
  const pf=await call(app,'POST','/api/lab/ratings',{experimentId:e.id,form:'pass_fail',trialIds:[t1]});
  await op(app,'questions_answer',{questionId:pf.id,answer:'fail: missing source'});
  // A judge score on the same trial (as a judge run would write): agreement compares it with the owner's pass_fail.
  await app.store.query("INSERT INTO score(id,experiment_id,project_id,trial_ids,evaluator,evaluator_version,form,value) VALUES('j1',$1,'hello-world',$2,'judge:j:pass_fail','1','pass_fail','true')",[e.id,[t1]]);
  let x=await call(app,'GET','/api/lab/experiments/'+e.id);
  const pick=x.scores.find((s:any)=>s.form==='pick_best');assert.deepEqual([pick.value,pick.trialIds,pick.questionId],[t2,[t1,t2],r.id]);
  assert.deepEqual(x.agreement,{compared:1,matched:0,percent:0});
  const owner=x.scores.find((s:any)=>s.evaluator==='human:owner:pass_fail');assert.equal(owner.rationale,'missing source');
  const fixed=await call(app,'POST',`/api/lab/scores/${owner.id}/correct`,{value:true,rationale:'misread'});assert.equal(fixed.replacesScoreId,owner.id);
  await call(app,'POST',`/api/lab/scores/${owner.id}/correct`,{value:false},409);
  x=await call(app,'GET','/api/lab/experiments/'+e.id);assert.ok(!x.scores.some((s:any)=>s.id===owner.id));assert.deepEqual(x.agreement,{compared:1,matched:1,percent:100});
  // The skill variant won the one pick_best; the baseline cell shows no win and keeps its own pass_fail separate.
  const skill=x.cells.find((c:any)=>c.variant==='skill');assert.deepEqual(skill.summary['human:owner:pick_best'],{form:'pick_best',n:1,wins:1});
 }finally{await app.close()}
});
