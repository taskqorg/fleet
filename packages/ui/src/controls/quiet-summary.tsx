// Quiet summary: Home when nothing needs the person (controls/quiet-summary.md, OR-50, board master `Quiet summary`).
import { Show } from 'solid-js';
import { Button } from './primitives.tsx';

/**
 * "Nothing needs you", one line of numbers since the last visit (or today on a first visit) naming the watching bot,
 * then two buttons: ask the watcher what happened, open Live. It takes the place of the empty Waiting and Failed groups
 * and leaves in place when one arrives (OR-23). Loading: a block of the same height.
 */
export function QuietSummary(p: {
  /** The facts in words, already counted: "12 tasks done, 2 pull requests merged, 0 failures". */
  facts?: string;
  /** "Overnight", "Since 14:20", "Today": the span the facts cover. */
  span?: string;
  /** The bot that keeps watch, if any. */
  watcher?: string;
  loading?: boolean;
  onAsk?: () => void;
  onLive?: () => void;
}) {
  return (
    <section class={['qsum', { skeleton: !!p.loading }]} data-ui="quiet-summary" role="status" aria-busy={p.loading ? 'true' : undefined}>
      <Show when={!p.loading} fallback={<><i /><i /><i /></>}>
        <h2 class="qsum-title">Nothing needs you</h2>
        <p class="qsum-facts">
          <Show when={p.facts}>{p.span ? `${p.span}: ` : ''}{p.facts}.</Show>
          <Show when={p.watcher}> {p.watcher} keeps watch and wakes the right session if something comes up.</Show>
        </p>
        <div class="qsum-acts">
          <Show when={p.watcher && p.onAsk}><Button label={`What happened${p.span ? ' ' + p.span.toLowerCase() : ''}?`} kind="primary" onClick={() => p.onAsk?.()} /></Show>
          <Button label="Open Live" onClick={() => p.onLive?.()} />
        </div>
      </Show>
    </section>
  );
}
