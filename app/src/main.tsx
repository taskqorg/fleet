// Fleet operator app entry (docs/designs/fleet-operator-app.md): shell, routes, identity check, feed start, service worker.
// errors.ts first: error tracking starts before the other modules run.
import { captureRender, configureErrors } from './errors.ts';
import { createEffect, createMemo, createSignal, Errored, lazy, Loading, Match, onSettled, Show, Switch } from 'solid-js';
import { render, type JSX } from '@solidjs/web';
import { createAgent, registerTools } from '@ui-sdk/core/agent';
import { AppShell, EntityCards, Empty, Button, ListDetail, workspaces, matchSearch, FocusControl, FocusPresets, type NavItem, type SpotlightProvider, type SpotlightItem, type IconName, type Family } from '../../packages/ui/src/index.ts';
import { useLocation, useSearch, syncLocation } from './router.ts';
import { setProjectCodes } from './routes.ts';
import type { InfraSection } from './views/hosts.tsx';
import { startVersionChecks, versionInfo, tabVersion } from './version.ts';
import { access, accessGeneration, accessSignal, ensureAccessGeneration, invalidateAccess, registerAccessValidator, AccessChanged } from './access.ts';
import { op, accessControlOp, signedOut, OpError, type Ops } from './api.ts';
import { parseView, formatView, VIEWS, type ViewName } from '../../packages/view-url/view-url.ts';
import { prefs, prefsRead, loadPrefs, savePrefs, setWidth, setPanel, panelsOf, setFocus, setIgnored, saveFocusPreset, removeFocusPreset, noteView, sectionPath, saveOnLeave, outsideFocus, scopeIds } from './prefs.ts';
import { useOp, touchedBy, setIdentity, startStore } from './store.ts';
import { useNavCounts } from './nav-counts.ts';
import { useRailBots } from './rail-bots.ts';
import { entityCards } from './entity-cards.ts';
import { startFeed, stopFeed, subscribe, setResyncHandler, rebaseFeed } from './feed.ts';
import { adopt, forget, mirrored } from './persist.ts';
import { clearAll } from './offline.ts';
import type { LibrarySection } from './views/agents.tsx';

declare global {
  interface Window {
    __fleetAgent?: ReturnType<typeof createAgent>;
  }
}

const Projects = lazy(() => import('./views/projects.tsx'), { export: 'Projects' });
const Home = lazy(() => import('./views/home.tsx'), { export: 'Home' });
const Conversations = lazy(() => import('./views/conversations.tsx'), { export: 'Conversations' });
const Infrastructure = lazy(() => import('./views/hosts.tsx'), { export: 'Hosts' });
const PersonalLab = lazy(() => import('./views/personal-lab.tsx'), { export: 'PersonalLab' });
const Lab = lazy(() => import('./views/lab.tsx'), { export: 'Lab' });
const Library = lazy(() => import('./views/agents.tsx'), { export: 'Library' });
const Live = lazy(() => import('./views/live.tsx'), { export: 'Live' });

// Not reported: an operation's own failure (OpError) is in the coordinator's spans, and an access change aborts reads on
// purpose. The release is the version this tab runs.
configureErrors({ ignore: e => e instanceof OpError || e instanceof AccessChanged, release: () => tabVersion.value });

/** A render crash shows a recoverable error in the work area instead of a blank page; navigating away clears it. */
function RenderBoundary(props: { at: string; children: JSX.Element }) {
  let resetErr: (() => void) | undefined;
  createEffect(() => props.at, () => { resetErr?.(); resetErr = undefined; }, { defer: true });
  return (
    <Errored fallback={(err, reset) => {
      captureRender(err());
      resetErr = reset;
      return <Empty text="This page failed to render." action={<Button label="Reload" icon="rotate-ccw" onClick={() => location.reload()} />} />;
    }}>
      {props.children}
    </Errored>
  );
}

// Lab shows the architect's Lab or, without that role, the person's own drafts (Personal Lab).
function LabWorkspace(props: { id?: string }) {
  const me = useOp('me', {});
  return <Switch fallback={<Empty text="Loading access…" />}>
    <Match when={me.data?.roles.includes('architect')}><Lab id={props.id} /></Match>
    <Match when={me.data?.personalLab}><PersonalLab id={props.id} path="/lab" /></Match>
    <Match when={me.data || me.error}><Empty text="Lab access is required." /></Match>
  </Switch>;
}

