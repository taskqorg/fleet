// Delivery mockup (docs/design-system/pages/delivery.md): the pull requests by stage in the control column; the work area
// answers what needs a look, what moves now and the pace, or shows the opened pull request. Fixture data only.
// Pace: small charts stacked on one calendar-day axis (variant A, owner 2026-11-17; decisions.md D-delivery-pace).
import type { ComponentChildren } from 'preact';
import { useState } from 'preact/hooks';
import {
  ListDetail, ListFrame, FilterBar, StateCounters, GroupHeader, GroupControl, SortControl, SelectToggle, StateBadge, AgeChip, Num, Totals,
  TimeStrip, ProblemRow, DetailSection, KvRow, Button, Choice, Stale, Fact, RowFacts, Presets, Latest, DayChart, ShareBar, NormBar,
  capOf, quantile, day, dayStart, slotStart, hourLabel, duration, DELIVERY_ORDER, states, type StateOf, type DayCol, type DayMark, type SharePart,
} from '../src/index.ts';
import { NOW, min, hr } from './fixtures.ts';

type Stage = StateOf<'delivery'>;
const DAY = 24 * hr;
// Attention order of the stage groups.
const ORDER = DELIVERY_ORDER;
/** Each stage's norm: its P50 and P90 over the last 14 days (delivery_baseline rolling, read 17 Nov 2026), in ms. Open has none: people work there. */
const NORM: Partial<Record<Stage, [number, number]>> = {
  pr_ci: [6.5 * min, 42 * min], mq_wait: [0.8 * min, 8 * min], mq_ci: [5.8 * min, 15.6 * min], releasing: [1.2 * min, 4.4 * min], installing: [1.1 * min, 2.1 * min],
};
const BASE_TOTAL: [number, number] = [20.8 * min, 73.5 * min];
const late = (p: Pr) => NORM[p.stage] !== undefined && NOW - p.since > NORM[p.stage]![1];
const fmt = (ms: number) => duration(ms / 1000).text;
// Pull request numbers are built, so the token check does not read one as a colour.
const pr = (n: number) => '#' + n;

/* ---------- Fixture history: 9 recorded days shaped like the live delivery_history of 17 Nov 2026 ---------- */
// Merged per day 35, 150, 143, 131, 30, 18, 26, 34 and 7 so far today. The first two days have no queue times and a
// release that took hours (the backfill: about 31 h and 9 h medians); install times exist from the sixth day, and on
// that day for 9 of 18 pull requests only; the eighth day has three pull requests whose checks ran over 11 hours.
type Hist = { n: number; title: string; merged: number; d: { pr_ci: number; queue?: number; release: number; install?: number; total?: number }; removals: number; flakes: number };
const MERGED = [35, 150, 143, 131, 30, 18, 26, 34, 7];
const D0 = dayStart(NOW, -(MERGED.length - 1));
// Install times are recorded from noon of the sixth day: that day is measured for its afternoon only (9 of 18).
const REC = D0 + 5 * DAY + 12 * hr;
const rnd = (() => { let s = 0x2545f491; return () => { s ^= s << 13; s ^= s >>> 17; s ^= s << 5; return ((s >>> 0) % 1e6) / 1e6; }; })();
const logn = (median: number, spread: number) => { const z = Math.sqrt(-2 * Math.log(rnd() || 1e-6)) * Math.cos(2 * Math.PI * rnd()); return median * Math.exp(spread * z); };
const TITLES = ['Delivery: day axis in the viewer zone', 'fix: lease renewal after host sleep', 'Gateway: drain before snapshot swap', 'Lists: row menu on long press', 'fix: interface history fixture',
  'Spotlight: jump by entity code', 'Hosts: limits card projection', 'fix: queue flake in browser tests', 'Conversations: archived group', 'Docs: delivery health metrics', 'fix: resource replacement throughput', 'Results: copy state chips'];
