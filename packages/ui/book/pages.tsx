// Page mockups (stage 3): built only from packages/ui controls and patterns, with fixture data.
// Specs: docs/design-system/pages/*.md. Built by scripts/build-book.mjs into docs/design-system/pages/mockups.html.
import { render, type ComponentChildren } from 'preact';
import { useState } from 'preact/hooks';
import { Router, Route, Switch, useLocation } from 'wouter-preact';
import { useHashLocation } from 'wouter-preact/use-hash-location';
import {
  AppShell, TaskListView, StatStrip, DecisionBlock, ProblemRow, FillBar, StateBadge, Num, Totals, AgeChip, ExecTree, Transcript,
  ResultViewer, SettingField, SplitButton, Button, Empty, VirtualList, Icon, Stale, StateCounters, type TaskRow,
} from '../src/index';
import { NAV } from './nav';
import { NOW, min, hr, tasks, TREE, transcript, hosts, PROJECTS } from './fixtures';
import tokens from '../tokens.json';

const ROW = parseInt(tokens.size.row);

function PageHead({ title, children }: { title: string; children?: ComponentChildren }) {
  return <header class="page-h"><h1>{title}</h1><span class="grow" />{children}</header>;
}
const Panel = ({ title, actions, children, class: c }: { title?: string; actions?: ComponentChildren; children: ComponentChildren; class?: string }) => (
  <section class={`panel ${c ?? ''}`}>{title && <header class="panel-h"><h2>{title}</h2><span class="grow" />{actions}</header>}{children}</section>
);

/* ---------- Overview ---------- */

function Overview() {
  const [, go] = useLocation();
  const [answered, setAnswered] = useState<Record<string, string>>({});
  const byProject = PROJECTS.map(p => { const ts = ALL.filter(t => t.project === p); const c: Record<string, number> = {}; for (const t of ts) c[t.state] = (c[t.state] ?? 0) + 1; return { p, c }; });
  return <>
    <PageHead title="Overview"><Stale at={NOW - 20e3} now={NOW} /></PageHead>
    <StatStrip label="Operations" stats={[
      { label: 'need you', icon: 'bell', value: { value: 3, basis: 'measured' }, tone: 'attention', href: '#/tasks' },
      { label: 'failed today', icon: 'octagon-x', value: { value: 2, basis: 'measured' }, tone: 'danger', href: '#/tasks' },
      { label: 'running', icon: 'activity', value: { value: 10, basis: 'measured' }, of: 13, href: '#/hosts' },
      { label: 'queued', icon: 'list-ordered', value: { value: 7, basis: 'measured' } },
      { label: 'spent today', icon: 'chart-no-axes-column', value: { value: 41.2, basis: 'estimated', reason: 'tokens' }, kind: 'money' },
    ]} />
    <div class="cols">
      <Panel title="Needs you" class="wide">
        <div class="stack">
          {[{ id: 'q1', q: 'Accept this result?', by: 'reviewer (claude)', since: NOW - 12 * min, waits: 'T-9989 Write release notes 95', opts: [{ label: 'Accept', kbd: 'a', primary: true }, { label: 'Request rework', kbd: 'r' }] },
            { id: 'q2', q: 'Use the Windows store for the game bundle?', by: 'worker (codex)', since: NOW - 40 * min, waits: 'T-9976 Render prop batch 82', opts: [{ label: 'Yes', kbd: '1' }, { label: 'No, use mac', kbd: '2' }] }].map(d =>
            <DecisionBlock key={d.id} question={d.q} askedBy={d.by} addressedTo="you" since={d.since} now={NOW} waits={d.waits} options={d.opts}
              answered={answered[d.id] ? { by: 'you', answer: answered[d.id], at: NOW, delivered: true } : undefined} onAnswer={a => setAnswered({ ...answered, [d.id]: a })} />)}
          <ProblemRow severity="failure" title="Claude quota exhausted" facts={<>resets in <span class="mono">2h</span> · T-9937 interrupted</>}
            actions={[{ label: 'Continue on codex', primary: true }, { label: 'Wait for reset' }]} />
          <ProblemRow severity="warning" title="Low space on windows store" facts={<><span class="mono">18 GB</span> free, warning at <span class="mono">25 GB</span></>}
            actions={[{ label: 'Open host' }]} />
        </div>
      </Panel>
      <div class="stack">
        <Panel title="Workers per host" actions={<Button label="Hosts" icon="external-link" iconOnly onClick={() => go('/hosts')} />}>
          <ul class="kv">{hosts.map(h => <li key={h.id}>
            <StateBadge family="host" state={h.online ? 'online' : 'offline'} compact />
            <span class="k">{h.id}</span>
            <span class="v"><Num m={{ value: h.running, basis: 'measured', partial: h.online ? undefined : 'last known' }} kind="count" /><span class="muted">/{h.slots}</span></span>
            <span class="v muted">{h.queued ? `${h.queued} queued` : ''}</span>
          </li>)}</ul>
        </Panel>
        <Panel title="Accounts">
          <ul class="kv">
            {[{ a: 'claude-max', used: 0.97, reset: NOW - 3 * hr }, { a: 'codex-pro', used: 0.41, reset: NOW - 1 * hr }, { a: 'antigravity', used: null as number | null, reset: 0 }].map(x => <li key={x.a}>
              <Icon name="bot" /><span class="k">{x.a}</span>
              <FillBar total={x.used === null ? null : 100} segments={x.used === null ? [] : [{ label: 'window', bytes: x.used * 100 }]} warnAt={10} unit="percent" />
            </li>)}
          </ul>
        </Panel>
        <Panel title="Projects">
          <ul class="kv">{byProject.map(({ p, c }) => <li key={p}><Icon name="folder-open" /><span class="k">{p}</span>
            <StateCounters family="task" label={`${p} by state`} counts={{ waiting: c.waiting ?? 0, failed: c.failed ?? 0, in_progress: c.in_progress ?? 0, queued: c.queued ?? 0 }} active={[]} onToggle={() => go('/tasks')} /></li>)}</ul>
        </Panel>
      </div>
    </div>
  </>;
}

