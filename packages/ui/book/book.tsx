// Design book: the live demo page of packages/ui. Built by scripts/build-book.mjs into one HTML file.
import { render, type ComponentChildren } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import {
  Choice, DraftBar, StepList, FlowStrip, TimeStrip,
  Icon, Button, StateBadge, Num, Totals, AgeChip, FillBar, AreaChart, ExecTree, DecisionBlock, ResultViewer, SplitButton, ActionMenu, NavRail, StatStrip,
  TextField, SettingField, ProblemRow, TooltipLayer, Skeleton, Empty, ErrorBanner, Stale, Thread, TaskListView, workspaces, states, actions,
  Composer, CodeEditor, DiffText, OfflineMarker, HarnessCounts, HarnessMark, StageChip, OutcomeBlock, DetailSection, KvRow, stages, StateCounters, ListFrame, RowCheck,
  type ThreadBlock, type Family, type Stage,
} from '../src/index.ts';
import { NAV } from './nav.ts';
import { NOW, min, hr, tasks, mkTask, TREE, transcript, hosts, type Task } from './fixtures.ts';


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

/** A pool's left over two weeks: recorded to now, projected after it with a step at a member's reset. */
function AreaDemo() {
  const d = 24 * hr, from = NOW - 7 * d, to = NOW + 7 * d;
  const rec = [{ at: from, v: 180 }, { at: NOW - 5 * d, v: 120 }, { at: NOW - 4 * d, v: 200 }, { at: NOW - d, v: 110 }, { at: NOW, v: 90 }];
  const proj = [{ at: NOW, v: 90 }, { at: NOW + 2 * d, v: 20 }, { at: NOW + 2 * d, v: 120 }, { at: NOW + 6 * d, v: 40 }];
  return <div class="col">
    <AreaChart label="codex weekly left" from={from} to={to} now={NOW} max={200} value={v => `${Math.round(v / 2)}%`}
      series={[{ label: 'recorded', points: rec, tone: 'd1' }, { label: 'projected', points: proj, tone: 'd1', projected: true }]} />
    <AreaChart label="codex weekly burn" from={from} to={to} now={NOW} max={4} value={v => `${v.toFixed(1)}/h`}
      series={[{ label: 'burn', points: [{ at: from, v: 1.2 }, { at: NOW - 3 * d, v: 3.1 }, { at: NOW, v: 2.0 }], tone: 'd2', step: true },
        { label: 'burn', points: [{ at: NOW, v: 2.0 }, { at: to, v: 2.0 }], tone: 'd2', projected: true, step: true }]} />
  </div>;
}

/** Runs on a 24-hour strip (Runners): an ended run, a failed one, one still running to now. */
function TimeStripDemo() {
  const from = NOW - 24 * hr;
  return <div class="col" style={{ maxWidth: '20rem' }}>
    <TimeStrip from={from} to={NOW} start={NOW - 20 * hr} end={NOW - 17 * hr} now={NOW} state="succeeded" label="succeeded, 3h" />
    <TimeStrip from={from} to={NOW} start={NOW - 9 * hr} end={NOW - 8.5 * hr} now={NOW} state="failed" label="failed, 30m" />
    <TimeStrip from={from} to={NOW} start={NOW - 2 * hr} end={null} now={NOW} state="running" label="running for 2h" />
  </div>;
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
  const [blocks, setBlocks] = useState<ThreadBlock[]>(transcript);
  const [on, setOn] = useState(false);
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setBlocks(bs => [...bs, { kind: 'text', key: `${1000 + bs.length}:0`, role: 'agent', harness: 'codex', model: 'gpt-6.1-sol', effort: 'low', markdown: `Streamed reply ${bs.length}.` }]), 700);
    return () => clearInterval(t);
  }, [on]);
  return <div class="pane"><Thread blocks={blocks} label="Transcript" now={Date.now()} onStop={() => setOn(false)} onAnswer={() => {}} />
    <div class="bar"><Button label={on ? 'Stop' : 'Stream'} icon={on ? 'pause' : 'activity'} pressed={on} onClick={() => setOn(!on)} /><span class="muted type-sm">Tool and reasoning rows open downward. At the bottom it follows; scroll up and it holds still.</span></div></div>;
}

function RailDemo() {
  const [cur, setCur] = useState('tasks');
  const [pinned, setPinned] = useState<string[]>(['tasks', 'hosts']);
  return <div class="pane rail-demo"><NavRail items={NAV} pinned={pinned} current={cur} onGo={setCur} onPins={setPinned} /></div>;
}

