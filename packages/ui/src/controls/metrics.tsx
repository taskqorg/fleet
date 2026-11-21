// Operational metrics, fill bars, stat strips, execution trees, outcome blocks, step/flow strips, and charts.
import type { ComponentChildren } from 'preact';
import { useState } from 'preact/hooks';
import { type IconName, type StateOf, type Tone, type Harness, type StepKind, actions, states, stepKinds } from '../registry.ts';
import { type Measured, type Kind, type CostFigureLike, bytes, exact, stamp, percent } from '../format.ts';
import { Icon, StateBadge, Num, CostFigures, Ago, HarnessCounts } from './primitives.tsx';

/* ---------- Fill bar (W9–W11) ---------- */

type FillProps = { total: number | null; segments: { label: string; bytes: number }[]; warnAt?: number; stale?: number; now?: number;
  /** Units of total and segments: bytes (stores) or percent (quota windows). */
  unit?: 'bytes' | 'percent' };
export function FillBar(p: FillProps) {
  if (p.total === null) return <span class="fill-wrap" data-ui="fill-bar"><span class="fill" role="img" aria-label="unavailable" /><span class="legend muted">— unavailable</span></span>;
  const used = p.segments.reduce((s, x) => s + x.bytes, 0), free = p.total - used;
  const state = free <= 0 ? 'full' : p.warnAt !== undefined && free < p.warnAt ? 'warn' : '';
  const legend = p.segments.map(s => `${s.label} ${Math.round(s.bytes / p.total! * 100)}%`).join(' · ');
  const amount = (n: number) => p.unit === 'percent' ? percent(n, p.total!).text : bytes(n).text;
  const freeText = free <= 0 ? 'full' : `${amount(free)} ${p.unit === 'percent' ? 'left' : 'free'}${state ? `, warning at ${amount(p.warnAt!)}` : ''}`;
  return (
    <span class="fill-wrap" data-ui="fill-bar">
      <span class={`fill ${state}${p.stale !== undefined ? ' stale' : ''}`} role="img" aria-label={`${legend}; ${freeText}`}>
        {p.segments.map((s, i) => <i key={s.label} class={`d${i % 4 + 1}`} style={{ width: `${s.bytes / p.total! * 100}%` }} data-tip={`${s.label} ${p.unit === 'percent' ? percent(s.bytes, p.total!).text : bytes(s.bytes).text}`} />)}
        {p.warnAt !== undefined && <b class="mark" style={{ left: `${(1 - p.warnAt / p.total) * 100}%` }} />}
      </span>
      <span class={`legend${state ? ` t-${state === 'full' ? 'danger' : 'attention'}` : ''}`} data-important={state ? '' : undefined}>{state ? `${freeText} · ${legend}` : `${legend} · ${freeText}`}{p.stale !== undefined && <> · <Ago t={p.stale} /> old</>}</span>
    </span>
  );
}

/* ---------- Execution tree (§ execution-tree.md) ---------- */

export type TreeNode = {
  id: string; name: string; state: StateOf<'execution'>; lastSeen?: number; note?: string;
  time?: Measured; tokens?: Measured; cost?: Measured; costFigures?: readonly CostFigureLike[]; children?: TreeNode[];
};
export function ExecTree({ nodes, now, label, selectedId, onSelect }: { nodes: TreeNode[]; now: number; label: string; selectedId?: string | null; onSelect?: (id: string) => void }) {
  const openByDefault = (n: TreeNode): boolean => n.state !== 'succeeded' && n.state !== 'cancelled' || !!n.children?.some(openByDefault);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [selState, setSelState] = useState<string | null>(null);
  const sel = selectedId !== undefined ? selectedId : selState;
  const row = (n: TreeNode, level: number): ComponentChildren => {
    const isOpen = open[n.id] ?? openByDefault(n);
    return (
      <li key={n.id} role="treeitem" aria-level={level} aria-expanded={n.children?.length ? isOpen : undefined} aria-selected={sel === n.id}>
        <div class="ti" onClick={() => { setSelState(n.id); onSelect?.(n.id); }}>
          {n.children?.length ? <button type="button" class="tw" aria-label={isOpen ? 'Collapse' : 'Expand'} onClick={e => { e.stopPropagation(); setOpen({ ...open, [n.id]: !isOpen }); }}>
            <span class={isOpen ? 'chev open' : 'chev'}><Icon name={actions.expand} /></span></button> : <span class="tw" />}
          <StateBadge family="execution" state={n.state} lastSeen={n.lastSeen} now={now} compact />
          <span class="nm">{n.name}{n.note && <span class="muted"> · {n.note}</span>}</span>
          <span class="m">{n.time && <Num m={n.time} kind="duration" />}{n.tokens && <Num m={n.tokens} kind="count" />}{n.costFigures?.length ? <CostFigures cost={n.costFigures} /> : n.cost && <Num m={n.cost} kind="money" />}</span>
        </div>
        {n.children?.length && isOpen ? <ul role="group">{n.children.map(c => row(c, level + 1))}</ul> : null}
      </li>
    );
  };
  return <ul class="tree" data-ui="execution-tree" role="tree" aria-label={label}>{nodes.map(n => row(n, 1))}</ul>;
}

