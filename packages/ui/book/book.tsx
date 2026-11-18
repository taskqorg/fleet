// Design book: the live demo page of packages/ui. Built by scripts/build-book.mjs into one HTML file.
import { render, type ComponentChildren } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import {
  Choice, DraftBar, StepList, FlowStrip, TimeStrip,
  Icon, Button, StateBadge, Num, CostFigures, Totals, AgeChip, FillBar, AreaChart, ExecTree, DecisionBlock, ResultViewer, SplitButton, ActionMenu, NavRail, Spotlight, sectionProvider, spotlightMemory, FocusPresets, StatStrip,
  TextField, SettingField, ProblemRow, TooltipLayer, EntityCardLayer, Skeleton, Empty, ErrorBanner, Stale, Thread, TaskListView, workspaces, states, actions, GroupControl, SortControl, SelectToggle, SelectBar,
  Composer, DropZone, TextRow, type ComposerAttachment, CodeEditor, DiffText, OfflineMarker, ProjectPick, PresetPicker, HarnessCounts, HarnessMark, StageChip, OutcomeBlock, DetailSection, KvRow, stages, StateCounters, ListFrame, RowCheck,
  type ThreadBlock, type TreeNode, type Family, type Stage, type SpotlightProvider, type SpotlightItem,
} from '../src/index.ts';
import { EntityCardDemo, CardsProvider } from './entity-cards.tsx';
import { NAV, NAV_FOCUS } from './nav.ts';
import { FocusDemo } from './focus-demo.tsx';
import { NOW, min, hr, tasks, mkTask, TREE, transcript, hosts, type Task } from './fixtures.ts';

const BOOK_TREE: TreeNode[] = [
  { ...TREE[0], cost: undefined, costFigures: [{ value: 0.3, currency: 'USD', basis: 'measured' }, { value: 0.18, currency: 'USD', basis: 'subscription' }], children: [
    ...(TREE[0].children ?? []),
    { id: 'wt', name: 'Tool calls', note: '14 calls', state: 'succeeded', time: { value: 18, basis: 'measured' }, children: [
      { id: 'wt1', name: 'web.search', note: '14×', state: 'succeeded', time: { value: 18, basis: 'measured' }, costFigures: [{ value: 0.04, currency: 'USD', basis: 'estimated' }] }] }] },
  ...TREE.slice(1),
];

const bookTranscript = (): ThreadBlock[] => {
  const [head, ...rest] = transcript();
  return [
    head.kind === 'boundary' ? { ...head, resources: { executions: 1, tokens: { input: 14000, output: 4200, cacheRead: 0, cacheWrite: 0, reasoning: 0, total: 18200, basis: 'measured' },
      durations: { elapsedMs: 600000, executionWallMs: 540000, activeMs: 420000, apiMs: 280000, cpuMs: 9200, measuredCpuRuns: 1, totalRuns: 1, basis: 'measured' },
      peakMemoryBytes: { value: 268435456, basis: 'measured' },
      tools: { totalCalls: 6, failedCalls: 0, turns: 4, byTool: [{ name: 'Bash', calls: 4, errors: 0 }], basis: 'measured' },
      cost: [{ value: 0.42, currency: 'USD', basis: 'subscription', note: 'Claude Max' }] } } : head,
    ...rest,
  ];
};


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
      <Num m={{ value: 0.42, basis: 'subscription', reason: 'Claude Max' }} kind="money" />
      <Num m={{ value: null, basis: 'unavailable', reason: 'no gateway' }} kind="money" />
      <Num m={{ value: tok, basis: 'measured', partial: '2 of 3 measured' }} kind="count" rise="neutral" />
      <CostFigures cost={[{ value: 1.2, currency: 'USD', basis: 'measured' }, { value: 0.42, currency: 'USD', basis: 'subscription', note: 'Claude Max' }]} />
      <Button label="Change values" onClick={() => { setCost(c => +(c + 0.37).toFixed(2)); setTok(t => t + 4100); }} />
    </div>
    <Totals items={[
      { label: 'Time', m: { value: 860, basis: 'measured' }, kind: 'duration' },
      { label: 'Tokens', m: { value: tok, basis: 'measured', partial: '2 of 3 measured' }, kind: 'count', note: '2 of 3 measured' },
      { label: 'Cost', cost: [{ value: 0.42, currency: 'USD', basis: 'estimated', reason: 'tokens' }, { value: 0.38, currency: 'USD', basis: 'subscription' }], note: 'metered + subscription' },
      { label: 'API calls', m: { value: null, basis: 'unavailable', reason: 'no gateway' }, kind: 'count', note: 'no gateway' },
    ]} />
    <div class="row">
      <span class="muted type-sm">Age chips:</span>
      <AgeChip t={NOW - 40e3} /><AgeChip t={NOW - 4 * min} /><AgeChip t={NOW - 2 * hr} alertAfter={3600} />
      <AgeChip t={NOW - 3 * 24 * hr} /><AgeChip t={NOW - 40 * 24 * hr} />
    </div>
  </>;
}