/** Wall mode (pages/live.md): `/live?wall` hides the navigation and the shell header for a TV, and nothing opens. */
const isWall = (q: string) => new URLSearchParams(q).has('wall');
/** A viewer (people access, Wall viewer) reads only: it sees Live and nothing else. */
const viewerOnly = (roles: readonly string[] | undefined) => !!roles && roles.length > 0 && roles.every(r => r === 'viewer');

// The screens rendered by their own view. Projects (with its facets Tasks, Results and Delivery), Library and System
// render every screen of their section in one view (PrivateRoutes), so moving inside a section keeps its tree (OR-47).
// A list screen takes an optional focus segment, so focusing a row never unmounts the list.
const SCREENS = [
  { id: 'live', focus: false, view: () => <Live wall={isWall(useSearch()())} /> },
  { id: 'overview', focus: true, view: (p: { id?: string }) => <Home id={p.id} /> },
  { id: 'conversations', focus: true, view: (p: { id?: string }) => <Conversations id={p.id} /> },
  { id: 'lab', focus: true, view: (p: { id?: string }) => <LabWorkspace id={p.id} /> },
  { id: 'personal-lab', focus: true, view: (p: { id?: string }) => <PersonalLab id={p.id} /> },
] as const satisfies readonly { id: string; focus: boolean; view: (p: { id?: string }) => JSX.Element }[];
// The rail's seven sections (OR-46); each screen highlights the section that owns it (OR-57).
const NAV: NavItem[] = workspaces.filter(w => w.nav).map(w => ({ id: w.id, label: w.label, icon: w.icon }));
const sectionOf = (screen: string): string => workspaces.find(w => w.id === screen)?.section ?? '';
// The screens inside a section that Spotlight lists as pages (registry `nav: false`).
const PAGES = workspaces.filter(w => !w.nav);
const ROOT: Record<string, string> = Object.fromEntries(workspaces.filter(w => w.nav).map(w => [w.id, (w as { root?: string }).root ?? w.id]));
// The counter each section shows (nav-rail.md, Counters): Projects its Tasks facet, System its Hosts.
const COUNTER: Record<string, string> = { conversations: 'conversations', projects: 'tasks', system: 'hosts' };
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

/** Spotlight provider for deep-linked workspaces and consolidated sub-pages outside the navigation rail. */
const subPages = (go: (to: string) => void, show: (id: string) => boolean): SpotlightProvider => ({
  id: 'pages',
  label: 'Pages',
  find: q => PAGES.map(w => ({ id: w.id, label: w.label, icon: w.icon }))
    .filter(s => show(s.id) && matchSearch(q, s.label))
    .map(s => ({ key: 'sub:' + s.id, icon: s.icon, label: s.label, run: () => go(sectionPath(s.id)) })),
});

// The project focus (OR-34): '-' is work outside projects; an empty focus holds everything. The server marks hits outside it.
const toggleFocus = (id: string) => { const f = prefs.peek().focus; setFocus(f.includes(id) ? f.filter(x => x !== id) : [...f, id]); };

