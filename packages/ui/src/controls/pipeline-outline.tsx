// Pipeline outline: how a pipeline works as plain numbered lines (controls/pipeline-outline.md; board screens/projects.bl,
// Approved P-A5 and P-A7). No step diagram: the same text at every width, wrapping, never scrolling sideways (OR-66).
import { For, Show } from 'solid-js';

/** One line of the outline, as the server writes it (contract PipelineOutlineLine). */
export type PipelineOutlineLine = { n?: number; step?: string; text: string; depth: number; condition?: boolean };

/**
 * An ordered list: each step `n.` (muted mono), the step id (mono) and what it does (wraps). A nested step (`depth` 1)
 * is indented one step; a condition line (no number) is in the attention tone.
 */
export function PipelineOutline(p: { lines: readonly PipelineOutlineLine[]; label?: string }) {
  return (
    <ol class="p-outline" data-ui="pipeline-outline" aria-label={p.label ?? 'How it works'}>
      <For each={p.lines}>{l => (
        <li class={['po-line', { cond: !!l.condition }]} data-depth={Math.max(0, Math.min(l.depth, 4))}>
          <Show when={l.n !== undefined}><span class="po-n">{l.n}.</span> </Show>
          <Show when={l.step}><span class="po-step">{l.step}</span> </Show>
          <span class="po-text">{l.text}</span>
        </li>
      )}</For>
    </ol>
  );
}
