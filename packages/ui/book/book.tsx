// Design book: the live demo page of packages/ui. Built by scripts/build-book.mjs into one HTML file.
import { render, type ComponentChildren } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import {
  Icon, Button, StateBadge, Num, Totals, AgeChip, FillBar, FilterBar, ExecTree, DecisionBlock, ResultViewer,
  SettingField, ProblemRow, TooltipLayer, Skeleton, Empty, ErrorBanner, Stale, Transcript, VirtualList, workspaces, states, actions,
  type Chip, type TreeNode, type Entry, type Family,
} from '../src/index';
import tokens from '../tokens.json';

const NOW = Date.now();
const min = 60e3, hr = 60 * min;
const ROW = parseInt(tokens.size.row);

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

type Task = { id: number; title: string; state: keyof typeof states.task; agent: string; started: number; cost: number | null; est: boolean; pinned?: boolean; flash?: boolean };
const AGENTS = ['codex', 'claude', 'pipeline'];
const STATES = Object.keys(states.task) as Task['state'][];
const WORDS = ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Compare 3D methods', 'Investigate disk alert', 'Write release notes', 'Character hands rework', 'Migrate store layout'];
const mkTask = (id: number): Task => ({ id, title: `${WORDS[id % WORDS.length]} ${id % 97}`, state: STATES[id % 7], agent: AGENTS[id % 3], started: NOW - (id * 37 % 900) * min, cost: id % 11 === 0 ? null : (id % 130) / 10, est: id % 4 === 0 });

function ListDemo() {
  const [items, setItems] = useState(() => Array.from({ length: 10000 }, (_, i) => mkTask(10000 - i)));
  const [stream, setStream] = useState(false);
  const [query, setQuery] = useState('');
  const [chips, setChips] = useState<Chip[]>([{ field: 'Project', value: 'dashq' }]);
  const [saved, setSaved] = useState('My waiting');
  useEffect(() => {
    if (!stream) return;
    const t = setInterval(() => setItems(xs => [mkTask(xs[0].id + 1), ...xs]), 900);
    return () => clearInterval(t);
  }, [stream]);
  const insert = () => setItems(xs => [...Array.from({ length: 5 }, (_, i) => mkTask(xs[0].id + 5 - i)), ...xs]);
  const change = () => setItems(xs => xs.map((x, i) => i % 3 === 0 && i < 400 ? { ...x, cost: (x.cost ?? 0) + 0.25, flash: true } : x));
  const pin = (id: number) => setItems(xs => xs.map(x => x.id === id ? { ...x, pinned: !x.pinned } : x));
  const q = query.toLowerCase();
  const shown = (q ? items.filter(x => x.title.toLowerCase().includes(q) || `t-${x.id}`.includes(q)) : items);
  const ordered = [...shown.filter(x => x.pinned), ...shown.filter(x => !x.pinned)];
  return <div class="pane">
    <FilterBar query={query} onQuery={setQuery} placeholder="Search tasks" chips={chips}
      onRemove={c => setChips(cs => cs.filter(x => x.field !== c.field || x.value !== c.value))} onAdd={c => setChips(cs => [...cs, c])} onClear={() => setChips([])}
      fields={[{ field: 'State', values: STATES.map((s, i) => ({ value: states.task[s].label, count: 1400 - i * 90 })) }, { field: 'Agent', values: AGENTS.map((a, i) => ({ value: a, count: 3300 - i * 200 })) }, { field: 'Project', values: [{ value: 'dashq', count: 4100 }, { value: 'game', count: 3800 }, { value: 'fleet', count: 2100 }] }]}
      saved={['My waiting', 'Failed today', 'dashq open'].map(n => ({ name: n, pinned: true, active: n === saved }))} onSaved={setSaved}
      shown={ordered.length} total={items.length} />
    <VirtualList items={ordered} getKey={x => String(x.id)} rowHeight={ROW} follow="top" label="Tasks"
      columns="minmax(0,1fr) 4.5rem 1.25rem 4rem 2.75rem 4.5rem 1.75rem"
      header={<><span>Task</span><span>ID</span><span /><span>Agent</span><span class="r">Age</span><span class="r">Cost</span><span /></>}
      render={x => <>
        <span class="title">{x.title}</span>
        <span class="id svc">T-{x.id}</span>
        <StateBadge family="task" state={x.state} reason={x.state === 'waiting' ? 'acceptance by you' : x.state === 'failed' ? 'render step exit 1' : undefined} compact />
        <span class="svc">{x.agent}</span>
        <span class="r"><AgeChip t={x.started} now={NOW} alertAfter={x.state === 'in_progress' ? 2 * 3600 : undefined} /></span>
        <span class="r"><Num m={x.cost === null ? { value: null, basis: 'unavailable', reason: 'no meter' } : { value: x.cost, basis: x.est ? 'estimated' : 'measured' }} kind="money" rise="bad" /></span>
        <Button label={x.pinned ? 'Unpin' : 'Pin'} icon={x.pinned ? actions.unpin : actions.pin} iconOnly pressed={x.pinned} onClick={() => pin(x.id)} />
      </>} />
    <div class="bar">
      <Button label="Insert 5 above" icon="plus" onClick={insert} />
      <Button label={stream ? 'Stop stream' : 'Stream rows'} icon={stream ? 'pause' : 'activity'} pressed={stream} onClick={() => setStream(!stream)} />
      <Button label="Change costs" onClick={change} />
      <span class="muted type-sm">10,000 rows; only visible rows render. Scroll down, then insert: the row you read stays put.</span>
    </div>
  </div>;
}

