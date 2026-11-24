// Host band: one host on Live, its health, its worker slots and its last 24 hours of runs (controls/host-band.md).
import { For, Show } from 'solid-js';
import { Ago, Skeleton } from './primitives.tsx';
import { Gauge, type GaugeProps } from './gauge.tsx';
import { WorkerSlot, type WorkerSlotProps } from './worker-slot.tsx';
import { RunStrip, type RunStripProps } from './run-strip.tsx';

export type HostBandProps = {
  name: string;
  state: 'normal' | 'problem' | 'offline' | 'loading';
  /** Platform and free disk, in words: "darwin · 318 GB free". */
  facts?: string;
  gauges?: readonly GaugeProps[];
  /** The day's runs and active time, in mono on the right: "38 runs · 5h 33m". */
  day?: string;
  /** Problem: what fails, since when and what still works, in one line (repeats aggregated). */
  problem?: string;
  slots?: readonly WorkerSlotProps[];
  strip?: RunStripProps;
  /** Offline: when the host was last seen. */
  seen?: number;
  /** Opens the host in System. Omit in wall mode. */
  onOpen?: () => void;
};

export function HostBand(p: HostBandProps) {
  return (
    <Show when={p.state !== 'loading'} fallback={<section class="hband" data-state="loading" data-ui="host-band" aria-label={p.name} aria-busy="true"><Skeleton rows={3} /></section>}>
      <section class="hband" data-state={p.state} data-ui="host-band" aria-label={p.name}>
        <div class="hb-head">
          <Show
            when={p.onOpen}
            keyed
            fallback={<span class="hb-name"><i class="dot" aria-hidden="true" /><b>{p.name}</b></span>}
          >
            {open => <button type="button" class="hb-name" onClick={open}><i class="dot" aria-hidden="true" /><b>{p.name}</b></button>}
          </Show>
          <span class="hb-facts">{p.facts}<Show when={p.state === 'offline'}> · offline<Show when={p.seen !== undefined}>, seen <Ago t={p.seen!} /></Show></Show></span>
          <span class="hb-right">
            <For each={p.gauges ?? []}>{g => <Gauge {...g} />}</For>
            <Show when={p.day}><span class="hb-day mono">{p.day}</span></Show>
          </span>
        </div>
        <Show when={p.state === 'problem' && p.problem}>
          <p class="hb-problem" role="status">{p.problem}</p>
        </Show>
        {/* By position: slot i stays the same tile while its contents change (OR-23), so a tick never rebuilds it. */}
        <div class="hb-slots"><For each={p.slots ?? []} keyed={false}>{s => <WorkerSlot {...s()} />}</For></div>
        <Show when={p.strip} keyed>{s => <RunStrip {...s} />}</Show>
      </section>
    </Show>
  );
}
