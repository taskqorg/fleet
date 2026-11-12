// Page mockups (stage 3): built only from packages/ui controls and patterns, with fixture data.
// Specs: docs/design-system/pages/*.md. Built by scripts/build-book.mjs into docs/design-system/pages/mockups.html.
import { render, type ComponentChildren } from 'preact';
import { useState } from 'preact/hooks';
import { Router, Route, Switch, useLocation } from 'wouter-preact';
import { useHashLocation } from 'wouter-preact/use-hash-location';
import {
  AppShell, TaskListView, ROW2, StatStrip, AreaChart, DecisionBlock, ProblemRow, FillBar, StateBadge, Num, Totals, AgeChip, ExecTree, Thread,
  ResultViewer, EntityCode, SettingField, SplitButton, Button, Empty, VirtualList, Icon, Stale, StateCounters, FilterBar,
  ListDetail, DefinitionEditor, Choice, DraftBar, StepList, FlowStrip, HarnessMark, type StepItem, type FlowNode, type Harness, type StateOf, NeedsYou, OpsSummary, ListFrame, OutcomeBlock, DetailSection, KvRow, HarnessCounts, type TaskRow,
} from '../src/index.ts';
import { NAV } from './nav.ts';
import { NOW, min, hr, tasks, TREE, transcript, hosts, PROJECTS } from './fixtures.ts';
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
    <OpsSummary>
    <StatStrip label="Operations" stats={[
      { label: 'failed today', icon: 'octagon-x', value: { value: 2, basis: 'measured' }, tone: 'danger', href: '#/tasks' },
      { label: 'running', icon: 'activity', value: { value: 10, basis: 'measured' }, of: 13, href: '#/hosts' },
      { label: 'queued', icon: 'list-ordered', value: { value: 7, basis: 'measured' } },
      { label: 'spent today', icon: 'chart-no-axes-column', value: { value: 41.2, basis: 'estimated', reason: 'tokens' }, kind: 'money' },
    ]} />
    <div class="cols">
      <NeedsYou>
          {[{ id: 'q1', q: 'Accept this result?', by: 'reviewer (claude)', since: NOW - 12 * min, waits: 'T-9989 Write release notes 95', opts: [{ label: 'Accept', kbd: 'a', primary: true }, { label: 'Request rework', kbd: 'r' }] },
            { id: 'q2', q: 'Use the Windows store for the game bundle?', by: 'worker (codex)', since: NOW - 40 * min, waits: 'T-9976 Render prop batch 82', opts: [{ label: 'Yes', kbd: '1' }, { label: 'No, use mac', kbd: '2' }] }].map(d =>
            <DecisionBlock key={d.id} question={d.q} askedBy={d.by} addressedTo="you" since={d.since} now={NOW} waits={d.waits} options={d.opts}
              answered={answered[d.id] ? { by: 'you', answer: answered[d.id], at: NOW, delivered: true } : undefined} onAnswer={a => setAnswered({ ...answered, [d.id]: a })} />)}
          <ProblemRow severity="failure" title="Claude quota exhausted" facts={<>resets in <span class="mono">2h</span> · T-9937 interrupted</>}
            actions={[{ label: 'Continue on codex', primary: true }, { label: 'Wait for reset' }]} />
          <ProblemRow severity="warning" title="Low space on windows store" facts="18 GB free, warning at 25 GB"
            actions={[{ label: 'Open host' }]} />
      </NeedsYou>
      <div class="stack">
        <Panel title="Workers per host" actions={<Button label="Hosts" icon="external-link" iconOnly onClick={() => go('/hosts')} />}>
          <ul class="kv">{hosts.map(h => <li key={h.id}>
            <StateBadge family="host" state={h.online ? 'online' : 'offline'} compact />
            <span class="k">{h.id}</span>
            <span class="v"><Num m={{ value: h.running, basis: 'measured', partial: h.online ? undefined : 'last known' }} kind="count" /><span class="muted">/{h.slots}</span></span>
            <HarnessCounts counts={h.byHarness} label={`${h.id} workers by harness`} />
            <span class="v muted">{h.queued ? `${h.queued} queued` : ''}</span>
          </li>)}</ul>
        </Panel>
        <Panel title="Accounts">
          <ul class="kv">
            {[{ a: 'claude-max', used: 0.97, reset: NOW - 3 * hr }, { a: 'codex-pro', used: 0.41, reset: NOW - 1 * hr }, { a: 'antigravity', used: null as number | null, reset: 0 }].map(x => <li key={x.a}>
              <Icon name="bot" /><span class="k">{x.a}</span>
              <FillBar total={x.used === null ? null : 100} segments={x.used === null ? [] : [{ label: 'used', bytes: x.used * 100 }]} warnAt={10} unit="percent" />
            </li>)}
          </ul>
        </Panel>
        <Panel title="Projects">
          <ul class="kv">{byProject.map(({ p, c }) => <li key={p}><Icon name="folder-open" /><span class="k">{p}</span>
            <StateCounters family="task" label={`${p} by state`} counts={{ waiting: c.waiting ?? 0, failed: c.failed ?? 0, in_progress: c.in_progress ?? 0, queued: c.queued ?? 0 }} active={[]} onToggle={() => go('/tasks')} /></li>)}</ul>
        </Panel>
      </div>
    </div>
    </OpsSummary>
  </>;
}

/* ---------- Tasks: list and detail ---------- */

const ALL: TaskRow[] = tasks(3000);

function Tasks({ id }: { id?: string }) {
  const [items, setItems] = useState(ALL);
  const [, go] = useLocation();
  const sel = items.find(t => String(t.id) === id);
  return <ListDetail
    list={<>
      <PageHead title="Tasks"><Button label="New task" icon="plus" kind="primary" /></PageHead>
      <Panel><TaskListView items={items} now={NOW} selected={sel?.id} row={ROW2} onOpen={t => go(`/tasks/${t.id}`)}
        onPin={pid => setItems(xs => xs.map(x => x.id === pid ? { ...x, pinned: !x.pinned } : x))} /></Panel>
    </>}
    detail={sel && <TaskDetail t={sel} onClose={() => go('/tasks')} />} detailKey={sel?.id} />;
}

