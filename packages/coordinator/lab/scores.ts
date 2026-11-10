// Lab scores (docs/designs/fleet-lab.md "Ratings", Stage 3 item 27). A score is one row of the score table: one trial (or N
// trials for pick_best), one evaluator and its version, one form. Forms are never averaged together; a correction is a new
// row with replaces_score_id, and the replaced row drops out of every view. Evaluators: script (the example's checks, run by
// the coordinator over the trial's files), judge (a pipeline run whose last step writes {value, rationale}) and the owner
// (a question of kind 'rating').
import {createHash,randomUUID} from 'node:crypto';
import {spawn} from 'node:child_process';
import {mkdtempSync,mkdirSync,writeFileSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,dirname,delimiter} from 'node:path';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};

export const FORMS=['metric','pass_fail','pick_best','scale'] as const;
export type Form=typeof FORMS[number];

// Seeded shuffle (mulberry32 seeded from sha256(seed)): the same seed always gives the same order, so a blind rating shows
// its outputs in one stable order however often it is opened.
export function shuffle<T>(items:T[],seed:string):T[]{let a=createHash('sha256').update(seed).digest().readUInt32LE(0);
 const rnd=()=>{a=(a+0x6D2B79F5)|0;let t=Math.imul(a^a>>>15,1|a);t=(t+Math.imul(t^t>>>7,61|t))^t;return ((t^t>>>14)>>>0)/4294967296};
 const out=[...items];for(let i=out.length-1;i>0;i--){const j=Math.floor(rnd()*(i+1));[out[i],out[j]]=[out[j],out[i]]}return out}

// One value per form. pass_fail: true|false; scale: 1-5; pick_best: one of the rated trial IDs; metric: a finite number.
export function checkValue(form:Form,value:any,trialIds:string[]=[]){
 const ok=form==='pass_fail'?typeof value==='boolean':form==='scale'?Number.isInteger(value)&&value>=1&&value<=5:form==='pick_best'?trialIds.includes(value):Number.isFinite(value);
 if(!ok)fail(400,`Invalid ${form} value`);return value}
// Owner answers, typed from the keyboard: pass_fail "pass" | "fail" with optional ": reason tags"; scale "1".."5" with an
// optional ": note"; pick_best the chosen trial ID.
export function parseRating(form:Form,answer:string,trialIds:string[]){
 const [head,...rest]=answer.split(':'),note=rest.join(':').trim()||null,h=head.trim();
 if(form==='pass_fail'){if(h!=='pass'&&h!=='fail')fail(400,'A pass_fail answer is pass or fail');return {value:h==='pass',rationale:note}}
 if(form==='scale')return {value:checkValue('scale',Number(h)),rationale:note};
 if(form==='pick_best')return {value:checkValue('pick_best',answer.trim(),trialIds),rationale:null};
 return fail(400,'Owner ratings are pass_fail, pick_best or scale')}

// Runs each check (a shell command, exit 0 = pass) in a fresh directory holding the given files, FLEET_OUTPUT_DIR set to it,
// as scripts/lab-examples.ts check does. The directory of the running node is appended to PATH so checks that call node work.
export async function runChecks(checks:{id:string,run:string}[],files:{path:string,bytes:Buffer}[]){
 const dir=mkdtempSync(join(tmpdir(),'fleet-eval-'));
 try{for(const f of files){mkdirSync(dirname(join(dir,f.path)),{recursive:true});writeFileSync(join(dir,f.path),f.bytes)}
  const failed:string[]=[];
  for(const c of checks){const code=await new Promise<number>(res=>{const p=spawn('sh',['-c',c.run],{cwd:dir,env:{PATH:[process.env.PATH,dirname(process.execPath)].filter(Boolean).join(delimiter),HOME:dir,FLEET_OUTPUT_DIR:dir},stdio:'ignore'});p.on('error',()=>res(127));p.on('close',x=>res(x??1))});
   if(code!==0)failed.push(c.id)}
  return {passed:checks.length-failed.length,total:checks.length,failed}}
 finally{rmSync(dir,{recursive:true,force:true})}}

