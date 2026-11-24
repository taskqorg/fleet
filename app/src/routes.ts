// Route consolidation (docs/designs/fleet-ia-and-journeys.md §1, Target route column): the address bar shows the target
// route of the seven-section rail; the views still speak their earlier paths (the view-url codec, `/tasks/<id>`). The
// router translates both ways, so an earlier link, a bookmark or a view's own link lands on its target route, keeping the
// item and the query (UR-69).
//
// Projects (docs/design-system/pages/projects.md, Routes): a project's facet `/projects/FLT/tasks/T-FLT-42?state=x` is the
// Tasks view `/tasks/T-FLT-42?state=x&project=<id>`: the project is the list's own project filter, so every list change
// the view makes keeps it. Back to the address bar, a facet list filtered to exactly one project is that project's facet;
// any other filter is the cross-project facet `/projects/tasks`. `/projects/FLT/rules` is `/projects/FLT?facet=rules`.

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

/** The list facets of a project; under /projects these names are reserved: they name the cross-project facets, never a project. */
export const LIST_FACETS = ['tasks', 'results', 'delivery'] as const;
export type ListFacet = (typeof LIST_FACETS)[number];
const isListFacet = (s: string | undefined): s is ListFacet => (LIST_FACETS as readonly string[]).includes(s ?? '');

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

// Project codes (D17): the address bar names a project by its code, the views and the server by its id. Filled from
// project_list (setProjectCodes); until then, or for a project without a code, the id stands in both ways.
const codeById = new Map<string, string>();
const idByCode = new Map<string, string>();
/** Records each project's code; answers whether anything changed. */
export function setProjectCodes(ps: readonly { id: string; code?: string | null }[]): boolean {
  let changed = false;
  for (const p of ps) {
    if (!p.code || codeById.get(p.id) === p.code) continue;
    codeById.set(p.id, p.code);
    idByCode.set(p.code.toUpperCase(), p.id);
    changed = true;
  }
  return changed;
}
/** The project id an address-bar segment names (a code, case-insensitive, or the id itself). */
export const projectIdOf = (seg: string) => idByCode.get(seg.toUpperCase()) ?? seg;
/** The segment that names a project in the address bar: its code, else its id. */
export const projectSegment = (id: string) => codeById.get(id) ?? id;

// `prefix` matches the path itself or the path followed by a segment.
const under = (path: string, prefix: string) => path === prefix || path.startsWith(prefix + '/');
const swap = (path: string, from: string, to: string) => to + path.slice(from.length);
const enc = (s: string) => encodeURIComponent(s);
const dec = (s: string) => { try { return decodeURIComponent(s); } catch { return s; } };

type Url = { path: string; query: string };
const split = (url: string): Url => {
  const h = url.indexOf('#'), u = h < 0 ? url : url.slice(0, h), q = u.indexOf('?');
  return q < 0 ? { path: u, query: '' } : { path: u.slice(0, q), query: u.slice(q + 1) };
};
const join = (u: Url) => u.path + (u.query ? '?' + u.query : '');
const parts = (query: string) => query.split('&').filter(Boolean);
const keyOf = (part: string) => dec(part.split('=')[0]);
const without = (query: string, key: string) => parts(query).filter(p => keyOf(p) !== key).join('&');
const valueOf = (query: string, key: string): string | undefined => {
  const p = parts(query).findLast(x => keyOf(x) === key);
  return p === undefined ? undefined : dec((p.split('=')[1] ?? '').replace(/\+/g, ' '));
};

/** The path a view understands for an address-bar path (target or earlier); unknown paths stay as they are. */
export function toInternal(path: string): string {
  return internalUrl(path);
}

/** The address-bar path (the target route) for a path a view understands. */
export function toTarget(path: string): string {
  return targetUrl(path);
}

/** The path and query a view understands for an address-bar path and query. */
export function internalUrl(url: string): string {
  const { path, query } = split(url);
  if (path === '/' || path === '') return join({ path: '/overview', query });
  const seg = path.split('/');
  if (seg[1] === 'projects' && seg[2] && !isListFacet(seg[2])) {
    const id = projectIdOf(dec(seg[2]));
    // A project's list facet: the cross-project view filtered to that project (its own project filter is replaced).
    if (isListFacet(seg[3])) return join({ path: '/' + seg.slice(3).join('/'), query: [without(query, 'project'), 'project=' + enc(id)].filter(Boolean).join('&') });
    if (seg[3] === 'rules' && seg.length === 4) return join({ path: '/projects/' + enc(id), query: [without(query, 'facet'), 'facet=rules'].filter(Boolean).join('&') });
    if (seg.length === 3 || (seg.length === 4 && seg[3] === '')) return join({ path: '/projects/' + enc(id), query });
  }
  const root = ROOTS[path.replace(/\/$/, '')];
  if (root) return join({ path: root, query });
  // A skill once opened as /agents/skill:<id>.
  const skill = /^\/(?:library\/)?agents\/skill:([^/]+)\/?$/.exec(path);
  if (skill) return join({ path: '/library/skills/' + skill[1], query });
  for (const [earlier, target] of LIBRARY) if (under(path, earlier)) return join({ path: swap(path, earlier, target), query });
  // `/publishing` is the oldest name of Delivery.
  if (under(path, '/publishing')) return join({ path: swap(path, '/publishing', '/delivery'), query });
  for (const [earlier, target] of ROUTES) if (under(path, target)) return join({ path: swap(path, target, earlier), query });
  return join({ path, query });
}

/** The address-bar path and query (the target route) for a path and query a view understands, or for an earlier one. */
export function targetUrl(url: string): string {
  const { path, query } = split(internalUrl(url));
  const seg = path.split('/');
  // A list facet filtered to exactly one project is that project's facet.
  // System's idle page is the section itself (pages/system.md: `/system`).
  if (path === '/hosts' || path === '/hosts/') return join({ path: '/system', query });
  if (isListFacet(seg[1])) {
    const ps = valueOf(query, 'project')?.split(',').filter(Boolean) ?? [];
    if (ps.length === 1 && ps[0] !== '-') return join({ path: '/projects/' + enc(projectSegment(ps[0])) + path, query: without(query, 'project') });
  }
  if (seg[1] === 'projects' && seg[2] && seg.length === 3) {
    const at = '/projects/' + enc(projectSegment(dec(seg[2])));
    return valueOf(query, 'facet') === 'rules' ? join({ path: at + '/rules', query: without(query, 'facet') }) : join({ path: at, query: without(query, 'facet') });
  }
  for (const [earlier, target] of ROUTES) if (under(path, earlier)) return join({ path: swap(path, earlier, target), query });
  return join({ path, query });
}