function TaskDetail({ t, onClose }: { t: TaskRow; onClose: () => void }) {
  const [tab, setTab] = useState('Executions');
  const [rtab, setRtab] = useState('Diff');
  const [answer, setAnswer] = useState<string | null>(null);
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={t.title}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <span class="muted mono">{t.project} · T-{t.id}</span><span class="grow" />
      <SplitButton main={{ label: 'Pause', icon: 'pause' }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash' }, { label: 'Change account', icon: 'bot' }, { label: 'Hand off', icon: 'messages-square' }]} />
      <Button label="Share" icon="paperclip" iconOnly />
    </header>
    <h2 class="detail-title">{t.title}</h2>
    <div class="state-line"><StateBadge family="task" state={t.state} reason={t.reason} /><AgeChip t={t.started} now={NOW} /></div>
    <OutcomeBlock outcome="Release notes for 0.4 in releases/0.4.md, reviewed, linked from the changelog."
      requirements={[{ text: 'Covers every merged PR since 0.3', met: true }, { text: 'Reviewed by the reviewer role', met: false }, { text: 'Linked from CHANGELOG.md', met: false }]}
      inputs={[{ icon: 'file-text', label: 'template.md' }, { icon: 'git-pull-request', label: '14 merged PRs' }, { icon: 'messages-square', label: 'PM session' }]}
      depends="Depends on T-9976 (completed)" />
    <Totals items={[
      { label: 'Time', m: { value: 860, basis: 'measured' }, kind: 'duration' },
      { label: 'Tokens', m: { value: 182000, basis: 'measured', partial: '2 of 3 measured' }, kind: 'count', note: '2 of 3 measured' },
      { label: 'Cost', m: t.cost === null ? { value: null, basis: 'unavailable', reason: 'no meter' } : { value: t.cost, basis: t.est ? 'estimated' : 'measured', reason: 'tokens' }, kind: 'money' },
      { label: 'API calls', m: { value: null, basis: 'unavailable', reason: 'no gateway' }, kind: 'count', note: 'no gateway' },
    ]} />
    {t.state === 'waiting' && <DecisionBlock question="Accept this result?" askedBy="reviewer (claude)" addressedTo="you" since={NOW - 12 * min} now={NOW}
      context={<>Checks passed: links, contrast, screenshots.</>} options={[{ label: 'Accept', kbd: 'a', primary: true }, { label: 'Request rework', kbd: 'r' }]}
      answered={answer ? { by: 'you', answer, at: NOW, delivered: false } : undefined} onAnswer={setAnswer} />}
    {t.state === 'failed' && <ProblemRow severity="failure" title="render-07 failed" facts="exit 1 · dependants not started" actions={[{ label: 'Retry step', primary: true }, { label: 'Open log' }]} />}
    <div class="tabs detail-tabs" role="tablist">{['Executions', 'Transcript', 'Results'].map(x => <button type="button" role="tab" key={x} aria-selected={tab === x} onClick={() => setTab(x)}>{x}</button>)}</div>
    {tab === 'Executions' && <ExecTree nodes={TREE} now={NOW} label="Executions" />}
    {tab === 'Transcript' && <div class="pane"><Thread blocks={transcript()} label="Transcript" now={NOW} /></div>}
    {tab === 'Results' && <ResultViewer name="index.html" type="diff" size={4200} source="Worker · codex" tab={rtab} onTab={setRtab} tabs={['Diff', 'Preview', 'Checks 3']}
      copies={[{ where: 'windows', state: 'stored' }, { where: 'mac', state: 'pending' }]}>
      {rtab === 'Diff' ? <pre class="diff"><span class="del">- &lt;h1&gt;Welcome&lt;/h1&gt;</span><span class="add">+ &lt;h1&gt;Good things work together.&lt;/h1&gt;</span></pre> : <Empty text={`${rtab} viewer`} />}
    </ResultViewer>}
  </article>;
}

/* ---------- Results ---------- */

type Res = { id: string; name: string; type: string; size: number; project: string; task: number; at: number; copy: 'stored' | 'placed' | 'pending' | 'unavailable' };
const TYPES = ['diff', 'image', 'report', 'video', 'glb'];
const RESULTS: Res[] = Array.from({ length: 2000 }, (_, i) => ({ id: `r${i}`, name: `${['index.html', 'hero.png', 'review.md', 'walk.mp4', 'prop.glb'][i % 5]}`, type: TYPES[i % 5], size: ((i + 1) * 7919 % 4000 + 1) * 1e5,
  project: PROJECTS[i % 4], task: 9989 - i, at: NOW - i * 7 * min, copy: (['stored', 'placed', 'placed', 'pending', 'unavailable'] as const)[i * 3 % 5] }));

function Results() {
  const [sel, setSel] = useState<Res>(RESULTS[0]);
  const [tab, setTab] = useState('View');
  const [query, setQuery] = useState('');
  const [copies, setCopies] = useState<Res['copy'][]>([]);
  const counts = { stored: 0, placed: 0, pending: 0, unavailable: 0 } as Record<Res['copy'], number>;
  for (const r of RESULTS) counts[r.copy]++;
  const shown = RESULTS.filter(r => (!copies.length || copies.includes(r.copy)) && (!query || r.name.includes(query) || `t-${r.task}`.includes(query.toLowerCase())));
  return <ListDetail
    list={<>
      <PageHead title="Results" />
      <Panel><ListFrame bar={<FilterBar query={query} onQuery={setQuery} placeholder="Search results" chips={[]} onRemove={() => {}} onAdd={() => {}} onClear={() => setCopies([])}
          fields={[{ field: 'Type', values: TYPES.map(t => ({ value: t, count: 400 })) }, { field: 'Project', values: PROJECTS.map(p => ({ value: p, count: 500 })) }]}
          shown={shown.length} total={RESULTS.length}
          extra={<StateCounters family="copy" label="Results by copy state" counts={counts} active={copies} onAll={() => setCopies([])} onToggle={c => setCopies(copies.includes(c) ? copies.filter(x => x !== c) : [...copies, c])} />} />}>
        <VirtualList items={shown} getKey={r => r.id} rowHeight={ROW} follow="top" label="Results" rowClass={r => r.id === sel.id ? 'sel' : ''}
          columns="1.25rem minmax(0,1fr) 4rem 4.5rem 2.75rem"
          header={<><span /><span>Result</span><span>Project</span><span class="r">Size</span><span class="r">Age</span></>}
          render={r => <>
            <span class="lead"><StateBadge family="copy" state={r.copy} compact /></span>
            <span class="tt"><a class="title row-link" href={`#/results/${r.id}`} onClick={e => { e.preventDefault(); setSel(r); }}>{r.name}</a><EntityCode code={`T-FLT-${r.task}`} id={`00000000-0000-4000-8000-${String(r.task).padStart(12, '0')}`} /></span>
            <span class="svc">{r.project}</span>
            <span class="r"><Num m={{ value: r.size, basis: 'measured' }} kind="bytes" /></span>
            <span class="r"><AgeChip t={r.at} now={NOW} /></span>
          </>} />
      </ListFrame></Panel>
    </>}
    detailKey={sel.id}
    detail={<article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.name}>
      <ResultViewer name={sel.name} type={sel.type} size={sel.size} source={`T-${sel.task}`} tab={tab} onTab={setTab} tabs={['View', 'Checks 2', 'Versions 3']}
        copies={[{ where: 'windows', state: sel.copy }, { where: 'mac', state: 'pending' }]}>
        {sel.copy === 'unavailable' ? <Empty text="No holding store is online. Last copy on mac, seen 2h ago." action={<Button label="Notify when online" icon="bell" />} /> : <Empty text={`${sel.type} viewer for ${sel.name}`} />}
      </ResultViewer>
    </article>} />;
}

