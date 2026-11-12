// Narrow rollout: prose and Lab-create may omit unrelated families; unknown impact stays full.
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {pathToFileURL} from 'node:url';

export const families = ['go', 'app', 'browser'] as const;
type Family = typeof families[number];
type Event = {pull_request?: {base: {sha: string}}; merge_group?: {base_sha: string; head_sha: string}; before?: string; after?: string};
export type Plan = {version: 1; mode: 'selected'; paths: string[] | null; previousGreen: boolean; forced: boolean; policy: string; candidate: string; base: string | null; event: string; selected: Family[]; executed: readonly Family[]; deferred: readonly Family[]; reasons: string[]};
const policy = () => createHash('sha256').update(readFileSync(new URL(import.meta.url))).digest('hex');
const prose = (path: string) => /^docs\/.*\.md$/.test(path) || path === 'README.md';
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
    if (prose(path)) reasons.push(`prose: ${path}`);
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

export function execution(paths: string[] | null, previousGreen: boolean, forced: boolean): Family[] {
  if (!paths?.length || !previousGreen || forced) return [...families];
  if (!paths.every(p => prose(p) || /^(go\/(coordinator|executor|gateway|update)\/|app\/src\/|packages\/ui\/)/.test(p))) return [...families];
  const chosen = new Set(select(paths).selected);
  // Go candidates retain the app family's clean native archive builds and selftests.
  if (paths.some(p => /^go\//.test(p))) chosen.add('app');
  return families.filter(f => chosen.has(f));
}

export function makePlan(event: string, payload: Event, candidate: string, git: (args: string[]) => string, previousGreen = false, forced = false): Plan {
  let base: string | null = null, paths: string[] | null = null;
  let decision: ReturnType<typeof select>;
  try {
    base = comparison(event, payload, candidate);
    if (git(['rev-parse', 'HEAD']).trim() !== candidate) throw Error('checkout candidate mismatch');
    git(['merge-base', '--is-ancestor', base, candidate]);
    paths = changedPaths(git(['diff', '--name-status', '-z', '--find-renames', base, candidate]));
    decision = select(paths);
  } catch (error) {
    decision = {selected: [...families], reasons: [`full fallback: ${String(error)}`]};
  }
  return {version: 1, mode: 'selected', policy: policy(), candidate, base, event, paths, previousGreen, forced,
    ...decision, ...run(event, execution(paths, previousGreen, forced))};
}

// A pull request runs only the thin check; its heavy families are deferred to the merge queue, which qualifies the candidate.
export function run(event: string, families: readonly Family[]): {executed: readonly Family[]; deferred: readonly Family[]} {
  return event === 'pull_request' ? {executed: [], deferred: families} : {executed: families, deferred: []};
}

type Run = {databaseId: number; status: string; conclusion: string};
export function previousResult(runs: Run[], current: number): boolean | null {
  const previous = runs.find(r => r.databaseId !== current);
  return previous ? previous.status === 'completed' && previous.conclusion === 'success' : null;
}

function previousGreen(event: string, payload: Event, candidate: string): boolean {
  try {
    const list = (args: string[]) => JSON.parse(execFileSync('gh', ['run', 'list', '--workflow', 'ci', ...args,
      '--json', 'databaseId,status,conclusion'], {encoding: 'utf8'})) as Run[];
    const branch = process.env.GITHUB_HEAD_REF || process.env.GITHUB_REF_NAME;
    if (!branch) return false;
    const result = previousResult(list(['--branch', branch, '--limit', '2']), Number(process.env.GITHUB_RUN_ID));
    if (result === false) return false;
    // A green branch must not authorize omissions on an unqualified newer main base.
    return previousResult(list(['--commit', comparison(event, payload, candidate), '--event', 'push', '--limit', '1']),
      Number(process.env.GITHUB_RUN_ID)) === true;
  } catch { return false; }
}

export function verifyEvidence(plan: Plan, candidate: string, jobs: Record<string, {result: string}>): void {
  assert.equal(plan.version, 1);
  assert.equal(plan.mode, 'selected');
  assert.equal(plan.policy, policy(), 'selection policy mismatch');
  assert.ok(sha(candidate));
  assert.equal(plan.candidate, candidate);
  assert.ok(plan.base === null || sha(plan.base));
  assert.ok(['pull_request', 'merge_group', 'push'].includes(plan.event));
  assert.ok(Array.isArray(plan.reasons) && plan.reasons.length > 0);
  assert.ok(Array.isArray(plan.selected) && plan.selected.every(f => families.includes(f)));
  assert.equal(typeof plan.previousGreen, 'boolean');
  assert.equal(typeof plan.forced, 'boolean');
  assert.ok(plan.paths === null || (Array.isArray(plan.paths) && plan.paths.length > 0 && plan.paths.every(p => typeof p === 'string' && p.length > 0)));
  assert.deepEqual(plan.selected, plan.paths ? select(plan.paths).selected : [...families]);
  const expected = run(plan.event, execution(plan.paths, plan.previousGreen, plan.forced));
  assert.deepEqual(plan.executed, expected.executed);
  assert.deepEqual(plan.deferred, expected.deferred);
  assert.equal(jobs.selection?.result, 'success', 'selection: failed or missing');
  assert.equal(jobs.thin?.result, plan.event === 'pull_request' ? 'success' : 'skipped', 'thin: unexpected or missing execution');
  for (const family of families) assert.equal(jobs[family]?.result, plan.executed.includes(family) ? 'success' : 'skipped', `${family}: unexpected or missing execution`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (process.argv[2] === '--verify') {
    const plan = JSON.parse(readFileSync(process.argv[3], 'utf8')) as Plan;
    const jobs = JSON.parse(readFileSync(process.argv[4], 'utf8')) as Record<string, {result: string}>;
    verifyEvidence(plan, process.env.GITHUB_SHA!, jobs);
    if (plan.paths) {
      assert.ok(sha(plan.base));
      execFileSync('git', ['merge-base', '--is-ancestor', plan.base, plan.candidate]);
      assert.deepEqual(plan.paths, changedPaths(execFileSync('git', ['diff', '--name-status', '-z', '--find-renames', plan.base, plan.candidate], {encoding: 'utf8'})));
    }
    console.log('Exact-candidate selected execution evidence: PASS');
  } else {
    const event = process.env.GITHUB_EVENT_NAME || 'unknown';
    const payload = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH!, 'utf8')) as Event;
    const plan = makePlan(event, payload, process.env.GITHUB_SHA!, args => execFileSync('git', args, {encoding: 'utf8'}),
      previousGreen(event, payload, process.env.GITHUB_SHA!), Boolean(process.env.FLEET_TEST_FULL));
    console.log(JSON.stringify(plan, null, 2));
  }
}
