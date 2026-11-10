// Warning-only inventory; annotations belong in the title or adjacent preceding comments.
// File metadata does not silently annotate every case. No tests are executed.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import {dirname,extname,join} from 'node:path';
import {fileURLToPath} from 'node:url';

const requirement=/\b(?:R[1-6]|UC(?:10|[1-9]))\b/,risk=/\brisk\s*:/i;
const declaration=/\b(?:test|it)(?:\.(?:only|skip|todo))?\s*\(|^\s*#\[(?:tokio::)?test\]|^\s*def test_/;
const missing=(text:string)=>[...(!requirement.test(text)?['requirement ID']:[]),...(!risk.test(text)?['risk:']:[])];
type Case={line:number,title:string,context:string};

function cases(text:string,suffix:string):Case[]{
 const lines=text.split(/\r?\n/),found:Case[]=[];
 const preceding=(i:number,comment:RegExp)=>{let start=i;while(start&&comment.test(lines[start-1].trim()))start--;return start};
 for(let i=0;i<lines.length;i++){
  const line=lines[i];if(!declaration.test(line))continue;
  const start=preceding(i,/^(?:\/\/|#|\/\*|\*)/);
  const end=i+1+(suffix==='.rs'||line.trimEnd().endsWith('(')?1:0);
  found.push({line:i+1,title:line.trim(),context:lines.slice(start,end).join('\n')});
 }
 if(suffix==='.py'){
  // Mask Python strings/comments before finding assert statements, including inline ones.
  // shortcut: lexical inventory, not a Python parser; use a parser if Python syntax needs semantic analysis.
  const masked=text.replace(/'''[\s\S]*?'''|"""[\s\S]*?"""|'(?:\\[\s\S]|[^'\\\n])*'|"(?:\\[\s\S]|[^"\\\n])*"|#[^\n]*/g,s=>s.replace(/[^\r\n]/g,' '));
  const clean=masked.split(/\r?\n/);
  for(let i=0;i<clean.length;i++)for(const _ of clean[i].matchAll(/\bassert\b/g)){
   const start=preceding(i,/^#/);let end=i,depth=0;
   do{
    for(const c of clean[end]){if('([{'.includes(c))depth++;else if(')]}'.includes(c))depth--}
    end++;
   }while(end<clean.length&&(depth>0||clean[end-1].trimEnd().endsWith('\\')));
   found.push({line:i+1,title:'assert',context:lines.slice(start,end).join('\n')});
  }
 }
 return found;
}

function report(root:string){
 const files=execFileSync('git',['ls-files'],{cwd:root,encoding:'utf8'}).trimEnd().split('\n');
 const counts={files:0,cases:0,unnamed_requirement:0,unnamed_risk:0};
 for(const name of files){
  const suffix=extname(name);
  if(!((name.startsWith('tests/')&&/\.(?:test\.ts|check\.(?:ts|mjs)|py)$/.test(name))||(name.startsWith('crates/')&&suffix==='.rs')))continue;
  const text=readFileSync(join(root,name),'utf8'),found=cases(text,suffix);if(!found.length)continue;
  counts.files++;
  const absent=missing(text);if(absent.length)console.log(`::warning file=${name}::File has no ${absent.join(', ')}`);
  for(const {line,title,context} of found){
   counts.cases++;const absent=missing(context);
   counts.unnamed_requirement+=Number(absent.includes('requirement ID'));counts.unnamed_risk+=Number(absent.includes('risk:'));
   if(absent.length)console.log(`::warning file=${name},line=${line}::Case ${title}: no ${absent.join(', ')}`);
  }
 }
 console.log('test-rule-report (static declarations/asserts; dynamic expansions are not counted): '+Object.entries(counts).map(([k,v])=>`${k}=${v}`).join(', '));
}

function selfCheck(){
 // R1; risk: inventory misses cases, counts strings as assertions, or inherits unrelated file metadata.
 const found=cases("// R1 risk: file context\nconst x = 1;\ntest('missing', () => {});\n// UC10 risk: lost result\ntest('named', () => {});",'.ts');
 assert.equal(found.length,2);assert.deepEqual(missing(found[0].context),['requirement ID','risk:']);assert.deepEqual(missing(found[1].context),[]);
 assert.equal(cases('# R2 risk: wrong value\nassert True\n','.py').length,1);
 assert.equal(cases('#[test]\nfn protects_result() {}','.rs').length,1);
 const python=cases('s = "assert False" # assert False\ns = """\nassert False\n"""\nif True: assert (\n True\n)\nassert True; assert False\n','.py');
 assert.deepEqual(python.map(c=>c.line),[5,8,8]);assert.match(python[0].context,/True\n\)/);
 console.log('test-rule-report self-check: PASS');
}

if(process.argv.includes('--self-check'))selfCheck();
else report(join(dirname(fileURLToPath(import.meta.url)),'..'));
