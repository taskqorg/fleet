// Fleet operator app entry (docs/designs/fleet-operator-app.md): shell, routes, identity check, feed start, service worker.
import { render } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import { Router, Route, Switch, Redirect, useLocation } from 'wouter-preact';
import { AppShell, Empty, Button, type NavItem } from '../../packages/ui/src/index';
import { op, signedOut } from './api';
import { startFeed } from './feed';
import { startStore } from './store';
import { checkGrants, clearAll, lowestCursor } from './offline';
import { Overview } from './views/overview';
import { Tasks } from './views/tasks';
import { Conversations } from './views/conversations';
import { Hosts } from './views/hosts';

const NAV: NavItem[] = [
  { id: 'overview', label: 'Overview', icon: 'house', group: 'Operator' },
  { id: 'tasks', label: 'Tasks', icon: 'list', group: 'Operator' },
  { id: 'conversations', label: 'Conversations', icon: 'messages-square', group: 'Operator' },
  { id: 'hosts', label: 'Hosts', icon: 'panels-top-left', group: 'Architect' },
  { id: 'legacy', label: 'Legacy views', icon: 'history', group: 'Other' },
];

function App() {
  const [loc, go] = useLocation();
  const [update, setUpdate] = useState(false);
  const current = loc.split('/')[1] || 'overview';
  useEffect(() => {
    // Identity and grants (D3): a changed grants version clears stored data. Re-read on focus too.
    const me = () => op('me').then(m => checkGrants(m.grantsVersion)).catch(() => {});
    me(); addEventListener('focus', me);
    startStore(); lowestCursor().then(c => startFeed(c));
    if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').then(r => r.addEventListener('updatefound', () => { if (navigator.serviceWorker.controller) setUpdate(true); })).catch(() => {});
    return () => removeEventListener('focus', me);
  }, []);
  useEffect(() => { if (signedOut.value) clearAll(true); }, [signedOut.value]);
  return <AppShell nav={NAV} current={current} waiting={0} onGo={id => id === 'legacy' ? (location.href = '/legacy') : go('/' + id)}>
    {update && <div class="problem warning" role="status"><b>Update available</b><span class="facts">A new version of the app is ready.</span><span class="acts"><Button label="Reload" onClick={() => location.reload()} /></span></div>}
    {signedOut.value && <div class="problem failure" role="alert"><b>Session expired</b><span class="facts">Sign in again to continue; stored data was cleared.</span><span class="acts"><Button label="Sign in again" kind="primary" onClick={() => location.reload()} /></span></div>}
    <Switch>
      <Route path="/"><Redirect to="/overview" /></Route>
      <Route path="/overview"><Overview /></Route>
      {/* One route per list view with an optional focus segment, so focusing a row never unmounts the list. */}
      <Route path="/tasks/:id?">{(p: { id?: string }) => <Tasks id={p.id} />}</Route>
      <Route path="/conversations/:id?">{(p: { id?: string }) => <Conversations id={p.id} />}</Route>
      <Route path="/hosts/:id">{(p: { id: string }) => <Hosts id={p.id} />}</Route>
      <Route path="/hosts"><Hosts /></Route>
      <Route><Empty text="This page does not exist." action={<Button label="Overview" icon="house" onClick={() => go('/overview')} />} /></Route>
    </Switch>
  </AppShell>;
}

render(<Router><App /></Router>, document.getElementById('app')!);
