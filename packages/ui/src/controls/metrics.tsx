// Operational metrics, fill bars, stat strips, execution trees, outcome blocks, step/flow strips, and charts.
import { createMemo, createSignal, For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { type IconName, type StateOf, type Tone, type Harness, type StepKind, actions, states, stepKinds } from '../registry.ts';
import { type Measured, type Kind, type CostFigureLike, bytes, exact, stamp, percent } from '../format.ts';
import { Icon, StateBadge, Num, CostFigures, Ago, HarnessCounts } from './primitives.tsx';

/* ---------- Fill bar (W9–W11) ---------- */

type FillProps = {
  total: number | null; segments: { label: string; bytes: number }[]; warnAt?: number; stale?: number; now?: number;
  /** Units of total and segments: bytes (stores) or percent (quota windows). */
  unit?: 'bytes' | 'percent';
};
export function FillBar(p: FillProps) {
  return (
    <Show
      when={p.total !== null}
      fallback={
        <span class="fill-wrap" data-ui="fill-bar">
          <span class="fill" role="img" aria-label="unavailable" />
          <span class="legend muted">— unavailable</span>
        </span>
      }
    >
      {(() => {
        const total = () => p.total!;
        const used = createMemo(() => p.segments.reduce((s, x) => s + x.bytes, 0));
        const free = createMemo(() => total() - used());
        const state = createMemo(() => free() <= 0 ? 'full' : p.warnAt !== undefined && free() < p.warnAt ? 'warn' : '');
        const legend = createMemo(() => p.segments.map(s => `${s.label} ${Math.round(s.bytes / total() * 100)}%`).join(' · '));
        const amount = (n: number) => p.unit === 'percent' ? percent(n, total()).text : bytes(n).text;
        const freeText = createMemo(() => free() <= 0 ? 'full' : `${amount(free())} ${p.unit === 'percent' ? 'left' : 'free'}${state() ? `, warning at ${amount(p.warnAt!)}` : ''}`);
        return (
          <span class="fill-wrap" data-ui="fill-bar">
            <span class={['fill', state(), { stale: p.stale !== undefined }]} role="img" aria-label={`${legend()}; ${freeText()}`}>
              <For each={p.segments}>
                {(s, i) => (
                  <i
                    class={`d${i() % 4 + 1}`}
                    style={{ width: `${s.bytes / total() * 100}%` }}
                    data-tip={`${s.label} ${p.unit === 'percent' ? percent(s.bytes, total()).text : bytes(s.bytes).text}`}
                  />
                )}
              </For>
              <Show when={p.warnAt !== undefined}>
                <b class="mark" style={{ left: `${(1 - p.warnAt! / total()) * 100}%` }} />
              </Show>
            </span>
            <span class={['legend', state() ? `t-${state() === 'full' ? 'danger' : 'attention'}` : '']} data-important={state() ? '' : undefined}>
              {state() ? `${freeText()} · ${legend()}` : `${legend()} · ${freeText()}`}
              <Show when={p.stale !== undefined}> <> · <Ago t={p.stale!} /> old</></Show>
            </span>
          </span>
        );
      })()}
    </Show>
  );
}

/* ---------- Execution tree (§ execution-tree.md) ---------- */

export type TreeNode = {
  id: string; name: string; state: StateOf<'execution'>; lastSeen?: number; note?: string;
  time?: Measured; tokens?: Measured; cost?: Measured; costFigures?: readonly CostFigureLike[]; children?: TreeNode[];
};
export function ExecTree(p: { nodes: TreeNode[]; now: number; label: string; selectedId?: string | null; onSelect?: (id: string) => void }) {
  const openByDefault = (n: TreeNode): boolean => n.state !== 'succeeded' && n.state !== 'cancelled' || !!n.children?.some(openByDefault);
  const [open, setOpen] = createSignal<Record<string, boolean>>({});
  const [selState, setSelState] = createSignal<string | null>(null);
  const sel = createMemo(() => p.selectedId !== undefined ? p.selectedId : selState());

  const renderNode = (n: TreeNode, level: number): JSX.Element => {
    const isOpen = createMemo(() => open()[n.id] ?? openByDefault(n));
    return (
      <li role="treeitem" aria-level={level} aria-expanded={n.children?.length ? (isOpen() ? 'true' : 'false') : undefined} aria-selected={sel() === n.id ? 'true' : 'false'}>
        <div class="ti" onClick={() => { setSelState(n.id); p.onSelect?.(n.id); }}>
          <Show when={n.children?.length} fallback={<span class="tw" />}>
            <button
              type="button"
              class="tw"
              aria-label={isOpen() ? 'Collapse' : 'Expand'}
              onClick={e => { e.stopPropagation(); setOpen(prev => ({ ...prev, [n.id]: !isOpen() })); }}
            >
              <span class={['chev', { open: isOpen() }]}><Icon name={actions.expand} /></span>
            </button>
          </Show>
          <StateBadge family="execution" state={n.state} lastSeen={n.lastSeen} now={p.now} compact />
          <span class="nm">{n.name}<Show when={n.note} keyed>{note => <span class="muted"> · {note}</span>}</Show></span>
          <span class="m">
            <Show when={n.time} keyed>{tm => <Num m={tm} kind="duration" />}</Show>
            <Show when={n.tokens} keyed>{tk => <Num m={tk} kind="count" />}</Show>
            <Show
              when={n.costFigures?.length}
              fallback={<Show when={n.cost} keyed>{c => <Num m={c} kind="money" />}</Show>}
            >
              <CostFigures cost={n.costFigures} />
            </Show>
          </span>
        </div>
        <Show when={n.children?.length && isOpen()}>
          <ul role="group">
            <For each={n.children ?? []}>{c => renderNode(c, level + 1)}</For>
          </ul>
        </Show>
      </li>
    );
  };

  return (
    <ul class="tree" data-ui="execution-tree" role="tree" aria-label={p.label}>
      <For each={p.nodes}>{n => renderNode(n, 1)}</For>
    </ul>
  );
}

/* ---------- Stat strip: operational counts (stat-strip.md) ---------- */

export type Stat = { label: string; icon: IconName; value: Measured; kind?: Kind; of?: number; tone?: Tone; href?: string; harness?: Partial<Record<Harness, number>> };
export function StatStrip(p: { stats: Stat[]; label: string }) {
  return (
    <div class="stats" data-ui="stat-strip" role="list" aria-label={p.label}>
      <For each={p.stats}>
        {s => {
          const body = () => (
            <>
              <Icon name={s.icon} />
              <span class="sv">
                <Num m={s.value} kind={s.kind ?? 'count'} />
                <Show when={s.of !== undefined}><span class="of">/{s.of}</span></Show>
              </span>
              <span class="sl">{s.label}</span>
              <Show when={s.harness} keyed>{h => <HarnessCounts counts={h} label={`${s.label} by harness`} />}</Show>
            </>
          );
          return (
            <span role="listitem" class={['stat', s.tone ? `t-${s.tone} toned` : '']}>
              <Show when={s.href} keyed fallback={body()}>
                {href => <a href={href}>{body()}</a>}
              </Show>
            </span>
          );
        }}
      </For>
    </div>
  );
}

/* ---------- Outcome block (D11, B2) ---------- */

export function OutcomeBlock(p: {
  outcome: string; requirements: { text: string; met?: boolean }[]; inputs: { icon: IconName; label: string }[]; depends?: JSX.Element;
}) {
  const [open, setOpen] = createSignal(false);
  const hasChecks = createMemo(() => p.requirements.length > 0 && p.requirements.every(r => r.met !== undefined));
  const met = createMemo(() => p.requirements.filter(r => r.met).length);
  return (
    <div class="outcome" data-ui="outcome-block">
      <Show when={p.outcome}>
        <button type="button" class="tw" aria-expanded={open() ? 'true' : 'false'} aria-label={open() ? 'Collapse outcome' : 'Expand outcome'} onClick={() => setOpen(v => !v)}>
          <span class={['chev', { open: open() }]}><Icon name="chevron-down" /></span>
        </button>
      </Show>
      <div class="outcome-b">
        <Show when={p.outcome}><p class={open() ? '' : 'clamp'}>{p.outcome}</p></Show>
        <Show when={hasChecks() || p.inputs.length > 0}>
          <div class="outcome-meta">
            <Show when={hasChecks()}>
              <span class={['chip-age', { 't-success alert': met() >= p.requirements.length }]} aria-label={`${met()} of ${p.requirements.length} requirements met`}>
                {met()}/{p.requirements.length}
              </span>
            </Show>
            <For each={p.inputs}>
              {i => <span class="chip in"><Icon name={i.icon} />{i.label}</span>}
            </For>
          </div>
        </Show>
        <Show when={p.requirements.length > 0}>
          <ul class="reqs">
            <For each={p.requirements}>
              {r => <li><Show when={r.met !== undefined}><Icon name={r.met ? 'circle-check' : 'square'} /></Show>{r.text}</li>}
            </For>
          </ul>
        </Show>
        <Show when={p.depends} keyed>{d => <div class="muted">{d}</div>}</Show>
      </div>
    </div>
  );
}

/* ---------- Step list and flow strip: a pipeline's steps, with a run's state on top (controls/step-list.md) ---------- */

export type StepItem =
  | { band: 'loop' | 'parallel'; label: string; inset?: boolean }
  | { id: string; kind: StepKind; executor: JSX.Element; reads: string; writes: string; inset?: boolean; condition?: string; result?: boolean;
      activation?: StateOf<'activation'>; activationReason?: string;
      /** The step in one run: shown in place of what it reads and writes. */
      run?: { state: StateOf<'execution'>; reason?: string; note?: string; time: Measured; cost: Measured; error?: string } };

/** The one place a pipeline is edited (D20). `children` is the editor of the open step, shown under its row (OR-14). */
export function StepList(p: { items: StepItem[]; open?: string; onOpen?: (id: string) => void; label: string; children?: JSX.Element }) {
  const numbered = createMemo(() => {
    let n = 0;
    return p.items.map(s => ('band' in s ? { item: s, no: 0 } : { item: s, no: ++n }));
  });
  return (
    <div class="steps" data-ui="step-list" role="list" aria-label={p.label}>
      <For each={numbered()}>
        {entry => {
          const s = entry.item;
          if ('band' in s) {
            return (
              <div class={['step-band', { inset: !!s.inset }]}>
                <Icon name={s.band === 'loop' ? 'rotate-cw' : 'rows-3'} />
                <span>{s.label}</span>
              </div>
            );
          }
          const k = stepKinds[s.kind];
          return (
            <div role="listitem" class={['step-item', { inset: !!s.inset }]}>
              <button
                type="button"
                class={['step', { open: p.open === s.id }]}
                aria-expanded={p.onOpen ? (p.open === s.id ? 'true' : 'false') : undefined}
                onClick={() => p.onOpen?.(s.id)}
              >
                <span class="no">{String(entry.no).padStart(2, '0')}</span>
                <Show
                  when={s.run}
                  keyed
                  fallback={
                    <Show
                      when={s.activation}
                      keyed
                      fallback={<span class="lead" data-tip={k.label}><Icon name={k.icon} label={k.label} /></span>}
                    >
                      {act => <StateBadge family="activation" state={act} reason={s.activationReason} compact />}
                    </Show>
                  }
                >
                  {run => <StateBadge family="execution" state={run.state} reason={run.reason} compact />}
                </Show>
                <b class="nm">{s.id}</b>
                <span class="ex">{s.executor}{s.result && ' · result'}</span>
                <Show
                  when={s.run}
                  keyed
                  fallback={
                    <span class="io">
                      <Show
                        when={s.activation}
                        keyed
                        fallback={
                          <>
                            {s.condition && <>when {s.condition} · </>}reads {s.reads} · writes <span class="w">{s.writes}</span>
                          </>
                        }
                      >
                        {act => <>{states.activation[act].label}{s.activationReason && ` · ${s.activationReason}`}</>}
                      </Show>
                    </span>
                  }
                >
                  {run => (
                    <span class="svc">
                      <Show when={run.note} keyed>{note => <span class="chip-age">{note}</span>}</Show>
                      <Num m={run.time} kind="duration" />
                      <Num m={run.cost} kind="money" />
                    </span>
                  )}
                </Show>
              </button>
              <Show when={s.run?.error} keyed>{err => <div class="step-error" data-important="">{err}</div>}</Show>
              <Show when={p.open === s.id && p.children}><div class="step-edit">{p.children}</div></Show>
            </div>
          );
        }}
      </For>
    </div>
  );
}

export type FlowNode =
  | { id: string; kind: StepKind; state?: StateOf<'execution'>; current?: boolean; optional?: boolean }
  | { loop: string; nodes: FlowNode[] } | { parallel: FlowNode[] };

/** A read-only picture of the steps: wires for order, a bracket for parallel steps, a dashed frame for a loop. */
export function FlowStrip(p: { nodes: FlowNode[]; label: string }) {
  const row = (ns: FlowNode[]): JSX.Element => (
    <For each={ns}>
      {(n, i) => <><Show when={i() > 0}><span class="flow-w" /></Show>{one(n)}</>}
    </For>
  );
  const one = (n: FlowNode): JSX.Element => {
    if ('loop' in n) return <span class="flow-loop"><span class="flow-cap"><Icon name="rotate-cw" />{n.loop}</span>{row(n.nodes)}</span>;
    if ('parallel' in n) return <span class="flow-par"><For each={n.parallel}>{one}</For></span>;
    return (
      <span class={['flow-n', { cur: !!n.current, opt: !!n.optional }]}>
        {n.state ? <StateBadge family="execution" state={n.state} compact /> : <Icon name={stepKinds[n.kind].icon} />}
        {n.id}
      </span>
    );
  };
  return <div class="flow" data-ui="flow-strip" role="img" aria-label={p.label}>{row(p.nodes)}</div>;
}

/* ---------- Area chart (controls/area-chart.md): recorded and projected lines over a time range, CSS only (OR-8) ---------- */

export type ChartPoint = { at: number; v: number; end?: number };
export type ChartBucket = { key: string; label: string; tip?: string; valueLabel?: string };
/** pattern: stripes over the tone, for a fifth and later series (tokens.md: never new hues). */
export type ChartSeries = { label: string; points: ChartPoint[]; tone: 'd1' | 'd2' | 'd3' | 'd4'; projected?: boolean; step?: boolean; pattern?: boolean };
type AreaChartProps = {
  label: string; series: ChartSeries[]; from: number; to: number; now: number; max: number;
  /** Layers on top of each other, first series at the bottom (points share their times); with a legend of last values. */
  stacked?: boolean;
  /** Text of a value for the summary and the axis, e.g. `40%` or `12/h`. */
  value: (v: number) => string;
  /** Interactive bucket selectors rendered outside role="img" (CA-09). */
  buckets?: readonly ChartBucket[];
  selectedBucket?: string | null;
  onSelectBucket?: (key: string) => void;
};
export function AreaChart(p: AreaChartProps) {
  // Stacked: each layer is drawn as the sum of itself and the layers under it, top layer first, all opaque.
  const drawn = createMemo(() => p.stacked
    ? p.series.map((s, i) => ({
        ...s,
        points: s.points.map((q, j) => ({ at: q.at, v: p.series.slice(0, i + 1).reduce((n, t) => n + (t.points[j]?.v ?? 0), 0) })),
      })).reverse()
    : p.series);
  const span = () => Math.max(1, p.to - p.from);
  const x = (t: number) => Math.min(100, Math.max(0, (t - p.from) / span() * 100));
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
    const endX = x(lastPt.end ?? (pts.length === 1 ? Math.min(p.to, lastPt.at + span() / 24) : lastPt.at));
    if (endX > x(lastPt.at)) xy.push(`${endX}% ${y(lastPt.v)}%`);
    return `polygon(${x(pts[0].at)}% 100%, ${xy.join(', ')}, ${endX}% 100%)`;
  };
  const last = (s: ChartSeries) => s.points.length ? p.value(s.points[s.points.length - 1].v) : 'none';
  const summary = createMemo(() => p.series.map(s => `${s.label}${s.projected && !s.label.includes('projected') ? ' projected' : ''}: ${last(s)}`).join('; '));
  const dayTicks = createMemo(() => {
    const out: number[] = [];
    const d = new Date(p.from);
    d.setHours(24, 0, 0, 0);
    for (let t = d.getTime(); t < p.to && out.length < 31; t += 864e5) out.push(t);
    return out;
  });
  const drawnShapes = createMemo(() => drawn().map(s => ({ s, path: shape(s) })).filter(entry => !!entry.path));

  return (
    <div data-ui="area-chart" class="area-chart">
      <div class="ac-plot" role="img" aria-label={`${p.label}: ${summary()}`}>
        <For each={drawnShapes()}>
          {({ s, path }) => (
            <i
              class={['ac-area', s.tone, { projected: !!s.projected, stacked: !!p.stacked, pattern: !!s.pattern }]}
              style={{ 'clip-path': path }}
            />
          )}
        </For>
        <For each={dayTicks()}>{t => <b class="ac-day" style={{ left: `${x(t)}%` }} />}</For>
        <Show when={p.now >= p.from && p.now <= p.to}>
          <b class="ac-now" style={{ left: `${x(p.now)}%` }} data-tip={exact(p.now)} />
        </Show>
        <span class="ac-max">{p.value(p.max)}</span>
      </div>
      <div class="ac-axis"><time>{stamp(p.from, p.now)}</time><span class="grow" /><time>{stamp(p.to, p.now)}</time></div>
      <Show when={p.buckets?.length}>
        <div class="ac-buckets" role="group" aria-label={`${p.label} buckets`}>
          <For each={p.buckets ?? []}>
            {b => (
              <button
                type="button"
                class={['ac-bucket', { on: p.selectedBucket === b.key }]}
                aria-pressed={p.selectedBucket === b.key ? 'true' : 'false'}
                data-tip={b.tip ?? (b.valueLabel ? `${b.label}: ${b.valueLabel}` : b.label)}
                onClick={() => p.onSelectBucket?.(b.key)}
              >
                {b.label}
              </button>
            )}
          </For>
        </div>
      </Show>
      <Show when={p.stacked}>
        <ul class="ac-legend" aria-hidden="true">
          <For each={p.series}>
            {s => <li><i class={['ac-key', s.tone, { pattern: !!s.pattern }]} />{s.label} <span class="num">{last(s)}</span></li>}
          </For>
        </ul>
      </Show>
    </div>
  );
}

