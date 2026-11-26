// Book demos of the Live and catalogue-section blocks (controls/README.md, Blocks of Home, Live and the session view).
// Values follow the masters on docs/design-system/board/blocks.bl.
import { createSignal, For } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  StatusPill, FigureTile, Gauge, RunStrip, WorkerSlot, HostBand, LiveRow, ChangeRow, DriftRow, GapRow, StepTrail, GapCompare, DetailHead, DiffRow, InheritedRows, MatrixCell,
  CatalogueTree, FilterBar, StateCounters, Presets, SearchBox, GroupControl, SortControl, DetailSection, KvRow, TabBar,
  DataTable, TableRow, TableName, StateBadge, ShareBar, ProblemRow, Button, SplitButton, WorkFacts, peerFence,
  InfiniteList, Preview, StateDot, createPages, type Page,
  type StripRun, type GaugeProps, type WorkerSlotProps, type CatalogueTreeNode, type StateOf,
} from '../src/index.ts';
import { NOW, min, hr } from './fixtures.ts';

const m = (value: number) => ({ value, basis: 'measured' as const });
const runs = (seed: number, failing = false): StripRun[] => {
  const out: StripRun[] = [];
  for (let i = 0; i < 22; i++) {
    const start = NOW - 24 * hr + (i * 65 + (seed * 13 + i * 7) % 20) * min;
    out.push({ start, end: start + (18 + (i * seed) % 30) * min, failed: !failing && i === 13 });
  }
  if (failing) for (let i = 0; i < 6; i++) out.push({ start: NOW - (7 * hr) + i * 12 * min, end: NOW - (7 * hr) + i * 12 * min + 4 * min, failed: true });
  if (failing) out.push({ start: NOW - 6 * hr, end: NOW - 40 * min, failed: true });
  return out;
};
const trend = [2, 3, 2, 4, 3, 3, 5, 4, 3, 5, 6, 4, 5, 6, 4, 4];
const failTrend = [null, null, null, null, null, null, null, 1, 2, 4, 3, 6];
const gauges = (cpu: number, mem: number, disk: number): GaugeProps[] => [
  { label: 'CPU', name: 'Processor', value: cpu, warnAt: 80, critAt: 95 },
  { label: 'MEM', name: 'Memory', value: mem, warnAt: 80, critAt: 95 },
  { label: 'DISK', name: 'Disk', value: disk, warnAt: 80, critAt: 95 },
];
const busy = (slot: number, title: string, code: string, project: string, harness: string, started: number, tokens: number, color: 'd1' | 'd4' = 'd4'): WorkerSlotProps =>
  ({ slot, state: 'busy', title, code, project, harness, started, tokens: m(tokens), color, now: NOW, onOpen: () => {} });
const failing: WorkerSlotProps = { slot: 3, state: 'failing', title: 'Codex start failed ×22', code: 'Lab FLT-6', project: 'lab', harness: 'codex', retryAt: NOW + 40_000, cause: 'browser tool missing', now: NOW, onOpen: () => {} };
/** Attempts and hours in work of six open tasks: the 500-attempt one stands out against the rest (peerFence). */
const PEER_ATTEMPTS = [1, 1, 2, 1, 3, 500], PEER_HOURS = [1, 2, 3, 2, 1, 40];
const attemptsFence = peerFence(PEER_ATTEMPTS), workFence = peerFence(PEER_HOURS.map(h => h * 3600));

function Label(p: { children: JSX.Element }) { return <div class="frame-label">{p.children}</div>; }

function StatusPillDemo() {
  return <div class="row">
    <StatusPill state="working" />
    <StatusPill state="problem" text="pds18-native cannot start Codex" more={1} onOpen={() => {}} />
    <StatusPill state="connecting" />
    <StatusPill state="stale" at={NOW - 2 * min} />
  </div>;
}

