// Pulse line and blocker chip: how much is in flight and what is stuck, at the top of Home (controls/pulse-line.md, OR-50).
import { createMemo, For, Show } from 'solid-js';
import { type Life, states } from '../registry.ts';
import { OfflineMarker } from './primitives.tsx';

export type Blocker = { cause: string; n: number; href: string };

/** One cause of stuck work: a danger pill with the cause in words and ×N (OR-56, OR-58). */
export function BlockerChip(p: Blocker) {
  return (
    <a class="blocker t-danger" data-ui="blocker-chip" href={p.href} aria-label={`Blocker: ${p.cause}, ${p.n} ${p.n === 1 ? 'time' : 'times'}`} data-tip={`${p.cause} ×${p.n}`}>
      <i aria-hidden="true" /><span class="blocker-cause">{p.cause}</span><span class="num">×{p.n}</span>
    </a>
  );
}

const COUNTS: { key: 'working' | 'queued' | 'done'; life: Life; word: string }[] = [
  { key: 'working', life: 'working', word: 'working' },
  { key: 'queued', life: 'queued', word: 'queued' },
  { key: 'done', life: 'done', word: 'done today' },
];

/**
 * Counts (working, queued, done today; a zero count is hidden, OR-29), then one blocker chip per cause, newest first,
 * the first `chips` of them and `+K` (OR-30), then `Live ›`. Each count and chip is a link.
 */
export function PulseLine(p: {
  working: number; queued: number; done: number; blockers?: Blocker[]; chips?: number;
  countHref: (state: Life) => string; liveHref: string; moreHref?: string;
  staleSince?: number; clock?: number;
}) {
  const shown = createMemo(() => (p.blockers ?? []).slice(0, p.chips ?? 2));
  const more = createMemo(() => (p.blockers ?? []).length - shown().length);
  return (
    <div class="pulse" data-ui="pulse-line" role="group" aria-label="Pulse">
      <For each={COUNTS}>
        {c => (
          <Show when={p[c.key] > 0}>
            <a class={['pulse-count', `t-${states.life[c.life].tone}`]} href={p.countHref(c.life)} aria-label={`${p[c.key]} ${c.word}`}>
              <i aria-hidden="true" /><b class="num">{p[c.key]}</b><span>{c.word}</span>
            </a>
          </Show>
        )}
      </For>
      <For each={shown()}>{b => <BlockerChip {...b} />}</For>
      <Show when={more() > 0}><a class="pulse-more" href={p.moreHref ?? p.liveHref} aria-label={`${more()} more blockers`}>+{more()}</a></Show>
      <Show when={p.staleSince !== undefined}><OfflineMarker state="offline" since={p.staleSince} now={p.clock ?? Date.now()} /></Show>
      <a class="pulse-live" href={p.liveHref}>Live ›</a>
    </div>
  );
}
