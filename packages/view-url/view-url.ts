// View URL codec (docs/designs/fleet-view-urls-and-list-path.md §1): one canonical URL per operator-app view state.
// No imports: the root build compiles it for the coordinator (view_link, state lists) and esbuild bundles it into the app.
export type ViewName = 'tasks' | 'conversations' | 'hosts' | 'overview';
export type View = {
  view: ViewName; focus?: string; group?: string; preset?: string; project?: (string | null)[];
  q?: string; sel?: string[]; state?: string[]; archived?: boolean; extra?: Record<string, string>;
};
export type PageParams = { filter: { state?: string[]; search?: string; projectId?: (string | null)[]; archived?: boolean }; group?: string };

// The one source of these lists: the coordinator's task_page and sessions_list import them.
export const TASK_STATES = ['queued', 'in_progress', 'waiting', 'paused', 'completed', 'failed', 'cancelled'] as const;
export type TaskState = typeof TASK_STATES[number];
export const SESSION_STATES = ['idle', 'preparing', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'] as const;
export type SessionState = typeof SESSION_STATES[number];
export const TASK_GROUPS = ['priority', 'state', 'project', 'agent', 'host', 'none'] as const;
export const VIEWS: ViewName[] = ['tasks', 'conversations', 'hosts', 'overview'];
const NO_PROJECT = '-';

// Values: encodeURIComponent, so ',', '%', '&', '/', '+' and '#' inside a value are always percent-encoded; then ':' and '@'
// are put back for readability (both are legal in a path segment and in a query value).
const enc = (v: string) => encodeURIComponent(v).replace(/%3A/gi, ':').replace(/%40/gi, '@');
// Query values follow form encoding ('+' is a space); a path segment does not ('+' is literal).
const dec = (v: string, query: boolean) => { try { return decodeURIComponent(query ? v.replace(/\+/g, ' ') : v); } catch { return v; } };
const sorted = (xs: string[]) => [...new Set(xs)].sort();

type Key = {
  /** Merge one raw (still encoded) occurrence into the view; repeated keys merge for lists, last wins otherwise. */
  parse: (v: View, raw: string) => void;
  /** The canonical encoded value, or undefined when the view holds the default or nothing. */
  format: (v: View) => string | undefined;
  /** Part of the list key (store and IndexedDB identity of a list). */
  list: boolean;
};
const items = (raw: string) => raw.split(',').map(x => dec(x, true)).filter(Boolean);
const listKey_ = (field: 'state' | 'sel', allowed?: readonly string[]): Key => ({
  parse: (v, raw) => { const xs = items(raw).filter(x => !allowed || allowed.includes(x)); if (xs.length) v[field] = sorted([...(v[field] ?? []), ...xs]); },
  format: v => v[field]?.length ? sorted(v[field]!).map(enc).join(',') : undefined,
  list: field !== 'sel',
});
const text = (field: 'q' | 'preset', list: boolean): Key => ({
  parse: (v, raw) => { const x = dec(raw, true); if (x) v[field] = x; },
  format: v => v[field] ? enc(v[field]!) : undefined,
  list,
});
const project: Key = {
  // A bare '-' is "no project"; a project whose id is '-' is written %2D, so the two never collide.
  parse: (v, raw) => { const xs = raw.split(',').filter(Boolean).map(x => x === NO_PROJECT ? null : dec(x, true)).filter(x => x !== ''); if (xs.length) v.project = sortProjects([...(v.project ?? []), ...xs]); },
  format: v => v.project?.length ? sortProjects(v.project).map(p => p === null ? NO_PROJECT : p === NO_PROJECT ? '%2D' : enc(p)).join(',') : undefined,
  list: true,
};
// null ("no project") sorts first, as '-' would; equal views give equal URLs and equal page params.
function sortProjects(ps: (string | null)[]): (string | null)[] {
  const ids = sorted(ps.filter((p): p is string => p !== null));
  return ps.includes(null) ? [null, ...ids] : ids;
}

// One table per view: every key the view understands, with its parse and canonical format. Anything else goes to extra.
const TABLE: Record<ViewName, Record<string, Key>> = {
  tasks: {
    group: { parse: (v, raw) => { const g = dec(raw, true); if ((TASK_GROUPS as readonly string[]).includes(g)) v.group = g; }, format: v => v.group && v.group !== 'priority' ? v.group : undefined, list: true },
    preset: text('preset', false), project, q: text('q', true), sel: listKey_('sel'), state: listKey_('state', TASK_STATES),
  },
  conversations: {
    archived: { parse: (v, raw) => { if (raw === '1') v.archived = true; }, format: v => v.archived ? '1' : undefined, list: true },
    preset: text('preset', false), q: text('q', true), sel: listKey_('sel'), state: listKey_('state', SESSION_STATES),
  },
  hosts: {}, overview: {},
};
const DEFAULT_GROUP: Partial<Record<ViewName, string>> = { tasks: 'priority' };

/**
 * Parses a path with query ('/tasks/a?state=failed') or a full URL (its pathname and search are used).
 * Returns null when the path is not an operator-app view, or when a full URL's origin is not opts.origin (when given;
 * without opts.origin any origin is accepted), so callers can reject it.
 */
export function parseView(input: string, opts: { origin?: string | null } = {}): View | null {
  let s = input;
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) {
    let u: URL; try { u = new URL(s); } catch { return null; }
    if (opts.origin !== undefined) { let want: string; try { want = new URL(opts.origin ?? '').origin; } catch { return null; } if (u.origin !== want) return null; }
    s = u.pathname + u.search;
  }
  s = s.split('#')[0];
  const qi = s.indexOf('?'), path = qi < 0 ? s : s.slice(0, qi), query = qi < 0 ? '' : s.slice(qi + 1);
  const seg = path.split('/');
  if (seg[0] !== '' || seg.length > 3 || (seg.length === 3 && !seg[2])) return null;
  const name = dec(seg[1] ?? '', false);
  if (!(VIEWS as string[]).includes(name)) return null;
  const view: View = { view: name as ViewName };
  if (seg[2]) view.focus = dec(seg[2], false);
  const table = TABLE[view.view];
  for (const part of query.split('&').filter(Boolean)) {
    const eq = part.indexOf('='), k = dec(eq < 0 ? part : part.slice(0, eq), true), raw = eq < 0 ? '' : part.slice(eq + 1);
    if (Object.hasOwn(table, k)) table[k].parse(view, raw); else (view.extra ??= {})[k] = dec(raw, true);
  }
  return view;
}

