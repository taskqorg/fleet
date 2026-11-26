// Gap row, step trail and before/after: what agents lack, on System › Agents (controls/gap-row.md, board
// drafts/agent-garden frames 1, 2, 4 and 5). A gap row is one class of failure: its words, how many runs or calls, where,
// its days as a sparkline (the change marked), where its finding stands, and its last failure (OR-64: time last). The
// step trail says how far a finding got; before/after compares the class's rate on each side of the change.
import { For, Show } from 'solid-js';
import type { Tone } from '../states.ts';
import { Sparkline } from './figure-tile.tsx';

export function GapRow(p: {
  name: string;
  /** "743 runs · 3 hosts · codex, claude": the count and where. */
  detail: string;
  /** Failures per day of the window, oldest first. */
  days: readonly number[];
  /** Index into days of the change (the fix's release on every host). */
  changeAt?: number;
  /** Where the finding stands: Open, Investigating, Watching · #n on hosts, Below threshold. */
  chip: { text: string; tone: Tone; tip?: string };
  /** The last failure, short ("09:40", "25 Nov"); its exact time in factTip. */
  fact?: string;
  factTip?: string;
  href: string;
  current?: boolean;
  onOpen: () => void;
}) {
  return (
    <li class={['gaprow', { cur: !!p.current }]} data-ui="gap-row" aria-label={`${p.name}, ${p.chip.text}`}>
      <a class="gaprow-name" href={p.href} aria-current={p.current ? 'true' : undefined}
        onClick={e => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); p.onOpen(); } }}>{p.name}</a>
      <span class="gaprow-detail">{p.detail}</span>
      <Sparkline values={p.days} changeAt={p.changeAt} label={`${p.name}: ${p.days.join(', ')} a day`} />
      <span class={['badge', `t-${p.chip.tone}`]} data-tip={p.chip.tip}>{p.chip.text}</span>
      <Show when={p.fact}><span class="gaprow-fact" data-tip={p.factTip}>{p.fact}</span></Show>
    </li>
  );
}

export type TrailStep = { label: string; state: 'done' | 'current' | 'todo'; tone?: Tone };

/** "✓ Found · ✓ Investigating · Change · On hosts · Watching · Fixed": the steps of a finding. */
export function StepTrail(p: { steps: readonly TrailStep[]; label: string }) {
  return (
    <ol class="trail" data-ui="step-trail" aria-label={p.label}>
      <For each={p.steps}>{s => (
        <li class={[s.state, s.tone && `t-${s.tone}`]} aria-current={s.state === 'current' ? 'step' : undefined}>
          <Show when={s.state === 'done'}><span aria-hidden="true">✓ </span></Show>{s.label}
          <Show when={s.state === 'done'}><span class="sr-only"> (done)</span></Show>
        </li>
      )}</For>
    </ol>
  );
}

/** The class's rate before and after the change, with its days and what closes it. */
export function GapCompare(p: {
  before: { value: string; label: string };
  after?: { value: string; label: string; tone?: Tone };
  days: readonly number[];
  changeAt?: number;
  /** What happens next: closes as fixed on 3 Dec if ..., or the verdict. */
  note?: string;
  label: string;
}) {
  return (
    <section class="gapcmp" data-ui="gap-compare" aria-label={p.label}>
      <div class="gapcmp-figs">
        <span class="gapcmp-fig"><b>{p.before.value}</b><span>{p.before.label}</span></span>
        <Show when={p.after}>{a => (
          <span class={['gapcmp-fig', a().tone && `t-${a().tone}`]}><b>{a().value}</b><span>{a().label}</span></span>
        )}</Show>
        <Sparkline values={p.days} changeAt={p.changeAt} label={p.label} />
      </div>
      <Show when={p.note}><p>{p.note}</p></Show>
    </section>
  );
}
