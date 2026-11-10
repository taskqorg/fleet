// Conversation threads (docs/designs/fleet-conversations.md): one thread per task, or per session without a task, read in pages
// of typed blocks by block key (seq, index) in both directions, searched and exported on the server. Rows come from the shared
// seq: execution rows (boundary), operator messages, events (executor blocks), questions, and stream deltas (event-delta).
// Every read is a page over index ranges of the member executions; nothing reads a whole session.
type Query=(sql:string,params?:any[])=>Promise<any[]>;
export type Key={seq:number,index:number};
export type ThreadKey={task:string}|{session:string};
const fail=(statusCode:number,message:string):never=>{throw Object.assign(Error(message),{statusCode})};
export const keyText=(k:Key)=>`${k.seq}:${k.index}`;
export const parseKey=(v:any):Key=>{const m=typeof v==='string'?/^(\d+):(\d+)$/.exec(v):null;if(!m)fail(400,'Expected block key seq:index');return {seq:Number(m![1]),index:Number(m![2])}};
const cmp=(a:Key,b:Key)=>a.seq-b.seq||a.index-b.index;
const TERMINAL=new Set(['succeeded','failed','cancelled']);
const MAX='9223372036854775807';
const ms=(v:any)=>v==null?null:new Date(v).getTime();