function FigureDemo() {
  return <div class="grid2">
    <FigureTile label="Agents working" value={m(4)} of={8} sub="of 8 slots" trend={trend} trendLabel="last 24 hours, 2 to 6" onOpen={() => {}} />
    <FigureTile label="Failed starts 6h" value={m(22)} sub="coding-tool-unready" trend={failTrend} trendLabel="last 6 hours, 0 to 6" alert alertBars={5} onOpen={() => {}} />
    <FigureTile label="Spend today" value={{ value: null, basis: 'unavailable', reason: 'no reading yet' }} kind="money" sub="month $812" trend={[1, 2, 2, 3, null, null]} trendLabel="last 6 days" />
    <FigureTile label="Runs today" value={m(44)} sub="38 jobs" trend={trend} trendLabel="last 24 hours" stale />
  </div>;
}

function GaugeDemo() {
  return <div class="row"><For each={[...gauges(62, 84, 97), { label: 'MEM', name: 'Memory', value: null, warnAt: 80, critAt: 95 }]}>{g => <Gauge {...g} />}</For></div>;
}

function RunStripDemo() {
  return <div class="col"><RunStrip runs={runs(1)} now={NOW} /><RunStrip runs={runs(2, true)} now={NOW} /></div>;
}

function SlotDemo() {
  return <div class="grid2">
    <WorkerSlot {...busy(1, 'Fix receipt factory', 'T-FLT-51', 'fleet', 'codex', NOW - 12 * min, 430_000)} />
    <WorkerSlot slot={2} state="idle" />
    <WorkerSlot {...failing} />
    <WorkerSlot {...busy(4, 'Remaining parent accounting with a much longer title that has to truncate', 'T-CXX-39', 'cyprus.bz', 'claude', NOW - 34 * min, 1_200_000, 'd1')} />
    <WorkerSlot {...busy(5, 'Retry receipts import', 'T-FLT-58', 'fleet', 'codex', NOW - 3 * min, 0)} tokens={undefined} harness={undefined}
      work={{ attempts: 500, attemptsFence, workAt: NOW - 40 * hr, workFence, agent: 'codex' }} />
    <WorkerSlot slot={6} state="failing" title="Codex start failed" cause="browser tool missing" now={NOW} onOpen={() => {}}
      work={{ attempts: 22, lastAt: NOW - 2 * min, lastLabel: 'Latest failed start' }} />
  </div>;
}

function WorkFactsDemo() {
  return <ul class="pane" style={{ margin: 0, padding: 0 }}>
    <LiveRow kind="task" state="working" code="T-FLT-60" title="One attempt: time in work and the agent" facts={<WorkFacts attempts={1} lastAt={NOW - 25 * min} workAt={NOW - 25 * min} state={{ label: 'In progress', at: NOW - 25 * min }} agent="claude" />} />
    <LiveRow kind="task" state="waiting" code="T-FLT-61" title="Three attempts; hover the time for the latest attempt and the wait" facts={<WorkFacts attempts={3} attemptsFence={attemptsFence} lastAt={NOW - 40 * min} workAt={NOW - 3 * hr} workFence={workFence} state={{ label: 'Waiting', at: NOW - 35 * min }} agent="codex" />} />
    <LiveRow kind="task" state="queued" code="T-FLT-58" title="500 attempts and 40 hours: both stand out against the peers" facts={<WorkFacts attempts={500} attemptsFence={attemptsFence} lastAt={NOW - 3 * min} workAt={NOW - 40 * hr} workFence={workFence} state={{ label: 'Queued', at: NOW - 30 * 1000 }} agent="claude" />} />
  </ul>;
}