/* ---------- Hosts ---------- */

/** Limits (pages/hosts.md "Limits", D24): the work area while no host is open. */
function LimitsMock() {
  const d = 24 * hr, from = NOW - 7 * d, to = NOW + 7 * d;
  return <section class="limits" aria-label="Limits">
    <PageHead title="Limits"><Choice label="Burn window" options={[{ id: '1', label: '1h' }, { id: '3', label: '3h' }, { id: '6', label: '6h' }]} value="3" />
      <Choice label="Chart" options={[{ id: 'left', label: 'Left' }, { id: 'burn', label: 'Burn' }]} value="left" />
      <Button label="Earlier" icon="arrow-left" iconOnly /><Button label="Today" pressed /><Button label="Later" icon="arrow-right" iconOnly /><Button label="Refresh all" icon="rotate-cw" iconOnly /></PageHead>
    <div class="limit-cards">
      <article class="panel limit-card" aria-label="codex weekly"><header class="panel-h"><h2>codex <span class="id">weekly</span></h2><span class="grow" /><span class="num">45%</span></header>
        <dl class="limit-facts"><dt>Next reset</dt><dd><time>2d</time> <span class="num">+120</span></dd><dt>First empty</dt><dd><time>1d</time></dd><dt>Pool empty</dt><dd><span class="muted">not before the line ends</span></dd><dt>Burn</dt><dd><span class="num">2.0/h</span></dd></dl>
        <AreaChart label="codex weekly left" from={from} to={to} now={NOW} max={200} value={v => `${Math.round(v / 2)}%`}
          series={[{ label: 'recorded', points: [{ at: from, v: 180 }, { at: NOW - 4 * d, v: 200 }, { at: NOW, v: 90 }], tone: 'd1' },
            { label: 'projected', points: [{ at: NOW, v: 90 }, { at: NOW + 2 * d, v: 20 }, { at: NOW + 2 * d, v: 120 }, { at: NOW + 6 * d, v: 40 }], tone: 'd1', projected: true }]} /></article>
    </div>
    <Panel title="Subscriptions"><div class="table-frame"><table class="limit-table">
      <thead><tr><th scope="col">Subscription</th><th scope="col">5h</th><th scope="col">weekly</th><th scope="col">weekly-opus</th><th scope="col"><span class="sr-only">Refresh</span></th></tr></thead>
      <tbody><tr><th scope="row"><span class="title">alex-pro</span> <span class="id">codex</span></th>
        <td><span class="limit-cell"><FillBar total={100} segments={[{ label: 'used', bytes: 62 }]} warnAt={20} unit="percent" /><time>3h</time><span class="chip t-attention">ahead</span><AgeChip t={NOW - 4 * min} now={NOW} /><span class="muted">execution</span></span></td>
        <td><span class="limit-cell"><FillBar total={100} segments={[{ label: 'used', bytes: 55 }]} warnAt={20} unit="percent" /><time>2d</time><span class="chip">on</span><AgeChip t={NOW - 40 * min} now={NOW} /><span class="muted">probe</span></span></td>
        <td class="muted">—</td><td><Button label="Refresh alex-pro" icon="rotate-cw" iconOnly /></td></tr>
      <tr><th scope="row"><span class="title">team-max</span> <span class="id">claude</span><span class="num muted"> ×2.5</span></th>
        <td><span class="limit-cell"><FillBar total={100} segments={[{ label: 'used', bytes: 91 }]} warnAt={20} unit="percent" /><time>1h</time><span class="chip t-danger"><Icon name="triangle-alert" />Low</span><AgeChip t={NOW - 2 * min} now={NOW} /><span class="muted">execution</span></span></td>
        <td><span class="limit-cell"><FillBar total={100} segments={[{ label: 'used', bytes: 30 }]} warnAt={20} unit="percent" /><time>5d</time><span class="chip">under</span><AgeChip t={NOW - 2 * min} now={NOW} /><span class="muted">execution</span></span></td>
        <td><span class="limit-cell"><FillBar total={100} segments={[{ label: 'used', bytes: 70 }]} warnAt={20} unit="percent" /><time>5d</time><span class="chip t-attention">ahead</span><AgeChip t={NOW - 2 * min} now={NOW} /><span class="muted">execution</span></span></td>
        <td><Button label="Refresh team-max" icon="rotate-cw" iconOnly /></td></tr></tbody>
    </table></div></Panel>
  </section>;
}