const HIST: Hist[] = [];
{
  const ci = [20, 7.5, 10.4, 6.7, 2.4, 2.3, 7.8, 18, 4], q = [0, 0, 4.6, 5.2, 5.5, 6.6, 8.5, 9.2, 7], rel = [1850, 551, 2.8, 1.8, 0.9, 1, 0.9, 1.1, 1.2];
  let n = 612 - 9 - MERGED.reduce((a, b) => a + b, 0);
  MERGED.forEach((count, di) => {
    const start = D0 + di * DAY, span = di === MERGED.length - 1 ? Math.max(hr, NOW - start - 30 * min) : DAY;
    for (let k = 0; k < count; k++) {
      const merged = start + span * (k + 0.5) / count, outlier = di === 7 && k % 8 === 3;
      const d: Hist['d'] = { pr_ci: (outlier ? 690 : logn(ci[di], 0.9)) * min, release: logn(rel[di], 0.25) * min };
      if (di >= 2) d.queue = logn(q[di], 0.5) * min;
      if (di >= 6 || (di === 5 && merged >= REC)) { d.install = logn(1.1, 0.3) * min; d.total = d.pr_ci + d.queue! + d.release + d.install; }
      const removals = rnd() < 0.12 ? 1 + Math.floor(rnd() * 2) : 0;
      HIST.push({ n: ++n, title: TITLES[n % TITLES.length], merged, d, removals, flakes: removals && rnd() < 0.4 ? 1 : 0 });
    }
  });
}

/* ---------- Current pull requests: 9 in flight and the latest installed ---------- */
type Pr = { n: number; title: string; stage: Stage; since: number; opened: number; runs: number; removals: number; release?: string; author: string; repo?: string };
const INSTALLED: Pr[] = HIST.slice(-26).reverse().map((h, i) => ({ n: h.n, title: h.title, stage: 'installed', since: h.merged + (h.d.release + (h.d.install ?? 0)), opened: h.merged - h.d.pr_ci - (h.d.queue ?? 0), runs: 1 + h.removals, removals: h.removals, release: `1.261117.${30 - i}`, author: i % 3 ? 'agent' : 'kirs' }));
const PRS: Pr[] = [
  { n: 612, title: 'Delivery: pace by calendar day', stage: 'pr_failed', since: NOW - 25 * min, opened: NOW - 70 * min, runs: 0, removals: 0, author: 'agent' },
  { n: 611, title: 'Gateway: drain before snapshot swap', stage: 'mq_failed', since: NOW - 9 * min, opened: NOW - 48 * min, runs: 2, removals: 1, author: 'agent' },
  { n: 610, title: 'Version beside the person; one reliable update control', stage: 'mq_wait', since: NOW - 11 * min, opened: NOW - 95 * min, runs: 1, removals: 0, author: 'kirs' },
  { n: 609, title: 'Lists: row menu, selection mode, Pinned group', stage: 'pr_ci', since: NOW - 14 * min, opened: NOW - 16 * min, runs: 0, removals: 0, author: 'agent' },
  { n: 608, title: 'Hosts: limits card with projection', stage: 'pr_open', since: NOW - 2 * hr, opened: NOW - 5 * hr, runs: 1, removals: 1, author: 'agent' },
  { n: 607, title: 'fix: lease renewal after host sleep', stage: 'releasing', since: NOW - 3 * min, opened: NOW - 41 * min, runs: 1, removals: 0, author: 'agent' },
  { n: 606, title: 'Results: copy state chips', stage: 'installing', since: NOW - 4 * min, opened: NOW - 52 * min, runs: 1, removals: 0, release: '1.261117.31', author: 'kirs' },
  { n: 251, title: 'Catalogue: materials and direct entry', stage: 'pr_open', since: NOW - 2 * DAY, opened: NOW - 2 * DAY, runs: 0, removals: 0, author: 'kirs', repo: 'Amoenum/desiign' },
  { n: 250, title: 'Catalogue: frame step before the export', stage: 'pr_open', since: NOW - 8 * DAY, opened: NOW - 8 * DAY, runs: 0, removals: 0, author: 'kirs', repo: 'Amoenum/desiign' },
  ...INSTALLED,
];
/** Needs me: checks failed, or queue checks failed and the pull request left the merge queue (the stages only, OR-33). */
const needsMe = (p: Pr) => p.stage === 'pr_failed' || p.stage === 'mq_failed';
const PRESETS: Record<string, (p: Pr) => boolean> = { 'Needs me': needsMe, 'In flight': p => p.stage !== 'installed' && p.stage !== 'merged', Installed: p => p.stage === 'installed' };

/* ---------- The control column (all variants) ---------- */