/* ---------- Stat strip: operational counts (stat-strip.md) ---------- */

export type Stat = { label: string; icon: IconName; value: Measured; kind?: Kind; of?: number; tone?: Tone; href?: string; harness?: Partial<Record<Harness, number>> };
export function StatStrip({ stats, label }: { stats: Stat[]; label: string }) {
  return (
    <div class="stats" data-ui="stat-strip" role="list" aria-label={label}>
      {stats.map(s => {
        const body = <><Icon name={s.icon} /><span class="sv"><Num m={s.value} kind={s.kind ?? 'count'} />{s.of !== undefined && <span class="of">/{s.of}</span>}</span><span class="sl">{s.label}</span>{s.harness && <HarnessCounts counts={s.harness} label={`${s.label} by harness`} />}</>;
        return <span role="listitem" key={s.label} class={`stat${s.tone ? ` t-${s.tone} toned` : ''}`}>{s.href ? <a href={s.href}>{body}</a> : body}</span>;
      })}
    </div>
  );
}

/* ---------- Outcome block (D11, B2) ---------- */

export function OutcomeBlock({ outcome, requirements, inputs, depends }: {
  outcome: string; requirements: { text: string; met?: boolean }[]; inputs: { icon: IconName; label: string }[]; depends?: ComponentChildren;
}) {
  const [open, setOpen] = useState(false);
  const hasChecks = requirements.length > 0 && requirements.every(r => r.met !== undefined);
  const met = requirements.filter(r => r.met).length;
  return <div class="outcome" data-ui="outcome-block">
    {outcome && <button type="button" class="tw" aria-expanded={open} aria-label={open ? 'Collapse outcome' : 'Expand outcome'} onClick={() => setOpen(!open)}><span class={open ? 'chev open' : 'chev'}><Icon name="chevron-down" /></span></button>}
    <div class="outcome-b">
      {outcome && <p class={open ? '' : 'clamp'}>{outcome}</p>}
      {(hasChecks || inputs.length > 0) && <div class="outcome-meta">{hasChecks && <span class={`chip-age${met < requirements.length ? '' : ' t-success alert'}`} aria-label={`${met} of ${requirements.length} requirements met`}>{met}/{requirements.length}</span>}
        {inputs.map(i => <span key={i.label} class="chip in"><Icon name={i.icon} />{i.label}</span>)}</div>}
      {requirements.length > 0 && <ul class="reqs">{requirements.map(r => <li key={r.text}>
        {r.met !== undefined && <Icon name={r.met ? 'circle-check' : 'square'} />}{r.text}
      </li>)}</ul>}
      {depends && <div class="muted">{depends}</div>}
    </div>
  </div>;
}

/* ---------- Step list and flow strip: a pipeline's steps, with a run's state on top (controls/step-list.md) ---------- */

export type StepItem =
  | { band: 'loop' | 'parallel'; label: string; inset?: boolean }
  | { id: string; kind: StepKind; executor: ComponentChildren; reads: string; writes: string; inset?: boolean; condition?: string; result?: boolean;
      activation?: StateOf<'activation'>; activationReason?: string;
      /** The step in one run: shown in place of what it reads and writes. */
      run?: { state: StateOf<'execution'>; reason?: string; note?: string; time: Measured; cost: Measured; error?: string } };

