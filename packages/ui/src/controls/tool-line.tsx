// Tool line: consecutive tool calls and system events between two messages as one quiet line of chips, one chip per
// kind of call (controls/tool-line.md, OR-52; Conversations v2, owner pick c2). It is the folded form of the thread's
// tool rows in the one session view.
import { createMemo, createSignal, For, Show, untrack } from 'solid-js';
import { duration } from '../format.ts';
import { Icon, useTick } from './primitives.tsx';
import { toolChips, type ToolKind } from './tool-fold.ts';

/** One call inside a line; repeats aggregate as ×N (OR-58). input and output are the harness's own values. */
export type ToolCall = {
  name: string; target: string; secs: number; n?: number; failed?: boolean; running?: boolean;
  kind?: ToolKind; error?: string; input?: unknown; output?: unknown;
  /** The call's id for tool_output: input and output load when the call opens. */
  callId?: string;
};
export type ToolCallIO = { input: unknown; output: unknown };

const raw = (v: unknown) => typeof v === 'string' ? v : JSON.stringify(v, null, 1) ?? '';

/**
 * Chips, one per kind of call (count, failures, time), then the line's total time. A chip opens its calls below the line
 * on a sunken block, opening downward (OR-23); a call opens its input and output. Running: a running dot leads, the
 * running chip is marked and the time ticks from `startedAt`. Failed (not recovered): the line turns danger and starts
 * with the failed kind open; a failure the agent recovered from only shows in its chip's count.
 */
export function ToolLine(p: {
  words: string; calls: ToolCall[]; state?: 'done' | 'running' | 'failed';
  /** Total seconds when done or failed; the start time while running. */
  secs?: number; startedAt?: number;
  /** Starts with a chip open: the failed kind, else the first (the book, a deep link to a call). */
  open?: boolean; onToggle?: (open: boolean) => void;
  /** Loads an opened call's input and output by its callId (tool_output) when the line carries summaries only. */
  loadCall?: (callId: string) => Promise<ToolCallIO>;
}) {
  const chips = createMemo(() => toolChips(p.calls));
  const first = untrack(() => !p.open ? null : (chips().find(c => c.failed)?.kind ?? chips()[0]?.kind ?? null));
  const [openKind, setOpenKind] = createSignal<ToolKind | null>(first);
  const [openCall, setOpenCall] = createSignal<ToolCall | null>(null);
  const running = () => p.state === 'running';
  const now = useTick(1000, running);
  const time = createMemo(() => duration(running() && p.startedAt !== undefined ? Math.max(0, (now() - p.startedAt) / 1000) : p.secs ?? 0));
  const toggle = (k: ToolKind) => {
    const was = openKind() !== null, next = openKind() === k ? null : k;
    setOpenKind(next); setOpenCall(null);
    if (was !== (next !== null)) p.onToggle?.(next !== null); // switching chips keeps the line open
  };
  const shown = createMemo(() => chips().find(c => c.kind === openKind()));
  // Loaded input and output per callId; an error shows in place of them.
  const [io, setIO] = createSignal<Record<string, ToolCallIO | { error: string } | 'loading'>>({});
  const openOne = (call: ToolCall) => {
    const next = openCall() === call ? null : call;
    setOpenCall(next);
    const id = next?.callId;
    if (!next || !id || next.input != null || next.output != null || !p.loadCall || io()[id]) return;
    setIO(m => ({ ...m, [id]: 'loading' }));
    p.loadCall(id).then(r => setIO(m => ({ ...m, [id]: r })), e => setIO(m => ({ ...m, [id]: { error: e instanceof Error ? e.message : String(e) } })));
  };
  return (
    <div class={['tool-line', p.state ?? 'done', { open: !!shown() }]} data-ui="tool-line" role="group" aria-label={`${p.words}, ${time().long}`}>
      <div class="tool-line-head">
        <Show when={running()}><i class="tool-line-dot" aria-hidden="true" /></Show>
        <For each={chips()}>
          {c => (
            <button
              type="button"
              class={['tool-chip', { on: openKind() === c.kind, failed: c.failed > 0, running: c.running }]}
              aria-expanded={openKind() === c.kind ? 'true' : 'false'}
              aria-label={`${c.label}: ${c.n} ${c.n === 1 ? 'call' : 'calls'}${c.failed ? `, ${c.failed} failed` : ''}, ${duration(c.secs).long}`}
              onClick={() => toggle(c.kind)}
            >
              <Icon name={c.icon} />
              <span class="tool-chip-label">{c.label}</span>
              <span class="num">{c.n}</span>
              <Show when={c.failed}><span class="tool-chip-failed">{c.failed} failed</span></Show>
              <Show when={c.secs >= 0.5}><span class="num tool-chip-time">{duration(c.secs).text}</span></Show>
            </button>
          )}
        </For>
        <span class="tool-line-time num">{time().text}</span>
      </div>
      <Show when={shown()}>
        {c => (
          <div class="tool-calls-box">
            <div class="tool-calls-head">
              <b>{c().label}</b>
              <span>{c().n} {c().n === 1 ? 'call' : 'calls'}{c().failed ? ` · ${c().failed} failed` : ''} · {duration(c().secs).text}</span>
            </div>
            <ul
              class="tool-calls"
              aria-label={`${c().label} calls`}
              onKeyDown={e => {
                if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
                e.preventDefault();
                const rows = [...e.currentTarget.querySelectorAll<HTMLElement>('.tool-call')];
                const i = rows.indexOf(document.activeElement as HTMLElement);
                rows[(i + (e.key === 'ArrowDown' ? 1 : rows.length - 1)) % rows.length]?.focus();
              }}
            >
              <For each={c().calls}>
                {call => {
                  const isOpen = () => openCall() === call;
                  const hasIO = call.input != null || call.output != null || (!!call.callId && !!p.loadCall);
                  const got = () => { const x = call.callId ? io()[call.callId] : undefined; return x && x !== 'loading' && !('error' in x) ? x : { input: call.input, output: call.output }; };
                  const state = () => (call.callId ? io()[call.callId] : undefined);
                  return (
                    <li class={['tool-call', { failed: !!call.failed, open: isOpen() }]}>
                      <button type="button" class="tool-call-row" disabled={!hasIO} aria-expanded={hasIO ? (isOpen() ? 'true' : 'false') : undefined} onClick={() => openOne(call)}>
                        <span class="tool-call-name">{call.name}</span>
                        <span class="tool-call-target">{call.target}{call.n && call.n > 1 ? ` ×${call.n}` : ''}</span>
                        <Show when={call.error}><span class="tool-call-error">{call.error}</span></Show>
                        <span class="num">{duration(call.secs).text}</span>
                      </button>
                      <Show when={isOpen()}>
                        <div class="tool-call-io" aria-busy={state() === 'loading' ? 'true' : undefined}>
                          <Show when={state() === 'loading'}><span class="label">Loading…</span></Show>
                          <Show when={(() => { const x = state(); return x && x !== 'loading' && 'error' in x ? x.error : null; })()}>{e => <span class="tool-call-error">Not loaded: {e()}</span>}</Show>
                          <Show when={got().input != null}><span class="label">Input</span><pre>{raw(got().input)}</pre></Show>
                          <Show when={got().output != null}><span class="label">Output</span><pre>{raw(got().output)}</pre></Show>
                        </div>
                      </Show>
                    </li>
                  );
                }}
              </For>
            </ul>
          </div>
        )}
      </Show>
    </div>
  );
}
