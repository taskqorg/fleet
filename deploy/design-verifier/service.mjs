import { readFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { designGitHubConfig, fleetDesignChecks, fleetDesignGitHub } from '../../packages/ui/scripts/design-github-checks.mjs';
import { appId, installationId, repositoryId, githubCandidates, repo, sha } from './provider.mjs';
import { fleetVerifierClient } from './fleet.mjs';
import { openAsyncRepository } from './git-isolated.mjs';
import { receiptStore } from './receipts.mjs';
import { evaluate } from './verifier.mjs';
import { parityRunner } from './parity.mjs';
import { rendererController, rendererPoll, pollComplete } from './renderer-state.mjs';

const once = process.argv[2] === '--once';
if (process.argv.length !== (once ? 3 : 2)) throw new Error('Usage: service.mjs [--once]');
const state = process.env.STATE_DIRECTORY ?? '';
const period = Number(process.env.FLEET_DESIGN_POLL_SECONDS ?? 30);
if (!isAbsolute(state) || !Number.isSafeInteger(period) || period <= 0) throw new Error('Invalid verifier service state or poll interval');
const config = designGitHubConfig(process.env);
if (config.appId !== appId || config.installationId !== installationId) throw new Error('Unexpected Fleet design App identity');
const release = JSON.parse(await readFile(new URL('../../release.json', import.meta.url), 'utf8'));
if (!sha(release.commit)) throw new Error('The verifier needs an exact trusted release commit');
const stop = new AbortController();
const api = fleetDesignGitHub(config, { signal: stop.signal });
const checks = fleetDesignChecks(api, appId);
const provider = githubCandidates(api);
const receipts = await receiptStore(state, release.commit, checks);
const git = openAsyncRepository(state, { signal: stop.signal, onUnconfirmed: () => stop.abort() });
let fleet;
let fleetAccess = 'unconfigured';
try { fleet = await fleetVerifierClient(process.env.CREDENTIALS_DIRECTORY, fetch, { signal: stop.signal }); }
catch { fleetAccess = 'unknown'; }
const service = { appId, installationId, repositoryId, revision: release.commit };
for (const signal of ['SIGTERM', 'SIGINT']) process.once(signal, () => stop.abort());
const ensureRunning = () => { if (stop.signal.aborted) throw new Error('Verifier stopped'); };
const renderer = rendererController(service, onUnavailable => parityRunner({ signal: stop.signal, onUnavailable }), stop.signal);

let reportedProvider;
let providerReportedAt = 0;
async function report(observation, results) {
  if (!fleet) return fleetAccess;
  try {
    await fleet.probe();
    const providerReport = { kind: 'provider', state: observation.provider, revision: release.commit,
      ...(observation.provider === 'ready' ? observation.authentication : {}) };
    const signature = JSON.stringify(providerReport);
    if (signature !== reportedProvider || Date.now() - providerReportedAt >= 60_000) {
      await fleet.observe(providerReport);
      reportedProvider = signature;
      providerReportedAt = Date.now();
    }
    for (const check of results) {
      const { kind, head, externalId, checkRunId, conclusion } = check;
      if (!checkRunId) continue;
      await receipts.reportReadback(check, () => fleet.observe({ kind, head, externalId, checkRunId,
        revision: release.commit, state: conclusion ?? 'pending' }));
    }
    return 'ready';
  } catch {
    return 'unknown';
  }
}

async function poll() {
  let observation;
  const results = [];
  try {
    const info = await api(repo);
    if (info.id !== repositoryId || info.full_name !== 'taskqorg/fleet' || info.default_branch !== 'main')
      throw new Error('The provider default repository does not match Fleet main');
    const events = await provider.snapshot();
    const byHead = Map.groupBy(events, event => event.head);
    for (const [head, candidates] of byHead) {
      if (stop.signal.aborted) break;
      const [event] = candidates;
      try {
        if (candidates.length === 1) results.push(await evaluate(event, { provider, git, receipts, checks, fleet, runner: renderer.current, rendererPending: !renderer.current, signal: stop.signal }));
        else {
          ensureRunning();
          const reason = 'The SHA has multiple active provider identities';
          const evidence = { head, classification: 'pending', reason, candidates };
          const id = await receipts.retain(evidence, null);
          ensureRunning();
          const check = await checks.publish(head, id, null, reason, async () => {
            for (const current of candidates) await provider.assertCurrent(current);
            ensureRunning();
          });
          await receipts.recordReadback(id, check);
          results.push({ ...check, kind: event.kind, classification: 'pending', reason });
        }
      } catch {
        results.push({ kind: event.kind, head, ref: event.ref, number: event.number, state: 'unknown',
          reason: 'Could not publish or confirm the exact-head result' });
      }
    }
    const authentication = api.authentication();
    observation = authentication ? { provider: 'ready', authentication } :
      { provider: 'unknown', reason: 'The installation token was rejected' };
  } catch {
    observation = { provider: 'unknown', reason: 'Could not confirm the GitHub App installation and repository facts' };
  }
  const fleetEvidence = await report(observation, results);
  const status = { ...service, ...observation, at: new Date().toISOString(), fleetEvidence, results };
  await receipts.observe(status);
  console[observation.provider === 'ready' ? 'log' : 'error'](JSON.stringify(status));
  return pollComplete(observation, fleetEvidence, results);
}

let ok;
try {

  do {
    ok = await rendererPoll(renderer, poll, once);
    if (!once) await delay(period * 1000, null, { signal: stop.signal });
  } while (!once && !stop.signal.aborted);
} catch (err) {
  if (err.name !== 'AbortError') throw err;
} finally {
  const stopped = stop.signal.aborted;
  stop.abort();
  await renderer.wait();
  try { await api.close(); } catch { console.error('GitHub installation token revocation could not be confirmed'); }
  if (stopped) {
    const status = { ...service, provider: 'unknown', reason: 'Service stopped', at: new Date().toISOString() };
    await receipts.observe(status);
    await report(status, []);
  }
}
if (once && !ok) process.exitCode = 1;
