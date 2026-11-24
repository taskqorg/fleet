// Diff row: a configuration shows only what differs from its base (patterns/definition-editor.md#diff-row, OR-45).
import { createSignal, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Icon } from './primitives.tsx';

/** A changed field: its name in mono and its value. The base default is in the tooltip only, never a second value or an arrow (OR-7, OR-20). */
export function DiffRow(p: { field: string; value: string; base: string }) {
  return (
    <div class="diff-row" data-ui="diff-row">
      <span class="dr-field mono">{p.field}</span>
      <span class="dr-value" data-tip={`Default: ${p.base}`} aria-description={`Default: ${p.base}`}>{p.value}</span>
    </div>
  );
}

/** The folded "Inherited defaults (N)" row; it opens the unchanged fields in place. */
export function InheritedRows(p: { count: number; children: JSX.Element; open?: boolean }) {
  const [open, setOpen] = createSignal(!!p.open);
  return (
    <div class="diff-inherited" data-ui="diff-row">
      <button type="button" class="dr-fold" aria-expanded={open() ? 'true' : 'false'} onClick={() => setOpen(v => !v)}>
        <span class={['chev', { open: open() }]}><Icon name="chevron-down" /></span>
        Inherited defaults ({p.count})
      </button>
      <Show when={open()}><div class="dr-rows">{p.children}</div></Show>
    </div>
  );
}
