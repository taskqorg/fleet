// Design book: the live demo page of packages/ui. Built by scripts/build-book.mjs into one HTML file.
import { render, type ComponentChildren } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import {
  Icon, Button, StateBadge, Num, Totals, AgeChip, FillBar, ExecTree, DecisionBlock, ResultViewer, SplitButton, NavRail, StatStrip,
  SettingField, ProblemRow, TooltipLayer, Skeleton, Empty, ErrorBanner, Stale, Transcript, TaskListView, workspaces, states, actions,
  Composer, OfflineMarker, HarnessCounts, HarnessMark, StageChip, OutcomeBlock, DetailSection, KvRow, stages, StateCounters, ListFrame, RowCheck,
  type Entry, type Family, type Stage,
} from '../src/index';
import { NAV } from './nav';
import { NOW, min, hr, tasks, mkTask, TREE, transcript, hosts, type Task } from './fixtures';


function Frames({ children }: { children: () => ComponentChildren }) {
  return <div class="pair">{(['light', 'dark'] as const).map(t => <div key={t} class={`frame th-${t}`}><div class="frame-label">{t}</div>{children()}</div>)}</div>;
}
function Section({ id, title, spec, note, children }: { id: string; title: string; spec?: string; note?: ComponentChildren; children: () => ComponentChildren }) {
  return (
    <section class="bk" id={id}>
      <h2>{title}{spec && <a class="spec" href={`../controls/${spec}.md`}>{spec}.md</a>}</h2>
      {note && <p class="bk-note">{note}</p>}
      <Frames>{children}</Frames>
    </section>
  );
}

/* ---- Demos ---- */

function TokensDemo() {
  const sw = ['bg', 'surface', 'surface-sunken', 'float', 'fg', 'fg-muted', 'line', 'accent', 'attention-fg', 'attention-bg', 'danger-fg', 'running-fg', 'success-fg', 'neutral-bg', 'estimated-fg', 'data-1', 'data-2', 'data-3', 'data-4'];
  return <>
    <div class="swatches">{sw.map(n => <div key={n}><i style={{ background: `var(--${n})` }} /><span>{n}</span></div>)}</div>
    <p class="type-xl">Page title</p><p class="type-lg">Section</p>
    <p>Body and rows · <span class="mono">T-1042 · 1h 02m · $3.10 · 380 GB</span></p>
    <p class="muted type-sm">Secondary: reasons, IDs, column heads</p>
  </>;
}

function NavDemo() {
  const [cur, setCur] = useState('tasks');
  return <nav class="nav" aria-label="Workspaces">{workspaces.map(w =>
    <a key={w.id} href={`#${w.id}`} aria-current={cur === w.id ? 'page' : undefined} onClick={e => { e.preventDefault(); setCur(w.id); }}><Icon name={w.icon} />{w.label}</a>)}</nav>;
}

function ButtonsDemo() {
  return <div class="row">
    <Button label="Accept" kind="primary" kbd="a" />
    <Button label="Request rework" kbd="r" />
    <Button label="Clear" kind="quiet" />
    <Button label="Cancel task" kind="danger" />
    <Button label="Retry" icon={actions.retry} disabledReason="Paused tasks cannot be retried" />
    <Button label="Raise capacity" icon={actions.retry} busy />
    <Button label="Pin" icon={actions.pin} iconOnly />
    <Button label="Open" icon={actions.open} iconOnly />
    <Button label="More" icon={actions.more} iconOnly />
  </div>;
}

