// Lab examples outside the coordinator (docs/designs/fleet-lab.md, Stage 1).
//
//   pnpm exec tsx scripts/lab-examples.ts check FILE…    run each example's script checks with no agent
//   pnpm exec tsx scripts/lab-examples.ts import FILE…   send the examples to the coordinator as approved examples
//
// FILE is a JSON file holding one example or an array of them; "-" reads standard input. check validates each example
// against the registry, then runs its checks twice in a fresh directory: on inputs plus references (every check must pass)
// and on inputs alone (at least one check must fail, so a check that passes on untouched input is reported). A check is a
// shell command run in that directory with FLEET_OUTPUT_DIR set to it, the same contract a Stage 3 script evaluator uses.
// import posts to $FLEET_URL/api/lab/examples with the operator credential from $FLEET_TOKEN; --project ID sets the project.
import {execFileSync} from 'node:child_process';
import {mkdirSync,mkdtempSync,readFileSync,rmSync,writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {dirname,join,resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseExample,useCaseIds,type Example,type ExampleFile} from '../packages/coordinator/lab/examples.js';

export function readExamples(files:string[]):any[]{return files.flatMap(f=>{const v=JSON.parse(f==='-'?readFileSync(0,'utf8'):readFileSync(f,'utf8'));return Array.isArray(v)?v:[v]})}

// Runs every check in a directory holding the given files; returns the IDs of failed checks with their last output line.
export function runChecks(e:Example,files:ExampleFile[]){const dir=mkdtempSync(join(tmpdir(),'lab-example-'));
 try{for(const f of files){mkdirSync(dirname(join(dir,f.path)),{recursive:true});writeFileSync(join(dir,f.path),f.text)}
  const failed:{id:string,output:string}[]=[];
  for(const c of e.checks){try{execFileSync('sh',['-c',c.run],{cwd:dir,env:{...process.env,FLEET_TOKEN:undefined,FLEET_OUTPUT_DIR:dir},stdio:['ignore','pipe','pipe'],encoding:'utf8'})}
   catch(x:any){failed.push({id:c.id,output:String(x.stderr||x.stdout||x.message).trim().split('\n').filter(Boolean).pop()??''})}}
  return failed}finally{rmSync(dir,{recursive:true,force:true})}}

// Problems of one example: invalid shape, a check failing on the reference, or every check passing untouched input.
export function checkExample(raw:any,useCases:Set<string>):string[]{let e:Example;try{e=parseExample(raw,useCases)}catch(x:any){return [x.message]}
 if(!e.checks.length)return [];// rated by the owner or a judge only (assetq cases)
 const merged=new Map([...e.inputs,...e.references].map(f=>[f.path,f]));
 const problems=runChecks(e,[...merged.values()]).map(f=>`check ${f.id} fails on the reference: ${f.output}`);
 if(!runChecks(e,e.inputs).length)problems.push('every check passes on the inputs alone');
 return problems}

async function main(argv:string[]){const [cmd,...rest]=argv,root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
 const projectAt=rest.indexOf('--project'),projectId=projectAt>=0?rest[projectAt+1]:undefined,files=rest.filter((_,i)=>projectAt<0||i!==projectAt&&i!==projectAt+1);
 const examples=readExamples(files.length?files:['-']);
 if(cmd==='check'){const useCases=useCaseIds(join(root,'config/lab'));let bad=0;
  for(const x of examples){const p=checkExample(x,useCases);console.log(`${x?.id??'?'}: ${p.length?'FAIL\n  '+p.join('\n  '):Array.isArray(x?.checks)&&!x.checks.length?'no script checks (owner or judge rating)':'ok'}`);if(p.length)bad++}
  console.log(`${examples.length-bad} of ${examples.length} examples pass their checks with no agent`);if(bad)process.exitCode=1;return}
 if(cmd==='import'){const url=process.env.FLEET_URL,token=process.env.FLEET_TOKEN;if(!url||!token)throw Error('FLEET_URL and FLEET_TOKEN required');
  const r=await fetch(url.replace(/\/$/,'')+'/api/lab/examples',{method:'POST',headers:{'content-type':'application/json',authorization:'Bearer '+token},body:JSON.stringify({examples,projectId,approved:true})});
  const body=await r.text();if(!r.ok)throw Error(`import failed: ${r.status} ${body}`);
  for(const x of JSON.parse(body).examples)console.log(`${x.id}@${x.version}: ${x.created?'created':'already stored'} ${x.artifactId}`);return}
 throw Error('usage: lab-examples.ts check|import FILE… [--project ID]')}

if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url))main(process.argv.slice(2)).catch(e=>{console.error(e.message);process.exitCode=1});