function HostDemo() {
  return <div class="col">
    <HostBand name="owner-mac" state="normal" facts="darwin · 318 GB free" gauges={gauges(62, 57, 52)} day="38 runs · 5h 33m" onOpen={() => {}}
      slots={[busy(1, 'Fix receipt factory', 'T-FLT-51', 'fleet', 'codex', NOW - 12 * min, 430_000), busy(2, 'Design board review', 'O-FLT-596', 'design', 'claude', NOW - 4 * min, 58_000, 'd1'), { slot: 3, state: 'idle' }]}
      strip={{ runs: runs(1), now: NOW }} />
    <HostBand name="pds18-native" state="problem" facts="linux · 338 GB free" gauges={gauges(62, 84, 41)} day="44 runs · 38 jobs · 5h 18m" onOpen={() => {}}
      problem="Codex not ready: coding-tool-unready (browser). 22 failed starts since 18:40. Agent runs continue on claude."
      slots={[busy(1, 'Fix receipt factory', 'T-FLT-51', 'fleet', 'codex', NOW - 12 * min, 430_000), busy(2, 'Remaining parent accounting', 'T-CXX-39', 'cyprus.bz', 'claude', NOW - 34 * min, 1_200_000, 'd1'), failing, { slot: 4, state: 'idle' }]}
      strip={{ runs: runs(2, true), now: NOW }} />
    <HostBand name="windows" state="offline" facts="win32 · 41 GB free" seen={NOW - 18 * min} gauges={gauges(12, 40, 88)} day="9 runs · 1h 02m"
      slots={[{ slot: 1, state: 'idle' }, { slot: 2, state: 'idle' }]} strip={{ runs: runs(3), now: NOW }} />
    <HostBand name="mac-mini" state="loading" />
  </div>;
}

function LiveRowDemo() {
  return <ul class="pane" style={{ margin: 0, padding: 0 }}>
    <LiveRow kind="project" name="fleet" running={3} queued={4} done={23} trend={trend} trendLabel="runs, last 24 hours" onOpen={() => {}} />
    <LiveRow kind="task" state="working" code="T-FLT-51" title="Fix receipt factory" onOpen={() => {}}
      facts={<WorkFacts attempts={2} attemptsFence={attemptsFence} lastAt={NOW - 12 * min} workAt={NOW - 2 * hr} workFence={workFence} state={{ label: 'In progress', at: NOW - 2 * hr }} agent="codex" />} />
    <LiveRow kind="task" state="queued" code="T-FLT-58" title="Retry receipts import" onOpen={() => {}}
      facts={<WorkFacts attempts={500} attemptsFence={attemptsFence} lastAt={NOW - 3 * min} workAt={NOW - 40 * hr} workFence={workFence} state={{ label: 'Queued', at: NOW - 30 * 1000 }} agent="claude" />} />
    <LiveRow kind="delivery" tone="running" code={`#${827}`} title="retain initiating person" stage="merging" onOpen={() => {}}
      facts={<WorkFacts workAt={NOW - 50 * min} state={{ label: 'Merging', at: NOW - 6 * min }} />} />
    <LiveRow kind="runner" name="ubuntu-latest" fact="3 jobs · 142 min today" onOpen={() => {}} />
    <LiveRow kind="event" at={NOW - min} state="done" text={`T-FLT-43 merged · #${828} rolling out`} onOpen={() => {}} />
    <LiveRow kind="event" at={NOW - 2 * min} state="done" text="Nightly digest finished" onOpen={() => {}} facts={<WorkFacts attempts={207} agent="codex" />} />
    <LiveRow kind="runner" name="self-hosted pds18" fact="offline 12m" trouble onOpen={() => {}} />
    <LiveRow kind="event" at={NOW - 9 * min} state="done" text="Stale row: last read 9m ago" stale />
  </ul>;
}

function DriftRowDemo() {
  return <ul class="drift-rows">
    <DriftRow name="github" detail="19 granted · all 19 that code and profiles use" chip={{ text: 'In sync', tone: 'success' }} fact="tools changed 26 Nov · alex" />
    <DriftRow name="gradus-gitlab" detail="7 granted · lacks mrs_open, pipelines_failed and releases" chip={{ text: '3 missing', tone: 'attention', tip: 'mrs_open: Delivery (go/coordinator/delivery.go)' }} fact="tools changed 20 Nov · alex" />
    <DriftRow name="tavily" detail="6 of 6 keys active · least used · no next key" chip={{ text: 'No failover', tone: 'attention' }} />
    <DriftRow name="python" detail="3.13.1 · on 2 of 3 hosts" chip={{ text: 'Missing on pds18', tone: 'attention' }} href="#" onOpen={() => {}} />
    <DriftRow name="Codex: coding-worker" detail="no sandbox on writable runs · network on" chip={{ text: 'Applied on 3 hosts', tone: 'success' }} fact="release v1.261126.11" />
  </ul>;
}

