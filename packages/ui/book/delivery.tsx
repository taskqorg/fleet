// Delivery mockup (docs/design-system/pages/delivery.md): the pull requests by stage in the control column; the work area
// answers what needs a look, what moves now and the pace, or shows the opened pull request. Fixture data only.
import { useState } from 'preact/hooks';
import {
  ListDetail, ListFrame, FilterBar, StateCounters, GroupHeader, GroupControl, SortControl, SelectToggle, StateBadge, AgeChip, Num, Totals,
  AreaChart, TimeStrip, ProblemRow, DetailSection, KvRow, Button, Choice, Stale, Fact, RowFacts, states, type ChartSeries, type StateOf,
} from '../src/index.ts';
import { NOW, min, hr } from './fixtures.ts';

type Stage = StateOf<'delivery'>;
const LIMIT: Partial<Record<Stage, number>> = { pr_ci: 1800, mq_wait: 900, mq_ci: 1800, releasing: 1200, installing: 1200 };
const ORDER: Stage[] = ['mq_ci', 'mq_wait', 'pr_ci', 'pr_open', 'releasing', 'installing', 'installed'];
type Pr = { n: number; title: string; stage: Stage; since: number; opened: number; runs: number; removals: number; release?: string; author: string };
const PRS: Pr[] = [
  { n: 552, title: 'Delivery as list and detail', stage: 'pr_ci', since: NOW - 6 * min, opened: NOW - 40 * min, runs: 0, removals: 0, author: 'kirs' },
  { n: 551, title: 'Design docs: row menu, selection mode, row pin specs', stage: 'mq_ci', since: NOW - 4 * min, opened: NOW - 55 * min, runs: 1, removals: 0, author: 'kirs' },
  { n: 545, title: 'Version beside the person; one reliable update control', stage: 'mq_wait', since: NOW - 22 * min, opened: NOW - 3 * hr, runs: 4, removals: 3, author: 'kirs' },
  { n: 553, title: 'Gateway: drain before snapshot swap', stage: 'pr_open', since: NOW - 2 * hr, opened: NOW - 2 * hr, runs: 0, removals: 0, author: 'agent' },
  { n: 549, title: 'fix: interface history fixture', stage: 'installing', since: NOW - 9 * min, opened: NOW - 2.5 * hr, runs: 1, removals: 0, release: '1.261115.12', author: 'agent' },
  { n: 547, title: 'Pin mark on pinned rows; plain group and sort chips', stage: 'releasing', since: NOW - 26 * min, opened: NOW - 1.6 * hr, runs: 3, removals: 2, author: 'kirs' },
  { n: 546, title: 'List tools: group and sort chips with menus', stage: 'installed', since: NOW - 70 * min, opened: NOW - 3 * hr, runs: 1, removals: 0, release: '1.261115.11', author: 'kirs' },
  { n: 544, title: 'fix: resource replacement throughput', stage: 'installed', since: NOW - 3 * hr, opened: NOW - 6 * hr, runs: 1, removals: 0, release: '1.261115.10', author: 'agent' },
  { n: 543, title: 'Lists: row menu, selection mode, Pinned group', stage: 'installed', since: NOW - 4 * hr, opened: NOW - 7 * hr, runs: 2, removals: 1, release: '1.261115.9', author: 'kirs' },
];
const late = (p: Pr) => LIMIT[p.stage] !== undefined && NOW - p.since > LIMIT[p.stage]! * 1000;