/** A pool's left over two weeks: recorded to now, projected after it with a step at a member's reset. */
function AreaDemo() {
  const [bucket, setBucket] = useState<string | null>('d2');
  const d = 24 * hr, from = NOW - 7 * d, to = NOW + 7 * d;
  const rec = [{ at: from, v: 180 }, { at: NOW - 5 * d, v: 120 }, { at: NOW - 4 * d, v: 200 }, { at: NOW - d, v: 110 }, { at: NOW, v: 90 }];
  const proj = [{ at: NOW, v: 90 }, { at: NOW + 2 * d, v: 20 }, { at: NOW + 2 * d, v: 120 }, { at: NOW + 6 * d, v: 40 }];
  const buckets = [
    { key: 'd1', label: 'D-2', valueLabel: '120k tokens' },
    { key: 'd2', label: 'D-1', valueLabel: '182k tokens' },
    { key: 'd3', label: 'Today', valueLabel: '90k tokens' },
  ];
  return <div class="col">
    <AreaChart label="codex weekly left" from={from} to={to} now={NOW} max={200} value={v => `${Math.round(v / 2)}%`}
      series={[{ label: 'recorded', points: rec, tone: 'd1' }, { label: 'projected', points: proj, tone: 'd1', projected: true }]}
      buckets={buckets} selectedBucket={bucket} onSelectBucket={k => setBucket(b => b === k ? null : k)} />
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
    <FillBar total={1000 * G} segments={[{ label: 'cs', bytes: 400 * G }, { label: 'fleet', bytes: 220 * G }]} warnAt={50 * G} />
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

const BOOK_GROUPS = [['priority', 'Priority'], ['state', 'State'], ['project', 'Project'], ['agent', 'Agent'], ['host', 'Host'], ['none', 'None']] as const;
const BOOK_SORTS = [['changed', 'Changed'], ['created', 'Created'], ['title', 'Title', ['Z to A', 'A to Z'], 'value']] as const;
function GroupSortDemo() {
  const [group, setGroup] = useState<typeof BOOK_GROUPS[number][0]>('priority');
  const [sort, setSort] = useState('-changed');
  const [sel, setSel] = useState(false);
  return <div class="stack">
    <div class="fbar-line"><SelectToggle on={sel} onClick={() => setSel(!sel)} /><GroupControl value={group} options={BOOK_GROUPS} onChange={setGroup} /><SortControl value={sort} fields={BOOK_SORTS} onChange={setSort} /></div>
    {sel && <div class="fbar-line"><SelectBar count={2} total={8} onAll={() => {}} onNone={() => {}} onExit={() => setSel(false)} actions={[
      { label: 'Pin', icon: 'pin', onClick: () => {} }, { label: 'Pause', icon: 'pause', onClick: () => {} }, { label: 'Cancel', icon: 'circle-slash', danger: true, onClick: () => {} }]} /></div>}
    <span class="muted type-sm">Value: group={group}, sort={sort}</span>
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
  const [blocks, setBlocks] = useState<ThreadBlock[]>(bookTranscript);
  const [on, setOn] = useState(false);
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setBlocks(bs => [...bs, { kind: 'text', key: `${1000 + bs.length}:0`, role: 'agent', harness: 'codex', model: 'gpt-6.1-sol', effort: 'low', markdown: `Streamed reply ${bs.length}.` }]), 700);
    return () => clearInterval(t);
  }, [on]);
  return <div class="pane"><Thread blocks={blocks} label="Transcript" now={Date.now()} onStop={() => setOn(false)} onAnswer={() => {}} />
    <div class="bar"><Button label={on ? 'Stop' : 'Stream'} icon={on ? 'pause' : 'activity'} pressed={on} onClick={() => setOn(!on)} /><span class="muted type-sm">Tool and reasoning rows open downward. At the bottom it follows; scroll up and it holds still.</span></div></div>;
}

