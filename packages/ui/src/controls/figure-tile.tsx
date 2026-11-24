// Figure tile: one headline figure on Live with its recent trend (controls/figure-tile.md), and its sparkline.
import { createMemo, For, Show } from 'solid-js';
import { type Measured, type Kind } from '../format.ts';
import { Num } from './primitives.tsx';

/**
 * Bars of one figure over a range, the latest last. A null bar is a gap (no reading), drawn as a dot on the base.
 * `alertFrom` turns the bars from that index on to the danger tone.
 */
export function Sparkline(p: { values: readonly (number | null)[]; label: string; alertFrom?: number }) {
  const max = createMemo(() => Math.max(1, ...p.values.map(v => v ?? 0)));
  return (
    <span class="spark" data-ui="sparkline" role="img" aria-label={p.label}>
      <For each={p.values}>
        {(v, i) => (
          <i
            class={{ gap: v === null, alert: p.alertFrom !== undefined && i() >= p.alertFrom }}
            style={{ height: v === null ? undefined : `${Math.max(8, v / max() * 100)}%` }}
          />
        )}
      </For>
    </span>
  );
}

export function FigureTile(p: {
  label: string; value: Measured; kind?: Kind;
  /** "4 / 8": the whole the value is part of. */
  of?: number;
  /** One line under the value; in alert it names the cause. */
  sub?: string;
  trend: readonly (number | null)[]; trendLabel: string;
  /** The figure means trouble: the value and the latest bars turn danger. */
  alert?: boolean;
  /** How many of the latest bars are part of the trouble (default 3). */
  alertBars?: number;
  stale?: boolean;
  /** Opens the list behind the figure. Omit in wall mode. */
  onOpen?: () => void;
}) {
  const name = () => `${p.label}: ${p.value.value ?? 'unknown'}${p.of !== undefined ? ` of ${p.of}` : ''}${p.sub ? `, ${p.sub}` : ''}`;
  const body = () => (
    <>
      <span class="ft-text">
        <span class="ft-label">{p.label}</span>
        <span class="ft-value"><Show when={p.value.value !== null} fallback={<span data-tip={p.value.reason}>?</span>}><Num m={p.value} kind={p.kind ?? 'count'} /></Show><Show when={p.of !== undefined}><span class="ft-of"> / {p.of}</span></Show></span>
        <Show when={p.sub}><span class="ft-sub">{p.sub}</span></Show>
      </span>
      <Sparkline values={p.trend} label={p.trendLabel} alertFrom={p.alert ? p.trend.length - (p.alertBars ?? 3) : undefined} />
    </>
  );
  return (
    <div class="ftile" data-state={p.stale ? 'stale' : p.alert ? 'alert' : 'normal'} data-ui="figure-tile" role="group" aria-label={name()}>
      <Show when={p.onOpen && !p.stale} fallback={<span class="ft-in">{body()}</span>}>
        <button type="button" class="ft-in" onClick={() => p.onOpen?.()}>{body()}</button>
      </Show>
    </div>
  );
}