function GapRowDemo() {
  return <div class="stack">
    <ul class="gap-rows">
      <GapRow name="Sandbox blocks writes" detail="743 runs · 3 hosts · codex, claude, antigravity" days={[60, 180, 120, 217, 210, 30, 18]} changeAt={5} chip={{ text: 'Watching · #1133 on hosts', tone: 'running' }} fact="09:40" href="#" onOpen={() => {}} />
      <GapRow name="python missing" detail="17 runs · pds18, windows · codex" days={[0, 0, 0, 0, 17, 0, 0]} chip={{ text: 'Investigating', tone: 'running' }} fact="25 Nov" href="#" current onOpen={() => {}} />
      <GapRow name="jq missing" detail="9 runs · pds18 · codex" days={[0, 0, 0, 0, 8, 1, 0]} chip={{ text: 'Open', tone: 'attention' }} fact="25 Nov" href="#" onOpen={() => {}} />
      <GapRow name="Network blocked" detail="8 runs · owner-mac, pds18 · claude, codex" days={[0, 1, 0, 2, 4, 1, 0]} chip={{ text: 'Below threshold', tone: 'neutral' }} fact="25 Nov" href="#" onOpen={() => {}} />
    </ul>
    <StepTrail label="Where the finding stands" steps={[{ label: 'Found', state: 'done' }, { label: 'Investigating', state: 'done' }, { label: 'Change', state: 'done' }, { label: 'On hosts', state: 'done' }, { label: 'Watching', state: 'current' }, { label: 'Fixed', state: 'todo' }]} />
    <GapCompare label="Sandbox blocks writes: before and after" before={{ value: '217 failures a day', label: 'before, 25 Nov' }} after={{ value: '18', label: 'after, from 09:40', tone: 'success' }} days={[60, 180, 120, 217, 210, 30, 18]} changeAt={5} note="The change reached every host 26 Nov 09:40 (v1.261126.16)." />
  </div>;
}

function ChangeRowDemo() {
  const open = () => {};
  return <ul class="change-rows">
    <ChangeRow name="codex-designer" kind="agent" summary={'"Use GPT-6.2 Sol at medium"'} chip={{ text: 'PR #1102 · CI running', tone: 'running' }} at={NOW - 4 * min} href="#" onOpen={open} />
    <ChangeRow name="worker-agent" kind="pipeline" summary="196 runs in 30 d" chip={{ text: 'PR #1095 · merged · waits for release', tone: 'success' }} at={NOW - 25 * min} href="#" onOpen={open} />
    <ChangeRow name="doc-recognize-agent" kind="pipeline" tone="attention" summary="the repository changed it under your edit r9" chip={{ text: 'Conflict', tone: 'attention' }} at={NOW - 26 * hr} href="#" onOpen={open}
      actions={[{ label: 'Compare', onClick: open }, { label: 'Keep mine', onClick: open }, { label: 'Take repository', primary: true, onClick: open }]} />
    <ChangeRow name="connections-probe" kind="pipeline" tone="danger" summary="PR #1099 failed its checks" chip={{ text: 'Failed', tone: 'danger' }} at={NOW - 2 * hr} href="#" onOpen={open}
      actions={[{ label: 'Open task', primary: true, onClick: open }]} />
    <ChangeRow name="cs-review-helper" kind="agent" summary="made in Fleet, not named by any experiment" chip={{ text: 'New in Fleet', tone: 'neutral' }} at={NOW - 3 * hr} href="#" onOpen={open}
      actions={[{ label: 'Publish', primary: true, onClick: open }]} />
    <ChangeRow name="Measure Fleet code mode on antigravity: tokens, cost, time and correctness for multi-search research, and check every host that runs antigravity." chip={{ text: 'Draft', tone: 'neutral' }} summary="3 variants: antigravity-researcher, -code-only, -code-add" at={NOW - 9 * 24 * hr} href="#" onOpen={open} />
  </ul>;
}

