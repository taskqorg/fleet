// Connection client (fleet-connections.md §5): both entry points call the gateway with the run token and give the same line.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';
import {fakeGateway} from './fake-gateway.ts';

const script=fileURLToPath(new URL('../../packages/coordinator/connection_call.py',import.meta.url));
const run=(args:string[],input:string,env:Record<string,string>)=>new Promise<string>(r=>{const c=spawn('python3',[script,...args],{env:{PATH:process.env.PATH!,...env}});let o='';c.stdout.on('data',d=>o+=d);c.on('close',()=>r(o));c.stdin.end(input)});
async function setup(){const dir=mkdtempSync('/tmp/fleet-cc-');mkdirSync(dir+'/gateway');
 writeFileSync(dir+'/gateway/snapshot.json',JSON.stringify({tokens:[{sha256:createHash('sha256').update('run-token').digest('hex'),executionId:'exec-1',connections:['tavily']}]}));
 const gw=await fakeGateway(dir+'/gateway',b=>b.query==='quota'?[429,{detail:'over'}]:[200,{results:[{url:'https://e.com'}],usage:{credits:2}}]);
 return {gw,env:{FLEET_RUN_TOKEN:'run-token',FLEET_GATEWAY_URL:gw.url}}}

test('CLI and MCP send the run token and give the same call line; refusals and provider replies are classified',async()=>{const {gw,env}=await setup();try{
 const a=JSON.parse(await run(['call','tavily','web_search'],JSON.stringify({query:'x'}),env));
 assert.deepEqual([a.outcome,a.usage,a.error_code],['ok',{credits:2},null]);assert.ok(a.callId);
 const spec={instructions:'use it',tools:[{name:'web_search',description:'d',inputSchema:{type:'object'}}]};
 const input=[{jsonrpc:'2.0',id:1,method:'initialize',params:{}},{jsonrpc:'2.0',id:2,method:'tools/list'},{jsonrpc:'2.0',id:3,method:'tools/call',params:{name:'web_search',arguments:{query:'x'}}},{jsonrpc:'2.0',id:4,method:'tools/call',params:{name:'web_extract',arguments:{}}}].map(m=>JSON.stringify(m)).join('\n')+'\n';
 const replies=(await run(['mcp','tavily',JSON.stringify(spec)],input,env)).split('\n').filter(Boolean).map(x=>JSON.parse(x));
 assert.equal(replies[0].result.instructions,'use it');assert.deepEqual(replies[1].result.tools.map((t:any)=>t.name),['web_search']);
 assert.equal(replies[2].result.isError,false);assert.deepEqual(JSON.parse(replies[2].result.content[0].text).usage,{credits:2});
 assert.match(replies[3].result.content[0].text,/tool-not-granted/);
 assert.deepEqual(gw.seen.map(s=>[s.tool,s.executionId]),[['web_search','exec-1'],['web_search','exec-1']]);
 const quota=JSON.parse(await run(['call','tavily','web_search'],JSON.stringify({query:'quota'}),env));assert.deepEqual([quota.outcome,quota.error_code],['error','http-429']);
 const refused=JSON.parse(await run(['call','tavily','web_search'],'{}',{...env,FLEET_RUN_TOKEN:'other'}));assert.deepEqual([refused.outcome,refused.error_code],['error','unauthenticated']);
 const none=JSON.parse(await run(['call','tavily','web_search'],'{}',{FLEET_GATEWAY_URL:gw.url}));assert.equal(none.error_code,'connection-unavailable');
 const file=mkdtempSync('/tmp/fleet-cc-tok-')+'/token';writeFileSync(file,'run-token\n',{mode:0o600});
 const fromFile=JSON.parse(await run(['call','tavily','web_search'],JSON.stringify({query:'x'}),{FLEET_GATEWAY_URL:gw.url,FLEET_TOKEN_FILE:file}));assert.equal(fromFile.outcome,'ok');
}finally{await gw.close()}});
