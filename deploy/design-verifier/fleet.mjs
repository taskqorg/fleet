import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { designCredentialIsPrivate } from '../../packages/ui/scripts/design-github-checks.mjs';
import { DesignBlocked, repository, repositoryId } from './provider.mjs';

const origin = 'https://fleet.ufobe.com';
const identity = 'fleet-design-verifier';
const digest = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);

export async function fleetVerifierClient(credentialDir, send = fetch, { signal } = {}) {
  if (!credentialDir) return null;
  const path = join(credentialDir, 'fleet-access.json');
  let file;
  try { file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW); }
  catch (err) { if (err.code === 'ENOENT') return null; throw err; }
  let access;
  try {
    const stat = await file.stat();
    if (!designCredentialIsPrivate(path, stat)) throw new Error('The Fleet verifier credential must be service-private');
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
      method: 'POST', redirect: 'error', headers,
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(5_000)]) : AbortSignal.timeout(5_000), body: JSON.stringify(body),
    });
    if (!reply.ok) throw Object.assign(new Error('Fleet verifier API returned HTTP ' + reply.status), { status: reply.status });
    return reply.json();
  }
  async function exactEvidence(member, original) {
    const { number, head, branch } = member;
    let found;
    try { found = await op('design_verifier_evidence', { number, head, branch, ...(original && { original }) }); }
    catch (err) {
      if (err.status === 404 && !original) return null;
      if (err.status === 404 || err.status === 409)
        throw new DesignBlocked('Fleet no longer confirms this exact head, selection or original');
      throw err;
    }
    const review = found.review;
    const selected = review?.options?.find(option => option.key === review.selected);
    if (found.number !== number || found.head !== head || found.branch !== branch || !found.writer ||
        review?.projectId !== 'fleet' || review.repository !== repository || review.state !== 'selected' ||
        !review.owner || !review.questionId || !review.taskId || !review.executionId || !digest(review.digest) || !selected ||
        !Array.isArray(found.recipes) || !original && (found.recipes.length !== selected.cases.length ||
        !selected.cases.every(c => found.recipes.filter(r => r.scopeKey === c.scopeKey && digest(r.sha256)).length === 1)))
      throw new Error('Fleet did not confirm the exact admission and owner selection');
    return { found, review, selected };
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
      const admission = await exactEvidence(member);
      if (!admission) return null;
      if (!digest(admission.found.sourceSha256)) throw new Error('Fleet did not verify the selected source artifact bytes');
      return admission;
    },
    async original(member, admission, frame) {
      if (member.sourceRepositoryId !== repositoryId) throw new DesignBlocked('Original belongs to another repository');
      const { found, review, selected } = await exactEvidence(member, {
        decision: admission.review.digest, artifactId: frame.artifactId, sha256: frame.sha256,
      });
      const original = found.original;
      if (review.digest !== admission.review.digest || review.questionId !== admission.review.questionId ||
          review.executionId !== admission.review.executionId || found.writer !== admission.found.writer ||
          review.owner !== admission.review.owner || selected.key !== admission.selected.key ||
          original?.artifactId !== frame.artifactId || original.sha256 !== frame.sha256 ||
          typeof original.base64 !== 'string' || original.base64.length > 32 * 1024 * 1024)
        throw new DesignBlocked('Fleet returned a different selected original or admission');
      const bytes = Buffer.from(original.base64, 'base64');
      if (createHash('sha256').update(bytes).digest('hex') !== frame.sha256)
        throw new DesignBlocked('Selected original bytes do not match their retained hash');
      return bytes;
    },
  };
}