const cat = (): CatalogueTreeNode[] => [
  { id: 'pin', label: 'Fix receipt factory', sub: 'T-FLT-51', state: { family: 'session', state: 'running' }, right: <span class="mono">2 runs 12m</span>, pinned: true },
  { id: 'running', label: 'Running', count: 12, children: [
    { id: 'a', label: 'Fix receipt factory', sub: 'T-FLT-51', state: { family: 'session', state: 'running' }, right: <span class="mono">2 runs 12m</span> },
    { id: 'b', label: 'Design board review', sub: 'O-FLT-596', state: { family: 'session', state: 'waiting' }, right: <span class="mono">1 run 4m</span> },
    { id: 'c', label: 'Remaining parent accounting', sub: 'T-CXX-39', state: { family: 'session', state: 'failed' }, right: <span class="mono">3 runs 34m</span> },
  ] },
  { id: 'done', label: 'Done', count: 238, defaultCollapsed: true, children: [{ id: 'd', label: 'Render prop batch', sub: 'T-GAM-2', state: { family: 'session', state: 'succeeded' } }] },
];

function CatalogueDemo() {
  const nodes = cat();
  const [sel, setSel] = createSignal<string | null>('a');
  return <div class="pane"><CatalogueTree label="Sessions" nodes={nodes} selectedId={sel()} onSelect={n => setSel(n.id)} onPin={() => {}} /></div>;
}

function FilterDemo() {
  const [q, setQ] = createSignal('');
  const [view, setView] = createSignal<string | null>('Needs me');
  const [act, setAct] = createSignal<StateOf<'session'>[]>([]);
  const toggle = (s: StateOf<'session'>) => setAct(a => a.includes(s) ? a.filter(x => x !== s) : [...a, s]);
  return <div class="col">
    <div class="row"><Label>Search: icon, open</Label><SearchBox label="Search" value="" onInput={() => {}} /><SearchBox label="Search" value="agent:claude host:mac" onInput={() => {}} /></div>
    <div class="pane">
      <FilterBar query={q()} onQuery={setQ} placeholder="Search sessions" shown={84} total={1240}
        extra={<Presets system={['Needs me', 'Running']} presets={[]} active={view()} onPick={setView} onSave={() => {}} onUnpin={() => {}} />}
        counters={<StateCounters family="session" label="Sessions by state" counts={{ running: 12, waiting: 3, idle: 31, failed: 2 }} active={act()} onToggle={toggle} onAll={() => setAct([])} hideZero />}
        tools={<><GroupControl value="state" options={[['state', 'State'], ['project', 'Project']]} onChange={() => {}} /><SortControl value="age" fields={[['age', 'Age']]} onChange={() => {}} /></>} />
    </div>
  </div>;
}

function DetailDemo() {
  const [tab, setTab] = createSignal<'sessions' | 'tasks' | 'delivery' | 'results' | 'rules'>('sessions');
  return <div class="col">
    <div class="pane" style={{ padding: 'var(--space-4)' }}>
      <DetailHead crumb="fleet · T-FLT-51" title="Fix receipt factory" onClose={() => {}}
        state={<><StateBadge family="session" state="running" /> step 2 of 3 · taken 12m ago</>}
        actions={<><SplitButton main={{ label: 'Pause' }} alternatives={[{ label: 'Stop' }]} /><Button label="Share" icon="link" iconOnly kind="quiet" /></>} />
    </div>
    <TabBar label="Project facets" value={tab()} onChange={setTab}
      tabs={[{ id: 'sessions', label: 'Sessions' }, { id: 'tasks', label: 'Tasks', count: 3 }, { id: 'delivery', label: 'Delivery' }, { id: 'results', label: 'Results' }, { id: 'setup', label: 'Setup' }]} />
    <DetailSection title="Harnesses">
      <KvRow icon="network" label="pds18-native" meta="linux · 8 slots" value={<span class="mono">44 runs</span>} />
      <KvRow icon="network" label="owner-mac" meta="darwin · 4 slots" value={<span class="mono">38 runs</span>} />
    </DetailSection>
  </div>;
}