function BadgesDemo() {
  const reasons: Record<string, string> = { queued: 'no free Windows slot', in_progress: 'codex · windows', waiting: 'acceptance by you', paused: 'by Alex', completed: 'accepted automatically', failed: 'render-07 exit 1', cancelled: 'superseded by T-1039' };
  return <div class="badges">
    {(Object.keys(states.task) as (keyof typeof states.task)[]).map(s => <div key={s} class="brow">
      <StateBadge family="task" state={s} reason={reasons[s]} />
      <StateBadge family="task" state={s} compact />
    </div>)}
    <div class="brow"><StateBadge family="execution" state="running" lastSeen={NOW - 4 * min} now={NOW} /><StateBadge family="execution" state="running" lastSeen={NOW - 4 * min} now={NOW} compact /></div>
    {(['copy', 'setting', 'host'] as Family[]).map(f => <div key={f} class="brow">{Object.keys(states[f]).map(s =>
      <StateBadge key={s} family={f} state={s as never} />)}<span class="muted type-sm">{f}</span></div>)}
  </div>;
}

function NumbersDemo() {
  const [cost, setCost] = useState(3.1);
  const [tok, setTok] = useState(182000);
  return <>
    <div class="row">
      <Num m={{ value: cost, basis: 'measured' }} kind="money" rise="bad" />
      <Num m={{ value: 0.42, basis: 'estimated', reason: 'tokens' }} kind="money" />
      <Num m={{ value: null, basis: 'unavailable', reason: 'no gateway' }} kind="money" />
      <Num m={{ value: tok, basis: 'measured', partial: '2 of 3 measured' }} kind="count" rise="neutral" />
      <Button label="Change values" onClick={() => { setCost(c => +(c + 0.37).toFixed(2)); setTok(t => t + 4100); }} />
    </div>
    <Totals items={[
      { label: 'Time', m: { value: 860, basis: 'measured' }, kind: 'duration' },
      { label: 'Tokens', m: { value: tok, basis: 'measured', partial: '2 of 3 measured' }, kind: 'count', note: '2 of 3 measured' },
      { label: 'Cost', m: { value: 0.42, basis: 'estimated', reason: 'tokens' }, kind: 'money', note: 'from tokens' },
      { label: 'API calls', m: { value: null, basis: 'unavailable', reason: 'no gateway' }, kind: 'count', note: 'no gateway' },
    ]} />
    <div class="row">
      <span class="muted type-sm">Age chips:</span>
      <AgeChip t={NOW - 40e3} now={NOW} /><AgeChip t={NOW - 4 * min} now={NOW} /><AgeChip t={NOW - 2 * hr} now={NOW} alertAfter={3600} />
      <AgeChip t={NOW - 3 * 24 * hr} now={NOW} /><AgeChip t={NOW - 40 * 24 * hr} now={NOW} />
    </div>
  </>;
}

function FillDemo() {
  const G = 1e9;
  return <div class="col">
    <FillBar total={1000 * G} segments={[{ label: 'dashq', bytes: 400 * G }, { label: 'fleet', bytes: 220 * G }]} warnAt={50 * G} />
    <FillBar total={200 * G} segments={[{ label: 'game', bytes: 156 * G }, { label: 'design', bytes: 26 * G }]} warnAt={25 * G} />
    <FillBar total={100 * G} segments={[{ label: 'game', bytes: 100 * G }]} />
    <FillBar total={500 * G} segments={[{ label: 'design', bytes: 150 * G }]} stale={NOW - 2 * hr} now={NOW} />
    <FillBar total={null} segments={[]} />
  </div>;
}

/** A list while a newer answer loads: counters and rows keep their values, dimmed; rows can be checked (sel). */
function PendingDemo() {
  const [pending, setPending] = useState(true), [sel, setSel] = useState<string[]>(['b']);
  const rows = [['a', 'Render prop batch', 'in_progress'], ['b', 'Fix CI cache key', 'failed'], ['c', 'Rework hero copy', 'queued']] as const;
  return <div class="col">
    <Button label={pending ? 'Answer arrives' : 'Change filter'} onClick={() => setPending(p => !p)} />
    <ListFrame pending={pending} bar={<div class="fbar"><div class="fbar-line"><StateCounters family="task" label="Tasks by state" pending={pending}
      counts={{ queued: 4, in_progress: 2, failed: 1, waiting: 0 }} active={['failed']} onToggle={() => {}} onAll={() => {}} /></div></div>}>
      <div role="grid" aria-label="Tasks">{rows.map(([id, title, st]) => <div key={id} role="row" class="vl-row static">
        <span class="lead"><StateBadge family="task" state={st} compact /></span><span class="tt">{title}</span>
        <RowCheck checked={sel.includes(id)} label={'Select ' + title} onToggle={() => setSel(x => x.includes(id) ? x.filter(i => i !== id) : [...x, id])} /></div>)}</div>
    </ListFrame>
  </div>;
}