// Spotlight (OR-32): one provider per kind the server's `search` operation finds (agents call the same tool). The kinds of
// one query and focus share one request, sent 150 ms after the last key and reused while pending (up to 10 s) and for 2 s
// after; a failed one is not reused. The server puts each kind's hits in the focus first and counts what its list shows.
type Reply = Ops['search']['reply'];
let last: { key: string; at: number; reply: Promise<Reply> } | null = null;
const found = (kind: Hit['kind'], go: (to: string) => void): SpotlightProvider => {
  const w = KINDS[kind];
  return { id: kind, label: w.label, find: async (query, signal) => {
    // The focus without ignored projects, as lists apply it; the server leaves ignored projects out itself.
    const { ignored } = prefs.peek(), focus = scopeIds().projectIds ?? [], key = JSON.stringify([query, focus, ignored]);
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
  const current = createMemo(() => loc().split('/')[1] || 'overview');
  const activeNav = createMemo(() => sectionOf(current()));
  const shellTitle = createMemo(() => NAV.find(w => w.id === activeNav())?.label ?? '');
  const me = useOp('me', {});
  const showPage = (id: string) => id === 'humans' ? me.data?.owner === true || me.data?.managesPeople === true :
    id === 'personal-lab' ? !!me.data?.personalLab || me.data?.roles.includes('architect') === true : true;
  const search = [subPages(go, showPage), ...(Object.keys(KINDS) as Hit['kind'][]).map(k => found(k, go))];
  const viewer = createMemo(() => viewerOnly(me.data?.roles));
  const wall = createMemo(() => current() === 'live' && isWall(query()));
  // A viewer sees only Live: any other address opens it.
  createEffect(() => [viewer(), current()] as const, ([v, cur]) => { if (v && cur !== 'live') go('/live', { replace: true }); });
  // Pinned sections and the other preferences are person settings (prefs.ts), so they follow the person to every device.
  // Preferences load only after authorized baseline activation.
  // Focus and online check the person quietly (below); preferences changed elsewhere arrive as setting-change events.
  const [user, setUser] = createSignal<string | null>(null);
  // OR-23: the shell shows once the first settings read answers, or after 1.5 s at most; a later store clear does not hide it again.
  const [shown, setShown] = createSignal(false, { ownedWrite: true });
  createEffect(() => prefsRead.value, r => { if (r) setShown(true); });
  onSettled(() => {
    const t = setTimeout(() => setShown(true), 1500);
    return () => clearTimeout(t);
  });
  let lastGeometry = { widths: prefs.peek().widths, panels: panelsOf(prefs.peek()) };
  let lastActor: string | undefined;
  const geometry = createMemo(() => {
    const cur = access.value;
    if (cur.state === 'open') {
      const actor = cur.identity?.identityKey;
      if (actor && lastActor && actor !== lastActor && !prefsRead.value) {
        lastGeometry = { widths: {}, panels: {} };
      }
      if (actor) lastActor = actor;
      if (prefsRead.value) {
        lastGeometry = { widths: prefs.value.widths, panels: panelsOf(prefs.value) };
      }
    }
    return lastGeometry;
  });
  const pins = createMemo(() => (access.value.state === 'open' ? prefs.value.pins : []));
  const savePins = (ids: string[]) => savePrefs(() => ({ pins: ids }), { pins: ids });
  onSettled(() => {
    const unregister = registerAccessValidator(async ({ generation, signal }) => {
      last = null;
      const clear = clearAll();
      const m = await accessControlOp('me', {}, { signal });
      await clear; if (signal.aborted || generation !== accessGeneration()) throw Error('Access changed');
      if (!m.email) throw Error('Sign in to continue');
      setUser(m.name || m.email);
      const identity = { identityKey: m.email, grantsVersion: m.grantsVersion ?? '' };
      // The read mirror belongs to this person and these grants: another owner's copy is purged, and what this page showed
      // from it is cleared before the gate opens (persist.ts).
      if (adopt(identity)) await clearAll();
      if (signal.aborted || generation !== accessGeneration()) throw Error('Access changed');
      return identity;
    });
    startFeed(null); startStore();
    let validatedAt = 0;
    const validate = () => { validatedAt = Date.now(); invalidateAccess('focus'); last = null; void ensureAccessGeneration('focus').then(() => loadPrefs()).catch(() => {}); };
    // Focus and online check the person quietly: the same identity and grants keep everything on screen (the change
    // stream brings what happened meanwhile); a different person, changed grants or a closed gate fence and validate as
    // at start (docs/designs/fleet-analytics.md, the access gate). Offline keeps what is shown; signed out has its own path.
    // A check that got no answer (a timeout, a 5xx: the coordinator restarts on every release) keeps the page and tries
    // again after 1 s, doubling up to 30 s, until the person is confirmed; it never clears what is shown.
    let checking = false, again = false, rebaseWanted = false, retryMs = 0, retryTimer: ReturnType<typeof setTimeout> | undefined;
    const retryLater = () => { clearTimeout(retryTimer); retryMs = Math.min(Math.max(retryMs * 2, 1000), 30_000); retryTimer = setTimeout(() => { void recheck(); }, retryMs); };
    const recheck = async (): Promise<void> => {
      const s = access.peek();
      // A validation running for less than 10 s will answer by itself (restarting it would throw away the page it shows
      // from the mirror); a closed gate, or one validating longer, validates anew.
      if (s.state === 'validating' && Date.now() - validatedAt < 10_000) return;
      // A person validated moments ago (the focus that comes with a load) needs no second check.
      if (s.state === 'open' && Date.now() - validatedAt < 5_000) return;
      if (s.state !== 'open' || !s.identity) return validate();
      // A focus during a quiet check is checked right after it, so a change in between is never missed.
      if (checking) { again = true; return; }
      checking = true;
      try {
        const m = await accessControlOp('me', {}, { signal: AbortSignal.any([accessSignal(s.generation), AbortSignal.timeout(10000)]) });
        const cur = access.peek();
        if (cur.generation !== s.generation || cur.state !== 'open') return;
        clearTimeout(retryTimer); retryMs = 0;
        // Another person or changed grants: fence and validate as at start.
        if (m.email !== s.identity.identityKey || (m.grantsVersion ?? '') !== s.identity.grantsVersion) { rebaseWanted = false; validate(); return; }
        // The same person: preferences and data stay, and the change stream brings what changed (from a new cursor after a resync).
        if (rebaseWanted) { rebaseWanted = false; void rebaseFeed(); }
      } catch (e) {
        if (access.peek().generation !== s.generation || access.peek().state !== 'open') return;
        // Offline waits for online, and signed out has its own path; any other failure (a timeout, a 5xx, an incomplete
        // reply) keeps the page as it is and asks again later.
        if (!(e instanceof OpError && (e.status === 0 && e.message === 'offline' || e.status === 401 || e.status === 403))) retryLater();
      } finally { checking = false; if (again) { again = false; void recheck(); } }
    };
    // Preferences changed on another device or by an agent (person_settings_set writes a setting-change for this person).
    setIdentity(() => access.peek().identity?.identityKey);
    const offPrefs = subscribe({ resync: () => { void loadPrefs(); }, changes: cs => { if (cs.some(c => touchedBy(c, 'person_settings_get', {}))) void loadPrefs(); } });
    setResyncHandler(() => { rebaseWanted = true; void recheck(); });
    validate(); addEventListener('focus', recheck); addEventListener('online', recheck);
    if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
    const stopChecks = startVersionChecks();
    return () => { unregister(); setResyncHandler(null); clearTimeout(retryTimer); stopFeed(); offPrefs(); stopChecks(); removeEventListener('focus', recheck); removeEventListener('online', recheck); };
  });
  createEffect(() => signedOut.value, out => { if (out) { invalidateAccess('signed out'); void forget(); void clearAll(true); } });
  // Each section's list view is noted as it changes and saved when the person leaves the section or the tab hides, so the
  // navigation returns to it (prefs.ts). The open item and checked rows are not part of it (view-url drops focus and sel).
  // Unsent list group toggles are sent on the same section leave.
  let left: string | null = null;
  // A rail section opens the screen of it last open in this tab (Projects returns to its Tasks facet), else its first
  // screen, each with its list as left (prefs.ts sectionPath).
  const lastScreen = new Map<string, string>();
  const openSection = (id: string) => go(sectionPath(lastScreen.get(id) ?? ROOT[id] ?? id));
  createEffect(() => [loc(), query(), access.value.state, prefsRead.value, current()] as const, ([l, q, st, pr, cur]) => {
    // A viewer may only read: its views are not kept (person_settings_set answers 403).
    if (st !== 'open' || !pr || !sectionOf(cur) || viewer()) return;
    const parsed = (VIEWS as string[]).includes(cur) ? parseView(l + (q ? '?' + q : '')) : null;
    // Home keeps no view of its own: /overview?all (every project, from the outside-focus count) is never kept.
    noteView(cur, parsed && cur !== 'overview' ? formatView({ ...parsed, focus: undefined, sel: undefined }) : '/' + cur);
    lastScreen.set(sectionOf(cur), cur);
    if (left && left !== cur) saveOnLeave(left, false);
    left = cur;
  });
  onSettled(() => {
    // Unsent list group toggles go with them, in one keepalive request: a hidden tab may be closed without pagehide
    // (prefs.ts saveOnLeave; a section leave sends the same request without keepalive above).
    const flush = () => { if (document.visibilityState === 'hidden' && !viewer()) saveOnLeave(left); };
    const leave = () => { if (!viewer()) saveOnLeave(left); };
    document.addEventListener('visibilitychange', flush); addEventListener('pagehide', leave);
    return () => { document.removeEventListener('visibilitychange', flush); removeEventListener('pagehide', leave); };
  });
  // The person's time zone, from this browser (Intl): agents and Telegram write times in it (person settings timezone).
  // Saved when it differs from the stored one, so a trip abroad follows the device in use. A viewer's set is refused, so it waits for the roles.
  // One try per zone and tab: a refused save (a zone the server does not know) is not repeated.
  let zoneSent = '';
  createEffect(() => [access.value.state, prefsRead.value, prefs.value.timezone, me.data ? viewer() : true] as const, ([st, pr, tz, v]) => {
    const here = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (st !== 'open' || !pr || v || !here || here === tz || here === zoneSent) return;
    zoneSent = here;
    savePrefs(() => ({ timezone: here }), { timezone: here });
  });
  // Each section's counter (nav-rail.md, Counters); Needs you is the head chip, not a counter.
  const counts = useNavCounts(() => access.value.state === 'open');
  const nav = createMemo(() => {
    const c = counts();
    return NAV.filter(n => !viewer() || n.id === 'live').map(n => c[COUNTER[n.id]] ? { ...n, count: c[COUNTER[n.id]] } : n);
  });
  // Entity cards (EntityCode) read entity_card through the store; their Open goes to the in-app path.
  const cards = entityCards(go);
  const isOpen = createMemo(() => access.value.state === 'open');
  // While the person is validated, a tab that kept a read mirror shows its pages from it at once (operator-app D3);
  // their reads wait for the gate, and another person's copy is cleared before it opens.
  const showPrivate = createMemo(() => isOpen() || (access.value.state === 'validating' && mirrored()));
  const railBots = useRailBots(isOpen, go);
  return <EntityCards value={cards}><AppShell privateReady={isOpen()} privateGeneration={access.value.generation} nav={nav()} current={activeNav()} title={shellTitle()} waiting={0} onGo={openSection} pins={pins()} onPins={savePins} search={isOpen() && !viewer() ? search : []} ready={shown()} wall={wall()} bots={isOpen() ? railBots : undefined}
    searchIdle={<FocusPresets icon value={prefs.value.focus.filter(p => !prefs.value.ignored.includes(p))} presets={prefs.value.focusPresets} ignored={prefs.value.ignored} onChange={setFocus} />}
    focus={isOpen() && !viewer() ? <Focus go={go} /> : undefined}
    widths={geometry().widths} onWidth={setWidth} panels={geometry().panels} onPanel={setPanel}
    // Logout clears this person's stored data first (at most 2 s: a blocked IndexedDB delete must not keep the person signed in), then ends the Cloudflare Access session.
    user={isOpen() && user() ? { name: user()!, onLogout: () => { Promise.race([Promise.all([forget(), clearAll(true)]), new Promise(r => setTimeout(r, 2000))]).finally(() => location.assign('/cdn-cgi/access/logout')); }, version: versionInfo() } : undefined}>
    {signedOut.value && <div class="problem failure" role="alert"><b>Session expired</b><span class="facts">Sign in again to continue; stored data was cleared.</span><span class="acts"><Button label="Sign in again" kind="primary" onClick={() => location.reload()} /></span></div>}
    {showPrivate() ? (
      <RenderBoundary at={access.value.generation + ':' + loc() + '?' + query()}>
        <Show when={access.value.generation} keyed>{_gen => <PrivateRoutes go={go} />}</Show>
      </RenderBoundary>
    ) : current() === 'overview' ? <Empty text="Validating access…" /> : <ListDetail list={<Empty text="Validating access…" />} detail={<Empty text="Waiting for authorized data." />} detailKey="access" />}
  </AppShell></EntityCards>;
}

const decodeSegment = (s: string): string => { try { return decodeURIComponent(s); } catch { return s; } };

function PrivateRoutes(props: { go: (to: string) => void }) {
  const [loc] = useLocation();
  // Library speaks its target routes (routes.ts): /library, /library/<root>, /library/<root>/<id>.
  const libMatch = createMemo(() => /^\/library(?:\/(agents|pipelines|methods|bots|skills|changes)(?:\/([^/]+))?)?\/?$/.exec(loc()));
  // System (pages/system.md): every root and item renders in one view, so the tree stays mounted between them.
  const infraRoute = createMemo<{ section: InfraSection; id?: string } | null>(() => {
    const p = loc();
    const one = (re: RegExp) => re.exec(p);
    let x: RegExpExecArray | null;
    if ((x = one(/^\/hosts\/limits(?:\/([^/]+))?\/?$/))) return { section: 'limits', id: x[1] ? decodeSegment(x[1]) : undefined };
    if ((x = one(/^\/(hosts|keys|humans)(?:\/([^/]+))?\/?$/))) return { section: x[1] === 'humans' ? 'people' : x[1] as InfraSection, id: x[2] ? decodeSegment(x[2]) : undefined };
    if ((x = one(/^\/(runners|spend|access)\/?$/))) return { section: x[1] as InfraSection };
    return null;
  });
  const libSection = createMemo(() => { const m = libMatch(); return m ? (m[1] ?? 'idle') as LibrarySection : null; });
  const libId = createMemo(() => { const m = libMatch(); return m?.[2] ? decodeSegment(m[2]) : undefined; });
  const infraSection = createMemo(() => infraRoute()?.section ?? null);
  const infraId = createMemo(() => infraRoute()?.id);
  // Projects (pages/projects.md): the catalogue and its facets render in one view.
  const projectsMatch = createMemo(() => /^\/(projects|tasks|results|delivery)(?:\/([^/]+))?\/?$/.exec(loc()));
  const projectsScreen = createMemo(() => (projectsMatch()?.[1] ?? null) as 'projects' | 'tasks' | 'results' | 'delivery' | null);
  const screenMatch = createMemo(() => {
    const p = loc();
    for (const s of SCREENS) {
      const m = new RegExp(s.focus ? `^/${s.id}(?:/([^/]+))?/?$` : `^/${s.id}/?$`).exec(p);
      if (m) return { screen: s, id: m[1] ? decodeSegment(m[1]) : undefined };
    }
    return null;
  });
  const screen = createMemo(() => screenMatch()?.screen ?? null);
  const itemId = createMemo(() => projectsMatch()?.[2] ? decodeSegment(projectsMatch()![2]) : screenMatch()?.id);
  return (
    <Loading fallback={<Empty text="Loading…" />}>
      <Switch fallback={<Empty text="This page does not exist." action={<Button label="Home" icon="house" onClick={() => props.go('/home')} />} />}>
        <Match when={libSection()}>
          {sec => <Library section={sec()} id={libId()} />}
        </Match>
        <Match when={infraSection()}>
          {sec => <Infrastructure section={sec()} id={infraId()} />}
        </Match>
        <Match when={projectsScreen() !== null}>
          <Projects screen={projectsScreen()!} id={itemId()} />
        </Match>
        {SCREENS.map(s => (
          <Match when={screen() === s}>
            {s.view({ get id() { return itemId(); } })}
          </Match>
        ))}
      </Switch>
    </Loading>
  );
}

/** The focus chip with its own project list, so a definition change re-renders only the chip. */
function Focus(props: { go: (to: string) => void }) {
  const projects = useOp('project_list', {});
  const list = createMemo<{ id: string; name: string }[]>(() => (projects.data?.projects ?? []).map(p => ({ id: String(p.id), name: String(p.name ?? p.id) })));
  // The address bar names a project by its code (routes.ts): once the codes are known, the current address takes them.
  createEffect(() => projects.data?.projects, ps => {
    if (ps && setProjectCodes(ps.map(p => ({ id: String(p.id), code: typeof p.code === 'string' ? p.code : null })))) syncLocation();
  });
  // Not shown where it leads: Home without the focus.
  const [loc] = useLocation(), search = useSearch();
  const here = createMemo(() => loc() === '/overview' && new URLSearchParams(search()).has('all'));
  const outside = useOutside();
  return <FocusControl projects={list()} value={prefs.value.focus.filter(p => !prefs.value.ignored.includes(p))} presets={prefs.value.focusPresets} ignored={prefs.value.ignored} outside={{ n: here() ? 0 : outside(), onOpen: () => props.go('/overview?all') }}
    onChange={setFocus} onSavePreset={n => saveFocusPreset(n)} onRemovePreset={removeFocusPreset} onIgnore={setIgnored} />;
}

/**
 * How many items need the person (open questions and Needs-you problems, Home's set) in projects outside the
 * focus that are not ignored (prefs.ts outsideFocus); 0 with no focus. Read only while a focus is set; a new problem
 * shows within a minute.
 */
function useOutside() {
  const focused = createMemo(() => prefs.value.focus.some(p => !prefs.value.ignored.includes(p)));
  // The server leaves ignored projects out (scopeIds without the focus); outsideFocus drops the focused ones.
  const q = useOp('questions_list', () => (focused() ? { state: 'open', ...scopeIds(false) } : null));
  // Executions change many times a second (gateway calls): problems are read again at most once a minute.
  const e = useOp('executions_list', () => (focused() ? { needsYou: true, ...scopeIds(false) } : null), { quiet: 60_000, every: 60_000 });
  return createMemo(() => {
    if (!focused()) return 0;
    const projects = [...q.data?.questions ?? [], ...e.data?.executions ?? []].map(x => x.projectId ?? null);
    return projects.filter(outsideFocus).length;
  });
}

const agent = createAgent();
if (typeof window !== 'undefined') window.__fleetAgent = agent;
registerTools(agent, { allow: step => !step.confirm });

render(() => <App />, document.getElementById('app')!);
