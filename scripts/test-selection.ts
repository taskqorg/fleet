// Shadow impact selection; the full gate remains authoritative until qualified.
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {pathToFileURL} from 'node:url';

export const families = ['go', 'app', 'browser'] as const;
type Family = typeof families[number];
type Event = {pull_request?: {base: {sha: string}}; merge_group?: {base_sha: string; head_sha: string}; before?: string; after?: string};
export type Plan = {version: 1; mode: 'shadow'; policy: string; candidate: string; base: string | null; event: string; selected: Family[]; executed: readonly Family[]; reasons: string[]};
const policy = () => createHash('sha256').update(readFileSync(new URL(import.meta.url))).digest('hex');
const sha = (s: unknown): s is string => typeof s === 'string' && /^[a-f0-9]{40}$/.test(s) && !/^0+$/.test(s);

export function comparison(event: string, payload: Event, candidate: string): string {
  if (!sha(candidate)) throw Error('invalid candidate');
  let base: unknown;
  if (event === 'pull_request') base = payload.pull_request?.base.sha;
  else if (event === 'merge_group') {
    if (payload.merge_group?.head_sha !== candidate) throw Error('group candidate mismatch');
    base = payload.merge_group.base_sha;
  } else if (event === 'push') {
    if (payload.after !== candidate) throw Error('push candidate mismatch');
    base = payload.before;
  } else throw Error('unsupported event');
  if (!sha(base)) throw Error('missing base');
  return base;
}

// -z name-status retains both sides of renames, without parsing filenames as lines.
export function changedPaths(diff: string): string[] {
  const fields = diff.split('\0');
  if (fields.pop() !== '') throw Error('unterminated diff');
  const paths: string[] = [];
  for (let i = 0; i < fields.length;) {
    const status = fields[i++];
    if (!/^(?:[AMDT]|[RC]\d{1,3})$/.test(status)) throw Error('unknown diff status');
    const count = /^[RC]/.test(status) ? 2 : 1;
    for (let j = 0; j < count; j++) {
      const path = fields[i++];
      if (!path || path.startsWith('/') || path.split('/').includes('..')) throw Error('invalid diff path');
      paths.push(path);
    }
  }
  if (!paths.length) throw Error('empty comparison');
  return paths;
}

export function select(paths: string[]): {selected: Family[]; reasons: string[]} {
  const chosen = new Set<Family>();
  const reasons: string[] = [];
  for (const path of paths) {
    if (/^docs\/.*\.md$/.test(path) || path === 'README.md') reasons.push(`prose: ${path}`);
    else if (path.startsWith('go/coordinator/')) {
      chosen.add('go'); chosen.add('browser'); reasons.push(`coordinator and HTTP consumers: ${path}`);
    } else if (/^go\/(executor|gateway|update|obs)\//.test(path)) {
      chosen.add('go'); reasons.push(`Go runtime: ${path}`);
    } else if (/^(app\/src\/|packages\/ui\/)/.test(path)) {
      chosen.add('app'); chosen.add('browser'); reasons.push(`UI consumers: ${path}`);
    } else {
      families.forEach(f => chosen.add(f)); reasons.push(`full fallback: ${path}`);
    }
  }
  if (!paths.length) return {selected: [...families], reasons: ['full fallback: empty paths']};
  return {selected: families.filter(f => chosen.has(f)), reasons};
}

export function makePlan(event: string, payload: Event, candidate: string, git: (args: string[]) => string): Plan {
  let base: string | null = null;
  let decision: ReturnType<typeof select>;
  try {
    base = comparison(event, payload, candidate);
    if (git(['rev-parse', 'HEAD']).trim() !== candidate) throw Error('checkout candidate mismatch');
    git(['merge-base', '--is-ancestor', base, candidate]);
    decision = select(changedPaths(git(['diff', '--name-status', '-z', '--find-renames', base, candidate])));
  } catch (error) {
    decision = {selected: [...families], reasons: [`full fallback: ${String(error)}`]};
  }
  return {version: 1, mode: 'shadow', policy: policy(), candidate, base, event, ...decision, executed: families};
}

export function verifyEvidence(plan: Plan, candidate: string, jobs: Record<string, {result: string}>): void {
  assert.equal(plan.version, 1);
  assert.equal(plan.mode, 'shadow');
  assert.equal(plan.policy, policy(), 'selection policy mismatch');
  assert.ok(sha(candidate));
  assert.equal(plan.candidate, candidate);
  assert.ok(plan.base === null || sha(plan.base));
  assert.ok(['pull_request', 'merge_group', 'push'].includes(plan.event));
  assert.ok(Array.isArray(plan.reasons) && plan.reasons.length > 0);
  assert.ok(Array.isArray(plan.selected) && plan.selected.every(f => families.includes(f)));
  assert.deepEqual(plan.executed, families);
  for (const family of ['selection', ...families]) assert.equal(jobs[family]?.result, 'success', `${family}: failed or missing`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (process.argv[2] === '--verify') {
    const plan = JSON.parse(readFileSync(process.argv[3], 'utf8')) as Plan;
    const jobs = JSON.parse(readFileSync(process.argv[4], 'utf8')) as Record<string, {result: string}>;
    verifyEvidence(plan, process.env.GITHUB_SHA!, jobs);
    console.log('Exact-candidate full execution evidence: PASS');
  } else {
    const event = process.env.GITHUB_EVENT_NAME || 'unknown';
    const payload = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH!, 'utf8')) as Event;
    const plan = makePlan(event, payload, process.env.GITHUB_SHA!, args => execFileSync('git', args, {encoding: 'utf8'}));
    console.log(JSON.stringify(plan, null, 2));
  }
}