function ListDemo() {
  const [items, setItems] = useState<Task[]>(() => tasks(10000));
  const [stream, setStream] = useState(false);
  useEffect(() => {
    if (!stream) return;
    const t = setInterval(() => setItems(xs => [mkTask(xs[0].id + 1), ...xs]), 900);
    return () => clearInterval(t);
  }, [stream]);
  const insert = () => setItems(xs => [...Array.from({ length: 5 }, (_, i) => mkTask(xs[0].id + 5 - i)), ...xs]);
  const change = () => setItems(xs => xs.map((x, i) => i % 3 === 0 && i < 400 ? { ...x, cost: (x.cost ?? 0) + 0.25 } : x));
  return <div class="pane">
    <TaskListView items={items} now={NOW} onPin={id => setItems(xs => xs.map(x => x.id === id ? { ...x, pinned: !x.pinned } : x))} />
    <div class="bar">
      <Button label="Insert 5 above" icon="plus" onClick={insert} />
      <Button label={stream ? 'Stop stream' : 'Stream rows'} icon={stream ? 'pause' : 'activity'} pressed={stream} onClick={() => setStream(!stream)} />
      <Button label="Change costs" onClick={change} />
      <span class="muted type-sm">10,000 rows. Choose group None, scroll down, then insert: the row you read stays put.</span>
    </div>
  </div>;
}

function TranscriptDemo() {
  const [entries, setEntries] = useState<Entry[]>(transcript);
  const [on, setOn] = useState(false);
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setEntries(es => [...es, { id: `n${es.length}`, t: Date.now(), who: 'Assistant', text: `Streamed entry ${es.length}. API key [masked] not used.` }]), 700);
    return () => clearInterval(t);
  }, [on]);
  return <div class="pane"><Transcript entries={entries} label="Transcript" now={Date.now()} />
    <div class="bar"><Button label={on ? 'Stop' : 'Stream'} icon={on ? 'pause' : 'activity'} pressed={on} onClick={() => setOn(!on)} /><span class="muted type-sm">Times are relative; exact time on hover. At the bottom it follows; scroll up and it holds still.</span></div></div>;
}

function RailDemo() {
  const [cur, setCur] = useState('tasks');
  const [pinned, setPinned] = useState<string[]>(['tasks', 'hosts']);
  return <div class="pane rail-demo"><NavRail items={NAV} pinned={pinned} current={cur} onGo={setCur} onPin={(id, p) => setPinned(p ? [...pinned, id] : pinned.filter(x => x !== id))} /></div>;
}

function SplitDemo() {
  const [last, setLast] = useState('');
  return <div class="row">
    <SplitButton kind="primary" main={{ label: 'Retry', icon: 'rotate-ccw', onClick: () => setLast('Retry') }}
      alternatives={[{ label: 'Retry on another host', icon: 'panels-top-left', onClick: () => setLast('Retry on another host') }, { label: 'Continue on codex', icon: 'bot', onClick: () => setLast('Continue on codex') }, { label: 'Full rerun', icon: 'rotate-cw', onClick: () => setLast('Full rerun') }]} />
    <SplitButton main={{ label: 'Accept', icon: 'check' }} alternatives={[{ label: 'Accept and merge', icon: 'git-merge' }, { label: 'Accept without publishing', icon: 'save' }]} />
    {last && <span class="muted type-sm">ran: {last}</span>}
  </div>;
}