function Hosts({ limits }: { limits?: boolean }) {
  const [sel, setSel] = useState<string | null>(limits ? null : hosts[1].id);
  const h = hosts.find(x => x.id === sel);
  // Control column: the section's problem, then the hosts catalogue (patterns/list-detail.md). Rows are two-line; the store fill wraps under them.
  const list = <>
    <PageHead title="Hosts"><Stale at={NOW - 40e3} now={NOW} /></PageHead>
    <ProblemRow severity="warning" title="Low space on windows store" facts="18 GB free, warning at 25 GB · 4 uploads waiting"
      actions={[{ label: 'Raise capacity', primary: true }, { label: 'Change game locations' }, { label: 'Add retention rule' }]} />
    <Panel>
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
        {hosts.map(x => <div key={x.id} role="row" class={`vl-row static${x.id === sel ? ' sel' : ''}`}>
          <span class="lead"><StateBadge family="host" state={x.online ? 'online' : 'offline'} compact lastSeen={x.online ? undefined : x.lastSeen} now={NOW} /></span>
          <span class="tt"><a class="title row-link" href="#/hosts" onClick={e => { e.preventDefault(); setSel(x.id); }}>{x.id}</a>
            {!x.id.includes(x.platform.slice(0, 3)) && <span class="id">{x.platform}</span>}
            <span data-tip="Workers running of slots"><Num m={{ value: x.running, basis: 'measured', partial: x.online ? undefined : 'last known' }} kind="count" /><span class="muted">/{x.slots}</span></span>
            <HarnessCounts counts={x.byHarness} label={`${x.id} workers by harness`} />
            {x.queued > 0 && <span class="hc"><Icon name="list-ordered" label="Queued commands" /><Num m={{ value: x.queued, basis: 'measured' }} kind="count" /></span>}
            <FillBar total={x.store.total} segments={x.store.segments} warnAt={x.store.warnAt} stale={x.store.stale} now={NOW} /></span>
          <span><AgeChip t={x.lastSeen} now={NOW} alertAfter={15 * 60} tone="attention" /></span>
        </div>)}
      </div>
    </Panel>
  </>;
  return <ListDetail list={list} detailKey={sel ?? undefined} idle={<LimitsMock />} detail={h && <article class="detail" data-ui="detail" tabIndex={-1} aria-label={h.id}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => setSel(null)} />{!h.id.includes(h.platform.slice(0, 3)) && <span class="muted mono">{h.platform}</span>}</header>
    <h2 class="detail-title">{h.id}</h2>

      <div class="stack-pad">
        <DetailSection title="Environments">
          <KvRow icon="folder-open" label="game-worktree-3" meta="codex · T-9976" value={<Num m={{ value: 4.2e9, basis: 'measured' }} kind="bytes" />} />
          <KvRow icon="folder-open" label="design-main" meta="idle" value={<Num m={{ value: 1.1e9, basis: 'measured' }} kind="bytes" />} />
        </DetailSection>
        <DetailSection title="Account users">
          <KvRow icon="bot" label="fleet-codex" meta="codex-pro" /><KvRow icon="bot" label="fleet-claude" meta="claude-max" />
        </DetailSection>
        <DetailSection title="Harnesses">
          <KvRow icon="openai" label="codex" meta={<span class="mono">0.48.1</span>} value={<span class="muted">ready</span>} />
          <KvRow icon="claude" label="claude" meta={<span class="mono">2.1.0</span>} value={<span class="muted">ready</span>} />
          <KvRow icon="antigravity" label="agy" meta={<span class="mono">0.9.2</span>} value={<span class="muted">unchecked</span>} />
        </DetailSection>
        <DetailSection title="Store and tunnel">
          <KvRow icon="shield" label="Tunnel" meta="connected" value={<AgeChip t={NOW - 12 * min} now={NOW} />} />
          <SettingField label="Store capacity" value={h.capacity.value} help="Garage layout capacity." now={NOW} status={h.capacity.status} changed={{ by: 'Alex', at: NOW - 20 * min }} />
          <SettingField label="Free-space warning" value="25 GB" help="Below this, the store shows a problem." now={NOW} status={{ kind: 'applied', at: NOW - 2 * hr }} />
          <SettingField label="ClickHouse memory" value="4 GB" help="systemd MemoryMax for the Fleet unit." now={NOW} status={{ kind: 'applied', at: NOW - 5 * hr }} />
        </DetailSection>
      </div>
  </article>} />;
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
    <PageHead title="Laboratory"><span class="muted">Experiment: hero copy method · 3 variants × 75</span><SplitButton kind="primary" main={{ label: 'Repeat trials', icon: 'rotate-ccw' }} alternatives={[{ label: 'Add variant', icon: 'plus' }, { label: 'Run on new task set', icon: 'list' }]} /></PageHead>
    <div class="tabs" role="tablist">{['Compare', 'Trials', 'Assessments'].map(x => <button type="button" role="tab" key={x} aria-selected={tab === x} onClick={() => setTab(x)}>{x}</button>)}</div>
    {tab === 'Compare' && <Panel title="Variants">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) 4rem 4rem 4.5rem 4rem 4.5rem 5rem' }}>
        <div class="vl-head" role="row"><span /><span>Variant</span><span class="r">Score</span><span class="r">Judge</span><span class="r">Tokens</span><span class="r">Time</span><span class="r">Cost</span><span class="r">Failed</span></div>
        {VARIANTS.map((x, i) => <div key={x.v} role="row" class="vl-row static">
          <span class="lead"><StateBadge family="execution" state={i === 2 ? 'running' : 'succeeded'} reason={i === 2 ? 'trials still running' : 'all trials finished'} compact /></span>
          <span class="title">{x.v}</span>
          <span class="r"><Num m={{ value: x.score, basis: 'measured' }} kind="score" /></span>
          <span class="r"><Num m={{ value: x.judge, basis: x.judge === null ? 'unavailable' : 'measured', reason: 'judge not run' }} kind="score" /></span>
          <span class="r"><Num m={{ value: x.tokens, basis: 'measured' }} kind="count" /></span>
          <span class="r"><Num m={{ value: x.time, basis: 'measured' }} kind="duration" /></span>
          <span class="r"><Num m={{ value: x.cost, basis: x.cost === null ? 'unavailable' : x.est ? 'estimated' : 'measured', reason: x.cost === null ? 'no meter' : 'tokens' }} kind="money" /></span>
          <span class="r"><span class={`chip-age${x.failed > 5 ? ' t-danger alert' : ''}`} aria-label={`${x.failed} of ${x.trials} trials failed`}>{x.failed > 5 && <Icon name="triangle-alert" />}{x.failed}/{x.trials}</span></span>
        </div>)}
      </div>
    </Panel>}
    {tab === 'Trials' && <Panel><TaskListView items={ALL.slice(0, 300)} now={NOW} onPin={() => {}} height="420px" /></Panel>}
    {tab === 'Assessments' && <Empty text="No assessments yet. Write one from the comparison." />}
    <Panel>
      <DecisionBlock question="Use B new skill for hero copy in cs?" askedBy="lab agent" addressedTo="architect" since={NOW - 3 * hr} now={NOW}
        context="B scores 0.83 against 0.71 with fewer tokens; 2 failed trials kept for review." options={[{ label: 'Yes', kbd: 'a', primary: true }, { label: 'Keep current' }]}
        answered={note ? { by: 'you', answer: note, at: NOW, delivered: true } : undefined} onAnswer={setNote} />
    </Panel>
  </>;
}

/* ---------- Definitions kept in git: agents, pipelines, catalogue (patterns/definition-editor.md, D20) ---------- */

