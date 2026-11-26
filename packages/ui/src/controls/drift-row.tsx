// Drift row: one setting every future agent gets, on Library › Changes › Agent environment (controls/drift-row.md): its
// name, what it gives, whether it is in sync, and its last change or the finding about it (OR-64: that fact is last).
// Rows sit flat in a panel, one line each; a narrow pane stacks them (OR-66).
import { Show } from 'solid-js';
import type { Tone } from '../states.ts';

export function DriftRow(p: {
  name: string;
  /** What the setting gives: "19 granted · all 19 that code and profiles use", "pinned on every host". */
  detail?: string;
  /** In sync, or the drift in words: "Missing on pds18", "No failover". */
  chip: { text: string; tone: Tone; tip?: string };
  /** The last change (who, when) or the open finding; the row's last fact. */
  fact?: string;
  /** The fact's full text when it is long, or the exact time. */
  factTip?: string;
  href?: string;
  onOpen?: () => void;
}) {
  return (
    <li class="drow" data-ui="drift-row" aria-label={`${p.name}, ${p.chip.text}`}>
      <Show when={p.href} fallback={<span class="drow-name">{p.name}</span>}>{href => (
        <a class="drow-name drow-link" href={href()} onClick={e => { if (p.onOpen && !e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); p.onOpen(); } }}>{p.name}</a>
      )}</Show>
      <Show when={p.detail}><span class="drow-detail">{p.detail}</span></Show>
      <span class={['badge', `t-${p.chip.tone}`]} data-tip={p.chip.tip}>{p.chip.text}</span>
      <Show when={p.fact}><span class="drow-fact" data-tip={p.factTip}>{p.fact}</span></Show>
    </li>
  );
}
