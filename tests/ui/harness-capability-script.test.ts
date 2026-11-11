// R5; risk: a repository-backed Lab step loses its capability receipt in a disposable checkout instead of retained output.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync} from 'node:fs';
import {join, resolve} from 'node:path';
import {tmpdir} from 'node:os';
import {execFileSync} from 'node:child_process';

test('capability script retains receipts outside repository cwd without invoking a model', () => {
 const root=mkdtempSync(join(tmpdir(),'fleet-capability-output-'));
 try {
  const scripts=join(root,'release/scripts/lab'), input=join(root,'input'), output=join(root,'retained'), cwd=join(root,'checkout');
  for(const p of [scripts,join(input,'example'),cwd]) mkdirSync(p,{recursive:true});
  writeFileSync(join(input,'example/binding.json'),JSON.stringify({nativeAccount:'fixture-account',registry:join(root,'registry.json')}));
  const python=execFileSync('python3',['-c','import sys; print(sys.executable)'],{encoding:'utf8'}).trim();
  writeFileSync(join(root,'release/fleet-executor'),`#!${python}\nimport json,pathlib,sys\nassert sys.argv[1]=='qualify-serena'\nassert sys.argv[5]=='fixture-account'\nassert 'CapabilityWitness' in (pathlib.Path(sys.argv[3])/'fixture.go').read_text()\np=pathlib.Path(sys.argv[4]);p.write_text(json.dumps({'symbolObserved':True,'symbol':'CapabilityWitness','modelInvoked':False,'sourceSha256':'fixture-source','profileSha256':'fixture-profile'}))\n`,{mode:0o700});
  execFileSync(python,[resolve('scripts/lab/harness-capability.py')],{cwd,env:{PATH:process.env.PATH,FLEET_LAB_SCRIPTS:scripts,FLEET_INPUT_DIR:input,FLEET_OUTPUT_DIR:output}});
  assert.equal(existsSync(join(cwd,'output')),false);
  const receipt=JSON.parse(readFileSync(join(output,'capability-receipt.json'),'utf8'));
  assert.equal(receipt.symbolObserved,true);
  assert.equal(receipt.modelInvoked,false);
  const summary=JSON.parse(readFileSync(join(output,'output.json'),'utf8'));
  assert.deepEqual(summary,{pass:true,receipt:'capability-receipt.json',sourceSha256:'fixture-source',profileSha256:'fixture-profile',modelInvoked:false});
 } finally {rmSync(root,{recursive:true,force:true});}
});
