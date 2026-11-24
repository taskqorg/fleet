// Gauge: the one-value form of the fill bar, for host resources in a host band head (controls/fill-bar.md#gauge).
import { createMemo, Show } from 'solid-js';

export type GaugeProps = {
  /** Short label on the band: CPU, MEM, DISK. */
  label: string;
  /** Full name for the accessible name: Memory. */
  name: string;
  /** Percent used, 0 to 100; null when unknown. */
  value: number | null;
  /** The host's own thresholds in percent (host settings, never fixed here). */
  warnAt: number; critAt: number;
};

export function Gauge(p: GaugeProps) {
  const level = createMemo(() => p.value === null ? '' : p.value >= p.critAt ? 'crit' : p.value >= p.warnAt ? 'warn' : '');
  const text = createMemo(() => p.value === null ? '?' : `${Math.round(p.value)}%`);
  return (
    <span class={['gauge', level()]} data-ui="gauge" role="img" aria-label={`${p.name} ${p.value === null ? 'unknown' : text()}`}>
      <span class="g-label" aria-hidden="true">{p.label}</span>
      <span class="g-track" aria-hidden="true">
        <Show when={p.value !== null}><i style={{ width: `${Math.min(100, Math.max(0, p.value!))}%` }} /></Show>
      </span>
      <span class="g-val" aria-hidden="true">{text()}</span>
    </span>
  );
}