function TableDemo() {
  const [sel, setSel] = createSignal('b');
  const rows = [
    { id: 'a', name: 'codeMode · opus-5.5', score: '0.83', time: '4m 12s', cost: '$1.84', state: 'succeeded' as const },
    { id: 'b', name: 'baseline · sonnet-5.5', score: '0.71', time: '3m 40s', cost: '$0.92', state: 'succeeded' as const },
    { id: 'c', name: 'toolMode · haiku-5.5', score: '—', time: '—', cost: '—', state: 'running' as const },
  ];
  return <DataTable label="Leaderboard" head={<tr><th scope="col">Variant</th><th scope="col" class="num">Score</th><th scope="col" class="num">Time</th><th scope="col" class="num">Cost</th></tr>}>
    <For each={rows}>{r => (
      <TableRow selected={sel() === r.id} onOpen={() => setSel(r.id)}>
        <TableName lead={<StateBadge family="execution" state={r.state} compact />} label={r.name} current={sel() === r.id} onOpen={() => setSel(r.id)} />
        <td class="num">{r.score}</td><td class="num">{r.time}</td><td class="num">{r.cost}</td>
      </TableRow>
    )}</For>
  </DataTable>;
}

function DiffDemo() {
  return <div class="pane">
    <DiffRow field="model" value="opus-5.5" base="sonnet-5.5" />
    <DiffRow field="effort" value="high" base="medium" />
    <InheritedRows count={14}><DiffRow field="harness" value="claude" base="claude" /><DiffRow field="timeout" value="30m" base="30m" /></InheritedRows>
  </div>;
}

function ShareDemo() {
  const parts = [{ label: 'PR checks', v: 41, tone: 'd1' as const }, { label: 'Queue', v: 24, tone: 'd2' as const }, { label: 'Release', v: 8, tone: 'd3' as const }, { label: 'Install', v: 9, tone: 'd4' as const }];
  const v = (x: number) => `${x}m`;
  return <div class="col">
    <ShareBar label="Time per pull request" parts={parts} value={v} note="mean per pull request, 76 of 389 measured" />
    <ShareBar label="Time per pull request" parts={parts} value={v} partial note="12 of 389 measured" />
  </div>;
}

function MatrixDemo() {
  return <div class="grid2">
    <MatrixCell state="pass" label="Example 3, variant codeMode" score={0.91} k={3} n={3} time={m(252)} cost={{ value: 1.84, basis: 'measured', currency: 'USD' }} confounders="pds18-native · claude · opus-5.5" onOpen={() => {}} />
    <MatrixCell state="partial" label="Example 3, variant baseline" score={0.64} k={2} n={3} note="1 repeat left out: host changed" onOpen={() => {}} />
    <MatrixCell state="fail" label="Example 4, variant codeMode" k={0} n={3} note="timeout at step 4/6" onOpen={() => {}} onRetry={() => {}} />
    <MatrixCell state="running" label="Example 4, variant baseline" k={1} n={3} step="3/6" time={m(125)} onOpen={() => {}} />
    <MatrixCell state="excluded" label="Example 5, variant toolMode" k={0} n={3} onOpen={() => {}} />
  </div>;
}

