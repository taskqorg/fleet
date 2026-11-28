import { mkdir, open, readFile, rename, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { designCheckDigest, readDesignCheckReceipt } from '../../packages/ui/scripts/design-github-checks.mjs';
import { appId, installationId, repositoryId, DesignBlocked } from './provider.mjs';

// The state directory is private to this single systemd service. Persist before the check write.
async function durableJSON(dir, name, value) {
  const target = join(dir, name);
  const body = JSON.stringify(value) + '\n';
  try { if (await readFile(target, 'utf8') === body) return; }
  catch (err) { if (err.code !== 'ENOENT') throw err; }
  const temp = target + '.tmp';
  await rm(temp, { force: true });
  const file = await open(temp, 'wx', 0o600);
  try {
    await file.writeFile(body);
    await file.sync();
  } finally {
    await file.close();
  }
  await rename(temp, target);
  const directory = await open(dir, 'r');
  try { await directory.sync(); } finally { await directory.close(); }
}

export async function receiptStore(state, revision, checks) {
  const dir = join(state, 'receipts');
  await mkdir(dir, { mode: 0o700, recursive: true });
  const issuer = { appId, installationId, repositoryId, revision };
  return {
    async retain(evidence, conclusion) {
      const record = { issuer, evidence, conclusion };
      const id = designCheckDigest(record);
      await durableJSON(dir, id + '.json', record);
      return id;
    },
    async recordReadback(id, check) {
      await durableJSON(dir, id + '.check.json', check);
    },
    async reportReadback(check, report) {
      const name = check.externalId + '.fleet.json';
      try {
        if (JSON.parse(await readFile(join(dir, name), 'utf8')).checkRunId === check.checkRunId) return;
      } catch (err) { if (err.code !== 'ENOENT') throw err; }
      await report();
      await durableJSON(dir, name, { checkRunId: check.checkRunId });
    },
    async readPR(member) {
      const run = await checks.find(member.head);
      const id = run?.external_id;
      if (!/^[a-f0-9]{64}$/.test(id ?? '') || !['success', 'failure'].includes(run.conclusion)) return null;
      let record;
      try { record = JSON.parse(await readFile(join(dir, id + '.json'), 'utf8')); }
      catch (err) { if (err.code === 'ENOENT') return null; throw err; }
      if (designCheckDigest(record) !== id || JSON.stringify(record.issuer) !== JSON.stringify(issuer) ||
          record.conclusion !== run.conclusion || record.evidence.kind !== 'pull_request' ||
          record.evidence.number !== member.number || record.evidence.base !== member.base ||
          record.evidence.source?.branch !== member.branch || record.evidence.source?.repositoryId !== member.sourceRepositoryId) return null;
      const readback = readDesignCheckReceipt(await checks.read(run.id), { appId, head: member.head, externalId: id, conclusion: run.conclusion });
      await this.recordReadback(id, readback);
      if (run.conclusion === 'failure') throw new DesignBlocked('An original PR head has a protected design failure');
      return { ...record.evidence, externalId: id };
    },
    async observe(value) {
      await durableJSON(state, 'status.json', value);
    },
  };
}
