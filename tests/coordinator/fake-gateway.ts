// Pipelines against a fake gateway (fixtures/fake_connection.py): the client stand-in checks the run token in the snapshot
// and writes the spool line the coordinator ingests. Every <dir>/connections/<name> becomes an API key connection usable by
// all work.
import {readdirSync,existsSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
const fake=fileURLToPath(new URL('./fixtures/fake_connection.py',import.meta.url));
export const gatewayConfig=(dir:string)=>({gatewayDir:dir+'/gateway',gatewayUrl:'http://fake-gateway',connectionClient:['python3',fake,dir+'/connections'],connectionTickMs:0});
export async function seedConnections(app:any,dir:string){if(!dir)return;
 for(const name of existsSync(dir+'/connections')?readdirSync(dir+'/connections'):[])await app.store.tx(()=>app.store.put('api-connection',{id:'connection:'+name,name,provider:'tavily',holder:'owner@fleet.test',access:{people:[],projects:['*'],experiments:[]},intake:'all',tools:['web_search','web_extract','image_to_3d','job_status'],comment:'',createdAt:Date.now()}));
 await app.connections.writeSnapshot()}
// An HTTP fake of fleet-gateway for the real connection_call.py: run token checked against <gatewayDir>/snapshot.json, one
// spool line per authenticated call. answer(body, tool) gives [status, reply].
import {createServer} from 'node:http';
import {createHash,randomUUID} from 'node:crypto';
import {readFileSync,mkdirSync,appendFileSync} from 'node:fs';
export async function fakeGateway(gatewayDir:string,answer:(body:any,tool:string)=>[number,any]|Promise<[number,any]>){
 const seen:any[]=[];
 const server=createServer((req,res)=>{let input='';req.on('data',b=>input+=b);req.on('end',async()=>{
  const m=/^\/v1\/call\/([a-z0-9-]+)\/([a-z0-9_]+)$/.exec(req.url??''),sha=createHash('sha256').update(String(req.headers.authorization??'').replace(/^Bearer /,'')).digest('hex');
  let tokens:any[]=[];try{tokens=JSON.parse(readFileSync(gatewayDir+'/snapshot.json','utf8')).tokens}catch{}
  const run=tokens.find(t=>t.sha256===sha&&m&&t.connections.includes(m[1])),callId=randomUUID();
  res.setHeader('content-type','application/json');res.setHeader('x-fleet-call-id',callId);
  if(!m||!run){res.setHeader('x-fleet-error','unauthenticated');res.writeHead(401);res.end(JSON.stringify({error:'unauthenticated'}));return}
  const body=JSON.parse(input||'{}');seen.push({connection:m[1],tool:m[2],body,executionId:run.executionId});
  const [status,reply]=await answer(body,m[2]);if(status>=300)res.setHeader('x-fleet-error','provider-'+status);const credits=reply?.usage?.credits;
  mkdirSync(gatewayDir+'/state/spool',{recursive:true});
  appendFileSync(`${gatewayDir}/state/spool/${new Date().toISOString().slice(0,10)}.jsonl`,JSON.stringify({callId,executionId:run.executionId,tokenSha256:sha,connection:m[1],tool:m[2],status,
   outcome:status<300?'ok':'error',errorCode:status<300?null:reply?.error??'provider-'+status,usage:Number.isFinite(credits)?{unit:'credits',amount:credits}:null,startedAt:Date.now(),ms:1})+'\n');
  res.writeHead(status);res.end(typeof reply==='string'?reply:JSON.stringify(reply))})});
 await new Promise<void>(r=>server.listen(0,'127.0.0.1',r));
 return {url:`http://127.0.0.1:${(server.address() as any).port}`,seen,close:()=>new Promise<void>(r=>server.close(()=>r()))}}