function PrList({ sel, onSel, picked, onUnpick }: { sel?: number; onSel: (n: number) => void; picked: { label: string; rows: Pr[] } | null; onUnpick: () => void }) {
  const [preset, setPreset] = useState<string | null>(null);
  const [filter, setFilter] = useState<Stage[]>([]);
  const [closed, setClosed] = useState<Stage[]>(['installed']);
  const [group, setGroup] = useState<'stage' | 'project' | 'none'>('stage');
  const [sort, setSort] = useState('-since');
  const source = picked?.rows ?? PRS;
  const pre = preset ? source.filter(PRESETS[preset]) : source;
  const counts: Partial<Record<Stage, number>> = {};
  for (const s of ORDER) counts[s] = pre.filter(p => p.stage === s).length;
  const shown = (filter.length ? pre.filter(p => filter.includes(p.stage)) : pre).sort((a, b) => sort.startsWith('-') ? b.since - a.since : a.since - b.since);
  const row = (p: Pr) => <div key={p.n} role="row" class={`vl-row static${p.n === sel ? ' sel' : ''}`}>
    <span class="lead"><StateBadge family="delivery" state={p.stage} compact /></span>
    <span class="tt" style={{ flexWrap: 'wrap', rowGap: 0 }}><a class="title row-link" style={{ flex: '0 0 100%' }} href="#/delivery" onClick={e => { e.preventDefault(); onSel(p.n); }}>{p.title}</a>
      <span class="id">#{p.n}</span>{p.repo && <span class="id">{p.repo}</span>}{p.release && <span class="id">{p.release}</span>}</span>
    <span class="rz"><RowFacts>
      {p.removals > 0 && <Fact icon="rotate-ccw" text={String(p.removals)} tip={`${p.removals} removed from the queue`} />}
      <Fact tip={`${states.delivery[p.stage].label} for`}><AgeChip t={p.since} now={NOW} alertAfter={NORM[p.stage] && NORM[p.stage]![1] / 1000} alertLabel="past the stage norm (P90)" /></Fact>
    </RowFacts></span>
  </div>;
  const keys = group === 'stage' ? ORDER : group === 'project' ? [...new Set(shown.map(p => p.repo ?? 'taskqorg/fleet'))] : [];
  return <>
    <header class="page-h"><h1>Delivery</h1><span class="grow" /><Stale at={NOW - 2 * min} now={NOW} /></header>
    <section class="panel">
      <ListFrame bar={<FilterBar query="" onQuery={() => {}} placeholder="Search pull requests, state:mq_wait project:fleet" shown={shown.length} total={source.length}
        chips={picked ? [{ field: 'Merged', value: picked.label }] : []} onRemove={onUnpick}
        extra={<Presets system={Object.keys(PRESETS)} presets={[]} active={preset} onPick={n => { setPreset(preset === n ? null : n); setFilter([]); }} onSave={() => {}} onUnpin={() => {}} />}
        counters={<StateCounters family="delivery" label="Pull requests by stage" counts={counts} active={filter} onAll={() => setFilter([])} onToggle={s => setFilter(filter.includes(s) ? filter.filter(x => x !== s) : [...filter, s])} />}
        tools={<><SelectToggle on={false} onClick={() => {}} /><GroupControl value={group} options={[['stage', 'Stage'], ['project', 'Project'], ['none', 'None']] as const} onChange={setGroup} />
          <SortControl value={sort} fields={[['since', 'In stage since'], ['opened', 'Opened']]} onChange={setSort} /></>} />}>
        <div class="vl-wrap" role="grid" aria-label="Pull requests" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
          {group === 'none' ? shown.map(row) : keys.map(k => {
            const its = shown.filter(p => group === 'stage' ? p.stage === k : (p.repo ?? 'taskqorg/fleet') === k);
            if (!its.length) return null;
            const st = group === 'stage' ? k as Stage : null, open = !(st && closed.includes(st) && !picked);
            return <>
              <div class="vl-row static grp" key={'g' + k} role="row"><GroupHeader icon={st ? states.delivery[st].icon : 'folder-open'} label={st ? states.delivery[st].label : k} tone={st ? states.delivery[st].tone : 'neutral'}
                count={its.length} open={open} onToggle={() => st && setClosed(closed.includes(st) ? closed.filter(x => x !== st) : [...closed, st])} /></div>
              {open && its.map(row)}
            </>;
          })}
        </div>
      </ListFrame>
    </section>
  </>;
}

/* ---------- Pace data: buckets of the chosen range, per-bucket figures, the stage share ---------- */

