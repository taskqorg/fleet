// Live row: one compact row of the Live side column, a project, a task in work, a pull request, a CI runner or an event
// (controls/live-row.md). Work rows carry the shared work facts (controls/work-facts.md) on the right.
import { Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import type { Tone, Stage, Life } from '../registry.ts';
import { Ago, StageChip } from './primitives.tsx';
import { Sparkline } from './figure-tile.tsx';
import { StateDot } from './state-dot.tsx';

export type LiveRowProps = (
  | { kind: 'project'; name: string; running: number; queued: number; done: number; trend: readonly (number | null)[]; trendLabel: string }
  /** A task in work: its state in the one vocabulary (OR-55), code and title. */
  | { kind: 'task'; state: Life; code?: string; title: string }
  | { kind: 'delivery'; tone: Tone; code: string; title: string; stage: Stage }
  | { kind: 'runner'; name: string; fact: string }
  /** An event's state in the one vocabulary (OR-55): Done, Failed, Working. */
  | { kind: 'event'; at: number; state: Life; text: string }
) & {
  /** The dot and the right-hand fact in the danger tone. */
  trouble?: boolean;
  /** Task, delivery and event rows: the shared work facts (WorkFacts) on the right, after the stage chip; the time is always the last fact (an event's age after its facts). */
  facts?: JSX.Element;
  stale?: boolean;
  /** Opens the item. Omit in wall mode. */
  onOpen?: () => void;
};

/** A row's kind is fixed for its life: key rows by item and kind. */
export function LiveRow(p: LiveRowProps) {
  const tone = () => p.trouble ? 'danger' : 'tone' in p ? p.tone : p.kind === 'runner' ? 'success' : 'neutral';
  // Delivery stages and runner states are not session states: those rows keep a dot in their own tone.
  const body = (): JSX.Element => {
    const r = p;
    if (r.kind === 'project') return (
      <>
        <span class="lr-main lr-two">
          <span class="lr-text">{r.name}</span>
          <span class="lr-counts">
            <Show when={r.running > 0}><span class="t-running">{r.running} running</span></Show>
            <Show when={r.queued > 0}><span class="muted">{r.queued} queued</span></Show>
            <Show when={r.done > 0}><span class="t-success">{r.done} done</span></Show>
          </span>
        </span>
        <Sparkline values={r.trend} label={r.trendLabel} />
      </>
    );
    if (r.kind === 'task') return <><StateDot state={r.trouble ? 'failed' : r.state} bare /><span class="lr-main"><Show when={r.code}><span class="mono lr-code">{r.code}</span> </Show><span class="lr-text">{r.title}</span></span>{r.facts}</>;
    if (r.kind === 'delivery') return <><i class="dot" aria-hidden="true" /><span class="lr-main"><span class="mono lr-code">{r.code}</span> <span class="lr-text">{r.title}</span></span><StageChip stage={r.stage} />{r.facts}</>;
    if (r.kind === 'runner') return <><i class="dot" aria-hidden="true" /><span class="lr-main lr-text">{r.name}</span><span class="lr-fact mono">{r.fact}</span></>;
    return <><StateDot state={r.trouble ? 'failed' : r.state} bare /><span class="lr-main lr-text">{r.text}</span>{r.facts}<span class="lr-age"><Ago t={r.at} /></span></>;
  };
  return (
    <li class={['lrow', `t-${tone()}`]} data-kind={p.kind} data-state={p.stale ? 'stale' : p.trouble ? 'trouble' : 'normal'} data-ui="live-row">
      <Show when={p.onOpen && !p.stale} fallback={<span class="lr-in">{body()}</span>}>
        <button type="button" class="lr-in" onClick={() => p.onOpen?.()}>{body()}</button>
      </Show>
    </li>
  );
}
