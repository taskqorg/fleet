// Fleet operator app entry (docs/designs/fleet-operator-app.md): shell, routes, identity check, feed start, service worker.
import { Projects } from './views/projects.tsx';
import { render, Component, type ComponentChildren } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { Router, Route, Switch, Redirect, useLocation, useSearch } from 'wouter-preact';
import { AppShell, Empty, Button, ListDetail, workspaces, FocusControl, type NavItem, type AppScreen, type SpotlightProvider, type IconName } from '../../packages/ui/src/index.ts';
import { startVersionChecks, versionInfo, tabVersion } from './version.ts';
import { createReporter, installReporter, reportPath, sendReport } from './client-errors.ts';
import { access, accessGeneration, accessSignal, ensureAccessGeneration, invalidateAccess, registerAccessValidator, AccessChanged } from './access.ts';
import { op, accessControlOp, signedOut, OpError, type Ops } from './api.ts';
import { parseView, formatView, VIEWS } from '../../packages/view-url/view-url.ts';
import { preferencesTouched } from './invalidation.ts';
import { prefs, prefsRead, loadPrefs, savePrefs, setWidth, setFocus, saveFocusPreset, removeFocusPreset, noteView, saveView, sectionPath } from './prefs.ts';
import { useDoc } from './store.ts';
import { startFeed, stopFeed, subscribe } from './feed.ts';
import { startStore } from './store.ts';
import { clearAll } from './offline.ts';
import { Overview } from './views/overview.tsx';
import { Tasks } from './views/tasks.tsx';
import { Conversations } from './views/conversations.tsx';
import { Hosts } from './views/hosts.tsx';
import { Runners } from './views/runners.tsx';
import { Lab } from './views/lab.tsx';
import { Keys } from './views/keys.tsx';
import { Results } from './views/results.tsx';
import { Definitions } from './views/definitions.tsx';
import { Delivery } from './views/delivery.tsx';

// Browser errors reach the coordinator (client_error_report); an operation's own failure (OpError) is already in its
// spans, and an access change aborts reads on purpose, so neither is reported again.
const mainAsset = document.querySelector<HTMLScriptElement>('script[src*="/assets/main-"]')?.getAttribute('src')?.match(/main-([^./]+)/)?.[1];
const reporter = createReporter({ send: sendReport, path: () => reportPath(location.pathname, location.search), userAgent: navigator.userAgent,
  version: () => tabVersion.value ?? (mainAsset ? 'main-' + mainAsset : '') });
const ignoreError = (e: unknown) => e instanceof OpError || e instanceof AccessChanged;
installReporter(reporter, ignoreError);

/** A render crash shows a recoverable error in the work area instead of a blank page; navigating away clears it. */
class RenderBoundary extends Component<{ at: string; children: ComponentChildren }, { failed: boolean }> {
  state = { failed: false };
  componentDidCatch(error: unknown) { if (!ignoreError(error)) reporter.capture(error, 'render'); this.setState({ failed: true }); }
  componentDidUpdate(prev: { at: string }) { if (prev.at !== this.props.at && this.state.failed) this.setState({ failed: false }); }
  render() {
    return this.state.failed ? <Empty text="This page failed to render. The error was reported." action={<Button label="Reload" icon="rotate-ccw" onClick={() => location.reload()} />} /> : this.props.children;
  }
}

