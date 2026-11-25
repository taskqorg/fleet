// Thread list: the work-item threads of a bot's Home conversation, nested under the bot's row in the Conversations list
// (Conversations v2, owner pick B, 2026-11-26; board screens/conversations.bl "Thread list v2";
// docs/designs/fleet-conversations.md#work-item-threads). Open, then Idle, then Archived (folded into one row), each
// newest first; the timers' quiet checks fold into one row at the foot.
import { createMemo, createSignal, For, Show, type Accessor } from 'solid-js';
import { clock } from '../format.ts';
import { Ago, Icon, StateBadge } from './primitives.tsx';
import { states, type Family } from '../registry.ts';

export type ThreadItem = {
  /** task:<id> | question:<id> | execution:<id> | delivery:<id> | fleet | fleet:<kind>; "" is the main thread. */
  key: string; title: string; code?: string | null; itemState?: string | null;
  lastActivityAt?: number | null; turns: number; open: boolean; unread: number;
};

/** A run's thread (a wake named by its trigger, a host job) adds when it ran, in the viewer's zone: "Conversation check · 22:14". */
const runTitle = (t: ThreadItem) => {
  const title = t.title || t.key;
  return t.key.startsWith('execution:') && t.lastActivityAt ? `${title} · ${clock(t.lastActivityAt)}` : title;
};

/** The item's family by its key, for its state badge; none for Fleet threads and the main thread. */
const familyOf = (key: string): Family | null => {
  const f = key.split(':')[0];
  return f === 'task' || f === 'question' || f === 'execution' || f === 'delivery' ? f : null;
};

function Row(p: { t: ThreadItem; selected: boolean; onSelect: (key: string) => void }) {
  const family = createMemo(() => familyOf(p.t.key));
  const known = createMemo(() => { const f = family(); return !!f && !!p.t.itemState && p.t.itemState in states[f]; });
  return (
    <li>
      <button type="button" class={['thread-item', { on: p.selected, idle: !p.t.open }]} aria-current={p.selected ? 'true' : undefined} onClick={() => p.onSelect(p.t.key)}>
        <span class="thread-item-top">
          <span class="thread-item-title">{p.t.key === '' ? 'Main · all turns before threads' : runTitle(p.t)}</span>
          <Show when={p.t.unread > 0}><span class="thread-item-new">{p.t.unread} new</span></Show>
          <Show when={p.t.lastActivityAt}>{at => <Ago t={at()} />}</Show>
        </span>
        <span class="thread-item-foot">
          <Show when={p.t.code}><span class="thread-item-code">{p.t.code}</span></Show>
          <Show when={p.t.key === ''}><span class="muted">{p.t.turns} turns</span></Show>
          <Show when={known()}><StateBadge family={family()! as 'task'} state={p.t.itemState as never} /></Show>
          <Show when={!known() && p.t.itemState}><span class="muted">{p.t.itemState}</span></Show>
        </span>
      </button>
    </li>
  );
}

/** A thread idle this long is archived: it folds into the Archived row until a new event brings it back to Open. */
export const ARCHIVE_AFTER_MS = 7 * 24 * 3600_000;

/**
 * The threads as a list: Open (a queued or running turn, or an unfinished item; an empty line says so when none is),
 * Idle (nothing waiting) and Archived (idle for ARCHIVE_AFTER_MS or more: one row "Archived · N" that opens them), newest
 * first within each; the selected one is marked. `quiet` folds the timers' quiet checks into the foot row.
 */
export function ThreadList(p: {
  threads: ThreadItem[]; selected?: string; onSelect: (key: string) => void;
  quiet?: { schedule: string; text: string }; now?: number;
}) {
  const byTime = (a: ThreadItem, b: ThreadItem) => (b.lastActivityAt ?? 0) - (a.lastActivityAt ?? 0);
  const old = (t: ThreadItem) => !t.open && (p.now ?? Date.now()) - (t.lastActivityAt ?? 0) >= ARCHIVE_AFTER_MS;
  const open = createMemo(() => p.threads.filter(t => t.open).sort(byTime));
  const idle = createMemo(() => p.threads.filter(t => !t.open && !old(t)).sort(byTime));
  const archived = createMemo(() => p.threads.filter(old).sort(byTime));
  const [showArchived, setShowArchived] = createSignal(false);
  // Keyed by the thread key: a live reread of the list keeps the rows (and a focused row) in place.
  const row = (t: Accessor<ThreadItem>) => <Row t={t()} selected={t().key === p.selected} onSelect={p.onSelect} />;
  const key = (t: ThreadItem) => t.key;
  return (
    <nav class="thread-list" data-ui="thread-list" aria-label="Threads">
      <h3>Open</h3>
      <Show when={open().length} fallback={<p class="thread-list-empty">No open threads: nothing queued or running</p>}>
        <ul><For each={open()} keyed={key}>{row}</For></ul>
      </Show>
      <Show when={idle().length}>
        <h3>Idle</h3>
        <ul><For each={idle()} keyed={key}>{row}</For></ul>
      </Show>
      <Show when={archived().length}>
        <button type="button" class="thread-list-archived" aria-expanded={showArchived() ? 'true' : 'false'} onClick={() => setShowArchived(v => !v)}>
          <Icon name="archive" /><span>Archived · {archived().length}</span><span class="muted">idle 7+ days, back to Open on a new event</span>
        </button>
        <Show when={showArchived()}><ul><For each={archived()} keyed={key}>{row}</For></ul></Show>
      </Show>
      <Show when={p.quiet}>
        {q => (
          <div class="thread-list-quiet">
            <Icon name="clock" />
            <span><b>{q().schedule}</b><span>{q().text}</span></span>
          </div>
        )}
      </Show>
    </nav>
  );
}
