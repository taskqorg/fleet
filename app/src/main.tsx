// Fleet operator app entry (docs/designs/fleet-operator-app.md): shell, routes, identity check, feed start, service worker.
import { render } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { Router, Route, Switch, Redirect, useLocation } from 'wouter-preact';
import { AppShell, Empty, Button, workspaces, type NavItem, type AppScreen } from '../../packages/ui/src/index.ts';
import { op, signedOut } from './api.ts';
import { startFeed } from './feed.ts';
import { startStore } from './store.ts';
import { checkGrants, clearAll } from './offline.ts';
import { Overview } from './views/overview.tsx';
import { Tasks } from './views/tasks.tsx';
import { Conversations } from './views/conversations.tsx';
import { Hosts } from './views/hosts.tsx';
import { Lab } from './views/lab.tsx';
import { Keys } from './views/keys.tsx';
import { Results } from './views/results.tsx';
import { Definitions } from './views/definitions.tsx';

// The view of each screen the registry marks `app: true` (the type requires every one). Nav and routes follow registry
// order. A list screen takes an optional focus segment, so focusing a row never unmounts the list.
const SCREENS: Record<AppScreen, { focus: boolean; view: (p: { id?: string }) => any }> = {
  overview: { focus: false, view: () => <Overview /> },
  tasks: { focus: true, view: p => <Tasks id={p.id} /> },
  conversations: { focus: true, view: p => <Conversations id={p.id} /> },
  results: { focus: true, view: p => <Results id={p.id} /> },
  hosts: { focus: true, view: p => <Hosts id={p.id} /> },
  lab: { focus: true, view: p => <Lab id={p.id} /> },
  keys: { focus: true, view: p => <Keys id={p.id} /> },
  agents: { focus: true, view: p => <Definitions kind="agent" id={p.id} /> },
  pipelines: { focus: true, view: p => <Definitions kind="pipeline" id={p.id} /> },
  catalogue: { focus: true, view: p => <Definitions kind="catalogue" id={p.id} /> },
};
const BUILT = workspaces.filter(w => w.app).map(w => ({ ...w, ...SCREENS[w.id as AppScreen] }));
const NAV: NavItem[] = BUILT;

function App() {
  const [loc, go] = useLocation();
  const [update, setUpdate] = useState(false);
  const current = loc.split('/')[1] || 'overview';
  // Pinned sections are a person setting (person_settings_*), so they follow the person to every device.
  const [pins, setPins] = useState<string[]>([]);
  // ponytail: last write wins; seq drops a stale get reply or a rollback after a newer change.
  const seq = useRef(0);
  useEffect(() => { const n = seq.current; op('person_settings_get', {}).then(s => { if (seq.current === n) setPins(s.pins); }).catch(() => {}); }, []);
  const savePins = (ids: string[]) => { const prev = pins, n = ++seq.current; setPins(ids); op('person_settings_set', { pins: ids }).catch(() => { if (seq.current === n) setPins(prev); }); };
  useEffect(() => {
    // Identity and grants (D3): a changed grants version clears stored data. Re-read on focus too.
    const me = () => op('me', {}).then(m => checkGrants(m.grantsVersion ?? '')) // no person: '' still clears another person's data.catch(() => {});
    me(); addEventListener('focus', me);
    // The feed starts at the server head: every mounted list loads a fresh page (restored rows only fill the first frame),
    // and lists shown from storage or after a failed load reload when the feed comes back (store.ts).
    startStore(); startFeed(null);
    if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').then(r => r.addEventListener('updatefound', () => { if (navigator.serviceWorker.controller) setUpdate(true); })).catch(() => {});
    return () => removeEventListener('focus', me);
  }, []);
  useEffect(() => { if (signedOut.value) clearAll(true); }, [signedOut.value]);
  return <AppShell nav={NAV} current={current} waiting={0} onGo={id => go('/' + id)} pins={pins} onPins={savePins}>
    {update && <div class="problem warning" role="status"><b>Update available</b><span class="facts">A new version of the app is ready.</span><span class="acts"><Button label="Reload" onClick={() => location.reload()} /></span></div>}
    {signedOut.value && <div class="problem failure" role="alert"><b>Session expired</b><span class="facts">Sign in again to continue; stored data was cleared.</span><span class="acts"><Button label="Sign in again" kind="primary" onClick={() => location.reload()} /></span></div>}
    <Switch>
      <Route path="/"><Redirect to="/overview" /></Route>
      {BUILT.map(s => <Route key={s.id} path={s.focus ? `/${s.id}/:id?` : `/${s.id}`}>{(p: { id?: string }) => s.view(p)}</Route>)}
      <Route><Empty text="This page does not exist." action={<Button label="Overview" icon="house" onClick={() => go('/overview')} />} /></Route>
    </Switch>
  </AppShell>;
}

render(<Router><App /></Router>, document.getElementById('app')!);
