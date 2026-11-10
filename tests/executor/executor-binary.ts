// The executor binary for end-to-end tests. Builds it first (incremental, ~0.1 s when current), because a shared target dir
// (docs/designs/fleet-stack.md D2) may hold a binary built from another worktree's branch, then copies it to a private
// path so a later build from another worktree cannot replace it while the test runs. cargo metadata gives the real target
// dir (CARGO_TARGET_DIR, CARGO_BUILD_TARGET_DIR or a config target-dir).
import {execFileSync} from 'node:child_process';import {copyFileSync,mkdtempSync} from 'node:fs';import {tmpdir} from 'node:os';import {join} from 'node:path';
// FLEET_EXECUTOR_BIN names a prebuilt executor (the Go port: go build -o <path> ./executor) instead of the cargo build.
export function executorBinary(){
 if(process.env.FLEET_EXECUTOR_BIN)return process.env.FLEET_EXECUTOR_BIN;
 execFileSync('cargo',['build','-p','fleet-executor'],{stdio:'inherit',timeout:900000});
 const target=JSON.parse(execFileSync('cargo',['metadata','--format-version','1','--no-deps'],{encoding:'utf8'})).target_directory;
 const copy=join(mkdtempSync(join(tmpdir(),'fleet-executor-')),'fleet-executor');copyFileSync(join(target,'debug','fleet-executor'),copy);return copy;
}