/** The control column, the same in every variant: state counters by stage, grouped rows, facts on the right. */
function PrList({ sel, onSel, filter, onFilter }: { sel?: number; onSel: (n: number) => void; filter: Stage[]; onFilter: (s: Stage[]) => void }) {
  const counts: Partial<Record<Stage, number>> = {};
  for (const s of ORDER) counts[s] = PRS.filter(p => p.stage === s).length;
  const shown = filter.length ? PRS.filter(p => filter.includes(p.stage)) : PRS;
  return <>
    <header class="page-h"><h1>Delivery</h1><span class="grow" /><Stale at={NOW - 2 * min} now={NOW} /></header>
    <section class="panel">
      <ListFrame bar={<FilterBar query="" onQuery={() => {}} placeholder="Search pull requests, author:kirs" shown={shown.length} total={PRS.length}
        counters={<StateCounters family="delivery" label="Pull requests by stage" counts={counts} active={filter} onAll={() => onFilter([])} onToggle={s => onFilter(filter.includes(s) ? filter.filter(x => x !== s) : [...filter, s])} />}
        tools={<><SelectToggle on={false} onClick={() => {}} /><GroupControl value="stage" options={[['stage', 'Stage'], ['author', 'Author'], ['none', 'None']] as const} onChange={() => {}} />
          <SortControl value="-changed" fields={[['changed', 'In stage since'], ['opened', 'Opened']]} onChange={() => {}} /></>} />}>
        <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
          {ORDER.filter(s => shown.some(p => p.stage === s)).map(s => <>
            <div class="vl-row static grp" key={'g' + s}><GroupHeader icon={states.delivery[s].icon} label={states.delivery[s].label} tone={states.delivery[s].tone} count={shown.filter(p => p.stage === s).length} open onToggle={() => {}} /></div>
            {shown.filter(p => p.stage === s).map(p => <div key={p.n} role="row" class={`vl-row static${p.n === sel ? ' sel' : ''}`}>
              <span class="lead"><StateBadge family="delivery" state={p.stage} compact /></span>
              <span class="tt" style={{ flexWrap: 'wrap', rowGap: 0 }}><a class="title row-link" style={{ flex: '0 0 100%' }} href="#/delivery" onClick={e => { e.preventDefault(); onSel(p.n); }}>{p.title}</a><span class="id">#{p.n}</span>{p.release && <span class="id">{p.release}</span>}</span>
              <span class="rz"><RowFacts>
                {p.runs > 0 && <Fact icon="repeat" text={String(p.runs)} tip={`${p.runs} queue runs${p.removals ? `, ${p.removals} removed` : ''}`} />}
                {p.removals > 0 && <Fact icon="rotate-ccw" text={String(p.removals)} tip={`${p.removals} removed from the queue`} />}
                <Fact tip={`${states.delivery[p.stage].label} for`}><AgeChip t={p.since} now={NOW} alertAfter={LIMIT[p.stage]} alertLabel="past the stage limit" /></Fact>
              </RowFacts></span>
            </div>)}
          </>)}
        </div>
      </ListFrame>
    </section>
  </>;
}

const days = (n: number, f: (i: number) => number) => Array.from({ length: n }, (_, i) => ({ at: NOW - (n - 1 - i) * 24 * hr, v: f(i) }));
const mins = (v: number) => `${Math.round(v)}m`;
// Pull request numbers are built, so the token check does not read one as a colour.
const pr = (n: number) => '#' + n;
const regression = <ProblemRow severity="warning" title="Merge queue P90 is 26m, baseline 10m" facts={`Since ${pr(540)} · 7 runs · 3 removals for flaky browser checks`}
  actions={[{ label: 'Compare', primary: true }]} />;

