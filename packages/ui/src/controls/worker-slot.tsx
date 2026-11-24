// Worker slot: what runs in one worker slot of a host on Live, or that it is free or failing (controls/worker-slot.md).
import { createMemo, Show } from 'solid-js';
import { type Measured, duration, countdown } from '../format.ts';
import { type DataTone } from '../charts.tsx';
import { Num, useTick } from './primitives.tsx';

export type WorkerSlotProps = {
  /** Slot number on its host, from 1. */
  slot: number;
  state: 'busy' | 'idle' | 'failing';
  /** Busy: the task title; failing: what fails, with ×N for repeats. */
  title?: string;
  /** Second line: entity code, project and harness. */
  code?: string; project?: string; harness?: string;
  /** The project's colour for the left bar. */
  color?: DataTone;
  tokens?: Measured;
  /** Busy: when the run started (the elapsed time ticks). */
  started?: number;
  /** Failing: when it retries, and the cause in words. */
  retryAt?: number; cause?: string;
  now?: number;
  /** Opens the session view. Omit in wall mode. */
  onOpen?: () => void;
};

export function WorkerSlot(p: WorkerSlotProps) {
  const tick = useTick(1000, () => p.state === 'busy' && p.now === undefined);
  const now = () => p.now ?? tick();
  const elapsed = createMemo(() => p.started === undefined ? null : duration(Math.max(0, (now() - p.started) / 1000)));
  const retry = createMemo(() => p.retryAt === undefined ? null : countdown(p.retryAt, now()));
  const name = createMemo(() => p.state === 'idle' ? `Slot ${p.slot}, idle`
    : p.state === 'failing' ? `Slot ${p.slot}: ${p.title}${p.cause ? `, ${p.cause}` : ''}${retry() ? `, retry in ${retry()!.long}` : ''}`
    : `Slot ${p.slot}: ${p.title}${p.code ? `, ${p.code}` : ''}${elapsed() ? `, running ${elapsed()!.long}` : ''}`);
  const meta = () => [p.code, p.project, p.harness].filter(Boolean).join(' · ');
  const body = () => (
    <Show when={p.state !== 'idle'} fallback={<span class="ws-idle">idle</span>}>
      <span class="ws-l1">
        <i class="dot" aria-hidden="true" />
        <span class="ws-title">{p.title}</span>
        <Show when={p.state === 'busy'} fallback={<Show when={retry()} keyed>{r => <span class="ws-r mono">retry {r.text}</span>}</Show>}>
          <Show when={elapsed()} keyed>{e => <span class="ws-r mono">{e.text}</span>}</Show>
        </Show>
      </span>
      <span class="ws-l2">
        <span class="ws-meta">{meta()}</span>
        <Show when={p.state === 'busy'} fallback={<span class="ws-r">{p.cause}</span>}>
          <Show when={p.tokens} keyed>{t => <span class="ws-r"><Num m={t} kind="count" /> tok</span>}</Show>
        </Show>
      </span>
    </Show>
  );
  return (
    <div class={['wslot', p.color ? `c-${p.color}` : '']} data-state={p.state} data-ui="worker-slot" role="group" aria-label={name()}>
      <Show when={p.onOpen && p.state !== 'idle'} fallback={<span class="ws-in">{body()}</span>}>
        <button type="button" class="ws-in" onClick={() => p.onOpen?.()}>{body()}</button>
      </Show>
    </div>
  );
}