const m = (value: number | null, basis: 'measured' | 'estimated' | 'unavailable' = 'measured', reason?: string) => ({ value, basis, reason });
// A catalogue row built by hand: state, name with its second-line facts, one fact on the right.
function CatRow({ id, sel, onOpen, state, children, right }: { id: string; sel: boolean; onOpen: () => void; state: ComponentChildren; children?: ComponentChildren; right?: ComponentChildren }) {
  return <div role="row" class={`vl-row static${sel ? ' sel' : ''}`}>
    <span class="lead">{state}</span>
    <span class="tt"><a class="title row-link" href="#" onClick={e => { e.preventDefault(); onOpen(); }}>{id}</a>{children}</span>
    <span class="muted mono">{right}</span>
  </div>;
}
const CAT_COLS = { '--cols': '1.25rem minmax(0,1fr) auto' };

type AgentDef = { id: string; state: StateOf<'definition'>; h: Harness; model: string; role: string; right: string };
const AGENT_DEFS: AgentDef[] = [
  { id: 'claude-reviewer', state: 'draft', h: 'claude', model: 'sonnet', role: 'reviewer', right: '57' },
  { id: 'pm', state: 'ready', h: 'codex', model: 'gpt-6.1-sol', role: 'PM', right: '412' },
  { id: 'claude-worker', state: 'ready', h: 'claude', model: 'opus', role: 'worker', right: '1,204' },
  { id: 'codex-researcher', state: 'ready', h: 'codex', model: 'gpt-6.1', role: 'worker', right: '96' },
  { id: 'codex-judge', state: 'ready', h: 'codex', model: 'gpt-6.1', role: 'judge', right: '340' },
  { id: 'antigravity-worker', state: 'unchecked', h: 'antigravity', model: 'gemini-3', role: 'worker', right: '0' },
  { id: 'design-rules-data', state: 'incompatible', h: 'antigravity', model: 'gemini-3', role: 'worker', right: '0' },
];
const HARNESS_TOOLS = ['Read', 'Glob', 'Grep', 'Bash (read-only)', 'Edit', 'Write', 'WebFetch', 'Agent'];

function Agents() {
  const [sel, setSel] = useState<string | null>('claude-reviewer');
  const [tab, setTab] = useState('Configuration');
  const [role, setRole] = useState('reviewer');
  const [tools, setTools] = useState(['Read', 'Glob', 'Grep', 'Bash (read-only)']);
  const a = AGENT_DEFS.find(x => x.id === sel);
  const list = <>
    <PageHead title="Agents"><Button label="New agent" icon="plus" iconOnly kind="primary" /></PageHead>
    <Panel><div class="vl-wrap" style={CAT_COLS}>
      {AGENT_DEFS.map(x => <CatRow key={x.id} id={x.id} sel={x.id === sel} onOpen={() => setSel(x.id)} right={x.right}
        state={<StateBadge family="definition" state={x.state} compact />}>
        <HarnessMark h={x.h} /><span class="id">{x.model}</span>{!x.id.includes(x.role) && <span class="muted">{x.role}</span>}</CatRow>)}
    </div></Panel>
  </>;
  return <ListDetail list={list} detailKey={sel ?? undefined} detail={a && <DefinitionEditor label={a.id} crumb={`shared · config/agents/${a.id}.json · 4e5bf83`}
    title={<>{a.id}<HarnessMark h={a.h} /></>} tabs={['Configuration', 'Effective', 'Diff', 'Compatibility', 'Usage']} tab={tab} onTab={setTab} onClose={() => setSel(null)}>
    {tab === 'Configuration' && <>
      <DraftBar changes={2} base="4e5bf83" note="Instructions and tools differ. New executions keep using the committed revision."
        actions={[{ label: 'Test', icon: 'play' }, { label: 'Diff', onClick: () => setTab('Diff') }, { label: 'Save as commit', icon: 'save', primary: true }]} />
      <DetailSection title="Identity">
        <SettingField label="Purpose" value="Reviews a finished change against the task and the project rules. Never edits." help="One line; shown wherever this agent is picked." now={NOW} status={{ kind: 'applied', at: NOW - 2 * hr }} />
        <KvRow icon="bot" label="Role" value={<Choice label="Role" value={role} onChange={setRole} options={['PM', 'worker', 'reviewer', 'judge', 'custom'].map(r => ({ id: r, label: r }))} />} />
        <KvRow icon="settings" label="Harness" value={<Choice label="Harness" value={a.h} options={[{ id: 'claude', label: 'Claude', icon: 'claude' }, { id: 'codex', label: 'Codex', icon: 'openai' }, { id: 'antigravity', label: 'Antigravity', icon: 'antigravity' }]} />} />
        <SettingField label="Model" value={a.model} help="The harness lists the models it can run." now={NOW} status={{ kind: 'applied', at: NOW - 2 * hr }} />
      </DetailSection>
      <DetailSection title="Instructions">
        <pre class="diff block">{'Review the explicit supplied task and retained materials.\nDo not modify files or delegate work.\n'}<span class="add">Report each finding as: file, line, rule, severity (blocker | major | nit).</span><span class="add">Return "changes requested" when any blocker or major finding exists; otherwise "pass".</span></pre>
        <span class="muted">Three more layers reach the agent. The Effective tab lists all of them in order.</span>
      </DetailSection>
      <DetailSection title="Skills">
        <KvRow icon="file-text" label="source-citations" meta={<span class="mono">a41c9e0 · 4 agents</span>} value={<Button label="Remove" icon="x" iconOnly />} />
        <KvRow icon="file-text" label="review-checklist" meta={<span class="mono">9d02f71 · 2 agents</span>} value={<Button label="Remove" icon="x" iconOnly />} />
        <div><Button label="Add skill" icon="plus" /></div>
      </DetailSection>
      <DetailSection title="Tools">
        <KvRow icon="terminal" label="Harness" value={<Choice label="Harness tools" value={tools} onChange={t => setTools(tools.includes(t) ? tools.filter(x => x !== t) : [...tools, t])} options={HARNESS_TOOLS.map(t => ({ id: t, label: t }))} />} />
        <KvRow icon="link" label="Connections" meta="credentials stay in the connection" value={<Choice label="Connections" value={['fleet']} options={[{ id: 'fleet', label: 'fleet · questions, reply' }, { id: 'tavily', label: 'tavily · web_search' }, { id: 'github', label: 'github · pr_read' }]} />} />
        <KvRow icon="panels-top-left" label="Environment" value={<Choice label="Environment needs" value={['checkout', 'os']} options={[{ id: 'checkout', label: 'checkout: read' }, { id: 'os', label: 'os: any' }, { id: 'gpu', label: 'gpu' }]} />} />
      </DetailSection>
    </>}
    {tab === 'Effective' && <>
      <DetailSection title="Instruction layers, in the order the agent receives them">
        <KvRow icon="shield" label="Shared rules" meta={<span class="mono">specq @ 13772b7 · 3.1k tokens</span>} />
        <KvRow icon="bot" label="Role" meta={<span class="mono">config/roles/reviewer.md @ 77ab102 · 420 tokens</span>} />
        <KvRow icon="file-plus-2" label="This configuration" meta={<span class="mono">draft · 214 tokens</span>} />
        <KvRow icon="folder-open" label="Project override" meta="resolved when an execution starts" />
      </DetailSection>
      <DetailSection title="Effective settings">
        <KvRow icon="settings" label="Model" meta={<span class="mono">sonnet</span>} />
        <KvRow icon="terminal" label="Tools" meta="what this file allows, within the project grant and the task's choice" />
      </DetailSection>
    </>}
    {tab === 'Diff' && <>
      <pre class="diff block">{'  "instructions": "Review the explicit supplied task and retained materials. Do not modify files or delegate work.'}<span class="add">{' Report each finding as: file, line, rule, severity (blocker | major | nit). Return \\"changes requested\\" when any blocker or major finding exists; otherwise \\"pass\\".'}</span>{'",\n  "tools": ["Read", "Glob", '}<span class="del">{'"Grep"]'}</span><span class="add">{'"Grep", "Bash(read-only)"]'}</span></pre>
      <div><Button label="Compare in Lab" icon="chart-no-axes-column" /></div>
    </>}
    {tab === 'Compatibility' && <DetailSection title="Checked combinations">
      {([['ready', 'claude 2.1.0 · sonnet · mac, pds18', 'at 4e5bf83'], ['ready', 'claude 2.0.4 · sonnet · windows', 'at 0c1d7aa'], ['unchecked', 'claude 2.1.0 · sonnet · draft', 'tools changed'], ['incompatible', 'claude 1.9.8 · sonnet · windows', 'this version does not know the Bash sandbox flag']] as const).map(r =>
        <div key={r[1]} class="kv-row"><StateBadge family="definition" state={r[0]} compact /><span class="mono">{r[1]}</span><span class="muted">{r[2]}</span></div>)}
      <div><Button label="Check now" icon="play" /></div>
    </DetailSection>}
    {tab === 'Usage' && <>
      <DetailSection title="Used by">
        <KvRow icon="network" label="issue-to-pr" meta="pipeline · step review-code" />
        <KvRow icon="network" label="website-page" meta="pipeline · step review" />
        <KvRow icon="rows-3" label="code-review" meta="catalogue method · recommended for cs, fleet" />
      </DetailSection>
      <Totals items={[{ label: 'Executions, 30 days', m: m(58), kind: 'count', note: '3 failed' }, { label: 'Median time', m: m(250), kind: 'duration' }, { label: 'Cost', m: m(41.2, 'estimated', 'tokens'), kind: 'money' }]} />
    </>}
  </DefinitionEditor>} />;
}

