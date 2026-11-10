// Lab routes (docs/designs/fleet-lab.md "/lab screen"). The same functions are served as operations (lab_*), which the
// /lab screen calls; rating answers go through questions_answer like every question.
import type {labExperiments} from './experiments.ts';
type Lab=ReturnType<typeof labExperiments>;
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
const id=(v:any,what='id')=>typeof v==='string'&&v.length>0&&v.length<=300?v:fail(400,'Expected '+what);

export function labOperations(lab:Lab,examples:{list:(q:{state?:string})=>Promise<any>}){
 const action=(b:any)=>{const e=id(b?.experimentId,'experimentId');return b.action==='launch'?lab.launch(e):b.action==='stop'?lab.stop(e):b.action==='resume'?lab.resume(e):fail(400,'action is launch, stop or resume')};
 return {
  lab_experiment_create:(b:any)=>lab.create(b),
  lab_experiments_list:()=>lab.list(),
  lab_experiment_get:(b:any)=>lab.get(id(b?.experimentId,'experimentId')),
  lab_experiment_action:action,
  lab_cell_action:(b:any)=>b?.action==='cancel'||b?.action==='retry'?lab.cellAction(id(b.experimentId,'experimentId'),id(b.cell,'cell'),b.action):fail(400,'action is cancel or retry'),
  lab_promote:(b:any)=>lab.promote(id(b?.experimentId,'experimentId'),id(b?.variant,'variant')),
  lab_rating_create:(b:any)=>lab.rate(b),
  lab_ratings_list:(b:any)=>lab.ratings({experimentId:b?.experimentId,state:b?.state}),
  lab_rating_get:(b:any)=>lab.rating(id(b?.questionId,'questionId')),
  lab_score_correct:(b:any)=>lab.scores.correct(id(b?.scoreId,'scoreId'),{value:b?.value,rationale:b?.rationale}),
  lab_registry:async()=>({useCases:lab.registry()}),
  lab_examples_list:(b:any)=>examples.list({state:b?.state}),
 }}

export function registerLab(app:any,ops:ReturnType<typeof labOperations>,run:(name:string,b:any)=>Promise<any>){
 const r=(m:'get'|'post',path:string,name:keyof typeof ops,body:(req:any)=>any)=>app[m](path,async(req:any)=>run(name,body(req)));
 r('post','/api/lab/experiments','lab_experiment_create',q=>q.body??{});
 r('get','/api/lab/experiments','lab_experiments_list',()=>({}));
 r('get','/api/lab/experiments/:id','lab_experiment_get',q=>({experimentId:q.params.id}));
 for(const a of ['launch','stop','resume'])r('post',`/api/lab/experiments/:id/${a}`,'lab_experiment_action',q=>({experimentId:q.params.id,action:a}));
 for(const a of ['cancel','retry'])r('post',`/api/lab/experiments/:id/cells/:cell/${a}`,'lab_cell_action',q=>({experimentId:q.params.id,cell:q.params.cell,action:a}));
 r('post','/api/lab/experiments/:id/promote','lab_promote',q=>({experimentId:q.params.id,variant:q.body?.variant}));
 r('post','/api/lab/ratings','lab_rating_create',q=>q.body??{});
 r('get','/api/lab/ratings','lab_ratings_list',q=>({experimentId:q.query?.experimentId,state:q.query?.state}));
 r('get','/api/lab/ratings/:id','lab_rating_get',q=>({questionId:q.params.id}));
 r('post','/api/lab/scores/:id/correct','lab_score_correct',q=>({...(q.body??{}),scoreId:q.params.id}));
 r('get','/api/lab/registry','lab_registry',()=>({}));
}