/** The opened pull request (all variants): stages on one time axis, queue runs, release and installs per host. */
function PrDetail({ p, onClose }: { p: Pr; onClose: () => void }) {
  const t0 = p.opened, stages: [Stage, number, number | null][] = [['pr_ci', t0 + 2 * min, t0 + 14 * min], ['mq_wait', t0 + 14 * min, t0 + 30 * min], ['mq_ci', t0 + 30 * min, t0 + 52 * min], ['releasing', t0 + 52 * min, t0 + 58 * min], ['installing', t0 + 58 * min, null]];
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={p.title}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><span class="muted mono">taskqorg/fleet #{p.n}</span><span class="grow" /><Button label="Open on GitHub" icon="external-link" iconOnly /></header>
    <h2 class="detail-title">{p.title}</h2>
    <div class="state-line"><StateBadge family="delivery" state={p.stage} /><span class="muted">by {p.author}</span><AgeChip t={p.opened} now={NOW} /></div>
    <div class="stack-pad">
      <Totals items={[{ label: 'Lead so far', m: { value: (NOW - p.opened) / 1000, basis: 'measured' }, kind: 'duration' }, { label: 'Queue runs', m: { value: p.runs, basis: 'measured' }, kind: 'count' }, { label: 'Removed', m: { value: p.removals, basis: 'measured' }, kind: 'count' }]} />
      <DetailSection title="Stages">{stages.map(([s, a, b]) => <KvRow key={s} icon={states.delivery[s].icon} label={states.delivery[s].label}
        value={<span style={{ display: 'inline-flex', gap: 'var(--space-2)', alignItems: 'center' }}><TimeStrip from={t0} to={NOW} start={a} end={b && Math.min(b, NOW)} now={NOW} state={b && b < NOW ? 'succeeded' : 'running'} label={states.delivery[s].label} />
          <Num m={{ value: ((b && b < NOW ? b : NOW) - a) / 1000, basis: 'measured' }} kind="duration" /></span>} />)}</DetailSection>
      <DetailSection title="Queue runs">{Array.from({ length: Math.max(1, p.runs) }, (_, i) => <KvRow key={i} icon={i < p.removals ? 'octagon-x' : 'circle-check'} label={`Run ${i + 1}`}
        meta={i < p.removals ? 'browser-tests (1) failed: scrolled up pill' : 'all checks passed'} value={<Num m={{ value: (14 + i * 3) * 60, basis: 'measured' }} kind="duration" />} />)}</DetailSection>
      <DetailSection title="Installed">{[['pds18', 'coordinator and executor'], ['mac', 'executor'], ['windows', 'executor']].map(([h, what], i) =>
        <KvRow key={h} icon="terminal" label={h} meta={what} value={i < 2 ? <AgeChip t={NOW - (8 - i * 3) * min} now={NOW} /> : <span class="muted">waiting</span>} />)}</DetailSection>
    </div>
  </article>;
}

/** Stage parts of a pull request from its open time (fixture): each stage ends where the next starts, the current one is live. */
const STAGE_SEQ: Stage[] = ['pr_open', 'pr_ci', 'mq_wait', 'mq_ci', 'releasing', 'installing', 'installed'];
function partsOf(p: Pr) {
  const upto = STAGE_SEQ.indexOf(p.stage), span = p.since - p.opened, steps = Math.max(1, upto);
  return STAGE_SEQ.slice(0, upto + 1).filter(s => s !== 'installed').map((s, i) => {
    const start = s === p.stage ? p.since : p.opened + span * i / steps, end = s === p.stage ? null : p.opened + span * (i + 1) / steps;
    const over = s === p.stage && late(p);
    return { start, end, tone: over ? 'danger' as const : states.delivery[s].tone, label: `${states.delivery[s].label}${over ? ', past the limit' : ''}` };
  });
}
const BY_STAGE: ChartSeries[] = [
  { label: 'PR checks', tone: 'd1', points: days(7, i => [12, 13, 12, 14, 13, 14, 13][i]) },
  { label: 'Queue', tone: 'd2', points: days(7, i => [7, 8, 7, 10, 11, 15, 18][i]) },
  { label: 'Release', tone: 'd3', points: days(7, i => [5, 5, 6, 5, 5, 6, 5][i]) },
  { label: 'Install', tone: 'd4', points: days(7, i => [4, 4, 5, 4, 3, 4, 4][i]) },
];
const MERGED: ChartSeries[] = [{ label: 'Merged', tone: 'd1', step: true, points: days(7, i => [11, 14, 9, 16, 13, 12, 9][i]) }];

