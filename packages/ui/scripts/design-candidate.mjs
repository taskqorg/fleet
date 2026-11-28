// Unprivileged producer of candidate input evidence. CI does not use it to issue design-approval.
import { execFileSync } from 'node:child_process';
import { writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { buildScene, repository } from './scene-build.mjs';
import { readScenes } from './scenes.ts';

import { designImpact } from './design-impact.mjs';
export { designImpact } from './design-impact.mjs';

const git = (...args) => execFileSync('git', args, { cwd: repository, encoding: 'utf8' });

if (process.argv[1] === import.meta.filename) {
  const [, , base, expected, kind, out] = process.argv;
  if (![base, expected].every(value => /^[0-9a-f]{40}$/.test(value)) ||
      !['pull_request', 'merge_group'].includes(kind) || !out)
    throw new Error('Usage: node packages/ui/scripts/design-candidate.mjs <base-sha> <head-sha> <pull_request|merge_group> <output>');
  const head = git('rev-parse', 'HEAD').trim();
  if (head !== expected || git('status', '--porcelain').trim())
    throw new Error('Candidate tree does not match the clean event head');
  const mergeBase = git('merge-base', base, head).trim();
  if (kind === 'merge_group' && mergeBase !== base) throw new Error('Merge-group base is not an ancestor of the candidate');
  const paths = git('diff', '--no-renames', '--name-only', '-z', mergeBase, head).split('\0').filter(Boolean);
  const { affected, unknown } = designImpact(paths, readScenes(repository));
  const cases = [];
  for (const scene of affected) {
    for (const state of Object.keys(scene.states)) {
      const { recipe } = await buildScene(scene, state);
      cases.push({ scopeKey: `${scene.key}:${state}`, recipe: recipe.digest });
    }
  }
  const classification = unknown.length ? 'impact-review-needed' : cases.length ? 'owner-evidence-needed' : 'nonvisual';
  writeFileSync(resolve(out), JSON.stringify({ version: 1, kind, head, base: mergeBase, classification, cases, unknown }, null, 2) + '\n');
  console.log(`Design candidate evidence: ${classification}, ${cases.length} recipe(s), ${unknown.length} unmapped path(s)`);
}
