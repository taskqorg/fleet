// Fleet operator app entry (docs/designs/fleet-operator-app.md): shell, routes, identity check, feed start, service worker.
// errors.ts first: error tracking starts before the other modules run.
import { captureRender, configureErrors } from './errors.ts';
import { render, Component, type ComponentChildren } from 'preact';
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Router, Route, Switch, Redirect, useLocation, useSearch } from 'wouter-preact';
import { AppShell, EntityCards, Empty, Button, ListDetail, workspaces, matchSearch, FocusControl, FocusPresets, type NavItem, type AppScreen, type SpotlightProvider, type SpotlightItem, type IconName, type Family } from '../../packages/ui/src/index.ts';
import { startVersionChecks, versionInfo, tabVersion } from './version.ts';
import { access, accessGeneration, accessSignal, ensureAccessGeneration, invalidateAccess, registerAccessValidator, AccessChanged } from './access.ts';
import { op, accessControlOp, signedOut, OpError, type Ops } from './api.ts';
import { parseView, formatView, VIEWS, type ViewName } from '../../packages/view-url/view-url.ts';
import { prefs, prefsRead, loadPrefs, savePrefs, setWidth, setFocus, setIgnored, saveFocusPreset, removeFocusPreset, noteView, sectionPath, saveOnLeave, outsideFocus, scopeIds } from './prefs.ts';
import { useOp, touchedBy, setIdentity } from './store.ts';
import { useNavCounts } from './nav-counts.ts';
import { entityCards } from './entity-cards.ts';
import { startFeed, stopFeed, subscribe } from './feed.ts';
import { startStore } from './store.ts';
import { clearAll } from './offline.ts';

/** Dynamically loads a routed screen chunk once, caches it, and forwards load failures to RenderBoundary. */
function lazy<P extends object>(load: () => Promise<(props: P) => ComponentChildren>): (props: P) => ComponentChildren {
  let cached: ((props: P) => ComponentChildren) | null = null;
  let pending: Promise<void> | null = null;
  return function LazyView(props: P): ComponentChildren {
    const [View, setView] = useState<((props: P) => ComponentChildren) | null>(() => cached);
    const [err, setErr] = useState<unknown>(null);
    useEffect(() => {
      if (cached) { setView(() => cached); return; }
      let live = true;
      (pending ??= load().then(c => { cached = c; })).then(
        () => { if (live) setView(() => cached); },
        e => { pending = null; if (live) setErr(() => e); },
      );
      return () => { live = false; };
    }, []);
    if (err) throw err;
    return View ? <View {...props} /> : <Empty text="Loading…" />;
  };
}

const Bots = lazy(async () => (await import('./views/bots.tsx')).Bots);
const Projects = lazy(async () => (await import('./views/projects.tsx')).Projects);
const Overview = lazy(async () => (await import('./views/overview.tsx')).Overview);
const Tasks = lazy(async () => (await import('./views/tasks.tsx')).Tasks);
const Conversations = lazy(async () => (await import('./views/conversations.tsx')).Conversations);
const Results = lazy(async () => (await import('./views/results.tsx')).Results);
const Delivery = lazy(async () => (await import('./views/delivery.tsx')).Delivery);
const Infrastructure = lazy(async () => (await import('./views/hosts.tsx')).Hosts);
const Lab = lazy(async () => (await import('./views/lab.tsx')).Lab);
const Library = lazy(async () => (await import('./views/agents.tsx')).Agents);
const Spend = lazy(async () => (await import('./views/spend.tsx')).Spend);

// Not reported: an operation's own failure (OpError) is in the coordinator's spans, and an access change aborts reads on
// purpose. The release is the version this tab runs.
configureErrors({ ignore: e => e instanceof OpError || e instanceof AccessChanged, release: () => tabVersion.value });

