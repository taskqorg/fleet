import { sceneDirectory } from './scenes.ts';

// Narrow positive classification. Unknown paths, including application bindings and server behavior,
// require a trusted impact and normal-route review.
const nonvisual = path =>
  /^tests\/.*\.test\.(?:ts|mjs)$/.test(path) || /^go\/.*_test\.go$/.test(path) ||
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
