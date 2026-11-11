// Project export version 1 (contracts/lab-export.schema.json) to Lab examples (docs/designs/fleet-lab.md, Project data in
// examples). Each row becomes one inline example of the use case: the row's input is the prompt, its label the reference
// references/expected.json, and the check compares the trial's typed answer with it. Production cost stays in the
// example's production reference, context only.
import type {LabExport,LabExportRow} from '../contracts/generated/lab-export.ts';
import type {Example} from './lab-example-format.ts';

const fail=(m:string):never=>{throw Error(m)};
const text=(v:unknown,what:string)=>typeof v==='string'&&v.length>0?v:fail(`export: ${what} must be non-empty text`);
const SPLITS=new Set(['tune','held_out']),BASES=new Set(['measured','estimated','subscription','unavailable']);

export function parseExport(v:any):LabExport{
 if(!v||typeof v!=='object'||Array.isArray(v))fail('export: expected an object');
 if(v.schema!==1)fail(`export: schema ${v.schema} is not version 1`);
 for(const k of Object.keys(v))if(!['schema','project','stepId','exportedAt','rows'].includes(k))fail(`export: unknown field ${k}`);
 text(v.project,'project');text(v.stepId,'stepId');text(v.exportedAt,'exportedAt');
 if(!Array.isArray(v.rows))fail('export: rows must be a list');
 const seen=new Set<string>();
 for(const r of v.rows as LabExportRow[]){
  for(const k of Object.keys(r))if(!['rowId','input','label','tag','productionCost','split'].includes(k))fail(`export: row has unknown field ${k}`);
  text(r.rowId,'rowId');text(r.input,'input');text(r.label,'label');if(r.tag!==undefined)text(r.tag,'tag');
  if(!SPLITS.has(r.split))fail(`export: row ${r.rowId} split must be tune or held_out`);
  if(r.productionCost&&!BASES.has(r.productionCost.basis))fail(`export: row ${r.rowId} productionCost basis is invalid`);
  if(seen.has(r.rowId))fail(`export: duplicate rowId ${r.rowId}`);seen.add(r.rowId);
 }
 return v as LabExport}

// The check of every exported row: the trial's p (output.json) on the same side of 0.5 as the label.
const AGREES="p=$(sed -n 's/.*\"p\": *\\([0-9.eE+-]*\\).*/\\1/p' output.json); want=$(sed -n 's/.*\"expected\": *\"\\([a-z]*\\)\".*/\\1/p' references/expected.json); [ -n \"$p\" ] && [ -n \"$want\" ] && [ \"$(awk -v p=\"$p\" 'BEGIN{print (p>=0.5)?\"yes\":\"no\"}')\" = \"$want\" ]";

export function examplesFromExport(raw:any,useCaseId:string,version=1):Example[]{
 const e=parseExport(raw),slug=(s:string)=>s.replace(/[^A-Za-z0-9._-]+/g,'-').slice(0,40);
 return e.rows.map(r=>({id:`${slug(e.project)}-${slug(e.stepId)}-${slug(r.rowId)}`.slice(0,100),use_case_id:useCaseId,version,origin:`export:${slug(e.project)}`,fixture:null,prompt:r.input,inputs:[],
  // The known-good result: a trial output certain of the label, so the checks pass on the references alone.
  references:[{path:'output.json',text:JSON.stringify({answer:null,p:r.label.toLowerCase()==='yes'?1:0,answered_by:'label',tokens:null})+'\n'},{path:'expected.json',text:JSON.stringify({expected:r.label.toLowerCase(),tag:r.tag??null,productionCost:r.productionCost??null})+'\n'}],
  checks:[{id:'agrees',run:AGREES}],split:r.split}))}