/** A render crash shows a recoverable error in the work area instead of a blank page; navigating away clears it. */
class RenderBoundary extends Component<{ at: string; children: ComponentChildren }, { failed: boolean }> {
  state = { failed: false };
  componentDidCatch(error: unknown) { captureRender(error); this.setState({ failed: true }); }
  componentDidUpdate(prev: { at: string }) { if (prev.at !== this.props.at && this.state.failed) this.setState({ failed: false }); }
  render() {
    return this.state.failed ? <Empty text="This page failed to render." action={<Button label="Reload" icon="rotate-ccw" onClick={() => location.reload()} />} /> : this.props.children;
  }
}

// The view of each screen the registry marks `app: true` (the type requires every one). Nav and routes follow registry
// order. A list screen takes an optional focus segment, so focusing a row never unmounts the list.
const SCREENS: Record<AppScreen, { focus: boolean; view: (p: { id?: string }) => ComponentChildren }> = {
  bots: { focus: true, view: p => <Bots id={p.id} /> },
  projects: { focus: true, view: p => <Projects id={p.id} /> },
  overview: { focus: true, view: p => <Overview id={p.id} /> },
  tasks: { focus: true, view: p => <Tasks id={p.id} /> },
  conversations: { focus: true, view: p => <Conversations id={p.id} /> },
  results: { focus: true, view: p => <Results id={p.id} /> },
  delivery: { focus: true, view: p => <Delivery id={p.id} /> },
  hosts: { focus: true, view: p => <Infrastructure section="hosts" id={p.id} /> },
  runners: { focus: false, view: () => <Infrastructure section="runners" /> },
  lab: { focus: true, view: p => <Lab id={p.id} /> },
  keys: { focus: true, view: p => <Infrastructure section="keys" id={p.id} /> },
  agents: { focus: true, view: p => <Library section="agents" id={p.id} /> },
  pipelines: { focus: true, view: p => <Library section="pipelines" id={p.id} /> },
  catalogue: { focus: true, view: p => <Library section="catalogue" id={p.id} /> },
  changes: { focus: false, view: () => <Library section="changes" /> },
  spend: { focus: false, view: () => <Spend /> },
};
const BUILT = workspaces.filter(w => w.app).map(w => ({ ...w, ...SCREENS[w.id as AppScreen] }));
const NAV: NavItem[] = BUILT.filter(w => w.nav);
const LIBRARY_SECTIONS = new Set(['agents', 'pipelines', 'catalogue', 'changes']);
const INFRA_SECTIONS = new Set(['hosts', 'runners', 'keys']);
// Each kind the server finds: its group label, icon, the registry state family of its hits, where a hit opens (a question
// opens its task) and the list section that takes the same query (the "All N" row).
type Hit = Ops['search']['reply']['hits'][number];
const ws = (id: string) => workspaces.find(x => x.id === id)!;
const itemPath = (id: string, x: string) => `/${id}/` + encodeURIComponent(x);
const KINDS: Record<Hit['kind'], { label: string; icon: IconName; family?: Family; list?: ViewName; path: (h: Hit) => string }> = {
  task: { label: ws('tasks').label, icon: ws('tasks').icon, family: 'task', list: 'tasks', path: h => itemPath('tasks', h.id) },
  question: { label: 'Questions', icon: 'circle-question-mark', family: 'question', path: h => h.taskId ? itemPath('tasks', h.taskId) : h.sessionId ? itemPath('conversations', h.sessionId) : '/overview' },
  conversation: { label: ws('conversations').label, icon: ws('conversations').icon, family: 'session', list: 'conversations', path: h => itemPath('conversations', h.id) },
  result: { label: ws('results').label, icon: ws('results').icon, family: 'artifact', list: 'results', path: h => itemPath('results', h.id) },
  project: { label: ws('projects').label, icon: ws('projects').icon, path: h => itemPath('projects', h.id) },
  host: { label: 'Hosts', icon: ws('hosts').icon, path: h => itemPath('hosts', h.id) },
  agent: { label: 'Agents', icon: 'bot', path: h => itemPath('agents', h.id) },
  pipeline: { label: ws('pipelines').label, icon: ws('pipelines').icon, path: h => itemPath('pipelines', h.id) },
};