function LifecycleDemo() {
  return <div class="col">
    <div class="row"><span class="muted type-sm">Running on:</span><HarnessMark h="codex" /><HarnessMark h="claude" /><HarnessMark h="antigravity" />
      <span class="muted type-sm">Per host:</span><HarnessCounts counts={{ codex: 4, claude: 2, antigravity: 0 }} label="pds18 workers by harness" /></div>
    <div class="row"><span class="muted type-sm">Delivery stage:</span>{(Object.keys(stages) as Stage[]).map(s => <StageChip key={s} stage={s} />)}</div>
    <OutcomeBlock outcome="Release notes for 0.4 in docs/releases/0.4.md, reviewed, linked from the changelog."
      requirements={[{ text: 'Covers every merged PR since 0.3', met: true }, { text: 'Reviewed by the reviewer role', met: false }, { text: 'Linked from CHANGELOG.md', met: false }]}
      inputs={[{ icon: 'file-text', label: 'template.md' }, { icon: 'git-pull-request', label: '14 merged PRs' }]} depends="Depends on T-9976 (completed)" />
    <DetailSection title="Harnesses"><KvRow icon="openai" label="codex" meta={<span class="mono">0.48.1</span>} value={<span class="muted">ready</span>} /><KvRow icon="antigravity" label="agy" meta={<span class="mono">0.9.2</span>} value={<span class="muted">unchecked</span>} /></DetailSection>
  </div>;
}

function StatsDemo() {
  return <div class="col">
    <StatStrip label="Operations" stats={[
      { label: 'need you', icon: 'bell', value: { value: 3, basis: 'measured' }, tone: 'attention' },
      { label: 'failed today', icon: 'octagon-x', value: { value: 2, basis: 'measured' }, tone: 'danger' },
      { label: 'running', icon: 'activity', value: { value: 10, basis: 'measured' }, of: 13 },
      { label: 'queued', icon: 'list-ordered', value: { value: 7, basis: 'measured' } },
      { label: 'spent today', icon: 'chart-no-axes-column', value: { value: 41.2, basis: 'estimated', reason: 'tokens' }, kind: 'money' },
    ]} />
    <StatStrip label="Workers per host" stats={hosts.map(h => ({ label: h.id, icon: 'panels-top-left' as const, harness: h.byHarness, value: h.online ? { value: h.running, basis: 'measured' as const } : { value: h.running, basis: 'measured' as const, partial: `last seen ${Math.round((NOW - h.lastSeen) / hr)}h ago` }, of: h.slots, tone: h.running >= h.slots ? 'attention' as const : undefined }))} />
  </div>;
}

function DecisionDemo() {
  const [answer, setAnswer] = useState<string | null>(null);
  return <div class="col">
    <DecisionBlock question="Accept this result?" askedBy="reviewer (claude)" addressedTo="you" since={NOW - 12 * min} now={NOW} waits="T-1042 waits"
      context={<>Hero copy rewritten. Checks passed: links, contrast, screenshots. <a href="#result-viewer">Result</a></>}
      options={[{ label: 'Accept', kbd: 'a', primary: true }, { label: 'Request rework', kbd: 'r' }]}
      answered={answer ? { by: 'Alex', answer, at: NOW, delivered: true } : undefined} onAnswer={setAnswer} />
    {answer && <Button label="Reset demo" kind="quiet" onClick={() => setAnswer(null)} />}
    <DecisionBlock question="Use the Windows store for the game bundle?" askedBy="worker (codex)" addressedTo="operators" since={NOW - 40 * min} now={NOW}
      options={[{ label: 'Yes', kbd: '1' }, { label: 'No, use mac', kbd: '2' }]} />
  </div>;
}