/* ---------- Tasks: list and detail ---------- */

const ALL: TaskRow[] = tasks(3000);

function Tasks({ id }: { id?: string }) {
  const [items, setItems] = useState(ALL);
  const [, go] = useLocation();
  const sel = items.find(t => String(t.id) === id);
  return <div class={`split-view${sel ? ' has-detail' : ''}`}>
    <div class="split-list">
      <PageHead title="Tasks"><Button label="New task" icon="plus" kind="primary" /></PageHead>
      <Panel><TaskListView items={items} now={NOW} selected={sel?.id} height="var(--size-page-list)" onOpen={t => go(`/tasks/${t.id}`)}
        onPin={pid => setItems(xs => xs.map(x => x.id === pid ? { ...x, pinned: !x.pinned } : x))} /></Panel>
    </div>
    {sel && <TaskDetail t={sel} onClose={() => go('/tasks')} />}
  </div>;
}

function TaskDetail({ t, onClose }: { t: TaskRow; onClose: () => void }) {
  const [tab, setTab] = useState('Executions');
  const [rtab, setRtab] = useState('Diff');
  const [answer, setAnswer] = useState<string | null>(null);
  return <article class="detail" aria-label={t.title}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <span class="muted mono">{t.project} · T-{t.id}</span><span class="grow" />
      <SplitButton main={{ label: 'Pause', icon: 'pause' }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash' }, { label: 'Change account', icon: 'bot' }, { label: 'Hand off', icon: 'messages-square' }]} />
      <Button label="Share" icon="paperclip" iconOnly />
    </header>
    <h2 class="detail-title">{t.title}</h2>
    <div class="state-line"><StateBadge family="task" state={t.state} reason={t.reason} /><AgeChip t={t.started} now={NOW} /></div>
    <Totals items={[
      { label: 'Time', m: { value: 860, basis: 'measured' }, kind: 'duration' },
      { label: 'Tokens', m: { value: 182000, basis: 'measured', partial: '2 of 3 measured' }, kind: 'count', note: '2 of 3 measured' },
      { label: 'Cost', m: t.cost === null ? { value: null, basis: 'unavailable', reason: 'no meter' } : { value: t.cost, basis: t.est ? 'estimated' : 'measured', reason: 'tokens' }, kind: 'money' },
      { label: 'API calls', m: { value: null, basis: 'unavailable', reason: 'no gateway' }, kind: 'count', note: 'no gateway' },
    ]} />
    {t.state === 'waiting' && <DecisionBlock question="Accept this result?" askedBy="reviewer (claude)" addressedTo="you" since={NOW - 12 * min} now={NOW}
      context={<>Checks passed: links, contrast, screenshots.</>} options={[{ label: 'Accept', kbd: 'a', primary: true }, { label: 'Request rework', kbd: 'r' }]}
      answered={answer ? { by: 'you', answer, at: NOW, delivered: false } : undefined} onAnswer={setAnswer} />}
    {t.state === 'failed' && <ProblemRow severity="failure" title="render-07 failed" facts="exit 1 · log retained · dependants not started" actions={[{ label: 'Retry failed step', primary: true }, { label: 'Open log' }]} />}
    <div class="tabs detail-tabs" role="tablist">{['Executions', 'Transcript', 'Results'].map(x => <button type="button" role="tab" key={x} aria-selected={tab === x} onClick={() => setTab(x)}>{x}</button>)}</div>
    {tab === 'Executions' && <ExecTree nodes={TREE} now={NOW} label="Executions" />}
    {tab === 'Transcript' && <div class="pane"><Transcript entries={transcript()} label="Transcript" now={NOW} /></div>}
    {tab === 'Results' && <ResultViewer name="index.html" type="diff" size={4200} source="Worker · codex" tab={rtab} onTab={setRtab} tabs={['Diff', 'Preview', 'Checks 3']}
      copies={[{ where: 'windows', state: 'stored' }, { where: 'mac', state: 'pending' }]}>
      {rtab === 'Diff' ? <pre class="diff"><span class="del">- &lt;h1&gt;Welcome&lt;/h1&gt;</span><span class="add">+ &lt;h1&gt;Good things work together.&lt;/h1&gt;</span></pre> : <Empty text={`${rtab} viewer`} />}
    </ResultViewer>}
  </article>;
}

/* ---------- Results ---------- */

type Res = { id: string; name: string; type: string; size: number; project: string; task: number; at: number; copy: 'stored' | 'placed' | 'pending' | 'unavailable' };
const TYPES = ['diff', 'image', 'report', 'video', 'glb'];
const RESULTS: Res[] = Array.from({ length: 2000 }, (_, i) => ({ id: `r${i}`, name: `${['index.html', 'hero.png', 'review.md', 'walk.mp4', 'prop.glb'][i % 5]}`, type: TYPES[i % 5], size: (i * 7919 % 4000) * 1e5,
  project: PROJECTS[i % 4], task: 9989 - i, at: NOW - i * 7 * min, copy: (['stored', 'placed', 'placed', 'pending', 'unavailable'] as const)[i * 3 % 5] }));

function Results() {
  const [sel, setSel] = useState<Res>(RESULTS[0]);
  const [tab, setTab] = useState('View');
  return <div class="split-view has-detail">
    <div class="split-list">
      <PageHead title="Results" />
      <Panel>
        <VirtualList items={RESULTS} getKey={r => r.id} rowHeight={ROW} follow="top" label="Results" rowClass={r => r.id === sel.id ? 'sel' : ''}
          columns="minmax(0,1fr) 3.5rem 4rem 4.5rem 1.25rem 2.75rem"
          header={<><span>Result</span><span>Type</span><span>Project</span><span class="r">Size</span><span /><span class="r">Age</span></>}
          render={r => <>
            <a class="title row-link" href={`#/results/${r.id}`} onClick={e => { e.preventDefault(); setSel(r); }}>{r.name} <span class="id">T-{r.task}</span></a>
            <span class="svc">{r.type}</span><span class="svc">{r.project}</span>
            <span class="r"><Num m={{ value: r.size, basis: 'measured' }} kind="bytes" /></span>
            <StateBadge family="copy" state={r.copy} compact />
            <span class="r"><AgeChip t={r.at} now={NOW} /></span>
          </>} />
      </Panel>
    </div>
    <article class="detail">
      <ResultViewer name={sel.name} type={sel.type} size={sel.size} source={`T-${sel.task}`} tab={tab} onTab={setTab} tabs={['View', 'Checks 2', 'Versions 3']}
        copies={[{ where: 'windows', state: sel.copy }, { where: 'mac', state: 'pending' }]}>
        {sel.copy === 'unavailable' ? <Empty text="No holding store is online. Last copy on mac, seen 2h ago." action={<Button label="Notify when online" icon="bell" />} /> : <Empty text={`${sel.type} viewer for ${sel.name}`} />}
      </ResultViewer>
    </article>
  </div>;
}

/* ---------- Hosts ---------- */

function Hosts() {
  const [sel, setSel] = useState(hosts[1].id);
  const h = hosts.find(x => x.id === sel)!;
  return <>
    <PageHead title="Hosts"><Stale at={NOW - 40e3} now={NOW} /></PageHead>
    <StatStrip label="Workers per host" stats={hosts.map(x => ({ label: x.id, icon: 'panels-top-left' as const, value: { value: x.running, basis: 'measured' as const, partial: x.online ? undefined : 'last known' }, of: x.slots, tone: x.running >= x.slots ? 'attention' as const : undefined }))} />
    <ProblemRow severity="warning" title="Low space on windows store" facts={<><span class="mono">18 GB</span> free, warning at <span class="mono">25 GB</span> · 4 uploads waiting</>}
      actions={[{ label: 'Raise capacity', primary: true }, { label: 'Change game locations' }, { label: 'Add retention rule' }]} />
    <Panel title="Hosts and stores">
      <div class="vl-wrap" style={{ '--cols': 'minmax(6rem,10rem) 1.25rem 3.5rem 4rem 3rem minmax(0,1fr)' }}>
        <div class="vl-head" role="row"><span>Host</span><span /><span>Seen</span><span class="r">Workers</span><span class="r">Queue</span><span>Store</span></div>
        {hosts.map(x => <div key={x.id} role="row" class={`vl-row static${x.id === sel ? ' sel' : ''}`} onClick={() => setSel(x.id)}>
          <span class="title">{x.id} <span class="id">{x.platform}</span></span>
          <StateBadge family="host" state={x.online ? 'online' : 'offline'} compact lastSeen={x.online ? undefined : x.lastSeen} now={NOW} />
          <span><AgeChip t={x.lastSeen} now={NOW} alertAfter={15 * 60} tone="attention" /></span>
          <span class="r"><Num m={{ value: x.running, basis: 'measured', partial: x.online ? undefined : 'last known' }} kind="count" /><span class="muted">/{x.slots}</span></span>
          <span class="r"><Num m={{ value: x.queued, basis: 'measured' }} kind="count" /></span>
          <FillBar total={x.store.total} segments={x.store.segments} warnAt={x.store.warnAt} stale={x.store.stale} now={NOW} />
        </div>)}
      </div>
    </Panel>
    <Panel title={`${h.id} settings`}>
      <SettingField label="Store capacity" value={h.capacity.value} help="Garage layout capacity." now={NOW} status={h.capacity.status} changed={{ by: 'Alex', at: NOW - 20 * min }} />
      <SettingField label="Free-space warning" value="25 GB" help="Below this, the store shows a problem." now={NOW} status={{ kind: 'applied', at: NOW - 2 * hr }} />
      <SettingField label="ClickHouse memory" value="4 GB" help="systemd MemoryMax for the Fleet unit." now={NOW} status={{ kind: 'applied', at: NOW - 5 * hr }} />
    </Panel>
  </>;
}

/* ---------- Laboratory ---------- */

const VARIANTS = [
  { v: 'A baseline', score: 0.71, judge: 0.68, tokens: 182000, time: 860, cost: 4.1, est: false, trials: 75, failed: 4 },
  { v: 'B new skill', score: 0.83, judge: 0.8, tokens: 151000, time: 790, cost: 3.6, est: true, trials: 75, failed: 2 },
  { v: 'C claude', score: 0.79, judge: null as number | null, tokens: 210000, time: 1020, cost: null as number | null, est: false, trials: 75, failed: 9 },
];
function Laboratory() {
  const [note, setNote] = useState<string | null>(null);
  const [tab, setTab] = useState('Compare');
  return <>
    <PageHead title="Laboratory"><span class="muted">Experiment: hero copy method · 225 trials · 3 variants</span><SplitButton kind="primary" main={{ label: 'Repeat trials', icon: 'rotate-ccw' }} alternatives={[{ label: 'Add variant', icon: 'plus' }, { label: 'Run on new task set', icon: 'list' }]} /></PageHead>
    <div class="tabs" role="tablist">{['Compare', 'Trials', 'Assessments'].map(x => <button type="button" role="tab" key={x} aria-selected={tab === x} onClick={() => setTab(x)}>{x}</button>)}</div>
    {tab === 'Compare' && <Panel title="Variants">
      <div class="vl-wrap" style={{ '--cols': 'minmax(0,1fr) 4rem 4rem 4.5rem 4rem 4.5rem 5rem' }}>
        <div class="vl-head" role="row"><span>Variant</span><span class="r">Score</span><span class="r">Judge</span><span class="r">Tokens</span><span class="r">Time</span><span class="r">Cost</span><span class="r">Failed</span></div>
        {VARIANTS.map(x => <div key={x.v} role="row" class="vl-row static">
          <span class="title">{x.v}</span>
          <span class="r"><Num m={{ value: x.score, basis: 'measured' }} kind="count" /></span>
          <span class="r"><Num m={{ value: x.judge, basis: x.judge === null ? 'unavailable' : 'measured', reason: 'judge not run' }} kind="count" /></span>
          <span class="r"><Num m={{ value: x.tokens, basis: 'measured' }} kind="count" /></span>
          <span class="r"><Num m={{ value: x.time, basis: 'measured' }} kind="duration" /></span>
          <span class="r"><Num m={{ value: x.cost, basis: x.cost === null ? 'unavailable' : x.est ? 'estimated' : 'measured', reason: x.cost === null ? 'no meter' : 'tokens' }} kind="money" /></span>
          <span class="r"><span class={`chip-age${x.failed > 5 ? ' t-danger alert' : ''}`}>{x.failed}/{x.trials}</span></span>
        </div>)}
      </div>
    </Panel>}
    {tab === 'Trials' && <Panel><TaskListView items={ALL.slice(0, 300)} now={NOW} onPin={() => {}} height="420px" /></Panel>}
    {tab === 'Assessments' && <Empty text="No assessments yet. Write one from the comparison." />}
    <Panel title="Recommendation">
      <DecisionBlock question="Recommend B new skill for hero copy in dashq?" askedBy="lab agent" addressedTo="architect" since={NOW - 3 * hr} now={NOW}
        context="B scores 0.83 against 0.71 with fewer tokens; 2 failed trials kept for review." options={[{ label: 'Recommend', kbd: 'a', primary: true }, { label: 'Keep current' }]}
        answered={note ? { by: 'you', answer: note, at: NOW, delivered: true } : undefined} onAnswer={setNote} />
    </Panel>
  </>;
}

/* ---------- Shell and routes ---------- */

function App() {
  const [loc, go] = useLocation();
  const cur = loc.split('/')[1] || 'overview';
  return <AppShell nav={NAV} current={cur} onGo={id => go(`/${id}`)} waiting={3}>
    <Switch>
      <Route path="/tasks/:id">{(p: { id: string }) => <Tasks id={p.id} />}</Route>
      <Route path="/tasks"><Tasks /></Route>
      <Route path="/results"><Results /></Route>
      <Route path="/hosts"><Hosts /></Route>
      <Route path="/laboratory"><Laboratory /></Route>
      <Route path="/"><Overview /></Route>
      <Route path="/overview"><Overview /></Route>
      <Route><Empty text="This section is not mocked up in stage 3." action={<Button label="Overview" icon="house" onClick={() => go('/overview')} />} /></Route>
    </Switch>
  </AppShell>;
}

render(<Router hook={useHashLocation}><App /></Router>, document.getElementById('app')!);
