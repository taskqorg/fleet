// Connection client (docs/designs/fleet-lab.md Stage 2, item 25): hash pin, and identical call lines from both entry points.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {spawn} from 'node:child_process';
import {mkdtempSync,writeFileSync,chmodSync,readFileSync,existsSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';

const script=fileURLToPath(new URL('../../packages/coordinator/connection_call.py',import.meta.url));
const clientPath=fileURLToPath(new URL('./fixtures/pipe_client.py',import.meta.url));
async function lane(){let calls:any[]=[];const s=createServer((req,res)=>{let b='';req.on('data',d=>b+=d);req.on('end',()=>{calls.push({url:req.url,task:req.headers['x-pipe-task'],body:JSON.parse(b)});
 if(JSON.parse(b).query==='full'){res.writeHead(503,{'content-type':'application/json'});res.end(JSON.stringify({error:'queue-full',reason:'queue is full'}));return}
 res.setHeader('content-type','application/json');res.end(JSON.stringify({results:[{url:'https://e.com',content:'token-secret'}],usage:{credits:2}}))})});
 await new Promise<void>(r=>s.listen(0,'127.0.0.1',r));return {url:`http://127.0.0.1:${(s.address() as any).port}`,calls,close:()=>new Promise<void>(r=>s.close(()=>r()))}}
function record(dir:string,url:string,sha?:string){writeFileSync(dir+'/pipe.json',JSON.stringify({url,token:'token-secret'}));chmodSync(dir+'/pipe.json',0o600);
 writeFileSync(dir+'/connection.json',JSON.stringify({clientPath,clientSha256:sha??createHash('sha256').update(readFileSync(clientPath)).digest('hex'),pipeConfigPath:dir+'/pipe.json',tools:['web_search']}));chmodSync(dir+'/connection.json',0o600);return dir+'/connection.json'}
// Async: the lane answers from this process, so the client must not block the event loop.
const run=(args:string[],input:string)=>new Promise<string>(r=>{const c=spawn('python3',[script,...args]);let o='';c.stdout.on('data',d=>o+=d);c.on('close',()=>r(o));c.stdin.end(input)});
const call=async(rec:string,label:string,out:string,tool:string,args:any)=>JSON.parse(await run(['call',rec,label,out,tool],JSON.stringify(args)));
const lines=(out:string)=>existsSync(out+'/.connection-calls.jsonl')?readFileSync(out+'/.connection-calls.jsonl','utf8').split('\n').filter(Boolean).map(l=>JSON.parse(l)):[];

test('a client whose hash differs from clientSha256 is never loaded',async()=>{const l=await lane();try{const dir=mkdtempSync('/tmp/fleet-cc-'),rec=record(dir,l.url,'0'.repeat(64));
 const r=await call(rec,'step-1',dir+'/out','web_search',{query:'x'});assert.equal(r.outcome,'error');assert.equal(r.error_code,'client-hash-mismatch');assert.equal(l.calls.length,0);
 const mcp=await run(['mcp',rec,'step-1',dir+'/out'],JSON.stringify({jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'web_search',arguments:{query:'x'}}})+'\n');
 assert.match(JSON.parse(mcp).result.content[0].text,/client-hash-mismatch/);assert.equal(l.calls.length,0)}finally{await l.close()}});

test('CLI and MCP entry points send the label and write identical call lines; secrets are redacted; errors classified',async()=>{const l=await lane();try{const dir=mkdtempSync('/tmp/fleet-cc-'),rec=record(dir,l.url);
 const a=await call(rec,'step-1',dir+'/cli','web_search',{query:'x'});assert.equal(a.outcome,'ok');assert.deepEqual(a.usage,{credits:2});assert.equal(a.output.results[0].content,'[redacted]');
 const input=[{jsonrpc:'2.0',id:1,method:'initialize',params:{}},{jsonrpc:'2.0',id:2,method:'tools/list'},{jsonrpc:'2.0',id:3,method:'tools/call',params:{name:'web_search',arguments:{query:'x'}}}].map(m=>JSON.stringify(m)).join('\n')+'\n';
 const replies=(await run(['mcp',rec,'step-1',dir+'/mcp'],input)).split('\n').filter(Boolean).map(x=>JSON.parse(x));
 assert.deepEqual(replies[1].result.tools.map((t:any)=>t.name),['web_search']);assert.ok(!JSON.stringify(replies).includes('token-secret'));
 const strip=(x:any)=>{const {request_id,...rest}=x;return rest};
 assert.deepEqual(lines(dir+'/cli').map(strip),lines(dir+'/mcp').map(strip));assert.deepEqual(strip(lines(dir+'/cli')[0]),{tool:'web_search',label:'step-1',outcome:'ok',error_code:null,usage:{credits:2}});
 assert.deepEqual(l.calls.map(c=>c.task),['step-1','step-1']);
 const full=await call(rec,'step-2',dir+'/cli','web_search',{query:'full'});assert.deepEqual([full.outcome,full.error_code],['error','queue-full']);
 const denied=await call(rec,'step-2',dir+'/cli','web_extract',{urls:['x']});assert.equal(denied.error_code,'tool-not-granted');assert.equal(l.calls.length,3);
}finally{await l.close()}});
