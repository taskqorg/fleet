// Inventory of test cases without a named requirement or risk; annotations belong in the title or adjacent preceding
// comments. Warnings everywhere, except --strict: a case under tests/go/ that misses either fails the run (outside tests).
// File metadata does not silently annotate every case. No tests are executed.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import {dirname,extname,join} from 'node:path';
import {fileURLToPath} from 'node:url';

const requirement=/\b(?:R[1-6]|UC(?:10|[1-9]))\b|\b[Rr]equirement\s*:/,risk=/\brisk\s*:/i;
const declaration=/^\s*(?:test|it)(?:\.(?:only|skip|todo))?\s*\(|^func Test\w*\(/;
const missing=(text:string)=>[...(!requirement.test(text)?['requirement ID']:[]),...(!risk.test(text)?['risk:']:[])];
type Case={line:number,title:string,context:string};

function cases(text:string,suffix:string):Case[]{
 const lines=text.split(/\r?\n/),found:Case[]=[];
 const preceding=(i:number,comment:RegExp)=>{let start=i;while(start&&comment.test(lines[start-1].trim()))start--;return start};
 for(let i=0;i<lines.length;i++){
  const line=lines[i];if(!declaration.test(line))continue;
  const start=preceding(i,/^(?:\/\/|#|\/\*|\*)/);
  const end=i+1+(line.trimEnd().endsWith('(')?1:0);
  found.push({line:i+1,title:line.trim(),context:lines.slice(start,end).join('\n')});
 }
 return found;
}

function report(root:string){
 const files=execFileSync('git',['ls-files'],{cwd:root,encoding:'utf8'}).trimEnd().split('\n');
 const counts={files:0,cases:0,unnamed_requirement:0,unnamed_risk:0},strict=process.argv.includes('--strict');let failed=0;
 for(const name of files){
  const suffix=extname(name);
  if(!((name.startsWith('tests/')&&/\.(?:test\.ts|check\.(?:ts|mjs))$/.test(name))||(name.startsWith('go/')&&name.endsWith('_test.go'))))continue;
  const text=readFileSync(join(root,name),'utf8'),found=cases(text,suffix);if(!found.length)continue;
  counts.files++;
  const absent=missing(text);if(absent.length)console.log(`::warning file=${name}::File has no ${absent.join(', ')}`);
  for(const {line,title,context} of found){
   counts.cases++;const absent=missing(context);
   counts.unnamed_requirement+=Number(absent.includes('requirement ID'));counts.unnamed_risk+=Number(absent.includes('risk:'));
   const blocking=strict&&name.startsWith('tests/go/');failed+=Number(blocking&&absent.length>0);
   if(absent.length)console.log(`::${blocking?'error':'warning'} file=${name},line=${line}::Case ${title}: no ${absent.join(', ')}`);
  }
 }
 console.log('test-rule-report (static declarations/asserts; dynamic expansions are not counted): '+Object.entries(counts).map(([k,v])=>`${k}=${v}`).join(', '));
 if(failed)process.exit(1);
}

function selfCheck(){
 // R1; risk: inventory misses cases, counts strings as assertions, or inherits unrelated file metadata.
 const found=cases("// R1 risk: file context\nconst x = 1;\ntest('missing', () => {});\n// UC10 risk: lost result\ntest('named', () => {});",'.ts');
 assert.equal(found.length,2);assert.deepEqual(missing(found[0].context),['requirement ID','risk:']);assert.deepEqual(missing(found[1].context),[]);
 assert.equal(cases('// R2 risk: wrong value\nfunc TestValue(t *testing.T) {}','.go').length,1);
 assert.equal(cases("test('a (requirement: x; risk: y)', () => { assert.ok(/a/.test(s)); });",'.ts').length,1);assert.deepEqual(missing('(requirement: x; risk: y)'),[]);
 console.log('test-rule-report self-check: PASS');
}

if(process.argv.includes('--self-check'))selfCheck();
else report(join(dirname(fileURLToPath(import.meta.url)),'..'));
