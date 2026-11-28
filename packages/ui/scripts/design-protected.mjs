// Load this module from a protected revision. The adapter reads provider facts and trusted
// receipts. Candidate code runs only in a separate credential-free builder.
import { execFileSync } from 'node:child_process';
import { devNull } from 'node:os';
import { designImpact } from './design-impact.mjs';
import { verifyRetainedDesignEvidence } from './design-evidence.mjs';

const sha = value => typeof value === 'string' && /^[0-9a-f]{40}$/.test(value);
const digest = value => typeof value === 'string' && /^[0-9a-f]{64}$/.test(value);
const reject = message => { throw new Error('Protected design verification: ' + message); };

export function candidateChanges(repositoryDir, { kind, base, head }) {
  if (!sha(base) || !sha(head) || !['pull_request', 'merge_group'].includes(kind))
    reject('missing immutable provider revisions');
  const env = { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: devNull };
  const git = (...args) => execFileSync('git', ['--no-replace-objects', ...args], { cwd: repositoryDir, encoding: 'utf8', env });
  if (git('rev-parse', '--verify', base + '^{commit}').trim() !== base ||
      git('rev-parse', '--verify', head + '^{commit}').trim() !== head) reject('provider revisions are not local commits');
  const ancestor = git('merge-base', base, head).trim();
  if (kind === 'merge_group' && ancestor !== base) reject('the provider group base is not an ancestor');
  // Treat renames as an addition and deletion. Check both paths.
  return git('diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z', ancestor, head).split('\0').filter(Boolean);
}

function requireFence(record, expected) {
  if (!record || record.repository !== expected.repository || record.base !== expected.base || record.head !== expected.head)
    reject('evidence identifies a different candidate');
}

function membersOf(event, provider) {
  if (event.kind === 'pull_request') {
    const pr = provider;
    if (!Number.isSafeInteger(pr.number) || pr.number < 1 || pr.number !== event.number || pr.target !== event.target)
      reject('provider pull request does not match the event');
    return [pr];
  }
  if (!provider.complete || provider.target !== event.target || !Array.isArray(provider.members) || !provider.members.length)
    reject('provider merge-group membership is unknown');
  const numbers = new Set();
  for (const member of provider.members) {
    if (!Number.isSafeInteger(member.number) || member.number < 1 || numbers.has(member.number) ||
        !sha(member.head) || member.repository !== event.repository || member.target !== event.target)
      reject('invalid merge-group member');
    numbers.add(member.number);
  }
  return provider.members;
}

// readCandidate resolves authenticated PR/group facts, including complete membership.
// readPRReceipt retrieves receipts from the same protected verifier. measureImpact
// resolves unmapped paths from actual changes using protected semantic review.
// measureParity retains matching app-route and scene evidence keyed by this candidate.
export async function verifyProtectedDesignCandidate(event, adapter) {
  if (!sha(event.base) || !sha(event.head) || !['pull_request', 'merge_group'].includes(event.kind) ||
      event.repository !== 'github.com/taskqorg/fleet' || event.target !== 'main')
    reject('unsupported provider candidate');
  const provider = await adapter.readCandidate(event);
  requireFence(provider, event);
  if (provider.kind !== event.kind) reject('provider event kind does not match');
  const members = membersOf(event, provider);
  if (event.kind === 'merge_group') {
    for (const member of members) {
      const receipt = await adapter.readPRReceipt(member);
      if (!receipt || receipt.kind !== 'pull_request' || receipt.repository !== event.repository ||
          receipt.number !== member.number || receipt.head !== member.head || receipt.target !== event.target ||
          !['nonvisual', 'selected-preview-matches'].includes(receipt.classification))
        reject('no protected receipt for the exact member head');
    }
  }

  const changes = candidateChanges(adapter.repositoryDir, event);
  const scenes = await adapter.readSceneInventory(event);
  const impact = designImpact(changes, scenes);
  const scope = new Set(impact.affected.flatMap(scene => Object.keys(scene.states).map(state => scene.key + ':' + state)));
  if (impact.unknown.length) {
    const measured = await adapter.measureImpact(event, impact.unknown);
    requireFence(measured, event);
    for (const path of impact.unknown) {
      if (!Object.hasOwn(measured.paths ?? {}, path)) reject('visual impact is unverified for ' + path);
      const cases = measured.paths[path];
      // An empty array is a positive nonvisual finding by the protected reviewer.
      if (!Array.isArray(cases) || cases.some(key => typeof key !== 'string' || !key.trim()))
        reject('visual impact is unverified for ' + path);
      for (const key of cases) scope.add(key);
    }
  }

  const receipt = { kind: event.kind, repository: event.repository, target: event.target, base: event.base, head: event.head,
    ...(event.kind === 'pull_request' ? { number: event.number } : { members: members.map(({ number, head }) => ({ number, head })) }) };
  if (!scope.size) return { ...receipt, classification: 'nonvisual' };

  const parity = await adapter.measureParity(event, [...scope]);
  requireFence(parity, event);
  if (!Array.isArray(parity.cases) || parity.cases.length !== scope.size) reject('missing app parity cases');
  const cases = [];
  for (const item of parity.cases) {
    if (!scope.delete(item.scopeKey) || !digest(item.recipe) || !digest(item.receipt) || item.appMatches !== true)
      reject('app parity is unverified for ' + item.scopeKey);
    cases.push({ scopeKey: item.scopeKey, recipe: item.recipe });
  }
  const admissions = await adapter.readAdmittedTasks(event, members);
  if (!Array.isArray(admissions) || !admissions.length) reject('no admitted Fleet tasks for the candidate');
  const tasks = admissions.map(admission => {
    if (typeof admission?.task !== 'string' || !admission.task.trim() ||
        !members.some(member => member.number === admission.number && member.head === admission.head))
      reject('Fleet admission does not match an exact member head');
    return admission.task;
  });
  const { classification, decisions, selectedFrames } = await verifyRetainedDesignEvidence({
    project: 'fleet', repository: event.repository, cases, unknown: [],
  }, tasks, task => adapter.readReview(task), id => adapter.readOriginal(id));
  for (const item of parity.cases) {
    const originals = selectedFrames[item.scopeKey];
    if (!Array.isArray(item.originalSha256) || item.originalSha256.length !== originals.length ||
        originals.some((sha256, i) => sha256 !== item.originalSha256[i]))
      reject('app parity was not measured against the selected originals for ' + item.scopeKey);
  }
  return { ...receipt, classification, decisions, parity: parity.cases.map(({ scopeKey, recipe, receipt }) => ({ scopeKey, recipe, receipt })) };
}