const FACTORY: StepItem[] = [
  { id: 'intake', kind: 'agent', executor: <><b>intake</b> accepts, narrows, rejects or asks</>, reads: 'issue', writes: 'intake.md, risk, surfaces' },
  { id: 'spec', kind: 'agent', executor: <><b>spec-writer</b> records assumptions, never stops to ask</>, reads: 'issue, intake.md', writes: 'spec.md' },
  { band: 'loop', label: 'Repeat until every review passes · at most 3 rounds · then ask the operator' },
  { id: 'build', kind: 'agent', executor: <><b>claude-worker</b> in its own worktree</>, reads: 'spec.md, reviews, checks log', writes: 'change, build.md', inset: true },
  { id: 'gate', kind: 'program', executor: <><b>checks.sh</b> a fail goes back to build and no reviewer starts</>, reads: 'change', writes: 'checks.md', inset: true },
  { band: 'parallel', label: 'In parallel · intake decides which reviewers run', inset: true },
  { id: 'review-code', kind: 'agent', executor: <><b>claude-reviewer</b> model chosen by risk</>, reads: 'change, spec.md', writes: 'review, verdict', inset: true },
  { id: 'review-ui', kind: 'agent', executor: <><b>website-reviewer</b> only when the surface is ui</>, reads: 'change, spec.md', writes: 'review, verdict', inset: true },
  { id: 'review-security', kind: 'agent', executor: <><b>security-reviewer</b> only when the surface is security</>, reads: 'change, spec.md', writes: 'review, verdict', inset: true },
  { id: 'approve', kind: 'agent', executor: <><b>approver</b> approves, or escalates to the operator</>, reads: 'spec.md, reviews, checks.md', writes: 'decision.md' },
  { id: 'deliver', kind: 'tool', executor: <><b>session_deliver</b> syncs with main, reruns the gate, merges</>, reads: 'change, decision.md', writes: 'pull request' },
];
const FACTORY_FLOW = (cur?: string): FlowNode[] => [
  { id: 'intake', kind: 'agent' }, { id: 'spec', kind: 'agent' },
  { loop: 'until all pass · max 3', nodes: [{ id: 'build', kind: 'agent' }, { id: 'gate', kind: 'program' }, { parallel: [{ id: 'review-code', kind: 'agent', current: cur === 'review-code' }, { id: 'review-ui', kind: 'agent', optional: true, current: cur === 'review-ui' }, { id: 'review-security', kind: 'agent', optional: true, current: cur === 'review-security' }] }] },
  { id: 'approve', kind: 'agent' }, { id: 'deliver', kind: 'tool' }];
