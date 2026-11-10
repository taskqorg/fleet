// Starts the bundled coordinator from a copy of the release layout with no node_modules (docs/designs/fleet-toolchain.md D4),
// through a "current" symlink as on PDS18, against the PDS18 roles: migrations as the owner fleet, the store as fleet_app
// (pg-test.ts recreateFleetDatabase). Checks the operator app, one authenticated op and the migrate-sqlite CLI. A missing asset
// or a dependency the bundle did not include fails here, not on PDS18.
//   FLEET_TEST_PG=postgresql://admin:pass@host:port node tests/coordinator/release-smoke.ts   (after pnpm run build)
import {spawn,spawnSync} from 'node:child_process';import {cpSync,mkdtempSync,existsSync,rmSync,symlinkSync} from 'node:fs';import {createServer} from 'node:net';import {tmpdir} from 'node:os';import assert from 'node:assert/strict';
import {PG,recreateFleetDatabase,appUrl} from './pg-test.ts';
import {peopleFile,OWNER} from './test-app.ts';
if(!PG)throw Error('FLEET_TEST_PG required');
const freePort=()=>new Promise<number>((ok,no)=>{const s=createServer().once('error',no).listen(0,'127.0.0.1',()=>{const p=(s.address() as any).port;s.close(()=>ok(p))})});
const root=mkdtempSync(tmpdir()+'/fleet-release-'),release=root+'/release-smoke',current=root+'/current';
cpSync('package.json',release+'/package.json');cpSync('dist',release+'/dist',{recursive:true});symlinkSync(release,current);
assert.equal(existsSync(release+'/node_modules'),false);
const owner=await recreateFleetDatabase(),port=await freePort(),secret='smoke-browser',origin='http://127.0.0.1:'+port;
const env={PATH:process.env.PATH,PORT:String(port),FLEET_DATABASE_URL:appUrl(),FLEET_MIGRATION_DATABASE_URL:owner,FLEET_BROWSER_SECRET:secret,FLEET_PEOPLE_FILE:peopleFile(),FLEET_BROWSER_ORIGIN:origin};
const child=spawn(process.execPath,[current+'/dist/packages/coordinator/server.js'],{cwd:current,env,stdio:['ignore','inherit','inherit']});
const exited=new Promise<void>(r=>child.once('exit',()=>r()));
try{
 let page:Response|undefined;
 for(let i=0;i<100&&!page;i++){
  if(child.exitCode!==null||child.signalCode!==null)throw Error(`coordinator exited (${child.exitCode??child.signalCode})`);
  try{page=await fetch(origin+'/overview',{signal:AbortSignal.timeout(2000)})}catch{await new Promise(r=>setTimeout(r,200))}}
 assert.equal(page?.status,200);assert.match(await page!.text(),/<script/);
 const op=await fetch(origin+'/api/profiles',{headers:{authorization:'Bearer '+secret,'x-fleet-person':OWNER},signal:AbortSignal.timeout(5000)});assert.equal(op.status,200);
 const body=await op.json();assert.ok(body.profiles?.length>0,'profiles from dist/config/agents: '+JSON.stringify(body));
 // The CLI must run through the symlink: no arguments prints its usage and exits 2.
 const cli=spawnSync(process.execPath,[current+'/dist/packages/coordinator/migrate-sqlite.js'],{cwd:current,encoding:'utf8',timeout:30000});
 assert.equal(cli.status,2,cli.stderr);assert.match(cli.stderr,/usage: migrate-sqlite/);
 console.log('release smoke passed');
}finally{child.kill();await exited;rmSync(root,{recursive:true,force:true})}