// The view of each screen the registry marks `app: true` (the type requires every one). Nav and routes follow registry
// order. A list screen takes an optional focus segment, so focusing a row never unmounts the list.
const SCREENS: Record<AppScreen, { focus: boolean; view: (p: { id?: string }) => any }> = {
  projects: { focus: true, view: p => <Projects id={p.id} /> },
  overview: { focus: false, view: () => <Overview /> },
  tasks: { focus: true, view: p => <Tasks id={p.id} /> },
  conversations: { focus: true, view: p => <Conversations id={p.id} /> },
  results: { focus: true, view: p => <Results id={p.id} /> },
  delivery: { focus: true, view: p => <Delivery id={p.id} /> },
  hosts: { focus: true, view: p => <Hosts id={p.id} /> },
  runners: { focus: false, view: () => <Runners /> },
  lab: { focus: true, view: p => <Lab id={p.id} /> },
  keys: { focus: true, view: p => <Keys id={p.id} /> },
  agents: { focus: true, view: p => <Definitions kind="agent" id={p.id} /> },
  pipelines: { focus: true, view: p => <Definitions kind="pipeline" id={p.id} /> },
  catalogue: { focus: true, view: p => <Definitions kind="catalogue" id={p.id} /> },
};
const BUILT = workspaces.filter(w => w.app).map(w => ({ ...w, ...SCREENS[w.id as AppScreen] }));
const NAV: NavItem[] = BUILT;
// Each kind the server finds: its group label, icon and where a hit opens (a question opens its task).
type Hit = Ops['search']['reply']['hits'][number];
const ws = (id: string) => workspaces.find(x => x.id === id)!;
const KINDS: Record<Hit['kind'], { label: string; icon: IconName; path: (h: Hit) => string }> = {
  task: { label: ws('tasks').label, icon: ws('tasks').icon, path: h => '/tasks/' + encodeURIComponent(h.id) },
  question: { label: 'Questions', icon: 'circle-question-mark', path: h => h.taskId ? '/tasks/' + encodeURIComponent(h.taskId) : h.sessionId ? '/conversations/' + encodeURIComponent(h.sessionId) : '/overview' },
  conversation: { label: ws('conversations').label, icon: ws('conversations').icon, path: h => '/conversations/' + encodeURIComponent(h.id) },
  project: { label: ws('projects').label, icon: ws('projects').icon, path: h => '/projects/' + encodeURIComponent(h.id) },
  host: { label: ws('hosts').label, icon: ws('hosts').icon, path: h => '/hosts/' + encodeURIComponent(h.id) },
};

// Spotlight (OR-32): one provider per kind the server's `search` operation finds (agents call the same tool). The kinds of
// one query share one request, sent 150 ms after the last key and reused while pending (up to 10 s) and for 2 s after; a failed one is not reused.
let last: { query: string; at: number; hits: Promise<Ops['search']['reply']['hits']> } | null = null;
const found = (kind: Hit['kind'], go: (to: string) => void): SpotlightProvider => {
  const w = KINDS[kind];
  const fresh = (query: string) => last?.query === query && Date.now() - last.at < 2000;
  return { id: kind, label: w.label, find: async (query, signal) => {
    if (!fresh(query)) {
      await new Promise(r => setTimeout(r, 150));
      if (signal.aborted) return [];
      if (!fresh(query)) { const hits = op('search', { query }).then(r => r.hits); const l = last = { query, at: Date.now() + 8000, hits }; hits.then(() => { l.at = Date.now(); }, () => { if (last === l) last = null; }); }
    }
    return (await last!.hits).filter(h => h.kind === kind).map(h => ({ key: kind + ':' + h.id, icon: w.icon, label: h.title,
      code: h.code ?? undefined, sub: h.projectId ?? undefined, run: () => go(w.path(h)) }));
  } };
};