const TREE: TreeNode[] = [
  { id: 'w', name: 'Worker · codex · windows', state: 'succeeded', time: { value: 540, basis: 'measured' }, tokens: { value: 120000, basis: 'measured' }, cost: { value: 0.3, basis: 'measured' }, children: [
    { id: 'w1', name: 'edit index.html', state: 'succeeded', time: { value: 120, basis: 'measured' } },
    { id: 'w2', name: 'run checks', note: 'reused from run 2', state: 'succeeded', time: { value: 60, basis: 'measured' } }] },
  { id: 'r', name: 'Reviewer · claude · mac', state: 'waiting', time: { value: 240, basis: 'measured' }, tokens: { value: 62000, basis: 'measured' }, cost: { value: 0.12, basis: 'estimated' }, children: [
    { id: 'r1', name: 'screenshot phone', state: 'running', lastSeen: NOW - 4 * min, time: { value: 40, basis: 'measured' } },
    { id: 'r2', name: 'question: accept?', state: 'waiting', time: { value: 720, basis: 'measured' } }] },
  { id: 'm', name: 'Merge · program', state: 'preparing', time: { value: null, basis: 'unavailable', reason: 'not started' }, children: [
    { id: 'm1', name: 'merge to main', state: 'preparing' }] },
];

function TranscriptDemo() {
  const [entries, setEntries] = useState<Entry[]>(() => [
    { id: '1', t: NOW - 10 * min, who: 'User', text: 'Rework the hero copy; keep it short.' },
    { id: '2', t: NOW - 10 * min + 3e3, call: { name: 'file_read index.html', state: 'completed', secs: 0 } },
    { id: '3', t: NOW - 9 * min, gap: 'Events 120–180 not received yet' },
    ...Array.from({ length: 40 }, (_, i): Entry => i % 2 ? { id: `a${i}`, t: NOW - (8 - i / 6) * min, call: { name: 'shell npm test', state: 'completed', secs: i % 9 } } : { id: `a${i}`, t: NOW - (8 - i / 6) * min, who: 'Assistant', text: `Step ${i} done. Checking the next file; long messages wrap and keep their measured height${i % 4 === 0 ? ' across several lines when the content needs more space than one row offers in this narrow frame.' : '.'}` }),
  ]);
  const [on, setOn] = useState(false);
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setEntries(es => [...es, { id: `n${es.length}`, t: Date.now(), who: 'Assistant', text: `Streamed entry ${es.length}. API key [masked] not used.` }]), 700);
    return () => clearInterval(t);
  }, [on]);
  return <div class="pane"><Transcript entries={entries} label="Transcript" />
    <div class="bar"><Button label={on ? 'Stop' : 'Stream'} icon={on ? 'pause' : 'activity'} pressed={on} onClick={() => setOn(!on)} /><span class="muted type-sm">At the bottom it follows; scroll up and it holds still.</span></div></div>;
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
  ['button', 'Button', 'button', () => <ButtonsDemo />],
  ['state-badge', 'State badge', 'state-badge', () => <BadgesDemo />, <>Left: icon and word, for detail views. Right: icon only, the form lists use (B1); hover or focus for the name and reason.</>],
  ['number', 'Numbers, totals, age chips', 'number', () => <NumbersDemo />, <>Measured, estimated (≈), unavailable (—), partial (≥). "Change values" shows the brief tint (OR-6): cost rising tints red, tokens neutral. The 2h chip is red (OR-18).</>],
  ['fill-bar', 'Fill bar', 'fill-bar', () => <FillDemo />],
  ['data-list', 'Task list: filter bar, search, saved filters, virtual list', 'data-list', () => <ListDemo />, <>Filters expand inline, never in a popup (OR-14). Saved filters are pinned chips (OR-12). The new-rows pill has no arrow (OR-7). Title takes the space; service fields sit in one compact zone on the right (OR-21). State is an icon; its name and reason appear in a delayed tooltip (B1, OR-20). Pin is always visible (B2).</>],
  ['execution-tree', 'Execution tree', 'execution-tree', () => <ExecTree nodes={TREE} now={NOW} label="Executions" />],
  ['transcript-viewer', 'Live transcript', 'transcript-viewer', () => <TranscriptDemo />],
  ['decision-block', 'Decision block', 'decision-block', () => <DecisionDemo />, <>Click Accept or press a while the block has focus: the block collapses to the answered line.</>],
  ['result-viewer', 'Result viewer shell', 'result-viewer', () => <ResultDemo />],
  ['setting-field', 'Setting field', 'setting-field', () => <SettingsDemo />, <>Click a value to edit it inline.</>],
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

render(<Book />, document.getElementById('app')!);
