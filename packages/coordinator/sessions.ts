// Sessions list for the operator app's Conversations view (docs/designs/fleet-operator-app.md §6). One WHERE builder serves the
// page, the counts and membership, so the client never decides which sessions belong to a list.
type Raw={query:(sql:string,params?:any[])=>Promise<any[]>};
const fail=(code:number,message:string)=>{throw Object.assign(Error(message),{statusCode:code})};
// Session state is the state of its latest execution; a session without one is 'idle'.
const STATES=['idle','preparing','running','waiting','succeeded','failed','cancelled'];
export type SessionFilter={state?:string[],search?:string,archived?:boolean};

const base=`WITH x AS (SELECT d.id, d.data, d.project_id,
 coalesce((SELECT e.state::text FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1),'idle') AS state,
 (SELECT e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) AS harness,
 (SELECT m.data->>'text' FROM document m WHERE m.kind='message' AND m.data->>'sessionId'=d.id ORDER BY m.seq LIMIT 1) AS first_message,
 greatest(d.seq,coalesce((SELECT max(e.seq) FROM execution e WHERE e.session_id=d.id),0),coalesce((SELECT max(m.seq) FROM document m WHERE m.kind IN ('message','event') AND m.data->>'sessionId'=d.id),0)) AS last_seq,
 (SELECT count(*) FROM question q WHERE q.session_id=d.id AND q.state='open') AS open_questions
 FROM document d WHERE d.kind='session')`;

export function sessionModel(raw:Raw){
 const where=(f:SessionFilter,p:any[],withState=true)=>{const c:string[]=[f.archived?"x.data ? 'archivedAt'":"NOT (x.data ? 'archivedAt')"];
  if(withState&&Array.isArray(f.state)&&f.state.length){if(!f.state.every(s=>STATES.includes(s)))fail(400,'Invalid state');p.push(f.state);c.push(`x.state=ANY($${p.length})`)}
  if(typeof f.search==='string'&&f.search.trim()){p.push('%'+f.search.trim().toLowerCase().replace(/[%_\\]/g,m=>'\\'+m)+'%');c.push(`(lower(coalesce(x.first_message,'')) LIKE $${p.length} OR lower(x.id) LIKE $${p.length})`)}
  return c};
 const row=(x:any)=>({id:x.id,title:(x.first_message??'').slice(0,200)||null,state:x.state,harness:x.harness??x.data?.agentConfig?.harness??null,profileId:x.data?.agentConfig?.id??null,projectId:x.project_id,
  createdAt:x.data?.createdAt??null,openQuestions:Number(x.open_questions),archivedAt:x.data?.archivedAt??null,sortKey:[-Number(x.last_seq),x.id]});
 async function page(b:{filter?:SessionFilter,limit?:number,after?:[number,string]}={}){
  const f=b.filter??{},limit=Math.max(1,Math.min(500,Math.floor(b.limit??100))),p:any[]=[],c=where(f,p);
  if(b.after){if(!Array.isArray(b.after)||b.after.length!==2)fail(400,'Invalid after');p.push(Number(b.after[0]),String(b.after[1]));c.push(`(-x.last_seq,x.id) > ($${p.length-1},$${p.length})`)}
  const rows=await raw.query(`${base} SELECT * FROM x WHERE ${c.join(' AND ')} ORDER BY -x.last_seq,x.id LIMIT ${limit+1}`,p);
  const cp:any[]=[],cc=where(f,cp,false),counts=Object.fromEntries(STATES.map(s=>[s,0]));
  for(const r of await raw.query(`${base} SELECT x.state,count(*) AS n FROM x WHERE ${cc.join(' AND ')} GROUP BY 1`,cp))counts[r.state]=Number(r.n);
  const page=rows.slice(0,limit).map(row);
  return {sessions:page,nextAfter:rows.length>limit?page.at(-1)!.sortKey:null,counts,total:Object.values(counts).reduce((a:number,n:any)=>a+n,0)}}
 async function membership(f:SessionFilter,ids:string[]){const p:any[]=[],c=where(f,p);p.push(ids.filter(x=>typeof x==='string').slice(0,1000));c.push(`x.id=ANY($${p.length})`);
  return (await raw.query(`${base} SELECT * FROM x WHERE ${c.join(' AND ')}`,p)).map(row)}
 return {page,membership,states:STATES};
}
