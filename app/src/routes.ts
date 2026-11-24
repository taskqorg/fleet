// Route consolidation (docs/designs/fleet-ia-and-journeys.md §1, Target route column): the address bar shows the target
// route of the seven-section rail; the views still speak their earlier paths (the view-url codec, `/tasks/<id>`) until
// wave 2 moves them. The router translates both ways, so an earlier link, a bookmark or a view's own link lands on its
// target route, keeping the item and the query (UR-69).

/** Earlier path prefix and its target route, most specific first. Each pair maps one way and back. */
export const ROUTES: readonly (readonly [earlier: string, target: string])[] = [
  ['/overview', '/home'],
  ['/hosts/limits', '/system/accounts'],
  ['/hosts', '/system/hosts'],
  ['/runners', '/system/runners'],
  ['/keys', '/system/connections'],
  ['/spend', '/system/spend'],
  ['/humans', '/system/people'],
  ['/personal-lab', '/lab/mine'],
  ['/tasks', '/projects/tasks'],
  ['/results', '/projects/results'],
  ['/delivery', '/projects/delivery'],
];

/** Target-only section roots: System opens its first screen. */
const ROOTS: Record<string, string> = { '/system': '/hosts' };

/** Library speaks its target routes (pages/library.md): an earlier path moves to its Library route one way. */
const LIBRARY: readonly (readonly [earlier: string, target: string])[] = [
  ['/catalogue', '/library/methods'],
  ['/agents', '/library/agents'],
  ['/pipelines', '/library/pipelines'],
  ['/changes', '/library/changes'],
  ['/bots', '/library/bots'],
  ['/skills', '/library/skills'],
];

// `prefix` matches the path itself or the path followed by a segment.
const under = (path: string, prefix: string) => path === prefix || path.startsWith(prefix + '/');
const swap = (path: string, from: string, to: string) => to + path.slice(from.length);

/** The path a view understands for an address-bar path (target or earlier); unknown paths stay as they are. */
export function toInternal(path: string): string {
  if (path === '/' || path === '') return '/overview';
  // A project's facet (`/projects/FLT/tasks/T-FLT-42`) opens the cross-project facet with the item. `tasks`, `results`
  // and `delivery` are reserved under /projects: they name the cross-project facets, never a project.
  const facet = /^\/projects\/[^/]+\/(tasks|results|delivery)(\/.*)?$/.exec(path);
  if (facet && !['tasks', 'results', 'delivery'].includes(path.split('/')[2])) return '/' + facet[1] + (facet[2] ?? '');
  if (ROOTS[path.replace(/\/$/, '')]) return ROOTS[path.replace(/\/$/, '')];
  for (const [earlier, target] of LIBRARY) if (under(path, earlier)) return swap(path, earlier, target);
  // `/publishing` is the oldest name of Delivery.
  if (under(path, '/publishing')) return swap(path, '/publishing', '/delivery');
  for (const [earlier, target] of ROUTES) if (under(path, target)) return swap(path, target, earlier);
  return path;
}

/** The address-bar path (the target route) for a path a view understands. */
export function toTarget(path: string): string {
  const p = toInternal(path);
  for (const [earlier, target] of ROUTES) if (under(p, earlier)) return swap(p, earlier, target);
  return p;
}
