// Unprivileged producer of candidate input evidence. CI does not use it to issue design-approval.
import { execFileSync } from 'node:child_process';
import { writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { buildScene, repository } from './scene-build.mjs';
import { readScenes, sceneDirectory } from './scenes.ts';

// Narrow positive classification. Unknown paths, including application bindings and server behavior,
// require a trusted impact and normal-route review.
const nonvisual = path =>
  /^tests\/.*\.test\.(?:ts|mjs)$/.test(path) || /_test\.go$/.test(path) ||
  /^(?:README\.md|packages\/ui\/book\/README\.md)$/.test(path);

export function designImpact(paths, scenes) {
  const unknown = new Set();
  const affected = new Set();
  const owners = new Map(scenes.map(scene => [scene.dir, scene]));
  const designSpecs = new Set(scenes.map(scene => scene.spec));
  const shared = /^(?:packages\/ui\/(?:src\/|scripts\/|book\/(?!scenes\/)|tokens\.json$|icons\.json$)|packages\/view-url\/|pnpm-lock\.yaml$|package\.json$|contracts\/vocabulary\.json$|docs\/design-system\/(?:owner-rules\.md$|principles\.md$|controls\/|patterns\/))/;
  for (const path of paths) {
    if (nonvisual(path)) continue;
    if (path.startsWith(sceneDirectory + '/')) {
      const dir = path.split('/').slice(0, sceneDirectory.split('/').length + 3).join('/');
      if (owners.has(dir)) affected.add(owners.get(dir));
      else unknown.add(path); // removed or unmapped scenes need an explicit decision
    } else if (designSpecs.has(path)) {
      for (const scene of scenes) if (scene.spec === path) affected.add(scene);
    } else if (shared.test(path)) {
      for (const scene of scenes) affected.add(scene);
      // Shared sources also affect production and as-yet unmapped scenes.
      unknown.add(path);
    } else unknown.add(path);
  }
  return { affected: scenes.filter(scene => affected.has(scene)), unknown: [...unknown].sort() };
}

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