/** Spotlight provider for deep-linked workspaces and consolidated sub-pages not shown directly in the 8-section rail. */
const subPages = (go: (to: string) => void): SpotlightProvider => ({
  id: 'pages',
  label: 'Pages',
  find: q => [
    ...BUILT.filter(w => !w.nav).map(w => ({ id: w.id, label: w.label, icon: w.icon })),
    { id: 'agents', label: 'Agents', icon: 'bot' as const },
    { id: 'hosts', label: 'Hosts', icon: 'panels-top-left' as const },
  ]
    .filter(s => matchSearch(q, s.label))
    .map(s => ({ key: 'sub:' + s.id, icon: s.icon, label: s.label, run: () => go('/' + s.id) })),
});

// The project focus (OR-34): '-' is work outside projects; an empty focus holds everything. The server marks hits outside it.
const toggleFocus = (id: string) => { const f = prefs.value.focus; setFocus(f.includes(id) ? f.filter(x => x !== id) : [...f, id]); };

// Spotlight (OR-32): one provider per kind the server's `search` operation finds (agents call the same tool). The kinds of
// one query and focus share one request, sent 150 ms after the last key and reused while pending (up to 10 s) and for 2 s
// after; a failed one is not reused. The server puts each kind's hits in the focus first and counts what its list shows.
type Reply = Ops['search']['reply'];
let last: { key: string; at: number; reply: Promise<Reply> } | null = null;
const found = (kind: Hit['kind'], go: (to: string) => void): SpotlightProvider => {
  const w = KINDS[kind];
  return { id: kind, label: w.label, find: async (query, signal) => {
    // The focus without ignored projects, as lists apply it; the server leaves ignored projects out itself.
    const { ignored } = prefs.value, focus = scopeIds().projectIds ?? [], key = JSON.stringify([query, focus, ignored]);
    const fresh = () => last?.key === key && Date.now() - last.at < 2000;
    if (!fresh()) {
      await new Promise(r => setTimeout(r, 150));
      if (signal.aborted) return [];
      if (!fresh()) { const reply = op('search', { query, ...(focus.length ? { projectIds: focus } : {}) }); const l = last = { key, at: Date.now() + 8000, reply }; reply.then(() => { l.at = Date.now(); }, () => { if (last === l) last = null; }); }
    }
    const r = await last!.reply, hits = r.hits.filter(h => h.kind === kind);
    const items: SpotlightItem[] = hits.map(h => {
      const pinned = kind === 'project' && focus.includes(h.id), outside = !!h.outside, at = h.at ? Date.parse(h.at) : NaN;
      // The project is not repeated on every row when a one-project focus already names it (OR-17).
      return { key: kind + ':' + h.id, icon: w.icon, label: h.title, code: h.code ?? undefined, sub: kind === 'project' || focus.length === 1 && focus[0] === (h.projectId ?? '-') ? undefined : h.projectId ?? undefined,
        state: w.family && h.state ? { family: w.family, state: h.state } : undefined, at: Number.isFinite(at) ? at : undefined, outside, snippet: h.snippet ?? undefined,
        // An ignored project (found by naming it) cannot join the focus until it is restored, so it offers no focus action.
        action: kind === 'project' && !ignored.includes(h.id) ? { label: pinned ? 'Remove from focus' : 'Add to focus', icon: 'focus' as const, pressed: pinned, run: () => toggleFocus(h.id) } : undefined,
        run: () => go(w.path(h)) };
    });
    // "All N in <Section>": the list holds more than the focused rows shown; it opens with the same query (OR-36).
    const total = r.counts[kind] ?? 0, shown = items.filter(i => !i.outside).length;
    if (w.list && total > shown) items.push({ key: 'all:' + kind, icon: w.icon, label: `All ${total.toLocaleString('en')} in ${w.label}`, more: true, run: () => go(formatView({ view: w.list!, q: query })) });
    return items;
  } };
};