function SplitDemo() {
  const [last, setLast] = useState('');
  return <div class="row">
    <SplitButton kind="primary" main={{ label: 'Retry', icon: 'rotate-ccw', onClick: () => setLast('Retry') }}
      alternatives={[{ label: 'Retry on another host', icon: 'panels-top-left', onClick: () => setLast('Retry on another host') }, { label: 'Continue on codex', icon: 'bot', onClick: () => setLast('Continue on codex') }, { label: 'Full rerun', icon: 'rotate-cw', onClick: () => setLast('Full rerun') }]} />
    <SplitButton main={{ label: 'Accept', icon: 'check' }} alternatives={[{ label: 'Accept and merge', icon: 'git-merge' }, { label: 'Accept without publishing', icon: 'save' }]} />
    <ActionMenu label="Actions" items={[{ label: 'Retry last message', icon: 'rotate-ccw', onClick: () => setLast('Retry last message') }, { label: 'Switch profile or model', icon: 'sliders-horizontal', onClick: () => setLast('Switch') }, { label: 'Hand off to another agent', icon: 'bot', onClick: () => setLast('Hand off') }, { label: 'Fork', icon: 'git-merge', onClick: () => setLast('Fork') }]} />
    {last && <span class="muted type-sm">ran: {last}</span>}
  </div>;
}

