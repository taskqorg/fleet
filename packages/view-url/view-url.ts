// View URL codec (docs/designs/fleet-view-urls-and-list-path.md §1): one canonical URL per operator-app view state.
// No imports: the root build compiles it for the coordinator (view_link) and esbuild bundles it into the app.
export type ViewName = 'tasks' | 'conversations' | 'hosts' | 'overview';
export type View = {
  view: ViewName; focus?: string; group?: string; preset?: string; project?: (string | null)[];
  q?: string; sel?: string[]; state?: string[]; archived?: boolean; extra?: Record<string, string>;
};
export type PageParams = { filter: { state?: string[]; search?: string; projectId?: (string | null)[]; archived?: boolean }; group?: string };

const TASK_STATES = ['queued', 'in_progress', 'waiting', 'paused', 'completed', 'failed', 'cancelled'];
const SESSION_STATES = ['idle', 'preparing', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'];
const GROUPS = ['priority', 'state', 'project', 'agent', 'host', 'none'];
// Keys each view understands; anything else is kept in extra. Default group is omitted from the URL.
const KEYS: Record<ViewName, string[]> = {
  tasks: ['group', 'preset', 'project', 'q', 'sel', 'state'],
  conversations: ['archived', 'preset', 'q', 'sel', 'state'],
  hosts: [], overview: [],
};
const DEFAULT_GROUP: Partial<Record<ViewName, string>> = { tasks: 'priority' };
const VIEWS = Object.keys(KEYS) as ViewName[];
const NO_PROJECT = '-';

// Comma, percent and ampersand inside a value are percent-encoded; other characters stay readable.
const enc = (v: string) => encodeURIComponent(v).replace(/%2F/gi, '/').replace(/%3A/gi, ':').replace(/%40/g, '@');
const dec = (v: string) => { try { return decodeURIComponent(v.replace(/\+/g, ' ')); } catch { return v; } };
const list = (raw: string) => [...new Set(raw.split(',').map(dec).filter(Boolean))];
const sorted = (xs: string[]) => [...new Set(xs)].sort();

export function parseView(pathAndQuery: string): View {
  const qi = pathAndQuery.indexOf('?');
  const path = (qi < 0 ? pathAndQuery : pathAndQuery.slice(0, qi)).split('#')[0];
  const query = qi < 0 ? '' : pathAndQuery.slice(qi + 1).split('#')[0];
  const [, name = '', focus] = path.split('/');
  const view: View = { view: (VIEWS as string[]).includes(name) ? name as ViewName : 'overview' };
  if (focus) view.focus = dec(focus);
  for (const part of query.split('&').filter(Boolean)) {
    const eq = part.indexOf('='), k = dec(eq < 0 ? part : part.slice(0, eq)), raw = eq < 0 ? '' : part.slice(eq + 1);
    if (!KEYS[view.view].includes(k)) { (view.extra ??= {})[k] = dec(raw); continue; }
    if (k === 'state') { const ok = view.view === 'tasks' ? TASK_STATES : SESSION_STATES; const s = list(raw).filter(x => ok.includes(x)); if (s.length) view.state = s; }
    else if (k === 'group') { const g = dec(raw); if (GROUPS.includes(g)) view.group = g; }
    else if (k === 'project') { const p = list(raw).map(x => x === NO_PROJECT ? null : x); if (p.length) view.project = p; }
    else if (k === 'sel') { const s = list(raw); if (s.length) view.sel = s; }
    else if (k === 'q') { const q = dec(raw); if (q) view.q = q; }
    else if (k === 'preset') { const p = dec(raw); if (p) view.preset = p; }
    else if (k === 'archived') { if (raw === '1') view.archived = true; }
  }
  return view;
}

/** Known keys of a view as canonical [key, encoded value] pairs, defaults and empties omitted. */
function pairs(v: View, keep: (k: string) => boolean): [string, string][] {
  const out: [string, string][] = [];
  const add = (k: string, val: string | undefined) => { if (val !== undefined && val !== '' && keep(k)) out.push([k, val]); };
  const keys = KEYS[v.view];
  if (keys.includes('archived') && v.archived) add('archived', '1');
  if (keys.includes('group') && v.group && v.group !== DEFAULT_GROUP[v.view]) add('group', enc(v.group));
  if (keys.includes('preset') && v.preset) add('preset', enc(v.preset));
  if (keys.includes('project') && v.project?.length) add('project', sorted(v.project.map(p => p === null ? NO_PROJECT : p)).map(enc).join(','));
  if (keys.includes('q') && v.q) add('q', enc(v.q));
  if (keys.includes('sel') && v.sel?.length) add('sel', sorted(v.sel).map(enc).join(','));
  if (keys.includes('state') && v.state?.length) add('state', sorted(v.state).map(enc).join(','));
  return out;
}
const join = (ps: [string, string][]) => ps.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([k, x]) => `${k}=${x}`).join('&');

export function formatView(v: View): string {
  const ps = pairs(v, () => true);
  for (const [k, x] of Object.entries(v.extra ?? {})) if (!KEYS[v.view].includes(k)) ps.push([enc(k), enc(x)]);
  const q = join(ps);
  return `/${v.view}${v.focus ? '/' + enc(v.focus) : ''}${q ? '?' + q : ''}`;
}

/** The store and IndexedDB key of a list: view plus canonical query without focus, sel, preset and unknown keys. */
export function listKey(v: View): string {
  const q = join(pairs(v, k => k !== 'sel' && k !== 'preset'));
  return v.view + (q ? '?' + q : '');
}

export function toPageParams(v: View): PageParams {
  const filter: PageParams['filter'] = {};
  if (v.state?.length) filter.state = sorted(v.state);
  if (v.q) filter.search = v.q;
  if (v.project?.length) filter.projectId = v.project;
  if (v.view === 'conversations') filter.archived = !!v.archived;
  return v.view === 'tasks' ? { filter, group: v.group ?? DEFAULT_GROUP.tasks } : { filter };
}