/** The one place a pipeline is edited (D20). `children` is the editor of the open step, shown under its row (OR-14). */
export function StepList({ items, open, onOpen, label, children }: { items: StepItem[]; open?: string; onOpen?: (id: string) => void; label: string; children?: ComponentChildren }) {
  let n = 0;
  return <div class="steps" data-ui="step-list" role="list" aria-label={label}>{items.map((s, i) => {
    if ('band' in s) return <div key={i} class={`step-band${s.inset ? ' inset' : ''}`}><Icon name={s.band === 'loop' ? 'rotate-cw' : 'rows-3'} /><span>{s.label}</span></div>;
    n++;
    const k = stepKinds[s.kind];
    return <div key={s.id} role="listitem" class={`step-item${s.inset ? ' inset' : ''}`}>
      <button type="button" class={`step${open === s.id ? ' open' : ''}`} aria-expanded={onOpen ? open === s.id : undefined} onClick={() => onOpen?.(s.id)}>
        <span class="no">{String(n).padStart(2, '0')}</span>
        {s.run ? <StateBadge family="execution" state={s.run.state} reason={s.run.reason} compact /> : s.activation ? <StateBadge family="activation" state={s.activation} reason={s.activationReason} compact /> : <span class="lead" data-tip={k.label}><Icon name={k.icon} label={k.label} /></span>}
        <b class="nm">{s.id}</b><span class="ex">{s.executor}{s.result && ' · result'}</span>
        {s.run ? <span class="svc">{s.run.note && <span class="chip-age">{s.run.note}</span>}<Num m={s.run.time} kind="duration" /><Num m={s.run.cost} kind="money" /></span>
          : <span class="io">{s.activation ? <>{states.activation[s.activation].label}{s.activationReason && ` · ${s.activationReason}`}</> : <>{s.condition && <>when {s.condition} · </>}reads {s.reads} · writes <span class="w">{s.writes}</span></>}</span>}
      </button>
      {s.run?.error && <div class="step-error" data-important="">{s.run.error}</div>}
      {open === s.id && children && <div class="step-edit">{children}</div>}
    </div>;
  })}</div>;
}

export type FlowNode =
  | { id: string; kind: StepKind; state?: StateOf<'execution'>; current?: boolean; optional?: boolean }
  | { loop: string; nodes: FlowNode[] } | { parallel: FlowNode[] };

/** A read-only picture of the steps: wires for order, a bracket for parallel steps, a dashed frame for a loop. */
export function FlowStrip({ nodes, label }: { nodes: FlowNode[]; label: string }) {
  const row = (ns: FlowNode[]): ComponentChildren => ns.map((n, i) => <>{i > 0 && <span class="flow-w" />}{one(n, i)}</>);
  const one = (n: FlowNode, i: number): ComponentChildren =>
    'loop' in n ? <span key={i} class="flow-loop"><span class="flow-cap"><Icon name="rotate-cw" />{n.loop}</span>{row(n.nodes)}</span>
    : 'parallel' in n ? <span key={i} class="flow-par">{n.parallel.map(one)}</span>
    : <span key={n.id} class={`flow-n${n.current ? ' cur' : ''}${n.optional ? ' opt' : ''}`}>{n.state ? <StateBadge family="execution" state={n.state} compact /> : <Icon name={stepKinds[n.kind].icon} />}{n.id}</span>;
  return <div class="flow" data-ui="flow-strip" role="img" aria-label={label}>{row(nodes)}</div>;
}

/* ---------- Area chart (controls/area-chart.md): recorded and projected lines over a time range, CSS only (OR-8) ---------- */

export type ChartPoint = { at: number; v: number; end?: number };
export type ChartBucket = { key: string; label: string; tip?: string; valueLabel?: string };
/** pattern: stripes over the tone, for a fifth and later series (tokens.md: never new hues). */
export type ChartSeries = { label: string; points: ChartPoint[]; tone: 'd1' | 'd2' | 'd3' | 'd4'; projected?: boolean; step?: boolean; pattern?: boolean };
type AreaChartProps = { label: string; series: ChartSeries[]; from: number; to: number; now: number; max: number;
  /** Layers on top of each other, first series at the bottom (points share their times); with a legend of last values. */
  stacked?: boolean;
  /** Text of a value for the summary and the axis, e.g. `40%` or `12/h`. */
  value: (v: number) => string;
  /** Interactive bucket selectors rendered outside role="img" (CA-09). */
  buckets?: readonly ChartBucket[];
  selectedBucket?: string | null;
  onSelectBucket?: (key: string) => void };