function App() {
  const [loc, go] = useLocation();
  const query = useSearch();
  const current = loc.split('/')[1] || 'overview';
  const activeNav = LIBRARY_SECTIONS.has(current) ? 'agents' : INFRA_SECTIONS.has(current) ? 'hosts' : current;
  const shellTitle = BUILT.find(w => w.id === activeNav)?.label ?? BUILT.find(w => w.id === current)?.label;
  const search = useRef([subPages(go), ...(Object.keys(KINDS) as Hit['kind'][]).map(k => found(k, go))]).current;
  // Pinned sections and the other preferences are person settings (prefs.ts), so they follow the person to every device.
  // Preferences load only after authorized baseline activation.
  // Focus and online check the person quietly (below); preferences changed elsewhere arrive as setting-change events.
  const [user, setUser] = useState<string | null>(null);
  // OR-23: the shell shows once the first settings read answers, or after 1.5 s at most; a later store clear does not hide it again.
  const [shown, setShown] = useState(false);
  useEffect(() => { if (prefsRead.value) setShown(true); }, [prefsRead.value]);
  useEffect(() => { const t = setTimeout(() => setShown(true), 1500); return () => clearTimeout(t); }, []);
  const geometry = useRef({ widths: prefs.value.widths, navCollapsed: prefs.value.navCollapsed });
  if (access.value.state === 'open' && prefsRead.value) geometry.current = { widths: prefs.value.widths, navCollapsed: prefs.value.navCollapsed };
  const pins = prefs.value.pins, savePins = (ids: string[]) => savePrefs(() => ({ pins: ids }), { pins: ids });
  useEffect(() => {
    const unregister = registerAccessValidator(async ({ generation, signal }) => {
      last = null;
      const clear = clearAll();
      const m = await accessControlOp('me', {}, { signal });
      await clear; if (signal.aborted || generation !== accessGeneration()) throw Error('Access changed');
      if (!m.email) throw Error('Sign in to continue');
      setUser(m.name || m.email);
      return { identityKey: m.email, grantsVersion: m.grantsVersion ?? '' };
    });
    startFeed(null); startStore();
    const validate = () => { invalidateAccess('focus'); last = null; void ensureAccessGeneration('focus').then(() => loadPrefs()).catch(() => {}); };
    // Focus and online check the person quietly: the same identity and grants keep everything on screen (the change
    // stream brings what happened meanwhile); a different person, changed grants or a closed gate fence and validate as
    // at start (docs/designs/fleet-analytics.md, the access gate). Offline keeps what is shown; signed out has its own path.
    let checking = false, again = false;
    const recheck = async (): Promise<void> => {
      const s = access.value;
      // Not open (closed, or a validation still running): validate as before, which restarts a running one.
      if (s.state !== 'open' || !s.identity) return validate();
      // A focus during a quiet check is checked right after it, so a change in between is never missed.
      if (checking) { again = true; return; }
      checking = true;
      try {
        const m = await accessControlOp('me', {}, { signal: AbortSignal.any([accessSignal(s.generation), AbortSignal.timeout(10000)]) });
        const cur = access.value;
        if (cur.generation !== s.generation || cur.state !== 'open') return;
        // The same person: nothing to redo; preferences and data stay, and the change stream brings what changed.
        if (m.email !== s.identity.identityKey || (m.grantsVersion ?? '') !== s.identity.grantsVersion) validate();
      } catch (e) {
        if (access.value.generation !== s.generation || access.value.state !== 'open') return;
        // Offline keeps what is shown, and signed out has its own path; any other failure (a timeout, a 5xx, an
        // incomplete reply) leaves the person unverified, so the gate validates as at start.
        if (!(e instanceof OpError && (e.status === 0 && e.message === 'offline' || e.status === 401 || e.status === 403))) validate();
      } finally { checking = false; if (again) { again = false; void recheck(); } }
    };
    // Preferences changed on another device or by an agent (person_settings_set writes a setting-change for this person).
    setIdentity(() => access.value.identity?.identityKey);
    const offPrefs = subscribe({ resync: () => { void loadPrefs(); }, changes: cs => { if (cs.some(c => touchedBy(c, 'person_settings_get', {}))) void loadPrefs(); } });
    validate(); addEventListener('focus', recheck); addEventListener('online', recheck);
    if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
    const stopChecks = startVersionChecks();
    return () => { unregister(); stopFeed(); offPrefs(); stopChecks(); removeEventListener('focus', recheck); removeEventListener('online', recheck); };
  }, []);
  useEffect(() => { if (signedOut.value) { invalidateAccess('signed out'); void clearAll(true); } }, [signedOut.value]);
  // Each section's list view is noted as it changes and saved when the person leaves the section or the tab hides, so the
  // navigation returns to it (prefs.ts). The open item and checked rows are not part of it (view-url drops focus and sel).
  // Unsent list group toggles are sent on the same section leave.
  const left = useRef<string | null>(null);
  useEffect(() => {
    if (access.value.state !== 'open' || !prefsRead.value || !BUILT.some(n => n.id === current)) return;
    const parsed = (VIEWS as string[]).includes(current) ? parseView(loc + (query ? '?' + query : '')) : null;
    // The Overview keeps no view of its own: /overview?all (every project, from the outside-focus count) is never kept.
    noteView(current, parsed && current !== 'overview' ? formatView({ ...parsed, focus: undefined, sel: undefined }) : '/' + current);
    if (left.current && left.current !== current) saveOnLeave(left.current, false);
    left.current = current;
  }, [loc, query, access.value.state, prefsRead.value]);
  useEffect(() => {
    // Unsent list group toggles go with them, in one keepalive request: a hidden tab may be closed without pagehide
    // (prefs.ts saveOnLeave; a section leave sends the same request without keepalive above).
    const flush = () => { if (document.visibilityState === 'hidden') saveOnLeave(left.current); };
    const leave = () => saveOnLeave(left.current);
    document.addEventListener('visibilitychange', flush); addEventListener('pagehide', leave);
    return () => { document.removeEventListener('visibilitychange', flush); removeEventListener('pagehide', leave); };
  }, []);
  // The project focus and ignored projects (OR-34) are person settings; lists apply them where their view names no projects.
  // The empty palette offers the same presets as the focus chip, without ignored projects.
  const { focus, focusPresets, ignored } = prefs.value;
  // Each section's counter (nav-rail.md, Counters); Needs you is the head chip, not a counter.
  const counts = useNavCounts(access.value.state === 'open');
  const nav = NAV.map(n => counts[n.id] ? { ...n, count: counts[n.id] } : n);
  // Entity cards (EntityCode) read entity_card through the store; their Open goes to the in-app path.
  const cards = useMemo(() => entityCards(go), [go]);
  return <EntityCards.Provider value={cards}><AppShell privateReady={access.value.state === 'open'} privateGeneration={access.value.generation} nav={nav} current={activeNav} title={shellTitle} waiting={0} onGo={id => go(sectionPath(id))} pins={access.value.state === 'open' ? pins : []} onPins={savePins} search={access.value.state === 'open' ? search : []} ready={shown}
    searchIdle={<FocusPresets icon value={focus.filter(p => !ignored.includes(p))} presets={focusPresets} ignored={ignored} onChange={setFocus} />}
    focus={access.value.state === 'open' ? <Focus go={go} /> : undefined}
    widths={geometry.current.widths} onWidth={setWidth} collapsed={geometry.current.navCollapsed} onCollapsed={on => savePrefs(() => ({ navCollapsed: on }), { navCollapsed: on })}
    // Logout clears this person's stored data first (at most 2 s: a blocked IndexedDB delete must not keep the person signed in), then ends the Cloudflare Access session.
    user={access.value.state === 'open' && user ? { name: user, onLogout: () => { Promise.race([clearAll(true), new Promise(r => setTimeout(r, 2000))]).finally(() => location.assign('/cdn-cgi/access/logout')); }, version: versionInfo() } : undefined}>
    {signedOut.value && <div class="problem failure" role="alert"><b>Session expired</b><span class="facts">Sign in again to continue; stored data was cleared.</span><span class="acts"><Button label="Sign in again" kind="primary" onClick={() => location.reload()} /></span></div>}
    {access.value.state === 'open' ? (
      <RenderBoundary at={access.value.generation + ':' + loc + '?' + query}><PrivateRoutes key={access.value.generation} go={go} /></RenderBoundary>
    ) : current === 'overview' ? <Empty text="Validating access…" /> : <ListDetail list={<Empty text="Validating access…" />} detail={<Empty text="Waiting for authorized data." />} detailKey="access" />}
  </AppShell></EntityCards.Provider>;
}

