// Detail head: the top of the work area for an opened item (patterns/list-detail.md#board).
import { Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Button } from './primitives.tsx';

export function DetailHead(p: {
  /** `project · code` in mono (the project may be a link back to it), or a definition's path and revision; omit where
   * it would only repeat the title. */
  crumb?: JSX.Element;
  title: JSX.Element;
  /** The state line under the title: state, progress, age (OR-39). */
  state?: JSX.Element;
  /** The primary action (a split button in the built head) and Share, on the right. */
  actions?: JSX.Element;
  /** Close icon (OR-15); omit where the item is the whole page. */
  onClose?: () => void;
}) {
  return (
    <header class="dhead" data-ui="detail-head">
      <div class="detail-h">
        <Show when={p.onClose} keyed>{close => <Button label="Close" icon="x" iconOnly kind="quiet" onClick={close} />}</Show>
        <Show when={p.crumb}><span class="muted mono">{p.crumb}</span></Show>
        <span class="grow" />
        {p.actions}
      </div>
      <h2 class="detail-title">{p.title}</h2>
      <Show when={p.state} keyed>{s => <div class="dh-state">{s}</div>}</Show>
    </header>
  );
}