export function threads(deps:{query:Query,artifacts:(executionId:string)=>Promise<any[]>,cursor:()=>Promise<number>,stage?:(sessionId:string)=>Promise<string|null>}){
 const {query}=deps;
 const threadKey=(b:any):ThreadKey=>{const k=b?.key;if(k&&typeof k.task==='string'&&k.task)return {task:k.task};if(k&&typeof k.session==='string'&&k.session)return {session:k.session};return fail(400,'Expected key {task} or {session}')};
 // Membership (design "Thread scope"): a task's executions, plus the taskless executions of sessions whose first task it is.
 async function members(key:ThreadKey){
  const cols="id,session_id,harness,model,config_ref->>'effort' AS effort,state,started_at,ended_at,seq";
  const rows='task' in key
   // A session's task: the one it was attached to (session_task_attach), else the first task its executions got.
   ?await query(`WITH attached AS (SELECT id AS session_id FROM document WHERE kind='session' AND data->>'taskId'=$1),
     first_task AS (SELECT DISTINCT ON (session_id) session_id,task_id FROM execution WHERE task_id IS NOT NULL AND session_id IN (SELECT session_id FROM execution WHERE task_id=$1) ORDER BY session_id,seq)
     SELECT ${cols} FROM execution WHERE kind='agent_run' AND (task_id=$1 OR (task_id IS NULL AND (session_id IN (SELECT session_id FROM first_task WHERE task_id=$1) OR session_id IN (SELECT session_id FROM attached)))) ORDER BY seq`,[key.task])
   :await query(`SELECT ${cols} FROM execution WHERE kind='agent_run' AND session_id=$1 ORDER BY seq`,[key.session]);
  if(!rows.length&&'session' in key&&!(await query("SELECT 1 AS x FROM document WHERE kind='session' AND id=$1",[key.session])).length)fail(404,'Not found');
  if(!rows.length&&'task' in key&&!(await query('SELECT 1 AS x FROM task WHERE id=$1',[key.task])).length)fail(404,'Not found');
  return new Map(rows.map(r=>[r.id,{...r,seq:Number(r.seq)}]))}
 const mark=(e:any)=>({harness:e?.harness??null,model:e?.model??null,effort:e?.effort??null});
 const boundary=(e:any)=>({kind:'boundary',key:keyText({seq:e.seq,index:0}),sessionId:e.session_id,executionId:e.id,...mark(e),state:e.state,startedAt:ms(e.started_at),endedAt:ms(e.ended_at)});
 const question=(q:any)=>({kind:'question',key:keyText({seq:Number(q.seq),index:0}),executionId:q.execution_id,questionId:q.id,text:q.text,options:q.options??[],state:q.state,answer:q.answer??undefined,answeredBy:q.answered_by??undefined,askedAt:ms(q.asked_at),answeredAt:ms(q.answered_at)});
 // Row sources other than execution rows, one index range scan per member execution and kind. Events without blocks
 // (lifecycle, executor bookkeeping) and stream chunks other than a stream's first are skipped in SQL.
 // ponytail: the skip filters rows after the index scan; a partial index on (executionId, seq) of block-bearing events if measured slow.
 const ROW=`jsonb_build_object('id',id,'executionId',data->>'executionId','type',data->>'type','text',data->'text','blocks',data->'data'->'fleetNormalized'->'blocks','deltaOf',data->'data'->'fleetNormalized'->'deltaOf','closes',data->'data'->'fleetNormalized'->'closes')`;
 const rows=async(ids:string[],dir:'<'|'>',from:string,limit:number,extra='',params:any[]=[])=>{const desc=dir==='<';
  return (await query(`SELECT seq,src,v FROM (
    SELECT d.seq,d.src,d.v FROM unnest($1::text[]) m(id),unnest(ARRAY['message','event','event-delta']) k(kind),
     LATERAL (SELECT seq,kind AS src,${ROW} AS v FROM document WHERE document.kind=k.kind AND data->>'executionId'=m.id AND seq ${dir}= $2::bigint
      AND (document.kind='message' OR (document.kind='event' AND (data->>'type'='execution.ended' OR jsonb_array_length(coalesce(data->'data'->'fleetNormalized'->'blocks','[]'::jsonb))>0)) OR (document.kind='event-delta' AND data->'data'->'fleetNormalized'->>'opens'='true'))${extra}
      ORDER BY seq ${desc?'DESC':'ASC'} LIMIT $3) d
    UNION ALL SELECT seq,'question',to_jsonb(q)-'data' FROM question q WHERE execution_id=ANY($1) AND seq ${dir}= $2::bigint${extra?' AND false':''}
    UNION ALL SELECT seq,'execution',jsonb_build_object('id',id) FROM execution WHERE id=ANY($1) AND seq ${dir}= $2::bigint${extra?' AND false':''}
   ) x ORDER BY seq ${desc?'DESC':'ASC'} LIMIT $3`,[ids,from,limit,...params])).map(r=>({seq:Number(r.seq),src:r.src,v:r.v}))};
 // A stream (event-delta rows of one deltaOf) is one entry at its first chunk's key while no event closes it: open while its
 // execution runs, cut off once it ended. A closed stream is the final block on its closing event.
 async function stream(executionId:string,id:string){
  if((await query("SELECT 1 AS x FROM document WHERE kind='event' AND data->>'executionId'=$1 AND data->'data'->'fleetNormalized'->'closes' ? $2 LIMIT 1",[executionId,id])).length)return null;
  const parts=await query("SELECT data->'data'->'fleetNormalized'->'blocks' AS b FROM document WHERE kind='event-delta' AND data->>'executionId'=$1 AND data->'data'->'fleetNormalized'->>'deltaOf'=$2 ORDER BY seq",[executionId,id]);
  return parts.map(p=>(p.b??[]).map((x:any)=>x.markdown??'').join('')).join('')}
 // Blocks of one row, in index order.
 async function expand(r:{seq:number,src:string,v:any},m:Map<string,any>):Promise<any[]>{const v=r.v,e=m.get(v.executionId??v.execution_id),at=(index:number)=>keyText({seq:r.seq,index});
  if(r.src==='question')return [question({...v,seq:r.seq})];
  if(r.src==='message')return typeof v.text==='string'?[{kind:'text',key:at(0),executionId:v.executionId,role:'operator',markdown:v.text,plain:v.text}]:[];
  if(r.src==='event-delta'){const text=await stream(v.executionId,v.deltaOf);if(text===null)return [];const kind=String(v.deltaOf).startsWith('reasoning:')?'reasoning':'text';
   return [{kind,key:at(0),executionId:v.executionId,...(kind==='text'?{role:'agent',...mark(e)}:{}),markdown:text,plain:text,stream:TERMINAL.has(e?.state)?'cut-off':'open'}]}
  const own=(Array.isArray(v.blocks)?v.blocks:[]).map((b:any,i:number)=>({...b,key:at(i),executionId:v.executionId,...(b.kind==='text'?{role:b.role??'agent',...mark(e)}:{}),...(b.kind==='text'||b.kind==='reasoning'?{plain:b.markdown??''}:{})}));
  if(v.type==='execution.ended'){const list=(await deps.artifacts(v.executionId)).filter((a:any)=>a.state===undefined||a.state==='stored').map((a:any)=>({id:a.artifactId??a.id,name:a.path??a.name,mediaType:a.mediaType,size:Number(a.size)||0}));
   // The delivery of a session shows on the result of its newest ended run, and updates there in place (OR-23).
   const sid=e?.session_id,latest=sid&&deps.stage?(await query("SELECT id FROM execution WHERE session_id=$1 AND kind='agent_run' AND ended_at IS NOT NULL ORDER BY seq DESC LIMIT 1",[sid]))[0]?.id:null;
   const stage=latest===v.executionId?await deps.stage!(sid):null;
   if(list.length||stage)own.push({kind:'result',key:at(own.length),executionId:v.executionId,artifactIds:list.map((a:any)=>a.id),artifacts:list,...(stage?{stage}:{})})}
  return own}
 // Up to limit blocks strictly before (dir '<') or after ('>') the key, nearest first. Rows can hold no block (an ended
 // execution without results, a closed stream), so pages are read until enough blocks or no rows remain.
 async function side(m:Map<string,any>,dir:'<'|'>',from:Key|null,limit:number,inclusive=false){const ids=[...m.keys()],out:any[]=[],seen=new Set<number>();
  let cursor=from?String(from.seq):dir==='<'?MAX:'0';
  const keep=(b:any)=>{if(!from)return true;const c=cmp(parseKey(b.key),from);return dir==='<'?c<0:inclusive?c>=0:c>0};
  for(;;){const batch=await rows(ids,dir,cursor,limit+1);
   for(const r of batch){if(seen.has(r.seq))continue;seen.add(r.seq);const bs=r.src==='execution'?[boundary(m.get(r.v.id))]:await expand(r,m);
    for(const b of dir==='<'?bs.reverse():bs)if(keep(b))out.push(b);if(out.length>limit)break}
   if(out.length>limit||batch.length<=limit)break;cursor=String(batch.at(-1)!.seq)}
  const more=out.length>limit;out.length=Math.min(out.length,limit);return {blocks:dir==='<'?out.reverse():out,more}}
 // Entry count for "84 of 1,240" (OR-11): blocks of events, messages, questions, boundaries, results and open streams.
 async function total(m:Map<string,any>){const ids=[...m.keys()];
  const [r]=await query(`SELECT
    (SELECT count(*) FROM document WHERE kind='message' AND data->>'executionId'=ANY($1))
   +(SELECT coalesce(sum(jsonb_array_length(coalesce(data->'data'->'fleetNormalized'->'blocks','[]'::jsonb))),0) FROM document WHERE kind='event' AND data->>'executionId'=ANY($1))
   +(SELECT count(*) FROM question WHERE execution_id=ANY($1)) AS n`,[ids]);
  let n=Number(r.n)+m.size;
  for(const r of await query("SELECT data->>'executionId' AS e FROM document WHERE kind='event' AND data->>'type'='execution.ended' AND data->>'executionId'=ANY($1)",[ids]))if((await deps.artifacts(r.e)).some((a:any)=>a.state===undefined||a.state==='stored'))n++;
  for(const s of await query("SELECT data->>'executionId' AS e,data->'data'->'fleetNormalized'->>'deltaOf' AS id FROM document WHERE kind='event-delta' AND data->>'executionId'=ANY($1) AND data->'data'->'fleetNormalized'->>'opens'='true'",[ids]))if(await stream(s.e,s.id)!==null)n++;
  return n}
 const pageLimit=(v:any)=>Number.isSafeInteger(v)&&v>0?v:fail(400,'Expected positive integer limit');
 // thread_entries (design "Read path"): newest page, before, after or around a block key; one snapshot with its feed cursor.
 // resolve: a session key opens the thread the session shows (its first task's), returned as `key` (one round trip).
 async function entries(b:any){let key=threadKey(b);if(b?.resolve===true&&'session' in key)key=await keyOf(key.session);const limit=pageLimit(b?.limit),m=await members(key);
  const given=['before','after','around'].filter(k=>b?.[k]!==undefined);if(given.length>1)fail(400,'Expected one of before, after, around');
  let entries:any[],hasBefore=false,hasAfter=false,withTotal=false;
  if(b?.before!==undefined){const r=await side(m,'<',parseKey(b.before),limit);entries=r.blocks;hasBefore=r.more;hasAfter=true}
  else if(b?.after!==undefined){const r=await side(m,'>',parseKey(b.after),limit);entries=r.blocks;hasAfter=r.more;hasBefore=true}
  else if(b?.around!==undefined){const k=parseKey(b.around),l=await side(m,'<',k,Math.floor(limit/2)),r=await side(m,'>',k,limit-Math.floor(limit/2),true);
   entries=[...l.blocks,...r.blocks];hasBefore=l.more||(Math.floor(limit/2)===0&&(await side(m,'<',k,1)).blocks.length>0);hasAfter=r.more;withTotal=true}
  else{const r=await side(m,'<',null,limit);entries=r.blocks;hasBefore=r.more;withTotal=true}
  return {key,entries,first:entries[0]?.key??null,last:entries.at(-1)?.key??null,hasBefore,hasAfter,sessionIds:[...new Set([...m.values()].map(e=>e.session_id).filter(Boolean))],...(withTotal?{total:await total(m)}:{}),feedCursor:await deps.cursor()}}
 // thread_blocks: the blocks of rows named by fed changes (change kind and id), when they belong to the thread.
 async function blocks(b:any){const key=threadKey(b),m=await members(key),ids=[...m.keys()];
  const named=Array.isArray(b?.rows)?b.rows.filter((r:any)=>typeof r?.id==='string'&&typeof r?.kind==='string'):fail(400,'Expected rows [{kind,id}]');
  const out:any[]=[],removed:string[]=[];
  for(const c of named){
   if(c.kind==='execution'){const e=m.get(c.id);if(e)out.push(boundary(e));continue}
   if(c.kind==='question'){for(const q of await query('SELECT * FROM question WHERE id=$1 AND execution_id=ANY($2)',[c.id,ids]))out.push(question(q));continue}
   // A stored artifact updates the result block of its execution's end.
   if(c.kind==='artifact'){const e=(await query("SELECT execution_id AS e FROM artifact WHERE id=$1 UNION ALL SELECT data->>'executionId' FROM document WHERE kind='artifact' AND id=$1",[c.id]))[0]?.e;
    if(e&&m.has(e))for(const d of await query(`SELECT seq,kind AS src,${ROW} AS v FROM document WHERE id=$1 AND kind='event'`,['ended:'+e]))out.push(...await expand({seq:Number(d.seq),src:d.src,v:d.v},m));continue}
   if(c.kind!=='event')continue;
   for(const d of await query(`SELECT seq,kind AS src,${ROW} AS v FROM document WHERE id=$1 AND kind IN ('event','message','event-delta') AND data->>'executionId'=ANY($2)`,[c.id,ids])){
    // A chunk names its stream; the stream's first chunk holds its key.
    if(d.src==='event-delta'){const first=(await query(`SELECT seq,kind AS src,${ROW} AS v FROM document WHERE kind='event-delta' AND data->>'executionId'=$1 AND data->'data'->'fleetNormalized'->>'deltaOf'=$2 AND data->'data'->'fleetNormalized'->>'opens'='true'`,[d.v.executionId,d.v.deltaOf]))[0];
     if(first)out.push(...await expand({seq:Number(first.seq),src:first.src,v:first.v},m));continue}
    // A closing event replaces the entries of the streams it closes.
    for(const id of Array.isArray(d.v.closes)?d.v.closes:[])for(const o of await query("SELECT seq FROM document WHERE kind='event-delta' AND data->>'executionId'=$1 AND data->'data'->'fleetNormalized'->>'deltaOf'=$2 AND data->'data'->'fleetNormalized'->>'opens'='true'",[d.v.executionId,id]))removed.push(keyText({seq:Number(o.seq),index:0}));
    out.push(...await expand({seq:Number(d.seq),src:d.src,v:d.v},m))}}
  const seen=new Map(out.map(x=>[x.key,x]));return {removed,entries:[...seen.values()].sort((x,y)=>cmp(parseKey(x.key),parseKey(y.key)))}}
 // Plain text a search matches, per block kind.
 const searchable=(b:any)=>b.kind==='text'||b.kind==='reasoning'?b.plain??b.markdown??'':b.kind==='tool'?[b.name,JSON.stringify(b.input??''),JSON.stringify(b.output??'')].join(' '):b.kind==='question'?[b.text,b.answer??''].join(' '):'';
 // thread_search (design "Search"): newest first, ILIKE over stored block text; a hit opens with thread_entries around.
 async function search(b:any){const key=threadKey(b),limit=pageLimit(b?.limit),q=typeof b?.q==='string'&&b.q.trim()?b.q.trim():fail(400,'Expected q');
  const before=b?.before!==undefined?parseKey(b.before):null,m=await members(key),ids=[...m.keys()],needle=q.toLowerCase();
  const like='%'+q.replace(/[\\%_]/g,(c:string)=>'\\'+c)+'%',hits:any[]=[];let cursor=before?String(before.seq):MAX;
  // The SQL test is coarse (unescaped block text and the JSON of tool input and output); each block is checked again in JS.
  const extra=` AND document.kind<>'event-delta' AND (CASE WHEN document.kind='message' THEN data->>'text' ELSE (SELECT string_agg(concat_ws(' ',b->>'markdown',b->>'name',(b->'input')::text,(b->'output')::text),' ') FROM jsonb_array_elements(coalesce(data->'data'->'fleetNormalized'->'blocks','[]'::jsonb)) b) END) ILIKE $4`;
  for(;;){const batch=(await rows(ids,'<',cursor,limit+1,extra,[like]));
   const qs=await query("SELECT * FROM question WHERE execution_id=ANY($1) AND seq<=$2::bigint AND (text ILIKE $3 OR coalesce(answer,'') ILIKE $3) ORDER BY seq DESC LIMIT $4",[ids,cursor,like,limit+1]);
   const lo=batch.length>limit?batch.at(-1)!.seq:-1,all=[...batch.filter(r=>r.src!=='execution'),...qs.filter(x=>Number(x.seq)>=lo).map(x=>({seq:Number(x.seq),src:'question',v:x}))].sort((a,b)=>b.seq-a.seq);
   for(const r of all)for(const x of (await expand(r,m)).reverse()){if(before&&cmp(parseKey(x.key),before)>=0)continue;const t=searchable(x),i=t.toLowerCase().indexOf(needle);if(i<0)continue;
    if(!hits.some(h=>h.key===x.key))hits.push({key:x.key,snippet:(i>40?'…':'')+t.slice(Math.max(0,i-40),i+q.length+80)})}
   if(hits.length>limit||batch.length<=limit)break;cursor=String(lo)}
  return {hits:hits.slice(0,limit),hasMore:hits.length>limit}}
 // One markdown builder (design "Copy"). from/to cut text and reasoning blocks by plain offsets; other blocks are whole.
 const fence=(v:any)=>{const s=typeof v==='string'?v:JSON.stringify(v,null,1)??'';const ticks='`'.repeat(Math.max(3,...[...s.matchAll(/`+/g)].map(x=>x[0].length+1)));return ticks+'\n'+s+'\n'+ticks};
 function markdown(b:any,from?:{key:string,offset:number},to?:{key:string,offset:number}){
  const cut=(s:string)=>s.slice(from&&from.key===b.key?from.offset:0,to&&to.key===b.key?to.offset:undefined);
  switch(b.kind){
   case 'text':return `**${b.role==='operator'?'You':'Agent'+(b.model?` (${b.model})`:'')}:**\n\n${cut(b.plain??b.markdown??'')}\n`;
   case 'reasoning':return `*Reasoning:*\n\n${cut(b.plain??b.markdown??'')}\n`;
   case 'tool':return `**Tool** \`${String(b.name).replace(/`/g,'')}\` (${b.state})\n\n${b.input!=null?fence(b.input)+'\n':''}${b.output!=null?fence(b.output)+'\n':''}`;
   case 'question':return `**Question:** ${b.text}${b.answer?`\n\n**Answer:** ${b.answer}`:''}\n`;
   case 'result':return `**Result:** ${b.artifactIds.join(', ')}\n`;
   case 'boundary':return `---\n*Execution ${String(b.executionId).slice(0,8)} · ${[b.harness,b.model,b.effort].filter(Boolean).join(' ')} · ${b.state}*\n`;
   default:return ''}}
 // Pages of an export: each page is read in its own snapshot by the caller (read), so a long thread is never one read.
 async function* exportPages(b:any,read:<T>(fn:()=>Promise<T>)=>Promise<T>,page=500){const key=threadKey(b);
  const from=b?.from?{key:keyText(parseKey(b.from.key)),offset:Number(b.from.offset)||0}:undefined,to=b?.to?{key:keyText(parseKey(b.to.key)),offset:Number(b.to.offset)||0}:undefined;
  let after:Key|null=from?parseKey(from.key):null,first=true;
  for(;;){const r=await read(async()=>{const m=await members(key);return side(m,'>',after,page,first&&!!from)});first=false;
   const parts:string[]=[];let done=!r.more;
   for(const x of r.blocks){if(to&&cmp(parseKey(x.key),parseKey(to.key))>0){done=true;break}parts.push(markdown(x,from,to))}
   if(parts.length)yield parts.join('\n')+'\n';if(done||!r.blocks.length)return;after=parseKey(r.blocks.at(-1).key)}}
 async function exportText(b:any,read:<T>(fn:()=>Promise<T>)=>Promise<T>){let s='';for await(const p of exportPages(b,read))s+=p;return {markdown:s}}
 // The agent's reply text of one execution: final text blocks plus cut-off streams, in order (delegation, handoff, delivery).
 async function replyText(executionId:string):Promise<string[]>{const e=(await query("SELECT id,session_id,harness,model,config_ref->>'effort' AS effort,state,started_at,ended_at,seq FROM execution WHERE id=$1",[executionId]))[0];if(!e)return [];
  const m=new Map([[e.id,{...e,seq:Number(e.seq)}]]),out:string[]=[];let after:Key|null=null;
  for(;;){const r=await side(m,'>',after,500);for(const b of r.blocks)if(b.kind==='text'&&b.role==='agent'&&b.markdown)out.push(b.markdown);if(!r.more)return out;after=parseKey(r.blocks.at(-1).key)}}
 // The thread a session shows (design "Thread scope", D2): the thread of the first task the session got, else its own.
 async function keyOf(sessionId:string):Promise<ThreadKey>{const t=(await query("SELECT data->>'taskId' AS task_id FROM document WHERE kind='session' AND id=$1 AND data ? 'taskId'",[sessionId]))[0]?.task_id??(await query('SELECT task_id FROM execution WHERE session_id=$1 AND task_id IS NOT NULL ORDER BY seq LIMIT 1',[sessionId]))[0]?.task_id;return t?{task:t}:{session:sessionId}}
 return {keyOf,entries,blocks,search,exportPages,exportText,replyText,threadKey,markdown};
}
export type Threads=ReturnType<typeof threads>;
// Reply text for readers that need no results or feed (delegation, context handoff).
export const replyText=(query:Query,executionId:string)=>threads({query,artifacts:async()=>[],cursor:async()=>0}).replyText(executionId);
