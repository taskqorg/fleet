// Status pill: the fleet's state in words, the first thing read on Live (controls/status-pill.md).
import { Show } from 'solid-js';
import { Ago } from './primitives.tsx';

export type StatusPillState = 'working' | 'problem' | 'connecting' | 'stale';
const TONE = { working: 'success', problem: 'danger', connecting: 'neutral', stale: 'neutral' } as const;

export function StatusPill(p: {
  state: StatusPillState;
  /** Problem: the first problem in words. */
  text?: string;
  /** Problem: how many more problems there are; "· N more" when above zero. */
  more?: number;
  /** Stale: the time of the last update. */
  at?: number;
  /** Problem: opens the failing host or cause. Omit in wall mode. */
  onOpen?: () => void;
}) {
  const body = () => (
    <>
      <i class="dot" aria-hidden="true" />
      <Show when={p.state === 'working'}>All systems working</Show>
      <Show when={p.state === 'problem'}>{p.text}<Show when={(p.more ?? 0) > 0}> · {p.more} more</Show></Show>
      <Show when={p.state === 'connecting'}>Connecting…</Show>
      <Show when={p.state === 'stale'}>Last update <Show when={p.at !== undefined} fallback="unknown"><Ago t={p.at!} /></Show> ago</Show>
    </>
  );
  return (
    <span class={['spill', `t-${TONE[p.state]}`]} data-state={p.state} data-ui="status-pill" role="status" aria-live="polite">
      <Show when={p.state === 'problem' && p.onOpen} fallback={<span class="sp-in">{body()}</span>}>
        <button type="button" class="sp-in" onClick={() => p.onOpen?.()}>{body()}</button>
      </Show>
    </span>
  );
}