function ResultDemo() {
  const [tab, setTab] = useState('Diff');
  return <ResultViewer name="index.html" type="diff" size={4200} source="Worker · codex" tab={tab} onTab={setTab} tabs={['Diff', 'Preview', 'Checks 3']}
    copies={[{ where: 'windows', state: 'stored' }, { where: 'mac', state: 'pending' }]}>
    {tab === 'Diff' ? <pre class="diff"><span class="del">- &lt;h1&gt;Welcome to our page&lt;/h1&gt;</span><span class="add">+ &lt;h1&gt;Good things work together.&lt;/h1&gt;</span>  &lt;p class="lead"&gt;…&lt;/p&gt;</pre>
      : <Empty text={`${tab}: rendered by its viewer.`} />}
  </ResultViewer>;
}

function SettingsDemo() {
  return <div>
    <SettingField label="Store capacity" value="1 TB" help="Garage layout capacity on PDS18." now={NOW} status={{ kind: 'applied', at: NOW - 18 * min }} changed={{ by: 'Alex', at: NOW - 20 * min }} />
    <SettingField label="Store capacity" value="200 GB" help="windows store." now={NOW} status={{ kind: 'apply_error', error: 'Garage admin timeout' }} />
    <SettingField label="Free-space warning" value="25 GB" help="Below this, the store shows a problem." now={NOW} status={{ kind: 'pending' }} />
  </div>;
}

function ProblemDemo() {
  return <div class="col">
    <ProblemRow severity="warning" title="Low space on windows store" facts={<><span class="mono">18 GB</span> free, warning at <span class="mono">25 GB</span> · 4 tasks waiting</>}
      actions={[{ label: 'Raise capacity' }, { label: 'Change game locations' }, { label: 'Add retention rule' }]} />
    <ProblemRow severity="failure" title="Claude quota exhausted" facts={<>resets in <span class="mono">2h</span> · T-1039 interrupted</>}
      actions={[{ label: 'Continue on codex', primary: true }, { label: 'Wait for reset' }]} />
  </div>;
}

function StatesDemo() {
  return <div class="grid2">
    <div class="pane"><Skeleton rows={3} /></div>
    <div class="pane"><Empty text="No tasks match these filters." action={<Button label="Clear filters" />} /></div>
    <div class="pane"><ErrorBanner text="Host list did not update: timeout after 10s" /><Empty text="Earlier data stays below." /></div>
    <div class="pane"><div class="bar"><b>Hosts</b><span class="grow" /><Stale at={NOW - 6 * min} now={NOW} /></div><Empty text="Nothing is waiting for you." /></div>
  </div>;
}

/* ---- Page ---- */

function ThemeButton() {
  const modes = ['system', 'light', 'dark'];
  const [m, setM] = useState(() => { try { return localStorage.getItem('fleet-book-theme') || 'system'; } catch { return 'system'; } });
  useEffect(() => {
    const r = document.documentElement;
    if (m === 'system') delete r.dataset.theme; else r.dataset.theme = m;
    try { localStorage.setItem('fleet-book-theme', m); } catch { /* storage unavailable */ }
  }, [m]);
  return <Button label={`Page theme: ${m}`} onClick={() => setM(modes[(modes.indexOf(m) + 1) % 3])} />;
}

