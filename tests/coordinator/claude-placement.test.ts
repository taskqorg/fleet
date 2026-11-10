import {test} from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';

// delivery_worker.py uses the executor's Claude placement rule (docs/configuration.md).
test('delivery worker Claude placement: mac on macOS, windows-wsl on WSL Linux only',()=>{
 const cases:[string,string,string,boolean][]=[['mac','darwin','',true],['windows-wsl','linux','5.15.167.4-microsoft-standard-WSL2',true],['windows-wsl','linux','6.8.0-generic',false],['mac','linux','x',false],['','darwin','',false],['windows-wsl','darwin','',false]];
 const out=execFileSync('python3',['-I','-c',`import sys,json;sys.path.insert(0,'packages/coordinator');import delivery_worker as w;print(json.dumps([w.claude_placement_ok(*c) for c in json.loads(sys.argv[1])]))`,JSON.stringify(cases.map(c=>c.slice(0,3)))],{encoding:'utf8'});
 assert.deepEqual(JSON.parse(out),cases.map(c=>c[3]));
});