export function AreaChart(p: AreaChartProps) {
  // Stacked: each layer is drawn as the sum of itself and the layers under it, top layer first, all opaque.
  const drawn = p.stacked ? p.series.map((s, i) => ({ ...s, points: s.points.map((q, j) => ({ at: q.at, v: p.series.slice(0, i + 1).reduce((n, t) => n + (t.points[j]?.v ?? 0), 0) })) })).reverse() : p.series;
  const span = Math.max(1, p.to - p.from);
  const x = (t: number) => Math.min(100, Math.max(0, (t - p.from) / span * 100));
  const y = (v: number) => 100 - Math.min(100, Math.max(0, v / (p.max || 1) * 100));
  const shape = (s: ChartSeries) => {
    const pts = s.points.filter(q => q.at <= p.to).map(q => ({ at: Math.max(q.at, p.from), end: q.end != null ? Math.min(q.end, p.to) : undefined, v: q.v }));
    if (pts.length === 0) return '';
    const xy: string[] = [];
    pts.forEach((q, i) => {
      if (s.step && i > 0) {
        const pe = pts[i - 1].end;
        if (pe && pe < q.at) xy.push(`${x(pe)}% ${y(pts[i - 1].v)}%`, `${x(pe)}% 100%`, `${x(q.at)}% 100%`);
        else xy.push(`${x(q.at)}% ${y(pts[i - 1].v)}%`);
      }
      xy.push(`${x(q.at)}% ${y(q.v)}%`);
    });
    const lastPt = pts[pts.length - 1]!;
    const endX = x(lastPt.end ?? (pts.length === 1 ? Math.min(p.to, lastPt.at + span / 24) : lastPt.at));
    if (endX > x(lastPt.at)) xy.push(`${endX}% ${y(lastPt.v)}%`);
    return `polygon(${x(pts[0].at)}% 100%, ${xy.join(', ')}, ${endX}% 100%)`;
  };
  const last = (s: ChartSeries) => s.points.length ? p.value(s.points[s.points.length - 1].v) : 'none';
  const summary = p.series.map(s => `${s.label}${s.projected && !s.label.includes('projected') ? ' projected' : ''}: ${last(s)}`).join('; ');
  const days = (from: number, to: number) => { const out: number[] = []; const d = new Date(from); d.setHours(24, 0, 0, 0); for (let t = d.getTime(); t < to && out.length < 31; t += 864e5) out.push(t); return out; };
  return <div data-ui="area-chart" class="area-chart">
    <div class="ac-plot" role="img" aria-label={`${p.label}: ${summary}`}>
      {drawn.filter(s => shape(s)).map(s => <i key={s.label + (s.projected ? '-p' : '')} class={`ac-area ${s.tone}${s.projected ? ' projected' : ''}${p.stacked ? ' stacked' : ''}${s.pattern ? ' pattern' : ''}`} style={{ clipPath: shape(s) }} />)}
      {days(p.from, p.to).map(t => <b key={t} class="ac-day" style={{ left: `${x(t)}%` }} />)}
      {p.now >= p.from && p.now <= p.to && <b class="ac-now" style={{ left: `${x(p.now)}%` }} data-tip={exact(p.now)} />}
      <span class="ac-max">{p.value(p.max)}</span>
    </div>
    <div class="ac-axis"><time>{stamp(p.from, p.now)}</time><span class="grow" /><time>{stamp(p.to, p.now)}</time></div>
    {p.buckets?.length ? <div class="ac-buckets" role="group" aria-label={`${p.label} buckets`}>
      {p.buckets.map(b => <button key={b.key} type="button" class={`ac-bucket${p.selectedBucket === b.key ? ' on' : ''}`} aria-pressed={p.selectedBucket === b.key} data-tip={b.tip ?? (b.valueLabel ? `${b.label}: ${b.valueLabel}` : b.label)} onClick={() => p.onSelectBucket?.(b.key)}>{b.label}</button>)}
    </div> : null}
    {p.stacked && <ul class="ac-legend" aria-hidden="true">{p.series.map(s => <li key={s.label}><i class={`ac-key ${s.tone}${s.pattern ? ' pattern' : ''}`} />{s.label} <span class="num">{last(s)}</span></li>)}</ul>}
  </div>;
}

/* ---------- Time strip: where a run sits in a time window (Runners), toned by its state ---------- */

/** One run as a bar from its start to its end (to now while it runs) inside [from, to]; a run older than the window starts at its left edge. */
/** A part of a lane: one stage of a pull request, with its own tone and name (the name is in the strip's label too). */
export type StripPart = { start: number; end: number | null; tone: Tone; label: string };
/** One bar (a run: start..end in its execution state) or several consecutive parts in one lane (stages). */
type TimeStripProps = { from: number; to: number; now: number; label: string } &
  ({ start: number; end: number | null; state: StateOf<'execution'>; parts?: never } | { parts: StripPart[]; start?: never; end?: never; state?: never });
export function TimeStrip({ from, to, start, end, now, state, label, parts }: TimeStripProps) {
  const x = (t: number) => Math.min(100, Math.max(0, (t - from) / (to - from) * 100));
  const bar = (s: number, e: number | null, tone: Tone, tip?: string, key?: number) => { const a = x(s), b = x(e ?? now);
    return <i key={key} class={`t-${tone}${e === null ? ' live' : ''}`} data-tip={tip} style={{ left: `${a}%`, width: `max(2px, ${b - a}%)` }} />; };
  return <span class="tstrip" data-ui="time-strip" role="img" aria-label={parts ? `${label}: ${parts.map(p => p.label).join(', ')}` : label}>
    {parts ? parts.map((p, i) => bar(p.start, p.end, p.tone, p.label, i)) : bar(start!, end ?? null, states.execution[state!].tone)}</span>;
}