/* ---------- Time strip: where a run sits in a time window (Runners), toned by its state ---------- */

/** A part of a lane: one stage of a pull request, with its own tone and name (the name is in the strip's label too). */
export type StripPart = { start: number; end: number | null; tone: Tone; label: string };
/** One bar (a run: start..end in its execution state) or several consecutive parts in one lane (stages). */
type TimeStripProps = { from: number; to: number; now: number; label: string } &
  ({ start: number; end: number | null; state: StateOf<'execution'>; parts?: never } | { parts: StripPart[]; start?: never; end?: never; state?: never });
export function TimeStrip(p: TimeStripProps) {
  const x = (t: number) => Math.min(100, Math.max(0, (t - p.from) / (p.to - p.from) * 100));
  const bar = (s: number, e: number | null, tone: Tone, tip?: string) => {
    const a = x(s), b = x(e ?? p.now);
    return <i class={[`t-${tone}`, { live: e === null }]} data-tip={tip} style={{ left: `${a}%`, width: `max(2px, ${b - a}%)` }} />;
  };
  return (
    <span class="tstrip" data-ui="time-strip" role="img" aria-label={p.parts ? `${p.label}: ${p.parts.map(pt => pt.label).join(', ')}` : p.label}>
      <Show
        when={p.parts}
        keyed
        fallback={bar(p.start!, p.end ?? null, states.execution[p.state!].tone)}
      >
        {parts => <For each={parts}>{pt => bar(pt.start, pt.end, pt.tone, pt.label)}</For>}
      </Show>
    </span>
  );
}
