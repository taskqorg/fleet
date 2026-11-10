// The executor binary for end-to-end tests: FLEET_EXECUTOR_BIN names a prebuilt one; otherwise `go build ./executor` into a
// private path, so a later build from another worktree cannot replace it while the test runs.
import {execFileSync} from 'node:child_process';import {mkdtempSync} from 'node:fs';import {tmpdir} from 'node:os';import {join} from 'node:path';
export function executorBinary(){
 if(process.env.FLEET_EXECUTOR_BIN)return process.env.FLEET_EXECUTOR_BIN;
 const out=join(mkdtempSync(join(tmpdir(),'fleet-executor-')),'fleet-executor');
 execFileSync('go',['build','-o',out,'./executor'],{cwd:new URL('../../go',import.meta.url).pathname,stdio:'inherit',env:{...process.env,CGO_ENABLED:'0'},timeout:900000});
 return out;
}