const decodeSegment = (s: string): string => { try { return decodeURIComponent(s); } catch { return s; } };

function PrivateRoutes({ go }: { go: (to: string) => void }) {
  const [loc] = useLocation();
  const libMatch = /^\/(agents|pipelines|catalogue)(?:\/([^/]+))?\/?$|^\/(changes)\/?$/.exec(loc);
  if (libMatch) {
    const section = (libMatch[1] ?? libMatch[3]) as 'agents' | 'pipelines' | 'catalogue' | 'changes';
    const id = libMatch[2] ? decodeSegment(libMatch[2]) : undefined;
    return <Library section={section} id={id} />;
  }
  const infraMatch = /^\/(hosts|keys)(?:\/([^/]+))?\/?$|^\/(runners)\/?$/.exec(loc);
  if (infraMatch) {
    const section = (infraMatch[1] ?? infraMatch[3]) as 'hosts' | 'keys' | 'runners';
    const id = infraMatch[2] ? decodeSegment(infraMatch[2]) : undefined;
    return <Infrastructure section={section} id={id} />;
  }
  return <Switch>
      <Route path="/"><Redirect to="/overview" /></Route>
      {BUILT.map(s => <Route key={s.id} path={s.focus ? `/${s.id}/:id?` : `/${s.id}`}>{(p: { id?: string }) => s.view(p)}</Route>)}
      <Route><Empty text="This page does not exist." action={<Button label="Overview" icon="house" onClick={() => go('/overview')} />} /></Route>
    </Switch>;
}