function LifecycleDemo() {
  return <div class="col">
    <div class="row"><span class="muted type-sm">Running on:</span><HarnessMark h="codex" /><HarnessMark h="claude" /><HarnessMark h="antigravity" />
      <span class="muted type-sm">Per host:</span><HarnessCounts counts={{ codex: 4, claude: 2, antigravity: 0 }} label="pds18 workers by harness" /></div>
    <div class="row"><span class="muted type-sm">Delivery stage:</span>{(Object.keys(stages) as Stage[]).map(s => <StageChip key={s} stage={s} />)}</div>
    <OutcomeBlock outcome="Release notes for 0.4 in releases/0.4.md, reviewed, linked from the changelog."
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

function DraftFieldDemo() {
  const [value, setValue] = useState('Compare the existing pipeline variants');
  return <div class="col"><TextField label="Goal" value={value} onChange={setValue} help="Unsaved draft; creation is separate." />
    <TextField label="Hypothesis" multiline value="" onChange={() => {}} error="Describe what this experiment will test before launch." />
    <TextField label="Repeats" type="number" value="1" onChange={() => {}} disabledReason="Creation is pending" /></div>;
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

function StepsDemo() {
  const [open, setOpen] = useState<string | undefined>('review');
  const u = { value: null, basis: 'unavailable' as const, reason: 'not started' };
  return <div class="stack">
    <FlowStrip label="plan, then a loop of build and two parallel checks, then deliver" nodes={[{ id: 'plan', kind: 'agent' }, { loop: 'until pass · max 3', nodes: [{ id: 'build', kind: 'agent' }, { parallel: [{ id: 'checks', kind: 'program' }, { id: 'review', kind: 'agent', current: open === 'review' }] }] }, { id: 'deliver', kind: 'tool' }]} />
    <StepList label="Steps" open={open} onOpen={s => setOpen(open === s ? undefined : s)} items={[
      { id: 'plan', kind: 'agent', executor: <b>claude-worker</b>, reads: 'task', writes: 'plan.md' },
      { band: 'loop', label: 'Repeat until the review passes · at most 3 rounds' },
      { id: 'build', kind: 'agent', executor: <b>claude-worker</b>, reads: 'plan.md, review', writes: 'change', inset: true },
      { band: 'parallel', label: 'In parallel', inset: true },
      { id: 'checks', kind: 'program', executor: <b>pnpm test</b>, reads: 'change', writes: 'checks.md', inset: true },
      { id: 'review', kind: 'agent', executor: <b>claude-reviewer</b>, reads: 'change, plan.md', writes: 'review, verdict', inset: true },
      { id: 'deliver', kind: 'tool', executor: <b>session_deliver</b>, reads: 'change', writes: 'pull request' },
    ]}><KvRow icon="bot" label="Agent" meta="claude-reviewer" /><KvRow icon="rotate-cw" label="Changes requested" meta="back to build" /></StepList>
    <StepList label="Steps of a run" items={[
      { id: 'prepare', kind: 'program', executor: <b>prepare.sh</b>, reads: 'batch', writes: 'inputs/', run: { state: 'succeeded', note: 'reused', time: { value: 0, basis: 'measured' }, cost: u } },
      { id: 'render', kind: 'tool', executor: <b>render_asset</b>, reads: 'inputs/', writes: 'renders/', run: { state: 'failed', note: '7 of 8', time: { value: 372, basis: 'measured' }, cost: { value: 3.4, basis: 'estimated', reason: 'tokens' }, error: 'asset-07 · exit 1 · CUDA out of memory' } },
      { id: 'assemble', kind: 'agent', executor: <b>codex-designer</b>, reads: 'renders/', writes: 'page', run: { state: 'waiting', note: 'waits for render', time: u, cost: u } },
    ]} />
  </div>;
}
function DraftDemo() {
  const [v, setV] = useState('reviewer');
  const [many, setMany] = useState(['Read', 'Grep']);
  return <div class="stack">
    <DraftBar changes={2} base="4e5bf83" note="New executions keep using the committed revision." actions={[{ label: 'Test', icon: 'play' }, { label: 'Diff' }, { label: 'Save as commit', icon: 'save', primary: true }]} />
    <Choice label="Role" value={v} onChange={setV} options={['PM', 'worker', 'reviewer', 'judge'].map(r => ({ id: r, label: r }))} />
    <Choice label="Tools" value={many} onChange={t => setMany(many.includes(t) ? many.filter(x => x !== t) : [...many, t])} options={['Read', 'Glob', 'Grep', 'Edit', 'Write'].map(t => ({ id: t, label: t }))} />
  </div>;
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
  ['time-strip', 'Time strip', 'time-strip', () => <TimeStripDemo />, <>Runners: where each run sits in the last 24 hours, toned by its state; a running run reaches now.</>],
  ['area-chart', 'Area chart', 'area-chart', () => <AreaDemo />, <>Limits (people access §8): recorded area to now, projected area after it (lighter), a step at each member reset; day ticks and the now line. Left in percent of capacity, or burn per hour.</>],
  ['lifecycle', 'Harness marks, delivery stages, outcome block, detail sections', 'harness-counts', () => <LifecycleDemo />, <>D11: agent mark only while a task runs; delivery stage chip; harness counts per host omit zero; outcome block above task tabs (B2); stacked detail sections (C3).</>],
  ['stat-strip', 'Stat strip', 'stat-strip', () => <StatsDemo />, <>Operational counts: what needs you, failures, running against slots, workers per host. An offline host shows its last known count as a lower bound.</>],
  ['list-pending', 'Updating list: pending counters and rows, row checks', 'row-check', () => <PendingDemo />, <>A filter change keeps the last rows and counts on screen, dimmed under a thin bar, until the new answer replaces them in place. Checked rows are the view URL's sel.</>],
  ['data-list', 'Task list view: views, state counters, grouping, filters, virtual list', 'data-list', () => <ListDemo />, <>Saved views per person (pin, unpin, Save view). State counters show totals and filter on click. Group by state or project; groups collapse. Filters expand inline (OR-14). Title takes the space; service fields sit on the right (OR-21). State is an icon with a delayed tooltip (B1, OR-20).</>],
  ['execution-tree', 'Execution tree', 'execution-tree', () => <ExecTree nodes={TREE} now={NOW} label="Executions" />],
  ['step-list', 'Step list and flow strip', 'step-list', () => <StepsDemo />, <>D20: a pipeline is edited in the list; the picture above is drawn from it and is read-only. Click a step to open it in place. The second list shows a run on top of the same rows.</>],
  ['draft-bar', 'Draft bar and choice', 'draft-bar', () => <DraftDemo />, <>A definition that differs from its saved revision says so, and saving is a commit. A choice picks one or several of a few fixed options.</>],
  ['transcript-viewer', 'Live transcript', 'transcript-viewer', () => <TranscriptDemo />],
  ['decision-block', 'Decision block', 'decision-block', () => <DecisionDemo />, <>Click Accept or press a while the block has focus: the block collapses to the answered line.</>],
  ['result-viewer', 'Result viewer shell', 'result-viewer', () => <ResultDemo />],
  ['text-field', 'Text field', 'text-field', () => <DraftFieldDemo />, <>Draft values stay local until the form is submitted.</>],
  ['setting-field', 'Setting field', 'setting-field', () => <SettingsDemo />, <>Click a value to edit it inline.</>],
  ['composer', 'Composer', 'composer', () => <div class="pane"><Composer draftKey="book" onSend={() => {}} /></div>, <>Enter sends, Shift+Enter adds a line; the draft is kept per conversation.</>],
  ['code-editor', 'Code editor and diff text', 'code-editor', () => <div class="pane"><CodeEditor label="config/pipelines/hello-search.json" value={'{\n  "id": "hello-search",\n  "steps": [],\n  "version": 2\n}\n'} onChange={() => {}} problems={[{ path: '', message: 'Invalid pipeline: steps' }]} /><DiffText label="Draft against revision 3" text={'@@ -1,4 +1,4 @@\n {\n-  "id": "hello"\n+  "id": "hello-search"\n }'} /></div>, <>A definition's text is edited in place and kept as a draft; problems come from the coordinator and do not block typing.</>],
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