// Spotlight demo kinds (controls/spotlight.md): invented hits with states, ages and projects; the focus is "fleet".
const SPOT_HITS: { kind: 'tasks' | 'conversations' | 'projects'; family?: Family; state?: string; label: string; code?: string; project: string; ago: number; snippet?: string }[] = [
  { kind: 'tasks', family: 'task', state: 'in_progress', label: 'Deploy pipeline flaky on the release step', code: 'T-FLT-42', project: 'fleet', ago: 4 * min },
  { kind: 'tasks', family: 'task', state: 'failed', label: 'Deploy docs to the site', code: 'T-FLT-39', project: 'fleet', ago: 2 * hr },
  { kind: 'tasks', family: 'task', state: 'completed', label: 'Deploy preview builds for every pull request', code: 'T-WEB-7', project: 'website', ago: 30 * hr },
  { kind: 'conversations', family: 'session', state: 'waiting', label: 'Plan the deploy window with the PM', code: 'C-FLT-12', project: 'fleet', ago: 15 * min },
  // Found by a word deep in a long first message: the title is the instruction's start, the snippet shows the match.
  { kind: 'conversations', family: 'session', state: 'idle', label: 'You are a worker agent. Read AGENTS.md and every contract on the path before you start', code: 'C-DSG-3', project: 'design-system',
    ago: 3 * hr, snippet: '…Task: deploy the book to the review site and check its links before you start…' },
  { kind: 'projects', label: 'Website', project: 'website', ago: 0 },
];
function spotDemo(focus: string[], onFocus: (f: string[]) => void): SpotlightProvider[] {
  return (['tasks', 'conversations', 'projects'] as const).map(kind => ({ id: kind, label: workspaces.find(w => w.id === kind)!.label, find: (q: string) => {
    const items: SpotlightItem[] = SPOT_HITS.filter(h => h.kind === kind && `${h.label} ${h.code ?? ''} ${h.snippet ?? ''}`.toLowerCase().includes(q.toLowerCase().split(' ')[0])).map(h => ({
      key: kind + h.label, icon: workspaces.find(w => w.id === kind)!.icon, label: h.label, code: h.code, sub: kind === 'projects' ? undefined : h.project,
      state: h.family && h.state ? { family: h.family, state: h.state } : undefined, at: h.ago ? NOW - h.ago : undefined, outside: !!focus.length && !focus.includes(h.project), snippet: h.snippet, run: () => {},
      action: kind === 'projects' ? { label: focus.includes(h.project) ? 'Remove from focus' : 'Add to focus', icon: 'focus', pressed: focus.includes(h.project), run: () => onFocus(focus.includes(h.project) ? focus.filter(x => x !== h.project) : [...focus, h.project]) } : undefined }));
    if (kind === 'tasks' && items.length) items.push({ key: 'tasks:all', icon: 'list', label: 'All 24 in Tasks', more: true, run: () => {} });
    return items;
  } }));
}

function RailDemo() {
  const [cur, setCur] = useState('tasks');
  const [pinned, setPinned] = useState<string[]>(['tasks', 'hosts']);
  // Counters (nav-rail.md): without a focus, with a focus (in focus bright, outside dim) and collapsed (the in-focus badge only).
  return <div class="rail-demos">{([['No focus', NAV, false], ['Focus', NAV_FOCUS, false], ['Collapsed, focus', NAV_FOCUS, true]] as const).map(([t, items, mini]) =>
    <figure key={t} class="pane rail-demo"><figcaption class="muted type-sm">{t}</figcaption><NavRail items={items} pinned={pinned} current={cur} onGo={setCur} onPins={setPinned} mini={mini} /></figure>)}</div>;
}

