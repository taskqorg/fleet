import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const service = new URL('../../deploy/design-verifier/service.mjs', import.meta.url);
const parity = new URL('./parity.mjs', service).href;
const loader = `
  import { registerHooks } from 'node:module';
  const modules = {
    'node:fs/promises': 'export const readFile = async () => JSON.stringify({commit:"a".repeat(40)});',
    '../../packages/ui/scripts/design-github-checks.mjs': \`
      export const designGitHubConfig = () => ({appId:1,installationId:2});
      export const fleetDesignChecks = () => ({});
      export function fleetDesignGitHub() {
        const api = async () => ({id:3,full_name:'taskqorg/fleet',default_branch:'main'});
        api.authentication = () => ({authenticatedAt:1,expiresAt:2});
        api.close = async () => console.log('token revoked');
        return api;
      }\`,
    './provider.mjs': \`
      export const appId=1, installationId=2, repositoryId=3, repo='fixture', sha=()=>true;
      export const githubCandidates=()=>({snapshot:async()=>[]});\`,
    './fleet.mjs': \`
      export const fleetVerifierClient=async()=>({probe:async()=>{},observe:async(value,options)=>{
        console.log('Fleet '+value.state+(options ? ' fresh='+!options.signal.aborted : ''));
      }});\`,
    './git-isolated.mjs': 'export const openAsyncRepository=()=>({});',
    './receipts.mjs': \`
      export const receiptStore=async()=>({observe:async value=>{
        const scenario=process.env.T199_SHUTDOWN_FIXTURE;
        if(value.provider==='ready' && scenario==='poll-write' ||
           value.provider==='unknown' && scenario==='terminal-write')
          throw Object.assign(Error('fixture disk full'),{code:'ENOSPC'});
        if(value.provider==='ready') process.kill(process.pid,'SIGTERM');
      }});\`,
    './parity.mjs': \`
      import { EventEmitter } from 'node:events';
      import { PassThrough } from 'node:stream';
      import { collectLocalWorker } from '${parity}';
      export function parityRunner({signal,onUnconfirmed}) {
        return {probe:async()=>{
          if(process.env.T199_SHUTDOWN_FIXTURE!=='stuck-probe') return {};
          const child = Object.assign(new EventEmitter(), {
            stdin:new PassThrough(), stdout:new PassThrough(),
            kill:()=>console.log('local worker killed'), unref:()=>console.log('local worker released'),
          });
          return collectLocalWorker(child,{job:'render',max:1024,timeout:25000,signal,onUnconfirmed,cleanupTimeout:100});
        }};
      }\`,
  };
  registerHooks({resolve(specifier, context, next) {
    if(context.parentURL === '${service.href}' && Object.hasOwn(modules,specifier))
      return {url:'data:text/javascript,'+encodeURIComponent(modules[specifier]),shortCircuit:true};
    return next(specifier,context);
  }});
`;

for (const scenario of ['poll-write', 'terminal-write', 'stuck-probe']) {
  test('issuer sends final Fleet unknown on ' + scenario, () => {
    const result = spawnSync(process.execPath,
      ['--import', 'data:text/javascript,' + encodeURIComponent(loader), fileURLToPath(service)],
      { env: { STATE_DIRECTORY: tmpdir(), T199_SHUTDOWN_FIXTURE: scenario }, encoding: 'utf8', timeout: 4000 });
    assert.ifError(result.error);
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stdout, /Fleet ready/);
    assert.match(result.stdout, /Fleet unknown fresh=true/);
    assert.match(result.stdout, /token revoked/);
    if (scenario === 'stuck-probe') {
      assert.match(result.stdout, /local worker killed/);
      assert.match(result.stdout, /local worker released/);
      assert.ok(result.stdout.indexOf('Fleet unknown') < result.stdout.indexOf('local worker released'));
    }
  });
}
