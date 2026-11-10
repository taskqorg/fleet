// Converts assetq (Pipe) dataset revisions into Lab examples (docs/designs/fleet-lab.md, Stage 1 item 18; Open Question Q2).
//
//   assetq datasets > datasets.json            on a host with the Pipe CLI (owner credential stays there)
//   npx tsx scripts/lab-import-assetq.ts datasets.json > examples.json
//   npx tsx scripts/lab-examples.ts import examples.json
//
// Input: the GET /v1/datasets reply, an array of {id, version, document: {operation, cases: [{id, text, attachments}]}, sha256}
// (or one such revision). Each case becomes one synthetic example of use case assetq:datasets. Nothing is lost: the case's
// text is the prompt, and the whole case with its dataset identity, operation and attachment object IDs is the input file
// assetq-case.json. Pipe judged these cases by owner scores only, so the examples carry no script checks.
import {readFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import type {Example} from '../packages/coordinator/lab/examples.js';

const part=(s:string)=>s.replace(/[^A-Za-z0-9._-]/g,'-');
export function convert(revisions:any):Example[]{const list=Array.isArray(revisions)?revisions:[revisions];
 return list.flatMap((r:any)=>{if(typeof r?.id!=='string'||!Number.isSafeInteger(r.version)||!Array.isArray(r.document?.cases))throw Error('Expected dataset revisions {id, version, document.cases}');
  return r.document.cases.map((c:any):Example=>({id:`assetq-${part(r.id)}-${part(c.id)}`.slice(0,100),use_case_id:'assetq:datasets',version:r.version,origin:'synthetic',fixture:null,prompt:c.text,
   inputs:[{path:'assetq-case.json',text:JSON.stringify({dataset:`${r.id}@${r.version}`,datasetSha256:r.sha256??null,operation:r.document.operation,case:c},null,1)+'\n'}],references:[],checks:[],split:'tune'}))})}

if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)){const f=process.argv[2]??'-';
 console.log(JSON.stringify(convert(JSON.parse(readFileSync(f==='-'?0:f,'utf8'))),null,1))}