const RENDER_RUN: StepItem[] = [
  { id: 'prepare', kind: 'program', executor: <b>prepare-inputs.sh</b>, reads: 'batch', writes: 'inputs/', run: { state: 'succeeded', note: 'reused', reason: 'taken from run 40: inputs and definition unchanged', time: m(0), cost: m(null, 'unavailable', 'no meter') } },
  { band: 'parallel', label: 'In parallel, one per asset · 8 assets' },
  { id: 'render', kind: 'tool', executor: <><b>render_asset</b> through the blender connection</>, reads: 'inputs/', writes: 'renders/', inset: true,
    run: { state: 'failed', note: '7 of 8', reason: 'asset-07 failed', time: m(372), cost: m(3.4, 'estimated', 'tokens'), error: 'asset-07 · exit 1 after 2m 04s on windows · CUDA out of memory, tried to allocate 2.1 GB. The 7 finished renders are kept.' } },
  { id: 'assemble', kind: 'agent', executor: <><b>codex-designer</b> builds the catalogue page</>, reads: 'renders/', writes: 'catalogue.html', run: { state: 'waiting', note: 'waits for render', time: m(null, 'unavailable', 'not started'), cost: m(null, 'unavailable', 'not started') } },
];
type PipeDef = { id: string; state: ComponentChildren; facts: string; right: string };
const PIPE_DEFS: PipeDef[] = [
  { id: 'issue-to-pr', state: <StateBadge family="definition" state="draft" compact />, facts: '9 steps · 7 agents · shared', right: '2h' },
  { id: 'render-props', state: <StateBadge family="execution" state="failed" compact />, facts: '3 steps · csgo', right: '4m' },
  { id: 'web-research', state: <StateBadge family="execution" state="succeeded" compact />, facts: '4 steps · 1 agent · shared', right: '12m' },
  { id: 'compare-3d-methods', state: <StateBadge family="execution" state="running" compact />, facts: '5 steps · 2 agents · csgo', right: 'now' },
  { id: 'draft-examples', state: <StateBadge family="definition" state="never" compact />, facts: '2 steps · 1 agent · shared', right: 'never' },
];

function StepEditor({ onAgent }: { onAgent: () => void }) {
  return <>
    <KvRow icon="settings" label="Kind" value={<Choice label="Kind" value="agent" options={[{ id: 'agent', label: 'Agent', icon: 'bot' }, { id: 'tool', label: 'Tool', icon: 'settings' }, { id: 'program', label: 'Program', icon: 'terminal' }, { id: 'pipeline', label: 'Pipeline', icon: 'network' }]} />} />
    <KvRow icon="bot" label="Agent" meta="claude-reviewer" value={<Button label="Open configuration" icon="external-link" onClick={onAgent} />} />
    <KvRow icon="play" label="Runs" value={<Choice label="Runs" value="always" options={[{ id: 'always', label: 'always' }, { id: 'when', label: 'when an earlier output says so' }]} />} />
    <KvRow icon="sliders-horizontal" label="Model" meta="haiku when intake rates the risk low, otherwise sonnet" value={<Button label="Add rule" icon="plus" iconOnly />} />
    <SettingField label="Assignment" value="Review the change against spec.md and the project rules. Write the review and a verdict." help="What the agent is asked to do. It chooses its own actions." now={NOW} status={{ kind: 'applied', at: NOW - 3 * hr }} />
    <h3 class="sub-h">Inputs, each bound to an earlier output</h3>
    <KvRow icon="link" label="change" meta="from build" value={<span class="muted">required</span>} />
    <KvRow icon="link" label="spec.md" meta="from spec" value={<span class="muted">required</span>} />
    <KvRow icon="file-plus-2" label="checks.md" meta="from gate · added in this draft" value={<span class="muted">optional</span>} />
    <h3 class="sub-h">Outputs later steps may bind to</h3>
    <KvRow icon="file-text" label="review" meta="markdown · findings by file and severity" />
    <KvRow icon="check" label="verdict" meta="pass, or changes requested" />
    <h3 class="sub-h">Then</h3>
    <KvRow icon="circle-check" label="Pass" meta="counts once; when every reviewer passes, go on to approve" />
    <KvRow icon="rotate-cw" label="Changes requested" meta="back to build with this round's reviews · an outcome, not a failure" />
    <KvRow icon="clock" label="After 3 rounds" meta="stop, keep the change and its reviews, ask the operator" />
    <KvRow icon="octagon-x" label="The step fails" meta="the run fails and outputs are kept · project rules decide a retry" />
    <div><Button label="Remove step" icon="trash-2" kind="danger" /></div>
  </>;
}

function Pipelines({ id = 'issue-to-pr' }: { id?: string }) {
  const [, go] = useLocation();
  const [open, setOpen] = useState<string | undefined>('review-code');
  const [tab, setTab] = useState(id === 'render-props' ? 'Runs' : 'Definition');
  const failed = id === 'render-props';
  const list = <>
    <PageHead title="Pipelines"><Button label="New pipeline" icon="plus" iconOnly kind="primary" /></PageHead>
    <Panel><div class="vl-wrap" style={CAT_COLS}>
      {PIPE_DEFS.map(p => <CatRow key={p.id} id={p.id} sel={p.id === id} onOpen={() => go(p.id === 'render-props' ? '/pipelines/render-props' : '/pipelines')} state={p.state} right={p.right}><span class="facts">{p.facts}</span></CatRow>)}
    </div></Panel>
  </>;
  return <ListDetail list={list} detailKey={id} detail={<DefinitionEditor key={id} label={id} crumb={failed ? 'csgo · config/pipelines/render-props.json · run 41 at 94e588a' : 'shared · config/pipelines/issue-to-pr.json · 5eefade'}
    title={id} tabs={['Definition', 'Runs', 'Used by', 'History']} tab={tab} onTab={setTab} onClose={() => go('/pipelines')}>
    {tab === 'Definition' && !failed && <>
      <DraftBar changes={1} base="5eefade" note="The review-code step reads one more input." actions={[{ label: 'Run', icon: 'play' }, { label: 'Diff' }, { label: 'Save as commit', icon: 'save', primary: true }]} />
      <DetailSection title="Starts when">
        <Choice label="Triggers" value={['label', 'hand']} options={[{ id: 'label', label: 'an issue gets the label factory', icon: 'github' }, { id: 'hand', label: 'a person or an agent starts it' }, { id: 'add', label: 'Add trigger', icon: 'plus' }]} />
      </DetailSection>
      <DetailSection title="Flow"><FlowStrip nodes={FACTORY_FLOW(open)} label="intake, spec, then a loop of build, gate and parallel reviews, then approve and deliver" /></DetailSection>
      <DetailSection title="Steps">
        <StepList items={FACTORY} open={open} onOpen={s => setOpen(open === s ? undefined : s)} label="Steps of issue-to-pr"><StepEditor onAgent={() => go('/agents')} /></StepList>
      </DetailSection>
    </>}
    {tab === 'Definition' && failed && <DetailSection title="Steps"><StepList items={RENDER_RUN.map(s => 'band' in s ? s : { ...s, run: undefined })} label="Steps of render-props" /></DetailSection>}
    {tab === 'Runs' && failed && <>
      <ProblemRow severity="failure" title="Run 41 stopped at render" facts="Repeating asset-07 and assemble is enough: prepare and 7 renders are kept."
        actions={[{ label: 'Rerun failed', icon: 'rotate-ccw', primary: true }, { label: 'Repeat all steps' }, { label: 'Edit definition' }]} />
      <DetailSection title="Flow"><FlowStrip label="prepare, eight parallel renders with one failed, then assemble waiting"
        nodes={[{ id: 'prepare', kind: 'program', state: 'succeeded' }, { parallel: [{ id: 'render × 7', kind: 'tool', state: 'succeeded' }, { id: 'asset-07', kind: 'tool', state: 'failed', current: true }] }, { id: 'assemble', kind: 'agent', state: 'waiting' }]} /></DetailSection>
      <DetailSection title="Steps of run 41"><StepList items={RENDER_RUN} label="Steps of run 41" /></DetailSection>
      <Totals items={[{ label: 'Wall time', m: m(374), kind: 'duration' }, { label: 'Cost', m: m(3.4, 'estimated', 'tokens'), kind: 'money' }, { label: 'Renders kept', m: m(7), kind: 'count', note: 'of 8' }]} />
    </>}
    {tab === 'Runs' && !failed && <Empty text="41 runs. Open render-props to see a run with a failed step." />}
    {(tab === 'Used by' || tab === 'History') && <Empty text={`${tab} is not mocked up.`} />}
  </DefinitionEditor>} />;
}