/** D: one page in the order of the operator's questions: what needs a look, what moves now, what the pace is. */
function IdleD({ range, onRange, day, onDay }: { range: string; onRange: (r: string) => void; day: string | null; onDay: (d: string | null) => void }) {
  const from = NOW - 6 * hr, moving = ORDER.flatMap(s => PRS.filter(p => p.stage === s && s !== 'installed'));
  const dayBuckets = BY_STAGE[0].points.map((q, i) => ({ key: String(i), label: new Date(q.at).toUTCString().slice(5, 11) }));
  return <div class="stack-pad">
    <DetailSection title="Needs a look">
      {regression}
      <ProblemRow severity="warning" title={`${pr(545)} waiting in queue for 22m, limit 15m`} facts="Version beside the person; one reliable update control" actions={[{ label: `Open ${pr(545)}`, primary: true }]} />
    </DetailSection>
    <DetailSection title="Moving now, 6 hours">
      <div class="vl-wrap" style={{ '--cols': '3.5rem minmax(0,1fr) 3rem' }}>
        {moving.map(p => <div key={p.n} class="vl-row static" role="row">
          <span class="mono muted">#{p.n}</span>
          <TimeStrip from={from} to={NOW} start={p.opened} end={null} now={NOW} state="running" label={`#${p.n}`} parts={partsOf(p)} />
          <span class="r"><AgeChip t={p.since} now={NOW} alertAfter={LIMIT[p.stage]} alertLabel="past the stage limit" /></span>
        </div>)}
      </div>
    </DetailSection>
    <DetailSection title="Pace">
      <div class="state-line"><Choice label="Range" options={[{ id: '24h', label: '24 h' }, { id: '7d', label: '7 days' }, { id: '30d', label: '30 days' }]} value={range} onChange={onRange} /></div>
      <Totals items={[
        { label: 'Lead P50', m: { value: 66 * 60, basis: 'measured' }, kind: 'duration', note: 'baseline 50m' },
        { label: 'Lead P90', m: { value: 132 * 60, basis: 'measured' }, kind: 'duration', note: 'baseline 95m' },
        { label: 'Merged', m: { value: 84, basis: 'measured' }, kind: 'count', note: '12 a day' },
        { label: 'Queue removals', m: { value: 11, basis: 'measured' }, kind: 'count', note: '6 flaky' },
      ]} />
      <div class="cols">
        <div><b class="type-sm">Where the time goes, P50 per stage</b><AreaChart label="Lead time P50 by stage per day" series={BY_STAGE} stacked from={NOW - 6 * 24 * hr} to={NOW} now={NOW} max={50} value={mins}
          buckets={dayBuckets} selectedBucket={day} onSelectBucket={k => onDay(day === k ? null : k)} /></div>
        <div><b class="type-sm">Merged per day</b><AreaChart label="Pull requests merged per day" series={MERGED} from={NOW - 6 * 24 * hr} to={NOW} now={NOW} max={20} value={v => String(Math.round(v))} /></div>
      </div>
      <p class="muted type-sm">{day === null ? 'A day filters the list to the pull requests merged that day.' : `The list shows the pull requests merged on ${dayBuckets[+day].label}.`}</p>
    </DetailSection>
  </div>;
}

export function Delivery() {
  const [sel, setSel] = useState<number | undefined>(undefined);
  const [filter, setFilter] = useState<Stage[]>([]);
  const [day, setDay] = useState<string | null>(null);
  const [range, setRange] = useState('7d');
  const p = PRS.find(x => x.n === sel);
  return <ListDetail list={<PrList sel={sel} onSel={setSel} filter={filter} onFilter={setFilter} />} detailKey={sel}
    idle={<IdleD range={range} onRange={setRange} day={day} onDay={setDay} />} detail={p && <PrDetail p={p} onClose={() => setSel(undefined)} />} />;
}