type Range = '24h' | '7d' | '30d';
type Bucket = DayCol & { start: number; end: number; today: boolean };
function bucketsOf(range: Range): Bucket[] {
  if (range === '24h') {
    return Array.from({ length: 12 }, (_, i) => { const s = slotStart(NOW, 2, i - 11), e = slotStart(NOW, 2, i - 10); return { key: String(s), start: s, end: e, label: hourLabel(s), tip: `${day(s)} ${hourLabel(s)}–${hourLabel(e)}`, today: e > NOW }; });
  }
  // Calendar days in the viewer's zone, from the first recorded day when the range reaches past it.
  const n = range === '7d' ? 7 : 30, first = Math.max(dayStart(NOW, -(n - 1)), D0);
  const out: Bucket[] = [];
  for (let s = first; s <= NOW; s = dayStart(s, 1)) out.push({ key: String(s), start: s, end: dayStart(s, 1), label: day(s), tip: dayStart(NOW) === s ? `${day(s)}, today so far` : day(s), today: dayStart(NOW) === s });
  return out;
}
const q = quantile;
const inB = (b: { start: number; end: number }) => HIST.filter(h => h.merged >= b.start && h.merged < b.end);
// As the app: due a measure when it has one or was released before recording began (the fixture installs all it measures).
const due = (hs: Hist[]) => hs.filter(h => h.d.total !== undefined || h.merged < REC).length;
/** Open to installed per bucket: P50 and P90 of the pull requests with every stage measured; none measured is "not measured", never 0. */
function totalMark(b: Bucket): DayMark {
  const all = inB(b), xs = all.map(h => h.d.total).filter((v): v is number => v !== undefined);
  if (!all.length) return { v: null, missing: 'nothing merged' };
  if (!xs.length) return { v: null, missing: all.some(h => h.merged < REC) ? `install times recorded from ${day(REC)} ${hourLabel(REC)}` : 'not installed yet' };
  return { v: q(xs, 0.5), hi: q(xs, 0.9), some: xs.length < due(all) ? `${xs.length} of ${due(all)} measured` : undefined, partial: b.today ? 'so far' : undefined };
}
const mergedMark = (b: Bucket): DayMark => ({ v: inB(b).length, partial: b.today ? 'so far' : undefined });
const STAGES: { label: string; key: 'pr_ci' | 'queue' | 'release' | 'install'; tone: SharePart['tone'] }[] = [
  { label: 'PR checks', key: 'pr_ci', tone: 'd1' }, { label: 'Queue', key: 'queue', tone: 'd2' }, { label: 'Release', key: 'release', tone: 'd3' }, { label: 'Install', key: 'install', tone: 'd4' }];

function usePace(range: Range, at: string | null) {
  const bs = bucketsOf(range), from = bs[0].start, prs = HIST.filter(h => h.merged >= from);
  const totals = prs.map(h => h.d.total).filter((v): v is number => v !== undefined);
  const picked = at ? bs.find(b => b.key === at) ?? null : null;
  // Where the time goes: the mean of each stage over the pull requests with all four measured, so the parts add up to their mean open-to-installed.
  const scope = (picked ? inB(picked) : prs), done = scope.filter(h => h.d.total !== undefined);
  const mean = (k: typeof STAGES[number]['key']) => done.reduce((s, h) => s + (h.d[k] ?? 0), 0) / (done.length || 1);
  const share: SharePart[] = STAGES.map(s => ({ label: s.label, v: mean(s.key), tone: s.tone, tip: `${s.label}: mean ${fmt(mean(s.key))}, P50 ${fmt(q(done.map(h => h.d[s.key]!), 0.5) ?? 0)}` }));
  return {
    bs, prs, picked, share, done, scope,
    total: bs.map(totalMark), merged: bs.map(mergedMark),
    // Scale tops at about P95 of the drawn figures (capOf leaves out a figure over four times their median).
    capTotal: capOf(bs.flatMap(b => { const m = totalMark(b); return [m.v, m.hi]; }), { floor: 10 * min, unit: min }), capMerged: capOf(bs.map(b => inB(b).length), { floor: 4, fence: Infinity }),
    p50: q(totals, 0.5), p90: q(totals, 0.9), removals: prs.reduce((s, h) => s + h.removals, 0), flakes: prs.reduce((s, h) => s + h.flakes, 0),
    days: Math.max(1, (NOW - from) / DAY),
  };
}
type PaceData = ReturnType<typeof usePace>;

