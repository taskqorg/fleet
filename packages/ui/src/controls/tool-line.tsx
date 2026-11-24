// Tool line: consecutive tool calls and system events between two messages as one quiet line (controls/tool-line.md,
// OR-52, board master `Tool line`). It is the folded form of the thread's tool rows in the one session view.
import { createMemo, createSignal, For, Show, untrack } from 'solid-js';
import { duration } from '../format.ts';
import { Icon, useTick } from './primitives.tsx';

/** One call inside an open line; repeats aggregate as ×N (OR-58). */
export type ToolCall = { name: string; target: string; secs: number; n?: number; failed?: boolean };

/**
 * Folded: a caret, what was done in words, the total time. Open: the calls below it on a sunken block, opening downward
 * (OR-23). Running: a running dot, the words name the current call and the time ticks from `startedAt`. Failed: the line
 * turns danger and names the failed call. A click anywhere on the line opens or folds it.
 */
export function ToolLine(p: {
  words: string; calls: ToolCall[]; state?: 'done' | 'running' | 'failed';
  /** Total seconds when done or failed; the start time while running. */
  secs?: number; startedAt?: number;
  /** Starts open (the book, a deep link to a call). */
  open?: boolean; onToggle?: (open: boolean) => void;
}) {
  const [open, setOpen] = createSignal(untrack(() => !!p.open));
  const running = () => p.state === 'running';
  const now = useTick(1000, running);
  const time = createMemo(() => duration(running() && p.startedAt !== undefined ? Math.max(0, (now() - p.startedAt) / 1000) : p.secs ?? 0));
  const toggle = () => { const next = !open(); setOpen(next); p.onToggle?.(next); };
  return (
    <div class={['tool-line', p.state ?? 'done', { open: open() }]} data-ui="tool-line">
      <button
        type="button"
        class="tool-line-head"
        aria-expanded={open() ? 'true' : 'false'}
        aria-label={`${p.words}, ${time().long}`}
        onClick={toggle}
        onKeyDown={e => {
          if (!open() || (e.key !== 'ArrowDown' && e.key !== 'ArrowUp')) return;
          e.preventDefault();
          (e.currentTarget.parentElement?.querySelector<HTMLElement>('.tool-call'))?.focus();
        }}
      >
        <Show when={running()} fallback={<span class={['chev', { open: open() }]}><Icon name="chevron-down" /></span>}>
          <i class="tool-line-dot" aria-hidden="true" />
        </Show>
        <span class="tool-line-words">{p.words}</span>
        <span class="tool-line-time num">{time().text}</span>
      </button>
      <Show when={open()}>
        <ul
          class="tool-calls"
          aria-label="Calls"
          onKeyDown={e => {
            if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
            e.preventDefault();
            const rows = [...e.currentTarget.querySelectorAll<HTMLElement>('.tool-call')];
            const i = rows.indexOf(document.activeElement as HTMLElement);
            rows[(i + (e.key === 'ArrowDown' ? 1 : rows.length - 1)) % rows.length]?.focus();
          }}
        >
          <For each={p.calls}>
            {c => (
              <li class={['tool-call', { failed: !!c.failed }]} tabindex={-1}>
                <span class="tool-call-name">{c.name}</span>
                <span class="tool-call-target">{c.target}{c.n && c.n > 1 ? ` ×${c.n}` : ''}</span>
                <span class="num">{duration(c.secs).text}</span>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
}