function App() {
  const [loc, go] = useLocation();
  const query = useSearch();
  const current = loc.split('/')[1] || 'overview';
  const search = useRef((['task', 'question', 'conversation', 'project', 'host'] as const).map(k => found(k, go))).current;
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
    const offPrefs = subscribe({ resync: () => { void loadPrefs(); }, changes: cs => { if (preferencesTouched(cs, access.value.identity?.identityKey)) void loadPrefs(); } });
    validate(); addEventListener('focus', recheck); addEventListener('online', recheck);
    if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
    const stopChecks = startVersionChecks();
    return () => { unregister(); stopFeed(); offPrefs(); stopChecks(); removeEventListener('focus', recheck); removeEventListener('online', recheck); };
  }, []);
  useEffect(() => { if (signedOut.value) { invalidateAccess('signed out'); void clearAll(true); } }, [signedOut.value]);
  // Each section's list view is noted as it changes and saved when the person leaves the section or the tab hides, so the
  // navigation returns to it (prefs.ts). The open item and checked rows are not part of it (view-url drops focus and sel).
  const left = useRef<string | null>(null);
  useEffect(() => {
    if (access.value.state !== 'open' || !prefsRead.value || !NAV.some(n => n.id === current)) return;
    const parsed = (VIEWS as string[]).includes(current) ? parseView(loc + (query ? '?' + query : '')) : null;
    noteView(current, parsed ? formatView({ ...parsed, focus: undefined, sel: undefined }) : '/' + current);
    if (left.current && left.current !== current) saveView(left.current);
    left.current = current;
  }, [loc, query, access.value.state, prefsRead.value]);
  useEffect(() => {
    const flush = () => { if (document.visibilityState === 'hidden' && left.current) saveView(left.current); };
    const leave = () => { if (left.current) saveView(left.current); };
    document.addEventListener('visibilitychange', flush); addEventListener('pagehide', leave);
    return () => { document.removeEventListener('visibilitychange', flush); removeEventListener('pagehide', leave); };
  }, []);
  // The project focus (OR-34) is a person setting; lists apply it where their view names no projects.
  const focus = prefs.value.focus, presets = prefs.value.focusPresets;
  const focusCtl = (mini: boolean) => <Focus mini={mini} value={focus} presets={presets} />;
  return <AppShell privateReady={access.value.state === 'open'} privateGeneration={access.value.generation} nav={NAV} current={current} waiting={0} onGo={id => go(sectionPath(id))} pins={access.value.state === 'open' ? pins : []} onPins={savePins} search={access.value.state === 'open' ? search : []} ready={shown} focus={access.value.state === 'open' ? focusCtl : undefined}
    widths={geometry.current.widths} onWidth={setWidth} collapsed={geometry.current.navCollapsed} onCollapsed={on => savePrefs(() => ({ navCollapsed: on }), { navCollapsed: on })}
    // Logout clears this person's stored data first (at most 2 s: a blocked IndexedDB delete must not keep the person signed in), then ends the Cloudflare Access session.
    user={access.value.state === 'open' && user ? { name: user, onLogout: () => { Promise.race([clearAll(true), new Promise(r => setTimeout(r, 2000))]).finally(() => location.assign('/cdn-cgi/access/logout')); }, version: versionInfo() } : undefined}>
    {signedOut.value && <div class="problem failure" role="alert"><b>Session expired</b><span class="facts">Sign in again to continue; stored data was cleared.</span><span class="acts"><Button label="Sign in again" kind="primary" onClick={() => location.reload()} /></span></div>}
    {access.value.state === 'open' ? <RenderBoundary at={access.value.generation + ':' + loc + '?' + query}><PrivateRoutes key={access.value.generation} go={go} /></RenderBoundary> : current === 'overview' ? <Empty text="Validating access…" /> : <ListDetail list={<Empty text="Validating access…" />} detail={<Empty text="Waiting for authorized data." />} detailKey="access" />}
  </AppShell>;
}

function PrivateRoutes({ go }: { go: (to: string) => void }) {
  return <Switch>
      <Route path="/"><Redirect to="/overview" /></Route>
      {BUILT.map(s => <Route key={s.id} path={s.focus ? `/${s.id}/:id?` : `/${s.id}`}>{(p: { id?: string }) => s.view(p)}</Route>)}
      <Route><Empty text="This page does not exist." action={<Button label="Overview" icon="house" onClick={() => go('/overview')} />} /></Route>
    </Switch>;
}

/** The focus control with its own project list, so a definition change re-renders only the control. */
function Focus({ mini, value, presets }: { mini: boolean; value: string[]; presets: { name: string; projects: string[] }[] }) {
  const projects = useDoc<any>('projects', () => op('project_list', {}), c => c.kind === 'definition');
  return <FocusControl mini={mini} projects={(projects.data?.projects ?? []).map((p: any) => ({ id: p.id, name: p.name ?? p.id }))}
    value={value} presets={presets} onChange={setFocus} onSavePreset={n => saveFocusPreset(n)} onRemovePreset={removeFocusPreset} />;
}

render(<Router><App /></Router>, document.getElementById('app')!);
