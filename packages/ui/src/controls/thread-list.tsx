// Thread list: the work-item threads of a conversation (Conversations v2, owner pick b2; board drafts/conversations-v2.bl
// frames A and C; docs/designs/fleet-conversations.md#work-item-threads). Open threads first, then parked ones, each
// newest first; the timers' quiet checks fold into one row at the foot.
import { createMemo, For, Show } from 'solid-js';
import { Ago, Icon, StateBadge } from './primitives.tsx';
import { states, type Family } from '../registry.ts';

export type ThreadItem = {
  /** task:<id> | question:<id> | execution:<id> | delivery:<id> | fleet | fleet:<kind>; "" is the main thread. */
  key: string; title: string; code?: string | null; itemState?: string | null;
  lastActivityAt?: number | null; turns: number; open: boolean; unread: number;
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
      <button type="button" class={['thread-item', { on: p.selected, parked: !p.t.open }]} aria-current={p.selected ? 'true' : undefined} onClick={() => p.onSelect(p.t.key)}>
        <span class="thread-item-top">
          <span class="thread-item-title">{p.t.title || (p.t.key === '' ? 'Main thread' : p.t.key)}</span>
          <Show when={p.t.unread > 0}><span class="thread-item-new">{p.t.unread} new</span></Show>
          <Show when={p.t.lastActivityAt}>{at => <Ago t={at()} />}</Show>
        </span>
        <span class="thread-item-foot">
          <Show when={p.t.code}><span class="thread-item-code">{p.t.code}</span></Show>
          <Show when={known()}><StateBadge family={family()! as 'task'} state={p.t.itemState as never} /></Show>
          <Show when={!known() && p.t.itemState}><span class="muted">{p.t.itemState}</span></Show>
        </span>
      </button>
    </li>
  );
}

/**
 * The threads as a list: Open (a queued or running turn, or an unfinished item) and Parked, newest first within each; the
 * selected one is marked. `quiet` folds the timers' quiet checks into the foot row ("Every 5m · quiet checks").
 */
export function ThreadList(p: {
  threads: ThreadItem[]; selected: string; onSelect: (key: string) => void;
  quiet?: { schedule: string; text: string };
}) {
  const byTime = (a: ThreadItem, b: ThreadItem) => (b.lastActivityAt ?? 0) - (a.lastActivityAt ?? 0);
  const open = createMemo(() => p.threads.filter(t => t.open).sort(byTime));
  const parked = createMemo(() => p.threads.filter(t => !t.open).sort(byTime));
  return (
    <nav class="thread-list" data-ui="thread-list" aria-label="Threads">
      <div class="thread-list-body">
        <Show when={open().length}>
          <h3>Open</h3>
          <ul><For each={open()}>{t => <Row t={t} selected={t.key === p.selected} onSelect={p.onSelect} />}</For></ul>
        </Show>
        <Show when={parked().length}>
          <h3>Parked</h3>
          <ul><For each={parked()}>{t => <Row t={t} selected={t.key === p.selected} onSelect={p.onSelect} />}</For></ul>
        </Show>
      </div>
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
