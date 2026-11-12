// Fleet operator app entry (docs/designs/fleet-operator-app.md): shell, routes, identity check, feed start, service worker.
import { Projects } from './views/projects.tsx';
import { render } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { Router, Route, Switch, Redirect, useLocation } from 'wouter-preact';
import { AppShell, Empty, Button, Skeleton, workspaces, type NavItem, type AppScreen, type SpotlightProvider } from '../../packages/ui/src/index.ts';
import { op, signedOut, type Ops } from './api.ts';
import { prefs, prefsRead, loadPrefs, savePrefs } from './prefs.ts';
import { startFeed } from './feed.ts';
import { startStore } from './store.ts';
import { checkGrants, clearAll } from './offline.ts';
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

// The view of each screen the registry marks `app: true` (the type requires every one). Nav and routes follow registry
// order. A list screen takes an optional focus segment, so focusing a row never unmounts the list.
const SCREENS: Record<AppScreen, { focus: boolean; view: (p: { id?: string }) => any }> = {
  projects: { focus: true, view: p => <Projects id={p.id} /> },
  overview: { focus: false, view: () => <Overview /> },
  tasks: { focus: true, view: p => <Tasks id={p.id} /> },
  conversations: { focus: true, view: p => <Conversations id={p.id} /> },
  results: { focus: true, view: p => <Results id={p.id} /> },
  delivery: { focus: false, view: () => <Delivery /> },
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
const ICON = { task: 'tasks', project: 'projects', host: 'hosts' } as const;

// Spotlight (OR-32): one provider per kind the server's `search` operation finds (agents call the same tool). The kinds of
// one query share one request, sent 150 ms after the last key and reused while pending (up to 10 s) and for 2 s after; a failed one is not reused.
let last: { query: string; at: number; hits: Promise<Ops['search']['reply']['hits']> } | null = null;
const found = (kind: keyof typeof ICON, go: (to: string) => void): SpotlightProvider => {
  const w = workspaces.find(x => x.id === ICON[kind])!;
  const fresh = (query: string) => last?.query === query && Date.now() - last.at < 2000;
  return { id: kind, label: w.label, find: async (query, signal) => {
    if (!fresh(query)) {
      await new Promise(r => setTimeout(r, 150));
      if (signal.aborted) return [];
      if (!fresh(query)) { const hits = op('search', { query }).then(r => r.hits); const l = last = { query, at: Date.now() + 8000, hits }; hits.then(() => { l.at = Date.now(); }, () => { if (last === l) last = null; }); }
    }
    return (await last!.hits).filter(h => h.kind === kind).map(h => ({ key: kind + ':' + h.id, icon: w.icon, label: h.title,
      sub: h.projectId ?? undefined, run: () => go(`/${ICON[kind]}/${encodeURIComponent(h.id)}`) }));
  } };
};

function App() {
  const [loc, go] = useLocation();
  const [update, setUpdate] = useState(false);
  const current = loc.split('/')[1] || 'overview';
  const search = useRef((['task', 'project', 'host'] as const).map(k => found(k, go))).current;
  // Pinned sections and the other preferences are person settings (prefs.ts), so they follow the person to every device.
  useEffect(() => { loadPrefs(); }, []); // and again on window focus (below): agents and other devices change them too
  const pins = prefs.value.pins, savePins = (ids: string[]) => savePrefs(() => ({ pins: ids }), { pins: ids });
  useEffect(() => {
    // Identity and grants (D3): a changed grants version clears stored data. Re-read on focus too.
    const me = () => op('me', {}).then(m => checkGrants(m.grantsVersion ?? '')) // no person: '' still clears another person's data.catch(() => {});
    me(); addEventListener('focus', me); addEventListener('focus', loadPrefs);
    // The feed starts at the server head: every mounted list loads a fresh page (restored rows only fill the first frame),
    // and lists shown from storage or after a failed load reload when the feed comes back (store.ts).
    startStore(); startFeed(null);
    if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').then(r => r.addEventListener('updatefound', () => { if (navigator.serviceWorker.controller) setUpdate(true); })).catch(() => {});
    return () => { removeEventListener('focus', me); removeEventListener('focus', loadPrefs); };
  }, []);
  useEffect(() => { if (signedOut.value) clearAll(true); }, [signedOut.value]);
  return <AppShell nav={NAV} current={current} waiting={0} onGo={id => go('/' + id)} pins={pins} onPins={savePins} search={search}>
    {update && <div class="problem warning" role="status"><b>Update available</b><span class="facts">A new version of the app is ready.</span><span class="acts"><Button label="Reload" onClick={() => location.reload()} /></span></div>}
    {signedOut.value && <div class="problem failure" role="alert"><b>Session expired</b><span class="facts">Sign in again to continue; stored data was cleared.</span><span class="acts"><Button label="Sign in again" kind="primary" onClick={() => location.reload()} /></span></div>}
    {!prefsRead.value ? <Skeleton rows={8} /> : <Switch>
      <Route path="/"><Redirect to="/overview" /></Route>
      {BUILT.map(s => <Route key={s.id} path={s.focus ? `/${s.id}/:id?` : `/${s.id}`}>{(p: { id?: string }) => s.view(p)}</Route>)}
      <Route><Empty text="This page does not exist." action={<Button label="Overview" icon="house" onClick={() => go('/overview')} />} /></Route>
    </Switch>}
  </AppShell>;
}

render(<Router><App /></Router>, document.getElementById('app')!);