const sec = (v: number | null) => ({ value: v === null ? null : v / 1000, basis: v === null ? 'unavailable' as const : 'measured' as const });
function PaceTotals({ d, range }: { d: PaceData; range: Range }) {
  return <Totals items={[
    { label: 'To installed P50', m: sec(d.p50), kind: 'duration', note: `baseline ${fmt(BASE_TOTAL[0])}` },
    { label: 'To installed P90', m: sec(d.p90), kind: 'duration', note: `baseline ${fmt(BASE_TOTAL[1])}` },
    { label: 'Merged', m: { value: d.prs.length, basis: 'measured' }, kind: 'count', note: range === '24h' ? undefined : `${Math.round(d.prs.length / d.days)} a day` },
    { label: 'Queue removals', m: { value: d.removals, basis: 'measured' }, kind: 'count', note: d.flakes ? `${d.flakes} flaky` : undefined },
  ]} />;
}
const RANGES = [{ id: '24h', label: '24 h' }, { id: '7d', label: '7 days' }, { id: '30d', label: '30 days' }];
function Where({ d }: { d: PaceData }) {
  return <ShareBar title="Where the time goes" label="Where the time goes" parts={d.share} value={fmt}
    note={`mean per pull request, ${d.done.length} of ${d.scope.length} with every stage measured${d.picked ? ` · ${d.picked.tip}` : ''}`} />;
}

/* ---------- Moving now: each pull request in flight against its stage's norm ---------- */

function Moving({ compact }: { compact?: boolean }) {
  const moving = ORDER.flatMap(s => PRS.filter(p => p.stage === s && s !== 'installed' && s !== 'merged'));
  return <div class="vl-wrap" role="grid" aria-label="Pull requests in flight" style={{ '--cols': compact ? '1.25rem 3rem minmax(0,1fr) 2.75rem' : '1.25rem 3.25rem minmax(0,1fr) minmax(0,1.3fr) 2.75rem 4.5rem' }}>
    {moving.map(p => {
      const nm = NORM[p.stage], el = NOW - p.since, over = late(p);
      const bar = <NormBar elapsed={el} p50={nm?.[0]} p90={nm?.[1]} label={`${states.delivery[p.stage].label} ${pr(p.n)}`} value={fmt} />;
      return <div key={p.n} class="vl-row static" role="row">
        <span class="lead"><StateBadge family="delivery" state={p.stage} compact /></span>
        <a class="mono row-link" href="#/delivery">{pr(p.n)}</a>
        {compact ? bar : <span class="muted" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{p.title}</span>}
        {!compact && bar}
        <span class="r"><AgeChip t={p.since} now={NOW} alertAfter={nm && nm[1] / 1000} alertLabel="past the stage norm (P90)" /></span>
        {!compact && <span class={`r mono type-sm ${over ? 't-danger' : 'muted'}`} style={over ? { color: 'var(--tone-fg)' } : undefined} data-tip={nm ? `${states.delivery[p.stage].label}: P50 ${fmt(nm[0])}, P90 ${fmt(nm[1])} over 14 days` : undefined}>
          {nm ? `P90 ${fmt(nm[1])}` : 'no norm'}</span>}
      </div>;
    })}
  </div>;
}

const look = [
  <ProblemRow key="r" severity="warning" title="Merge queue P90 is 26m, baseline 16m" facts={`Since ${pr(598)} · 7 runs · 3 removals for flaky browser checks`} actions={[{ label: 'Compare', primary: true }]} />,
  <ProblemRow key="f" severity="failure" title={`${pr(612)} checks failed`} facts="Delivery: pace by calendar day · 25m ago" actions={[{ label: `Open ${pr(612)}`, primary: true }]} />,
  <ProblemRow key="q" severity="failure" title={`${pr(611)} removed from the merge queue`} facts="browser-tests (1) failed · removed once before" actions={[{ label: `Open ${pr(611)}`, primary: true }]} />,
  <ProblemRow key="w" severity="warning" title={`${pr(610)} waiting in queue for 11m, norm 8m`} facts="Version beside the person; one reliable update control" actions={[{ label: `Open ${pr(610)}`, primary: true }]} />,
];

const Gap = ({ children }: { children: ComponentChildren }) => <div style={{ display: 'grid', gap: 'var(--space-4)', minWidth: 0 }}>{children}</div>;
function RangeLine(p: PaceProps) {
  const from = bucketsOf(p.range)[0].start;
  return <div class="state-line"><Choice label="Range" options={RANGES} value={p.range} onChange={r => p.onRange(r as Range)} />
    {p.range === '30d' && from === D0 && <span class="muted type-sm">recorded from {day(D0)}</span>}</div>;
}
type PaceProps = { range: Range; onRange: (r: Range) => void; at: string | null; onAt: (k: string | null) => void };
const pickOf = (p: PaceProps) => (k: string) => p.onAt(p.at === k ? null : k);