function ProblemBlocksDemo() {
  return <div class="col">
    <ProblemRow severity="warning" title="Low space on windows store" facts={<><span class="mono">18 GB</span> free, warning at <span class="mono">25 GB</span> · 4 tasks waiting</>} onDetails={() => {}}
      actions={[{ label: 'Raise capacity', primary: true }, { label: 'Change location' }, { label: 'Add retention rule' }]} />
    <ProblemRow severity="failure" title="Codex cannot start on pds18-native" count={22} facts={<>coding-tool-unready (browser) since 18:40 · runs continue on claude</>} onDetails={() => {}}
      actions={[{ label: 'Reinstall browser tool', primary: true }, { label: 'Pause Codex here' }, { label: 'Open host' }]} />
  </div>;
}

/** OR-65: a list that loads on scroll inside its card (fake pages of 20, 400 ms each, the third page fails once),
 *  then a preview whose title opens the full list. */
function InfiniteDemo() {
  const rows = Array.from({ length: 70 }, (_, i) => ({ id: 'r' + i, t: `Session ${70 - i}`, state: (i % 7 === 3 ? 'failed' : 'done') as StateOf<'session'> }));
  const page = (from: number): Page<typeof rows[number], number> => ({ rows: rows.slice(from, from + 20), next: from + 20 < rows.length ? from + 20 : null });
  let failed = false;
  const pages = createPages({
    first: () => page(0),
    fetch: (from, signal) => new Promise((ok, no) => {
      const t = setTimeout(() => { if (from === 40 && !failed) { failed = true; no(new Error('network error')); } else ok(page(from)); }, 400);
      signal.addEventListener('abort', () => clearTimeout(t));
    }),
    key: r => r.id,
  });
  const row = (r: typeof rows[number]) => <div class="kv-row"><StateDot family="session" state={r.state} /><span class="kv-line">{r.t}</span></div>;
  return <div style={{ display: 'grid', 'grid-template-columns': 'repeat(auto-fit, minmax(280px, 1fr))', gap: 'var(--space-5)' }}>
    <div class="panel" style={{ height: '320px', overflow: 'auto' }}>
      <InfiniteList pages={pages} label="Sessions" endLabel="Older sessions"><For each={pages.rows}>{row}</For></InfiniteList>
    </div>
    <div class="panel">
      <DetailSection title="Latest finished" count={549} href="#latest" onOpen={() => {}}>
        <Preview n={5} items={rows.map(row)} />
      </DetailSection>
    </div>
  </div>;
}

