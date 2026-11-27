import { lstatSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';

export const sceneDirectory = 'packages/ui/book/scenes';
const segment = '[a-z0-9]+(?:-[a-z0-9]+)*';
const slug = new RegExp(`^${segment}$`);
const sceneKey = new RegExp(`^${segment}/${segment}/${segment}$`);
const specPath = new RegExp(`^docs/design-system/(?:pages|controls|patterns)/${segment}\\.md$`);

export type Scene = {
  key: string;
  dir: string;
  spec: string;
  route: string;
  states: Record<string, string>;
  landmark: string;
  stateLandmarks: Record<string, string>;
  details?: boolean;
};

const fail = (where: string, problem: string): never => { throw new Error(`${where}: ${problem}`); };
const regular = (path: string) => lstatSync(path).isFile() || fail(path, 'expected a regular file');
const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

export function readScenes(root: string): Scene[] {
  const scenes: Scene[] = [];
  const paths = new Map<string, string>();
  const details = new Set<string>();
  function walk(dir: string, depth: number) {
    for (const entry of readdirSync(join(root, dir), { withFileTypes: true }).sort((a, b) => a.name < b.name ? -1 : Number(a.name > b.name))) {
      const child = `${dir}/${entry.name}`;
      if (entry.isDirectory() && slug.test(entry.name)) {
        if (depth < 2) walk(child, depth + 1);
        else read(child);
      } else if (entry.name !== 'README.md' || !entry.isFile()) fail(child, 'expected a scene directory');
    }
  }
  function read(dir: string) {
    const key = dir.slice(sceneDirectory.length + 1);
    const file = join(root, dir, 'scene.json');
    for (const name of ['scene.json', 'scene.tsx', 'fixture.ts']) regular(join(root, dir, name));
    const value: unknown = JSON.parse(readFileSync(file, 'utf8'));
    if (!object(value) || Object.keys(value).some(k => !['version', 'spec', 'route', 'states', 'landmark', 'stateLandmarks', 'details'].includes(k)))
      return fail(key, 'unexpected manifest fields');
    const { version, spec, route, states, landmark, stateLandmarks, details: hasDetails } = value;
    if (version !== 1 || typeof spec !== 'string' || !specPath.test(spec)) return fail(key, 'expected a version 1 manifest and owning spec');
    const specFile = join(root, spec);
    regular(specFile);
    const backlinks = [...readFileSync(specFile, 'utf8').matchAll(/\]\(([^)\s#]+)(?:#[^)]*)?\)/g)]
      .filter(([, href]) => href.startsWith('.'))
      .map(([, href]) => join(dirname(spec), href).replaceAll('\\', '/').replace(/\/$/, ''));
    if (!backlinks.some(link => link === dir + '/scene.json' || link === dir || dir.startsWith(link + '/')))
      return fail(key, `owning spec ${spec} must link the scene or its directory`);
    if (typeof route !== 'string' || !new RegExp(`^/${segment}$`).test(route)) return fail(key, 'invalid route');
    if (!object(states) || typeof states.default !== 'string' || typeof landmark !== 'string' || !landmark.trim() ||
        (hasDetails !== undefined && typeof hasDetails !== 'boolean')) return fail(key, 'invalid states, landmark or details');
    const cases: Record<string, string> = Object.create(null);
    for (const [state, path] of Object.entries(states).sort()) {
      if (!slug.test(state) || typeof path !== 'string' || !new RegExp(`^${route}(?:/${segment})?$`).test(path)) return fail(key, `invalid state route ${state}`);
      if (paths.has(path)) return fail(key, `duplicate state route ${path} in ${paths.get(path)}`);
      paths.set(path, key);
      cases[state] = path;
    }
    const landmarks: Record<string, string> = Object.create(null);
    if (stateLandmarks !== undefined) {
      if (!object(stateLandmarks)) return fail(key, 'invalid state landmarks');
      for (const [state, selector] of Object.entries(stateLandmarks)) {
        if (!Object.hasOwn(cases, state) || typeof selector !== 'string' || !selector.trim())
          return fail(key, `invalid landmark for ${state}`);
        landmarks[state] = selector;
      }
    }
    if (hasDetails) {
      if (details.has(route)) return fail(key, `duplicate detail fallback ${route}`);
      details.add(route);
    }
    scenes.push({ key, dir, spec, route, states: cases, landmark, stateLandmarks: landmarks, details: hasDetails });
  }
  for (const dir of ['packages', 'packages/ui', 'packages/ui/book', sceneDirectory])
    if (!lstatSync(join(root, dir)).isDirectory()) fail(dir, 'expected a real directory');
  walk(sceneDirectory, 0);
  if (!scenes.length) fail(sceneDirectory, 'no scenes found');
  return scenes;
}

export function findScene(scenes: Scene[], key: string): Scene {
  if (!sceneKey.test(key)) return fail(key, 'expected an area/surface/scenario key');
  return scenes.find(scene => scene.key === key) ?? fail(key, 'scene not found');
}