/** A: what needs a look, what moves now, then the pace as small charts stacked on one day axis. */
function IdleA(p: PaceProps) {
  const d = usePace(p.range, p.at), pick = pickOf(p);
  return <div class="stack-pad">
    <DetailSection title="Needs a look"><Latest items={look} n={3} /></DetailSection>
    <DetailSection title="Moving now"><Moving /></DetailSection>
    <DetailSection title="Pace"><Gap>
      <RangeLine {...p} />
      <PaceTotals d={d} range={p.range} />
      <DayChart title="Open to installed" label="Open to installed per day, P50 and P90" kind="range" cols={d.bs} marks={d.total} value={fmt} cap={d.capTotal}
        capNote="about P95 of the figures shown; a figure over four times their median is drawn cut, with its value" names={['P50', 'P90']} values
        alertAbove={BASE_TOTAL[1]} alertLabel={`above the 14-day P90 ${fmt(BASE_TOTAL[1])}`} selected={p.at} onSelect={pick} />
      <DayChart title="Merged" label="Pull requests merged per day" kind="bar" size="sm" cols={d.bs} marks={d.merged} value={v => String(Math.round(v))} cap={d.capMerged} tone="d2" axis selected={p.at} onSelect={pick} />
      <Where d={d} />
    </Gap></DetailSection>
  </div>;
}

/** The opened pull request (all variants): stages on one time axis, queue runs, release and installs per host. */
function PrDetail({ p, onClose }: { p: Pr; onClose: () => void }) {
  const t0 = p.opened, stages: [Stage, number, number | null][] = [['pr_ci', t0 + 2 * min, t0 + 14 * min], ['mq_wait', t0 + 14 * min, t0 + 30 * min], ['mq_ci', t0 + 30 * min, t0 + 52 * min], ['releasing', t0 + 52 * min, t0 + 58 * min], ['installing', t0 + 58 * min, null]];
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={p.title}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><span class="muted mono">{p.repo ?? 'taskqorg/fleet'} #{p.n}</span><span class="grow" /><Button label="Open on GitHub" icon="external-link" iconOnly /></header>
    <h2 class="detail-title">{p.title}</h2>
    <div class="state-line"><StateBadge family="delivery" state={p.stage} /><span class="muted">by {p.author}</span><AgeChip t={p.opened} now={NOW} /></div>
    <div class="stack-pad">
      <Totals items={[{ label: 'Lead so far', m: { value: (NOW - p.opened) / 1000, basis: 'measured' }, kind: 'duration' }, { label: 'Queue runs', m: { value: p.runs, basis: 'measured' }, kind: 'count' }, { label: 'Removed', m: { value: p.removals, basis: 'measured' }, kind: 'count' }]} />
      <DetailSection title="Stages">{stages.map(([s, a, b]) => <KvRow key={s} icon={states.delivery[s].icon} label={states.delivery[s].label}
        value={<span style={{ display: 'inline-flex', gap: 'var(--space-2)', alignItems: 'center' }}><TimeStrip from={t0} to={NOW} start={a} end={b && Math.min(b, NOW)} now={NOW} state={b && b < NOW ? 'succeeded' : 'running'} label={states.delivery[s].label} />
          <Num m={{ value: ((b && b < NOW ? b : NOW) - a) / 1000, basis: 'measured' }} kind="duration" /></span>} />)}</DetailSection>
    </div>
  </article>;
}

export function Delivery() {
  const [sel, setSel] = useState<number | undefined>(undefined);
  const [at, setAt] = useState<string | null>(null);
  const [range, setRange] = useState<Range>('7d');
  const p = PRS.find(x => x.n === sel);
  const b = at ? bucketsOf(range).find(x => x.key === at) : undefined;
  const picked = b ? { label: b.tip ?? b.label, rows: inB(b).map(h => PRS.find(x => x.n === h.n) ?? { n: h.n, title: h.title, stage: 'installed' as Stage, since: h.merged + h.d.release + (h.d.install ?? 0), opened: h.merged - h.d.pr_ci, runs: 1 + h.removals, removals: h.removals, author: 'agent' }) } : null;
  return <ListDetail list={<PrList sel={sel} onSel={setSel} picked={picked} onUnpick={() => setAt(null)} />} detailKey={sel}
    idle={<IdleA range={range} onRange={r => { setRange(r); setAt(null); }} at={at} onAt={setAt} />} detail={p && <PrDetail p={p} onClose={() => setSel(undefined)} />} />;
}