/** The focus chip with its own project list, so a definition change re-renders only the chip. */
function Focus({ go }: { go: (to: string) => void }) {
  const projects = useOp('project_list', {});
  const list: { id: string; name: string }[] = (projects.data?.projects ?? []).map(p => ({ id: String(p.id), name: String(p.name ?? p.id) }));
  const { focus, focusPresets, ignored } = prefs.value;
  // Not shown where it leads: the Overview without the focus.
  const [loc] = useLocation(), search = useSearch(), here = loc === '/overview' && new URLSearchParams(search).has('all'), outside = useOutside();
  return <FocusControl projects={list} value={focus.filter(p => !ignored.includes(p))} presets={focusPresets} ignored={ignored} outside={{ n: here ? 0 : outside, onOpen: () => go('/overview?all') }}
    onChange={setFocus} onSavePreset={n => saveFocusPreset(n)} onRemovePreset={removeFocusPreset} onIgnore={setIgnored} />;
}

/**
 * How many items need the person (open questions and Needs-you problems, the Overview's set) in projects outside the
 * focus that are not ignored (prefs.ts outsideFocus); 0 with no focus. Read only while a focus is set; a new problem
 * shows within a minute.
 */
function useOutside() {
  const { focus, ignored } = prefs.value, focused = focus.some(p => !ignored.includes(p));
  // The server leaves ignored projects out (scopeIds without the focus); outsideFocus drops the focused ones.
  const scope = scopeIds(false);
  const q = useOp('questions_list', focused ? { state: 'open', ...scope } : null);
  // Executions change many times a second (gateway calls): problems are read again at most once a minute.
  const e = useOp('executions_list', focused ? { needsYou: true, ...scope } : null, { quiet: 60_000, every: 60_000 });
  const projects = [...q.data?.questions ?? [], ...e.data?.executions ?? []].map(x => x.projectId ?? null);
  return focused ? projects.filter(outsideFocus).length : 0;
}

render(<Router><App /></Router>, document.getElementById('app')!);