type Entry = [string, string, string | undefined, () => JSX.Element, (() => JSX.Element)?];
/** Appended to the book's SECTIONS. */
export const BLOCK_SECTIONS: Entry[] = [
  ['infinite-list', 'Lists: infinite list and card preview', undefined, () => <InfiniteDemo />, () => <>OR-65. The left list loads its next page as its end scrolls within about five rows (one request in flight); the third page fails once and shows the danger row with Retry; after the last row there is nothing. The right card previews five rows; its title with the count opens the full list.</>],
  ['status-pill', 'Live: status pill', 'status-pill', () => <StatusPillDemo />, () => <>Working, problem (opens the cause, "· N more"), connecting, stale. role="status": the words change in place and are announced politely.</>],
  ['figure-tile', 'Live: figure tile with sparkline', 'figure-tile', () => <FigureDemo />, () => <>Normal, alert (the value and the latest bars turn danger, the sub-line names the cause), unknown (?, never 0), stale (dimmed, opens nothing). A dot on the base is a bar with no reading.</>],
  ['gauge', 'Live: gauge', 'fill-bar', () => <GaugeDemo />, () => <>The fill bar's one-value form. Attention past the host's warning threshold, danger past its critical one; both are host settings. Unknown is ?.</>],
  ['run-strip', 'Live: run strip', 'time-strip', () => <RunStripDemo />, () => <>One host's last 24 hours: a tick per run in the running tone, failures in danger.</>],
  ['worker-slot', 'Live: worker slot', 'worker-slot', () => <SlotDemo />, () => <>Busy (pulsing dot, elapsed time, project colour bar), idle (dashed, opens nothing), failing (what fails ×N, retry countdown, cause). The title takes the width and truncates. With work facts the second line shows code · project, then ×N, time in work and the agent mark; a failing slot shows its cause, ×N and the time since the latest failed start.</>],
  ['host-band', 'Live: host band', 'host-band', () => <HostDemo />, () => <>Normal, problem (danger border and glow, one problem line), offline (last values dimmed with the age), loading. Narrow bands stack the slots and move the gauges under the name (container query).</>],
  ['drift-row', 'Changes: drift rows', 'drift-row', () => <DriftRowDemo />, () => <>Agent environment: in sync, missing grants (the tool and who uses it in the tooltip), no failover, a host tool missing on a host (opens it), a sandbox group; narrow, each row stacks with the state on its first line.</>],
  ['gap-row', 'System › Agents: gap rows', 'gap-row', () => <GapRowDemo />, () => <>Watching a change (bars after it in success, a marker at the change), investigating (the open class), open, below threshold; the step trail of a watched finding; before and after the change. Narrow, each row puts sparkline, state and time on one line under the detail.</>],
  ['change-row', 'Changes: change rows', 'change-row', () => <ChangeRowDemo />, () => <>A definition on its way (its pull request), a conflict and a failure that need a person, one made in Fleet, a Lab experiment; narrow, each row stacks with the time on its first line.</>],
  ['live-row', 'Live: live rows', 'live-row', () => <LiveRowDemo />, () => <>Project, task in work, delivery, runner, event; work rows carry the work facts; repeats are one event with ×N; trouble turns the dot and the fact red; stale rows are dimmed and open nothing.</>],
  ['work-facts', 'Rows: work facts', 'work-facts', () => <WorkFactsDemo />, () => <>Quiet form (owner pick B): ×N attempts from two, time in work, agent mark; the latest attempt and the time in the state are in the tooltip of the time in work. Peers: attempts 1, 1, 2, 1, 3, 500 and hours 1, 2, 3, 2, 1, 40, so ×500 and 40h take the attention tone (ten times the median or more).</>],
  ['catalogue-row', 'Sections: catalogue rows and list groups', 'catalogue-tree', () => <CatalogueDemo />, () => <>The catalogue row is CatalogueTree's leaf: state icon, title and code, facts on the right; selected with the accent bar; pinned with the pin first. Groups fold with their count.</>],
  ['filter-blocks', 'Sections: search, view chip, filter bar', 'filter-bar', () => <FilterDemo />, () => <>Search as an icon, then open with a query; saved views as chips; the count only while a filter narrows; state counters; group and sort as icons. No Filter button.</>],
  ['detail-head', 'Sections: detail head, tab bar, detail section and key rows', 'detail-section', () => <DetailDemo />, () => <>Close, project · code, the primary action as a split button and Share; the title and the state line. Facets in the tab bar; stacked areas in detail sections.</>],
  ['table-row', 'Sections: table rows', 'data-list', () => <TableDemo />, () => <>Header, row and selected (accent bar). The whole row opens; the name is the row's button for the keyboard; figures right aligned in mono.</>],
  ['diff-row', 'Sections: diff rows', undefined, () => <DiffDemo />, () => <>Only what differs from the base; the default is in the tooltip, never a second value or an arrow (OR-45, OR-7). Inherited defaults fold.</>],
  ['share-bar-partial', 'Sections: share bar, normal and partial', 'share-bar', () => <ShareDemo />, () => <>Partial: hollow segments because not every part was measured; the note says how many were.</>],
  ['matrix-cell', 'Sections: matrix cells', 'matrix-cell', () => <MatrixDemo />, () => <>Pass, partial, fail with Retry, running, excluded. Confounders (host, harness, model) are in the tooltip; one score per evaluator.</>],
  ['problem-blocks', 'Sections: problem rows with Details and repeats', 'problem-row', () => <ProblemBlocksDemo />, () => <>Attention and danger; repeats aggregate into ×N; Details on the right of the title; the recommended action is primary.</>],
];
