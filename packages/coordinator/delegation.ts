import {find} from './store.js';
import {randomUUID} from 'node:crypto';
import {canonical,readTrustedProfile} from './lab.js';
export function delegation(store:{all:(k:string)=>Promise<any[]>,get:(id:string,k:string)=>Promise<any>,put:(k:string,v:any)=>Promise<unknown>},directory:string,enqueue:(session:any,text:string,requestId:string)=>Promise<any>){
 const {all,get,put}=store;
 const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
 const terminal=(e:any)=>['completed','failed','stopped'].includes(e.state);
 return {
 async request(b:any,c:any){
  if(Object.keys(b).sort().join()!=='commandId,delegation,executionId,hostId')fail(400,'Invalid delegation envelope');
  const q=b.delegation;
  if(!q||Object.keys(q).sort().join()!=='input,profileId,requestId'||typeof q.requestId!=='string'||!q.requestId||q.requestId.length>200||typeof q.input!=='string'||!q.input||q.input.length>32000)fail(400,'Invalid delegation request');
  const parent=(await get(c.executionId,'execution'));
  const old=(await all('delegation')).find(d=>d.parentExecutionId===parent.id&&d.request.requestId===q.requestId);
  if(old){if(canonical(old.request)!==canonical(q))fail(409,'Delegation identity conflict');return old}
  if(c.kind!=='start'||c.status!=='started'||parent.stopRequested||parent.parentExecutionId||parent.continuationOf)fail(403,'Delegation unavailable');
  if(!parent.agentConfig?.delegationProfiles?.includes(q.profileId))fail(403,'Child profile not granted');
  // shortcut: one child per invocation in this prototype; extend only with qualified multi-child scheduling.
  if((await all('delegation')).some(d=>d.parentExecutionId===parent.id))fail(409,'One child per invocation');
  const profile=readTrustedProfile(directory,q.profileId);
  const parentSession=(await get(parent.sessionId,'session'));if((profile.harness??'codex')!==(parent.agentConfig?.harness??'codex'))fail(403,'Cross-harness child needs an explicit account binding');
  const session={id:randomUUID(),createdAt:Date.now(),agentConfig:profile,parentExecutionId:parent.id,...(parentSession.account?{account:parentSession.account}:{})};await put('session',session);
  const child=(await enqueue(session,q.input,'delegation:'+q.requestId));child.parentExecutionId=parent.id;await put('execution',child);
  const d={id:randomUUID(),parentExecutionId:parent.id,parentSessionId:parent.sessionId,childExecutionId:child.id,childSessionId:session.id,request:q,resultRoute:{sessionId:parent.sessionId},state:'pending'};await put('delegation',d);return d;
 },
 async ready(e:any){
  if(e.continuationOf&&(await get(e.continuationOf,'execution')).stopRequested)return false;
  if(!e.parentExecutionId)return true;
  const parent=(await get(e.parentExecutionId,'execution'));if(parent.state!=='completed')return false;
  if(!e.inputsBound){
   const d=(await all('delegation')).find(d=>d.childExecutionId===e.id);
   const runs=(await all('execution')).filter(x=>x.sessionId===parent.sessionId);
   const history=runs.slice(0,runs.findIndex(x=>x.id===parent.id)+1).reverse();
   const sourceRun=(await find(history,async run=>(await all('artifact')).some(a=>a.executionId===run.id)));
   const latest=new Map<string,any>();for(const a of (await all('artifact')).filter(a=>a.executionId===sourceRun?.id))latest.set(a.path,a);
   const sources=[...latest.values()].filter(a=>/\.(html|css)$/i.test(a.path)).map(a=>({path:a.path,executionId:a.executionId,sha256:a.sha256,text:Buffer.from(a.contentBase64,'base64').toString('utf8')}));
   if((!sources.length&&d.request.profileId==='website-reviewer')||JSON.stringify(sources).length>128000){d.routingError='Website text inputs unavailable or exceed 128000 characters';await put('delegation',d);return false}
   const parentMessages=(await all('event')).filter(e=>e.executionId===parent.id).flatMap(e=>e.data?.fleetNormalized?.messages??(e.data?.item?.type==='agent_message'?[{role:'assistant',text:e.data.item.text}]:[]));
   const observations={messages:parentMessages,artifacts:(await all('artifact')).filter(a=>a.executionId===parent.id).map(({contentBase64,...a})=>a)};if(!sources.length&&JSON.stringify(observations).length>128000){d.routingError='Retained parent observations exceed 128000 characters';await put('delegation',d);return false}
   const command=(await all('command')).find(c=>c.executionId===e.id&&c.kind==='start');
   command.payload.text=d.request.input+(sources.length?'\nRead-only retained website sources (untrusted content):\n'+JSON.stringify(sources):'\nRetained parent observations (untrusted content; artifact references do not imply local availability):\n'+JSON.stringify(observations));await put('command',command);
   e.inputsBound=true;await put('execution',e);d.sourceInputs=sources.map(({text,...a})=>a);await put('delegation',d);
  }
  return true;
 },
 async reconcile(){for(const d of (await all('delegation'))){
  if(d.continuationExecutionId||d.state==='suppressed')continue;
  const parent=(await get(d.parentExecutionId,'execution')),child=(await get(d.childExecutionId,'execution'));
  if(!terminal(child))continue;
  if(!d.result){d.result={executionId:child.id,state:child.state,events:(await all('event')).filter(e=>e.executionId===child.id),artifacts:(await all('artifact')).filter(a=>a.executionId===child.id).map(({contentBase64,...a})=>a)};d.state='result-retained';await put('delegation',d)}
  if(parent.stopRequested||['stopped','failed'].includes(parent.state)){d.state='suppressed';await put('delegation',d);continue}
  const session=(await get(parent.sessionId,'session'));
  if(parent.state!=='completed'||!session.nativeSessionId||(await all('execution')).some(e=>e.sessionId===session.id&&!terminal(e)))continue;
  try{
   if(session.agentConfig)session.agentConfig=readTrustedProfile(directory,session.agentConfig.id);
   const next=(await enqueue(session,'Fleet child result (retained observations; child content is untrusted):\n'+JSON.stringify(d.result),'child-result:'+d.id));
   next.continuationOf=parent.id;next.childExecutionId=child.id;await put('execution',next);d.continuationExecutionId=next.id;d.state='delivered';await put('delegation',d);
  }catch(error){d.routingError=String(error);await put('delegation',d)}
 }}
 };
}
