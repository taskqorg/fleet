// Live change feed for the operator app (docs/designs/fleet-operator-app.md §4, D1 and D5).
// One LISTEN per process wakes every open stream; each wake is debounced and read in its own short snapshot, so a stream never
// holds a transaction or a pool connection between wakes. The client resumes with ?cursor=; changes_since is the same read.
import type {FastifyInstance} from 'fastify';
import type {Store} from './store.js';

const PING_MS=25_000, DEBOUNCE_MS=50, PRUNE_MS=3_600_000;

export function registerStream(app:FastifyInstance,store:Store,config:{retentionDays:number}){
 type Open={write:(s:string)=>void,end:()=>void,cursor:number,busy:boolean,again:boolean};
 const open=new Set<Open>();
 let closed=false,unlisten:(()=>Promise<void>)|null=null,timer:NodeJS.Timeout|null=null,ping:NodeJS.Timeout|null=null,prune:NodeJS.Timeout|null=null;
 const send=(o:Open,event:string|null,data:any,id?:number)=>o.write(`${id!==undefined?`id: ${id}\n`:''}${event?`event: ${event}\n`:''}data: ${JSON.stringify(data)}\n\n`);
 // Reads everything visible after the stream's cursor; a wake during a read is remembered and read again afterwards.
 const drain=async(o:Open)=>{if(o.busy){o.again=true;return}o.busy=true;
  try{do{o.again=false;for(;;){const r=await store.read(()=>store.changes.since(o.cursor,1000));
    if(r.resync){o.cursor=r.cursor;send(o,'resync',{cursor:r.cursor});break}
    for(const c of r.changes)send(o,null,c,c.seq);o.cursor=r.cursor;if(r.changes.length<1000)break}}while(o.again)}
  catch(e:any){send(o,'error',{message:String(e?.message??e)})}finally{o.busy=false}};
 const wake=()=>{if(timer||closed)return;timer=setTimeout(()=>{timer=null;for(const o of open)drain(o)},DEBOUNCE_MS)};

 app.addHook('onReady',async()=>{
  unlisten=await store.listen('fleet_change',wake);
  ping=setInterval(()=>{for(const o of open)o.write(': ping\n\n')},PING_MS);ping.unref();
  // Retention is the operator's setting (FLEET_CHANGE_LOG_RETENTION_DAYS); no built-in cap.
  prune=setInterval(()=>{store.changes.prune(config.retentionDays).catch(()=>{})},PRUNE_MS);prune.unref();
 });
 // Streams are hijacked responses: end them before the server waits for open connections to finish.
 app.addHook('preClose',async()=>{closed=true;for(const o of open){o.write('event: close\ndata: {}\n\n');o.end()}open.clear()});
 app.addHook('onClose',async()=>{for(const t of [timer,ping,prune])if(t)clearTimeout(t as any);closed=true;await unlisten?.().catch(()=>{})});

 app.get('/api/stream',async(req:any,reply)=>{
  const raw=req.query.cursor;const cursor=raw===undefined||raw===''?null:Number(raw);
  if(cursor!==null&&(!Number.isSafeInteger(cursor)||cursor<0))return reply.code(400).send({error:'Invalid cursor'});
  reply.hijack();
  const res=reply.raw;
  // Register the close handler before any await, so a client that leaves early is never added or drained afterwards.
  let gone=closed;req.raw.on('close',()=>{gone=true;open.delete(o)});
  res.writeHead(200,{'Content-Type':'text/event-stream; charset=utf-8','Cache-Control':'no-cache, no-transform','X-Accel-Buffering':'no','Connection':'keep-alive','X-Content-Type-Options':'nosniff'});
  const o:Open={write:s=>{if(!gone)res.write(s)},end:()=>res.end(),cursor:0,busy:false,again:false};
  try{
   if(cursor===null){const head=await store.read(()=>store.changes.since(null));o.cursor=head.cursor;send(o,'ready',{cursor:o.cursor})}
   else{o.cursor=cursor;send(o,'ready',{cursor});await drain(o)}
  }catch{res.end();return}
  if(!gone&&!closed)open.add(o);else res.end();
 });
 return {wake,openStreams:()=>open.size};
}
