// Wraps a heavy CI command in merge_group so any sibling job failure aborts remaining heavy jobs instead of burning
// extra minutes while preserving the run's failure conclusion (unlike cancelling the whole workflow run).
// Usage: node scripts/ci-failfast.ts [--self-check | <cmd> [args...]]
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {pathToFileURL} from 'node:url';

export type JobSummary = {name: string; status: string; conclusion: string | null};

const IGNORED_JOBS = new Set(['reports', 'comment', 'docs-order', 'cache-warm', 'app']);
const FAIL_CONCLUSIONS = new Set(['failure', 'timed_out', 'cancelled']);
const INITIAL_DELAY_MS = 25_000;
const POLL_INTERVAL_MS = 20_000;

export function findSiblingFailure(jobs: JobSummary[]): JobSummary | undefined {
  return jobs.find(j => !IGNORED_JOBS.has(j.name) && j.status === 'completed' && j.conclusion !== null && FAIL_CONCLUSIONS.has(j.conclusion));
}

export function shouldPoll(env: NodeJS.ProcessEnv): boolean {
  return env.GITHUB_EVENT_NAME === 'merge_group' && Boolean(env.GH_TOKEN && env.GITHUB_RUN_ID && env.GITHUB_REPOSITORY);
}

function killGroup(pid: number | undefined, signal: NodeJS.Signals) {
  if (!pid) return;
  try { process.kill(-pid, signal); } catch {
    try { process.kill(pid, signal); } catch {}
  }
}

async function runCommand(argv: string[]): Promise<number> {
  const [cmd, ...args] = argv;
  if (!cmd) throw new Error('usage: node scripts/ci-failfast.ts <cmd> [args...]');
  const poll = shouldPoll(process.env);
  const child = spawn(cmd, args, {stdio: 'inherit', detached: poll});

  let aborted = false;
  let timer: NodeJS.Timeout | undefined;
  let interval: NodeJS.Timeout | undefined;
  let killTimer: NodeJS.Timeout | undefined;
  let etag = '';

  const stopPolling = () => {
    if (timer) clearTimeout(timer);
    if (interval) clearInterval(interval);
    timer = undefined;
    interval = undefined;
  };

  const abortChild = (reason: string) => {
    if (aborted) return;
    aborted = true;
    stopPolling();
    console.log(`::warning::${reason}`);
    killGroup(child.pid, 'SIGTERM');
    killTimer = setTimeout(() => killGroup(child.pid, 'SIGKILL'), 3_000);
    killTimer.unref();
  };

  const onSignal = (sig: NodeJS.Signals) => {
    stopPolling();
    killGroup(child.pid, sig);
    if (sig === 'SIGTERM' && !killTimer) {
      killTimer = setTimeout(() => killGroup(child.pid, 'SIGKILL'), 3_000);
      killTimer.unref();
    }
  };
  process.on('SIGTERM', () => onSignal('SIGTERM'));
  process.on('SIGINT', () => onSignal('SIGINT'));

  const pollOnce = async () => {
    if (aborted) return;
    try {
      const headers: Record<string, string> = {
        authorization: `Bearer ${process.env.GH_TOKEN}`,
        accept: 'application/vnd.github+json',
      };
      if (etag) headers['if-none-match'] = etag;
      const url = `https://api.github.com/repos/${process.env.GITHUB_REPOSITORY}/actions/runs/${process.env.GITHUB_RUN_ID}/jobs?per_page=100`;
      const res = await fetch(url, {headers, signal: AbortSignal.timeout(5_000)});
      if (res.status === 304) return;
      if (res.status === 401 || res.status === 403) {
        stopPolling();
        return;
      }
      if (!res.ok) return;
      etag = res.headers.get('etag') ?? etag;
      const body = await res.json() as {jobs?: JobSummary[]};
      const failed = findSiblingFailure(body.jobs ?? []);
      if (failed) abortChild(`Aborting ${process.env.GITHUB_JOB ?? 'job'}: sibling job "${failed.name}" ended with ${failed.conclusion}`);
    } catch {
      // Transient API or network errors must never abort a healthy job.
    }
  };

  if (poll) {
    timer = setTimeout(() => {
      void pollOnce();
      interval = setInterval(() => { void pollOnce(); }, POLL_INTERVAL_MS);
      interval.unref();
    }, INITIAL_DELAY_MS);
    timer.unref();
  }

  return await new Promise<number>(resolve => {
    child.on('error', err => {
      stopPolling();
      console.error(err);
      resolve(1);
    });
    child.on('exit', (code, signal) => {
      stopPolling();
      if (killTimer) clearTimeout(killTimer);
      if (aborted) resolve(1);
      else if (code !== null) resolve(code);
      else resolve(signal === 'SIGINT' ? 130 : 143);
    });
  });
}

function selfCheck() {
  assert.equal(shouldPoll({GITHUB_EVENT_NAME: 'merge_group', GH_TOKEN: 't', GITHUB_RUN_ID: '1', GITHUB_REPOSITORY: 'a/b'}), true);
  assert.equal(shouldPoll({GITHUB_EVENT_NAME: 'pull_request', GH_TOKEN: 't', GITHUB_RUN_ID: '1', GITHUB_REPOSITORY: 'a/b'}), false);
  assert.equal(shouldPoll({GITHUB_EVENT_NAME: 'merge_group', GH_TOKEN: '', GITHUB_RUN_ID: '1', GITHUB_REPOSITORY: 'a/b'}), false);
  assert.equal(findSiblingFailure([
    {name: 'selection', status: 'completed', conclusion: 'success'},
    {name: 'reports', status: 'completed', conclusion: 'skipped'},
    {name: 'go-tests (unit, true)', status: 'in_progress', conclusion: null},
  ]), undefined);
  assert.equal(findSiblingFailure([
    {name: 'selection', status: 'completed', conclusion: 'success'},
    {name: 'browser-tests (1)', status: 'completed', conclusion: 'failure'},
  ])?.name, 'browser-tests (1)');
  assert.equal(findSiblingFailure([
    {name: 'release-build-pds18 / run', status: 'completed', conclusion: 'cancelled'},
  ])?.name, 'release-build-pds18 / run');
  console.log('ci-failfast self-check ok');
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (process.argv[2] === '--self-check') selfCheck();
  else process.exit(await runCommand(process.argv.slice(2)));
}