const join = (ps: [string, string][]) => ps.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([k, x]) => `${k}=${x}`).join('&');
const known = (v: View, keep: (k: Key) => boolean): [string, string][] =>
  Object.entries(TABLE[v.view]).filter(([, k]) => keep(k)).map(([name, k]) => [name, k.format(v)] as [string, string | undefined])
    .filter((p): p is [string, string] => p[1] !== undefined);

export function formatView(v: View): string {
  const ps = known(v, () => true), table = TABLE[v.view];
  for (const [k, x] of Object.entries(v.extra ?? {})) if (!Object.hasOwn(table, k)) ps.push([enc(k), enc(x)]);
  const q = join(ps);
  return `/${v.view}${v.focus ? '/' + enc(v.focus) : ''}${q ? '?' + q : ''}`;
}

/** The store and IndexedDB key of a list: view plus canonical query without focus, sel, preset and unknown keys. */
export function listKey(v: View): string {
  const q = join(known(v, k => k.list));
  return v.view + (q ? '?' + q : '');
}

export function toPageParams(v: View): PageParams {
  const filter: PageParams['filter'] = {};
  if (v.state?.length) filter.state = sorted(v.state);
  if (v.q) filter.search = v.q;
  if (v.project?.length) filter.projectId = sortProjects(v.project);
  if (v.view === 'conversations') filter.archived = !!v.archived;
  return v.view === 'tasks' ? { filter, group: v.group ?? DEFAULT_GROUP.tasks } : { filter };
}
