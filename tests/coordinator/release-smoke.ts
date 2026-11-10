// Starts the bundled coordinator from a copy of the release layout with no node_modules (docs/designs/fleet-toolchain.md D4),
// against FLEET_SMOKE_PG (a Postgres URL), and checks the operator app and one authenticated op. A missing asset or a
// dependency the bundle did not include fails here, not on PDS18.
//   node tests/coordinator/release-smoke.ts   (after pnpm run build)
import {spawn} from 'node:child_process';import {cpSync,mkdtempSync,existsSync} from 'node:fs';import {tmpdir} from 'node:os';import assert from 'node:assert/strict';
const pg=process.env.FLEET_SMOKE_PG;if(!pg)throw Error('FLEET_SMOKE_PG required');
const release=mkdtempSync(tmpdir()+'/fleet-release-');cpSync('package.json',release+'/package.json');cpSync('dist',release+'/dist',{recursive:true});
assert.equal(existsSync(release+'/node_modules'),false);
const port='19473',secret='smoke-browser',env={PATH:process.env.PATH,PORT:port,FLEET_DATABASE_URL:pg,FLEET_BROWSER_SECRET:secret,FLEET_MACHINE_SECRET:'smoke-machine',FLEET_BROWSER_ORIGIN:'http://127.0.0.1:'+port};
const child=spawn(process.execPath,['--enable-source-maps','dist/packages/coordinator/server.js'],{cwd:release,env,stdio:['ignore','inherit','inherit']});
try{
 let page:Response|undefined;for(let i=0;i<100&&!page;i++){try{page=await fetch(`http://127.0.0.1:${port}/overview`)}catch{await new Promise(r=>setTimeout(r,200))}if(child.exitCode!==null)throw Error('coordinator exited '+child.exitCode)}
 assert.equal(page?.status,200);assert.match(await page!.text(),/<script/);
 const op=await fetch(`http://127.0.0.1:${port}/api/profiles`,{headers:{authorization:'Bearer '+secret}});assert.equal(op.status,200);const body=await op.json();assert.ok(body.profiles?.length>0,'profiles from dist/config/agents: '+JSON.stringify(body));
 console.log('release smoke passed');
}finally{child.kill()}