const SECTIONS: [string, string, string | undefined, () => ComponentChildren, ComponentChildren?][] = [
  ['tokens', 'Tokens', undefined, () => <TokensDemo />, <>Colour carries meaning only. Numbers and IDs use the monospace stack.</>],
  ['navigation', 'Navigation anchors', undefined, () => <NavDemo />, <>OR-3: one stable icon per workspace, from the registry.</>],
  ['nav-rail', 'Navigation rail with pinned sections', 'nav-rail', () => <RailDemo />, <>Hover a section and pin it: it moves to the pinned group at the top (D9). Counters show what needs a look. Search finds any section.</>],
  ['split-button', 'Split button', 'split-button', () => <SplitDemo />, <>Click runs the default action; the chevron opens a short menu of alternatives for the same place (allowed by OR-14).</>],
  ['button', 'Button', 'button', () => <ButtonsDemo />],
  ['state-badge', 'State badge', 'state-badge', () => <BadgesDemo />, <>Left: icon and word, for detail views. Right: icon only, the form lists use (B1); hover or focus for the name and reason.</>],
  ['number', 'Numbers, totals, age chips', 'number', () => <NumbersDemo />, <>Measured, estimated (≈), unavailable (—), partial (≥). "Change values" shows the brief tint (OR-6): cost rising tints red, tokens neutral. The 2h chip is red (OR-18).</>],
  ['fill-bar', 'Fill bar', 'fill-bar', () => <FillDemo />],
  ['lifecycle', 'Harness marks, delivery stages, outcome block, detail sections', 'harness-counts', () => <LifecycleDemo />, <>D11: agent mark only while a task runs; delivery stage chip; harness counts per host omit zero; outcome block above task tabs (B2); stacked detail sections (C3).</>],
  ['stat-strip', 'Stat strip', 'stat-strip', () => <StatsDemo />, <>Operational counts: what needs you, failures, running against slots, workers per host. An offline host shows its last known count as a lower bound.</>],
  ['list-pending', 'Updating list: pending counters and rows, row checks', 'row-check', () => <PendingDemo />, <>A filter change keeps the last rows and counts on screen, dimmed under a thin bar, until the new answer replaces them in place. Checked rows are the view URL's sel.</>],
  ['data-list', 'Task list view: views, state counters, grouping, filters, virtual list', 'data-list', () => <ListDemo />, <>Saved views per person (pin, unpin, Save view). State counters show totals and filter on click. Group by state or project; groups collapse. Filters expand inline (OR-14). Title takes the space; service fields sit on the right (OR-21). State is an icon with a delayed tooltip (B1, OR-20).</>],
  ['execution-tree', 'Execution tree', 'execution-tree', () => <ExecTree nodes={TREE} now={NOW} label="Executions" />],
  ['transcript-viewer', 'Live transcript', 'transcript-viewer', () => <TranscriptDemo />],
  ['decision-block', 'Decision block', 'decision-block', () => <DecisionDemo />, <>Click Accept or press a while the block has focus: the block collapses to the answered line.</>],
  ['result-viewer', 'Result viewer shell', 'result-viewer', () => <ResultDemo />],
  ['setting-field', 'Setting field', 'setting-field', () => <SettingsDemo />, <>Click a value to edit it inline.</>],
  ['composer', 'Composer', 'composer', () => <div class="pane"><Composer draftKey="book" onSend={() => {}} /></div>, <>Enter sends, Shift+Enter adds a line; the draft is kept per conversation.</>],
  ['offline-marker', 'Offline marker', 'offline-marker', () => <div class="row"><OfflineMarker state="updating" since={NOW - 18 * hr} now={NOW} /><OfflineMarker state="offline" since={NOW - 18 * hr} now={NOW} /><OfflineMarker state="signed-out" now={NOW} /></div>, <>Stored data is never shown as current (D3).</>],
  ['problem-row', 'Problem row', 'problem-row', () => <ProblemDemo />],
  ['status-states', 'Loading, empty, error, stale', 'status-states', () => <StatesDemo />],
];

function Book() {
  return <div class="page">
    <header class="bk-head">
      <h1>Fleet design book</h1>
      <p class="muted">Live demo of <span class="mono">packages/ui</span>, light and dark side by side. Rules: owner-rules.md.</p>
      <span class="grow" /><ThemeButton />
    </header>
    <nav class="toc" aria-label="Contents">{SECTIONS.map(s => <a key={s[0]} href={`#${s[0]}`}>{s[1]}</a>)}</nav>
    {SECTIONS.map(([id, title, spec, demo, note]) => <Section key={id} id={id} title={title} spec={spec} note={note}>{demo}</Section>)}
    <TooltipLayer />
  </div>;
}

const app = document.getElementById('app');
if (app) render(<Book />, app);
