import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { join } from 'node:path';
import { repository, repositoryId } from './provider.mjs';

const origin = 'https://fleet.ufobe.com';
const identity = 'fleet-design-verifier';
const digest = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);

export async function fleetVerifierClient(credentialDir, send = fetch) {
  if (!credentialDir) return null;
  let file;
  try { file = await open(join(credentialDir, 'fleet-access.json'), constants.O_RDONLY | constants.O_NOFOLLOW); }
  catch (err) { if (err.code === 'ENOENT') return null; throw err; }
  let access;
  try {
    const stat = await file.stat();
    if (!stat.isFile() || (stat.mode & 0o077) !== 0) throw new Error('The Fleet verifier credential must be service-private');
    access = JSON.parse(await file.readFile('utf8'));
    if (!access || Object.keys(access).length !== 2 || ![access.clientId, access.clientSecret]
      .every(x => typeof x === 'string' && x.length && !/[\r\n]/.test(x))) throw new Error('Invalid protected Fleet access credential');
  } catch {
    throw new Error('Could not load protected Fleet access');
  } finally {
    await file.close();
  }
  const headers = { Origin: origin, 'Content-Type': 'application/json',
    'CF-Access-Client-Id': access.clientId, 'CF-Access-Client-Secret': access.clientSecret };
  async function op(name, body) {
    const reply = await send(origin + '/api/ops/' + name, {
      method: 'POST', redirect: 'error', headers, signal: AbortSignal.timeout(30_000), body: JSON.stringify(body),
    });
    if (!reply.ok) throw Object.assign(new Error('Fleet verifier API returned HTTP ' + reply.status), { status: reply.status });
    return reply.json();
  }
  return {
    async probe() {
      const me = await op('me', {});
      if (me.email !== identity || me.owner !== false || me.roles?.length !== 1 || me.roles[0] !== 'viewer')
        throw new Error('The Fleet verifier identity does not match');
    },
    async observe(observation) {
      if ((await op('design_verifier_observe', observation)).ok !== true) throw new Error('Fleet did not retain the observation');
    },
    async evidence(member) {
      if (member.sourceRepositoryId !== repositoryId) return null;
      const { number, head, branch } = member;
      let found;
      try { found = await op('design_verifier_evidence', { number, head, branch }); }
      catch (err) { if (err.status === 404 || err.status === 409) return null; throw err; }
      const review = found.review;
      const selected = review?.options?.find(option => option.key === review.selected);
      if (found.number !== number || found.head !== head || found.branch !== branch || !found.writer ||
          review?.projectId !== 'fleet' || review.repository !== repository || review.state !== 'selected' ||
          !review.owner || !review.questionId || !review.taskId || !digest(review.digest) || !selected ||
          !Array.isArray(found.recipes) || found.recipes.length !== selected.cases.length ||
          !selected.cases.every(c => found.recipes.filter(r => r.scopeKey === c.scopeKey).length === 1))
        throw new Error('Fleet did not confirm the exact admission and owner selection');
      return { number, head, branch, writer: found.writer, task: review.taskId, question: review.questionId,
        selection: review.digest, option: selected.key, recipes: found.recipes,
        originals: selected.cases.map(c => ({ scopeKey: c.scopeKey, recipe: c.recipe,
          sha256: c.frames.map(f => f.sha256), artifacts: c.frames.map(f => f.artifactId) })) };
    },
  };
}