type Q=(sql:string,p?:any[])=>Promise<any[]>;
export function labScores(query:Q){
 const view=(r:any)=>({id:r.id,experimentId:r.experiment_id,trialIds:r.trial_ids,evaluator:r.evaluator,evaluatorVersion:r.evaluator_version,form:r.form,value:r.value,sourceExecutionId:r.source_execution_id,questionId:r.question_id,rationale:r.rationale,replacesScoreId:r.replaces_score_id,createdAt:r.created_at});
 async function add(s:{experimentId:string,projectId:string,trialIds:string[],evaluator:string,evaluatorVersion:string,form:Form,value:any,sourceExecutionId?:string|null,questionId?:string|null,rationale?:string|null,replaces?:string|null}){
  checkValue(s.form,s.value,s.trialIds);const id=randomUUID();
  await query('INSERT INTO score(id,experiment_id,project_id,trial_ids,evaluator,evaluator_version,form,value,source_execution_id,question_id,rationale,replaces_score_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)',
   [id,s.experimentId,s.projectId,s.trialIds,s.evaluator,s.evaluatorVersion,s.form,JSON.stringify(s.value),s.sourceExecutionId??null,s.questionId??null,s.rationale?.slice(0,4000)??null,s.replaces??null]);
  return id}
 // Current scores: rows no later row replaces.
 const current=async(experimentId:string)=>(await query('SELECT * FROM score s WHERE experiment_id=$1 AND NOT EXISTS (SELECT 1 FROM score r WHERE r.replaces_score_id=s.id) ORDER BY seq',[experimentId])).map(view);
 // A correction keeps the trials, evaluator and form of the score it replaces; only a current score can be corrected.
 async function correct(scoreId:string,b:{value:any,rationale?:string}){
  const old=(await query('SELECT * FROM score WHERE id=$1',[scoreId]))[0]??fail(404,'Unknown score');
  if((await query('SELECT 1 FROM score WHERE replaces_score_id=$1',[scoreId])).length)fail(409,'Score already corrected');
  const id=await add({experimentId:old.experiment_id,projectId:old.project_id,trialIds:old.trial_ids,evaluator:old.evaluator,evaluatorVersion:old.evaluator_version,form:old.form,value:b.value,questionId:old.question_id,sourceExecutionId:old.source_execution_id,rationale:typeof b.rationale==='string'?b.rationale:null,replaces:scoreId});
  return view((await query('SELECT * FROM score WHERE id=$1',[id]))[0])}
 return {add,current,correct,view}}

// Judge-to-owner agreement (the decision gate): of the trials both a judge and the owner rated pass_fail, the share where they
// match; and of the owner's pick_best answers, how often the judge's best-scored trial of that group was the owner's pick.
export function agreement(scores:{trialIds:string[],evaluator:string,form:string,value:any}[]){
 const owner=(f:string)=>scores.filter(s=>s.form===f&&s.evaluator.startsWith('human:')),judge=scores.filter(s=>s.evaluator.startsWith('judge:'));
 let compared=0,matched=0;
 for(const o of owner('pass_fail')){const j=judge.find(s=>s.form==='pass_fail'&&s.trialIds[0]===o.trialIds[0]);if(j){compared++;if(j.value===o.value)matched++}}
 for(const o of owner('pick_best')){const js=o.trialIds.map(t=>judge.find(s=>s.form==='scale'&&s.trialIds[0]===t));if(js.some(j=>!j))continue;
  const best=Math.max(...js.map(j=>j!.value)),top=o.trialIds.filter((_,i)=>js[i]!.value===best);if(top.length!==1)continue;compared++;if(top[0]===o.value)matched++}
 return {compared,matched,percent:compared?Math.round(100*matched/compared):null}}

// Summary of one cell (example × variant) per evaluator, over the cell's comparable trials: never one number across forms.
export function summarize(scores:{trialIds:string[],evaluator:string,form:string,value:any}[],trialIds:string[]){
 const out:Record<string,any>={};
 for(const s of scores){const mine=s.trialIds.filter(t=>trialIds.includes(t));if(!mine.length)continue;const k=s.evaluator;const e=out[k]??={form:s.form,n:0,values:[]};
  if(s.form==='pick_best'){e.n++;if(trialIds.includes(s.value))e.values.push(1)}else{e.n++;e.values.push(s.form==='pass_fail'?(s.value?1:0):s.value)}}
 for(const e of Object.values(out)){const v:number[]=e.values;
  if(e.form==='pick_best')Object.assign(e,{wins:v.length});
  else if(e.form==='pass_fail')Object.assign(e,{passed:v.reduce((a,x)=>a+x,0)});
  else if(v.length)Object.assign(e,{mean:v.reduce((a,x)=>a+x,0)/v.length,min:Math.min(...v),max:Math.max(...v)});delete e.values}
 return out}