const CAPS = [
  { id: 'web-research', state: 'ready', facts: '3 methods · 2 projects', right: '96' },
  { id: 'code-review', state: 'ready', facts: '2 methods · 3 projects', right: '58' },
  { id: '3d-model-from-description', state: 'unchecked', facts: '3 methods · comparing in Lab', right: '12' },
] as const;
function Catalogue() {
  const [sel, setSel] = useState<string | null>('web-research');
  const [tab, setTab] = useState('Methods');
  const list = <>
    <PageHead title="Catalogue" />
    <Panel><div class="vl-wrap" style={CAT_COLS}>
      {CAPS.map(c => <CatRow key={c.id} id={c.id} sel={c.id === sel} onOpen={() => setSel(c.id)} state={<StateBadge family="definition" state={c.state} compact />} right={c.right}><span class="facts">{c.facts}</span></CatRow>)}
    </div></Panel>
  </>;
  return <ListDetail list={list} detailKey={sel ?? undefined} detail={sel && <DefinitionEditor label={sel} crumb="shared · config/catalogue.json · 5eefade" title={sel}
    tabs={['Methods', 'Contract', 'Assessments']} tab={tab} onTab={setTab} onClose={() => setSel(null)} actions={<Button label="Add method" icon="plus" />}>
    {tab === 'Methods' && <>
      <DetailSection title="What the caller gets">
        <KvRow icon="arrow-down" label="Input" meta={<span class="mono">prompt · max sources</span>} />
        <KvRow icon="arrow-up" label="Result" meta={<span class="mono">summary.md · sources · pages (optional)</span>} />
      </DetailSection>
      <DetailSection title="Methods">
        <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1.4fr) 4rem minmax(0,1fr) 4rem 4rem 4.5rem minmax(0,1.2fr)' }}>
          <div class="vl-head" role="row"><span /><span>Method</span><span>Scope</span><span>Runs</span><span class="r">Quality</span><span class="r">Time</span><span class="r">Cost</span><span>For</span></div>
          <div role="row" class="vl-row static"><StateBadge family="definition" state="ready" compact /><span class="title">Source discovery</span><span class="muted">shared</span><span class="mono" data-tip="config/pipelines/search-basic.json">search-basic</span>
            <span class="r"><Num m={m(0.82)} kind="score" /></span><span class="r"><Num m={m(8)} kind="duration" /></span><span class="r"><Num m={m(0.02)} kind="money" /></span><span>everyone</span></div>
          <div role="row" class="vl-row static"><StateBadge family="definition" state="ready" compact /><span class="title">Summary with sources</span><span class="muted">shared</span><span class="mono" data-tip="config/pipelines/web-research.json">web-research</span>
            <span class="r"><Num m={m(0.91)} kind="score" /></span><span class="r"><Num m={m(130)} kind="duration" /></span><span class="r"><Num m={m(0.38, 'estimated', 'tokens')} kind="money" /></span><span>cs, fleet</span></div>
          <div role="row" class="vl-row static"><StateBadge family="definition" state="unchecked" compact /><span class="title">Guided delivery</span><span class="muted">csgo</span><span class="mono" data-tip="config/pipelines/prop-search.json">prop-search</span>
            <span class="r"><Num m={m(null, 'unavailable', 'not assessed')} kind="score" /></span><span class="r"><Num m={m(null, 'unavailable', 'not assessed')} kind="duration" /></span><span class="r"><Num m={m(null, 'unavailable', 'not assessed')} kind="money" /></span><span class="muted">nobody yet</span></div>
        </div>
      </DetailSection>
      <DetailSection title="Which method a call uses">
        <KvRow icon="list" label="The task names one" meta="that method, never replaced without permission" />
        <KvRow icon="folder-open" label="Else the project recommends one" meta="cs, fleet: Summary with sources" />
        <KvRow icon="rows-3" label="Else the shared default" meta="Source discovery" />
      </DetailSection>
      <div class="state-line"><Button label="Recommend for a project" icon="check-check" /><Button label="Compare in Lab" icon="chart-no-axes-column" /></div>
    </>}
    {tab !== 'Methods' && <Empty text={`${tab} is not mocked up.`} />}
  </DefinitionEditor>} />;
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
      <Route path="/hosts/limits"><Hosts limits /></Route>
      <Route path="/lab"><Laboratory /></Route>
      <Route path="/agents"><Agents /></Route>
      <Route path="/pipelines/:id">{(p: { id: string }) => <Pipelines key={p.id} id={p.id} />}</Route>
      <Route path="/pipelines"><Pipelines key="issue-to-pr" /></Route>
      <Route path="/catalogue"><Catalogue /></Route>
      <Route path="/"><Overview /></Route>
      <Route path="/overview"><Overview /></Route>
      <Route><Empty text="This section is not mocked up in stage 3." action={<Button label="Overview" icon="house" onClick={() => go('/overview')} />} /></Route>
    </Switch>
  </AppShell>;
}

render(<Router hook={useHashLocation}><App /></Router>, document.getElementById('app')!);