/** The palette as the header's search icon opens it; the focus is "fleet", "assetq" is ignored. */
function SpotlightDemo() {
  const [spot, setSpot] = useState(false), [focus, setFocus] = useState<string[]>(['fleet']), [mem] = useState(spotlightMemory);
  const presets = [{ name: 'Fleet', projects: ['fleet'] }, { name: 'Web', projects: ['website'] }, { name: 'Muted', projects: ['assetq'] }];
  return <div class="pane"><Button label="Search ⌘K" icon="search" onClick={() => setSpot(true)} />
    {spot && <Spotlight providers={[sectionProvider(NAV, () => {}), ...spotDemo(focus, setFocus)]} memory={mem}
      idle={<FocusPresets icon value={focus} presets={presets} ignored={['assetq']} onChange={setFocus} />} onClose={() => setSpot(false)} />}</div>;
}

/** The focus chip as it sits on the right of the shell header: neutral with every project, toned with a focus. */
const HeaderDemo = ({ start }: { start: string[] }) => <div class="shell-h"><h1 class="shell-title">Tasks</h1><span class="grow" />
  <FocusDemo start={start} ignored={['assetq']} /><Button label="Search ⌘K" icon="search" iconOnly kind="quiet" /></div>;

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
    <StepList label="Conditional plan" items={[
      { id: 'prepare', kind: 'program', executor: 'local', reads: 'packet', writes: 'candidate', activation: 'selected', run: {state:'succeeded',time:{value:1,basis:'measured'},cost:u} },
      { id: 'review', kind: 'agent', executor: 'qualified checker', reads: 'candidate', writes: 'verdict', activation: 'not_selected', activationReason: 'condition_false' },
      { id: 'pending', kind: 'program', executor: 'local', reads: 'candidate', writes: 'result', activation: 'pending' },
      { id: 'blocked', kind: 'program', executor: 'local', reads: 'verdict', writes: 'result', activation: 'blocked', activationReason: 'source_missing' },
      { id: 'prepared', kind: 'agent', executor: 'checker', reads: 'candidate', writes: 'verdict', activation: 'selected', run: {state:'preparing',time:u,cost:u} },
      { id: 'reused', kind: 'program', executor: 'local', reads: 'packet', writes: 'candidate', activation: 'selected', run: {state:'succeeded',note:'reused · original resources',time:u,cost:u} },
      { id: 'unconfirmed', kind: 'tool', executor: 'provider', reads: 'question', writes: 'answer', activation: 'selected', run: {state:'failed',note:'unconfirmed',time:u,cost:u} },
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

// Start a conversation (controls/start-picker.md): the system presets on a project without roles, as /conversations/new shows them.
const START_PRESETS = [
  { id: 'discuss', name: 'Discuss', icon: 'messages-square', description: 'Talk an idea or a question through. The code does not change; tasks and pipelines can be started.', facts: ['Codex gpt-6.1-sol · low', 'Code read only', 'No branch'], access: 'read', branch: false },
  { id: 'design', name: 'Design', icon: 'panels-top-left', description: 'Design or change an interface on its own branch, from the design system.', facts: ['Codex gpt-6.1-sol · medium', 'Can edit', 'New branch', 'Starter instruction'], access: 'write', branch: true },
  { id: 'fix', name: 'Fix / build', icon: 'terminal', description: 'Fix a bug or build a feature on its own branch.', facts: ['Codex gpt-6.1-sol · medium', 'Can edit', 'New branch'], access: 'write', branch: true },
  { id: 'review', name: 'Review', icon: 'check-check', description: 'Review code or a change and report the findings. The code does not change.', facts: ['Claude sonnet', 'Code read only', 'No branch'], access: 'read', branch: false },
  { id: 'investigate', name: 'Investigate a problem', icon: 'search', description: 'Find the cause of a problem in errors, logs and code. The code does not change; tasks can be written.', facts: ['Codex gpt-6.1-sol · low', 'Code read only', 'No branch', 'Starter instruction'], access: 'read', branch: false },
] as const;
function StartDemo() {
  const [project, setProject] = useState<string | null>('cs'), [preset, setPreset] = useState('discuss');
  const p = START_PRESETS.find(x => x.id === preset)!, [read, setRead] = useState<boolean | null>(null), [branch, setBranch] = useState<boolean | null>(null);
  const access = read ?? p.access === 'read', br = branch ?? p.branch;
  return <div class="stack">
    <ProjectPick value={project} onChange={setProject} n={4} projects={[{ id: 'fleet', name: 'fleet' }, { id: 'cs', name: 'cyprus.bz' }, { id: 'desiign', name: 'desiign' }, { id: 'specq', name: 'specq' }, { id: 'pilotq', name: 'pilotq' }, { id: 'assetq', name: 'assetq' }]} />
    <PresetPicker value={preset} onChange={id => { setPreset(id); setRead(null); setBranch(null); }} presets={START_PRESETS.map(x => ({ ...x, facts: [...x.facts] }))} />
    {/* The start facts are the composer's chips, as a conversation shows its profile and permissions. */}
    <div class="pane new-conversation"><Composer onSend={() => {}} onAttach={() => {}} placeholder="Describe the work in cyprus.bz" chipsLabel="Start settings" chips={[
      { key: 'project', icon: 'folder-open', label: project ? 'cyprus.bz' : 'No project', tip: 'Project' },
      { key: 'agent', harness: p.id === 'review' ? 'claude' : 'codex', label: p.facts[0].replace(/^(Codex|Claude) /, ''), tip: 'Profile pm, a system profile' },
      { key: 'access', icon: 'shield', label: access ? 'Code read only' : 'Can edit', tip: access ? 'Click to let it edit' : 'Click for read only', onClick: () => setRead(!access) },
      { key: 'branch', icon: 'git-pull-request', label: br ? 'fleet-test/new' : 'No branch', tip: br ? 'A new branch from origin/main' : 'A detached copy of origin/main', onClick: () => setBranch(!br) },
    ]} /></div>
  </div>;
}

const SECTIONS: [string, string, string | undefined, () => ComponentChildren, ComponentChildren?][] = [
  ['tokens', 'Tokens', undefined, () => <TokensDemo />, <>Colour carries meaning only. Numbers and IDs use the monospace stack.</>],
  ['navigation', 'Navigation anchors', undefined, () => <NavDemo />, <>OR-3: one stable icon per workspace, from the registry.</>],
  ['nav-rail', 'Navigation rail with pinned sections', 'nav-rail', () => <RailDemo />, <>Hover a section and pin it: it moves to the pinned group at the top (D9). Counters count each section's counter view; with a focus they split into in focus and outside, collapsed only the in-focus badge stays. Search is the icon in the shell header (OR-31, OR-43).</>],
  ['spotlight', 'Spotlight palette', 'spotlight', () => <SpotlightDemo />, <>OR-32. Empty, it shows the focus presets (a preset of only ignored projects is not offered), recent items and the terms; type <code>deploy</code> for state icons, ages, the results outside the focus and the "All N" row. Close and reopen: the query and the highlighted row stay.</>],
  ['focus-control', 'Focus chip in the shell header', 'focus-control', () => <div class="stack"><HeaderDemo start={[]} /><HeaderDemo start={['fleet', 'desiign', 'cs']} /></div>, <>OR-34, OR-43. Neutral with every project; toned with a focus, names two projects then +N. The bell counts what needs you outside the focus. The panel floats under the chip: presets, projects with an ignore action, and the folded Ignored section to restore them.</>],
  ['split-button', 'Split button', 'split-button', () => <SplitDemo />, <>Click runs the default action; the chevron opens a short menu of alternatives for the same place (allowed by OR-14).</>],
  ['button', 'Button', 'button', () => <ButtonsDemo />],
  ['entity-card', 'Entity reference and card', 'entity-card', () => <EntityCardDemo />, <>D17, OR-38. Click, tap, Enter or Space on a code opens its card; Escape, a click outside or the code again closes it. Every kind, a card still loading and one the person may not see; D17 codes inside an agent message are references too.</>],
  ['state-badge', 'State badge', 'state-badge', () => <BadgesDemo />, <>Left: icon and word, for detail views. Right: icon only, the form lists use (B1); hover or focus for the name and reason.</>],
  ['number', 'Numbers, totals, age chips', 'number', () => <NumbersDemo />, <>Measured, estimated (≈), unavailable (—), partial (≥). "Change values" shows the brief tint (OR-6): cost rising tints red, tokens neutral. The 2h chip is red (OR-18).</>],
  ['fill-bar', 'Fill bar', 'fill-bar', () => <FillDemo />],
  ['time-strip', 'Time strip', 'time-strip', () => <TimeStripDemo />, <>Runners: where each run sits in the last 24 hours, toned by its state; a running run reaches now.</>],
  ['area-chart', 'Area chart', 'area-chart', () => <AreaDemo />, <>Limits (people access §8): recorded area to now, projected area after it (lighter), a step at each member reset; day ticks and the now line. Left in percent of capacity, or burn per hour.</>],
  ['lifecycle', 'Harness marks, delivery stages, outcome block, detail sections', 'harness-counts', () => <LifecycleDemo />, <>D11: agent mark only while a task runs; delivery stage chip; harness counts per host omit zero; outcome block above task tabs (B2); stacked detail sections (C3).</>],
  ['stat-strip', 'Stat strip', 'stat-strip', () => <StatsDemo />, <>Operational counts: what needs you, failures, running against slots, workers per host. An offline host shows its last known count as a lower bound.</>],
  ['list-pending', 'Updating list: pending counters and rows, row checks', 'row-check', () => <PendingDemo />, <>A filter change keeps the last rows and counts on screen, dimmed under a thin bar, until the new answer replaces them in place. Checked rows are the view URL's sel.</>],
  ['group-sort', 'Group and sort', 'filter-bar', () => <GroupSortDemo />, <>OR-35. Group: one chip with the icon and the grouping; its choices in a short menu, the cross removes the grouping. Sort: the direction icon first, which flips in one click, then the field (menu of fields and directions). The square at the left starts selection; the selection bar gets its own line.</>],
  ['data-list', 'Task list view: views, state counters, grouping, filters, virtual list', 'data-list', () => <ListDemo />, <>Saved views per person (pin, unpin, Save view). State counters show totals and filter on click. Group by state or project; groups collapse. Filters expand inline (OR-14). Title takes the space; service fields sit on the right (OR-21). State is an icon with a delayed tooltip (B1, OR-20).</>],
  ['execution-tree', 'Execution tree', 'execution-tree', () => <ExecTree nodes={BOOK_TREE} now={NOW} label="Executions" />],
  ['step-list', 'Step list and flow strip', 'step-list', () => <StepsDemo />, <>D20: a pipeline is edited in the list; the picture above is drawn from it and is read-only. Click a step to open it in place. The second list shows a run on top of the same rows.</>],
  ['draft-bar', 'Draft bar and choice', 'draft-bar', () => <DraftDemo />, <>A definition that differs from its saved revision says so, and saving is a commit. A choice picks one or several of a few fixed options.</>],
  ['transcript-viewer', 'Live transcript', 'transcript-viewer', () => <TranscriptDemo />],
  ['decision-block', 'Decision block', 'decision-block', () => <DecisionDemo />, <>Click Accept or press a while the block has focus: the block collapses to the answered line.</>],
  ['result-viewer', 'Result viewer shell', 'result-viewer', () => <ResultDemo />],
  ['text-field', 'Text field', 'text-field', () => <DraftFieldDemo />, <>Draft values stay local until the form is submitted.</>],
  ['setting-field', 'Setting field', 'setting-field', () => <SettingsDemo />, <>Click a value to edit it inline.</>],
  ['start-picker', 'Start a conversation: project, preset, start facts', 'preset-pick', () => <StartDemo />, <>/conversations/new. Focused and recent projects first, No project is one choice, search finds the rest (OR-9). A preset card says what it sets in words; the chips under the composer show the project, agent, access and branch, and access and branch flip in one click.</>],
  ['composer', 'Composer: idle, with files, running', 'composer', () => <ComposerDemo />, <>One box: the files above the text, then + (attach), the agent profile (opens Switch profile or model), the permissions and one Send button, Stop while the agent runs and the box is empty. Enter sends, Shift+Enter adds a line; files also come by paste (screenshots, files, long text as a .txt) and by drop anywhere over the conversation. Send waits for uploads. A sent message shows its files: images as thumbnails, other files as chips; each opens in the result viewer.</>],
  ['code-editor', 'Code editor and diff text', 'code-editor', () => <div class="pane"><CodeEditor label="config/pipelines/search-basic.json" value={'{\n  "id": "search-basic",\n  "steps": [],\n  "version": 2\n}\n'} onChange={() => {}} problems={[{ path: '', message: 'Invalid pipeline: steps' }]} /><DiffText label="Draft against revision 3" text={'@@ -1,4 +1,4 @@\n {\n-  "id": "hello"\n+  "id": "search-basic"\n }'} /></div>, <>A definition's text is edited in place and kept as a draft; problems come from the coordinator and do not block typing.</>],
  ['offline-marker', 'Offline marker', 'offline-marker', () => <div class="row"><OfflineMarker state="updating" since={NOW - 18 * hr} now={NOW} /><OfflineMarker state="offline" since={NOW - 18 * hr} now={NOW} /><OfflineMarker state="signed-out" now={NOW} /></div>, <>Stored data is never shown as current (D3).</>],
  ['problem-row', 'Problem row', 'problem-row', () => <ProblemDemo />],
  ['status-states', 'Loading, empty, error, stale', 'status-states', () => <StatesDemo />],
];

const BOOK_DOT = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR4nGN4VqFBEmIY1TCqYfhqAAAUGoYQFRF6QAAAAABJRU5ErkJggg==';
function ComposerDemo() {
  const chips = [{ harness: 'claude', label: 'opus-5.5 · high', tip: 'Profile claude-worker · switch profile or model', onClick: () => {} }, { icon: 'shield' as const, label: 'Workspace', tip: 'Permissions of the profile: the harness tools, no git network' }];
  const [files, setFiles] = useState<ComposerAttachment[]>([
    { id: 'a', name: 'screenshot.png', size: 48_210, mediaType: 'image/png', state: 'ready', preview: BOOK_DOT },
    { id: 'b', name: 'build-log.txt', size: 2_400_000, mediaType: 'text/plain', state: 'uploading', progress: 0.42 },
  ]);
  return <div class="col">
    <div class="pane"><Composer onSend={() => {}} onAttach={() => {}} chips={chips} /></div>
    <div class="pane"><DropZone onFiles={() => {}}><Composer onSend={() => {}} onAttach={() => {}} chips={chips} attachments={files} onRemoveAttachment={id => setFiles(f => f.filter(x => x.id !== id))} /></DropZone></div>
    <div class="pane"><Composer placeholder="Steer" onSend={() => {}} onAttach={() => {}} chips={chips} onStop={() => {}} /></div>
    <div class="pane"><TextRow who="operator" by="owner@fleet" markdown="Quote the word in the note and name the colour of the dot." at={NOW - 4 * 60_000} now={NOW}
      attachments={[{ id: 'a', name: 'dot.png', size: 210, mediaType: 'image/png' }, { id: 'b', name: 'note.txt', size: 31, mediaType: 'text/plain' }]} src={() => BOOK_DOT} /></div>
  </div>;
}

function Book() {
  return <CardsProvider><div class="page">
    <header class="bk-head">
      <h1>Fleet design book</h1>
      <p class="muted">Live demo of <span class="mono">packages/ui</span>, light and dark side by side. Rules: owner-rules.md.</p>
      <span class="grow" /><ThemeButton />
    </header>
    <nav class="toc" aria-label="Contents">{SECTIONS.map(s => <a key={s[0]} href={`#${s[0]}`}>{s[1]}</a>)}</nav>
    {SECTIONS.map(([id, title, spec, demo, note]) => <Section key={id} id={id} title={title} spec={spec} note={note}>{demo}</Section>)}
    <TooltipLayer />
    <EntityCardLayer />
  </div></CardsProvider>;
}

const app = document.getElementById('app');
if (app) render(<Book />, app);
