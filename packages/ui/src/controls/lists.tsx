// List filtering, state counters, saved views, grouping/sorting, row facts, and selection controls.
import { createEffect, createMemo, createSignal, For, onSettled, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Input, Menu, MenuContent, MenuGroupLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from '@ui-sdk/solid';
import { type IconName, type Family, type StateOf, type StateDef, type Tone, type Harness, states, actions, harnesses } from '../registry.ts';
import { isSubmitEnter } from '../keys.ts';
import { exact } from '../format.ts';

import { Icon, Button, SearchBox, focusOnMount, AgeChip, HarnessMark } from './primitives.tsx';
import { ContextMenu, type MenuItem } from './overlays.tsx';

/* ---------- Filter bar: search with query syntax, applied-filter chips and the list's controls (§4, OR-31, OR-36) ---------- */

export type Chip = { field: string; value: string };
type FilterBarProps = {
  query: string; onQuery: (q: string) => void; placeholder: string;
  /** Filters applied from outside the query (a link from another screen); each removes on its own. */
  chips?: Chip[]; onRemove?: (c: Chip) => void; onClear?: () => void;
  /** After the search, on the same wrapping line: saved views. */
  extra?: JSX.Element;
  /** The state quick filter (Σ and the state chips), on a line of its own. */
  counters?: JSX.Element;
  /** List tools after the state quick filter, on a line of their own: the selection toggle, group and sort. */
  tools?: JSX.Element;
  /** While selection mode is on: the selection bar, on its own line under the tools (nothing it shows covers them). */
  selection?: JSX.Element;
  shown: number; total: number;
};
export function FilterBar(p: FilterBarProps) {
  const chips = createMemo(() => p.chips ?? []);
  return (
    <div class="fbar" data-ui="filter-bar">
      <div class="fbar-line">
        <SearchBox label={p.placeholder} value={p.query} onInput={p.onQuery} />
        {p.extra}
        <For each={chips()}>
          {c => (
            <span class="chip">
              {c.field}: {c.value}
              <button type="button" aria-label={`Remove filter ${c.field}: ${c.value}`} onClick={() => p.onRemove?.(c)}>
                <Icon name={actions.clear} />
              </button>
            </span>
          )}
        </For>
        <Show when={chips().length > 0 && p.onClear}>
          <Button label="Clear" kind="quiet" onClick={() => p.onClear?.()} />
        </Show>
        <span class="count" aria-live="polite">
          {p.shown < p.total ? `${p.shown.toLocaleString('en')} of ${p.total.toLocaleString('en')}` : ''}
        </span>
      </div>
      <Show when={p.counters} keyed>{c => <div class="fbar-line">{c}</div>}</Show>
      <Show when={p.tools} keyed>{t => <div class="fbar-line">{t}</div>}</Show>
      <Show when={p.selection} keyed>{s => <div class="fbar-line">{s}</div>}</Show>
    </div>
  );
}

/* ---------- State counters: quick filter by state with totals (state-counters.md) ---------- */

export function StateCounters<F extends Family>(p: {
  family: F; counts: Partial<Record<StateOf<F>, number | null>>; active: StateOf<F>[]; onToggle: (s: StateOf<F>) => void;
  /** Σ button: show all (clear the state filter). Omit to hide the Σ button. */
  onAll?: () => void; label: string;
  /** A newer answer is loading: the last counts stay, dimmed (docs/designs/fleet-view-urls-and-list-path.md §2). */
  pending?: boolean;
  /** Whether the total has been confirmed by a read. */
  unknown?: boolean;
  /** A counter block, not a quick filter (OR-29): zero chips are hidden, and the caller hides an all-zero block. */
  hideZero?: boolean;
}) {
  const all = createMemo(() => Object.keys(states[p.family]) as StateOf<F>[]);
  const total = createMemo(() => {
    const values = all().map(s => p.counts[s]).filter(n => n !== undefined) as (number | null)[];
    return p.unknown || values.includes(null) ? null : (values as number[]).reduce((a, b) => a + b, 0);
  });
  return (
    <div class={['counters', { pending: !!p.pending }]} data-ui="state-counters" role="group" aria-label={p.label} aria-busy={p.pending ? 'true' : undefined}>
      <Show when={p.onAll} keyed>
        {onAll => (
          <button
            type="button"
            class={['counter t-neutral sigma', { on: p.active.length === 0 }]}
            aria-pressed={p.active.length === 0 ? 'true' : 'false'}
            aria-label={`All: ${total() === null ? 'unknown' : total()}`}
            onClick={onAll}
          >
            <span class="sig" aria-hidden="true">Σ</span>
            <span class="num">{total() === null ? '?' : total()!.toLocaleString('en')}</span>
          </button>
        )}
      </Show>
      {/* OR-29: a chip with 0 shows only its icon; its accessible name still says 0. */}
      <For each={all()}>
        {s => {
          const d = createMemo(() => states[p.family][s] as StateDef);
          const n = createMemo(() => p.counts[s]);
          const on = createMemo(() => p.active.includes(s));
          return (
            <Show when={n() !== undefined && !(p.hideZero && n() === 0)}>
              <button
                type="button"
                class={['counter', `t-${d().tone}`, { on: on(), zero: n() === 0 }]}
                aria-pressed={on() ? 'true' : 'false'}
                aria-label={`${d().label}: ${n() === null ? 'unknown' : n()}`}
                data-tip={d().label}
                onClick={() => p.onToggle(s)}
              >
                <Icon name={d().icon} />
                <Show when={n() !== 0}>
                  <span class="num">{n() === null ? '?' : n()!.toLocaleString('en')}</span>
                </Show>
              </button>
            </Show>
          );
        }}
      </For>
    </div>
  );
}

/* ---------- Group header row for grouped lists ---------- */

export function GroupHeader(p: { icon: IconName; label: string; count: number; open: boolean; onToggle: () => void; tone?: Tone }) {
  return (
    <button type="button" class={['group-h', `t-${p.tone ?? 'neutral'}`]} aria-expanded={p.open ? 'true' : 'false'} onClick={() => p.onToggle()}>
      <span class={['chev', { open: p.open }]}><Icon name="chevron-down" /></span>
      <Icon name={p.icon} />
      <b>{p.label}</b>
      <span class="num">{p.count.toLocaleString('en')}</span>
    </button>
  );
}

/* ---------- Saved views: system presets and the person's own (OR-33) ---------- */

export function Presets(p: {
  /** Views every person has (Tasks "Running"); they cannot be removed. */
  system?: string[];
  /** Optional tooltip and accessible description per system preset to clarify its scope. */
  systemTips?: Record<string, string>;
  presets: string[]; active: string | null; onPick: (name: string) => void; onSave: (name: string) => void; onUnpin: (name: string) => void;
  /**
   * The view the section's navigation counter counts (nav-rail.md, Counters), null when the counter is off. With onCount,
   * each view's menu (right click, Menu key or Shift+F10) offers to count it, or to stop counting it.
   */
  counted?: string | null; onCount?: (name: string | null) => void;
}) {
  const [naming, setNaming] = createSignal<string | null>(null);
  const [menu, setMenu] = createSignal<{ name: string; at: { x: number; y: number } } | null>(null);
  const closeMenu = () => setMenu(null);
  const onContext = (n: string, e: MouseEvent) => {
    if (!p.onCount || n === '-') return;
    e.preventDefault();
    setMenu({ name: n, at: { x: e.clientX, y: e.clientY } });
  };
  const onKey = (n: string, e: KeyboardEvent & { currentTarget: HTMLButtonElement }) => {
    if (!p.onCount || n === '-') return;
    if (e.key !== 'ContextMenu' && !(e.shiftKey && e.key === 'F10')) return;
    e.preventDefault();
    const r = e.currentTarget.getBoundingClientRect();
    setMenu({ name: n, at: { x: r.left, y: r.bottom } });
  };
  const submitName = () => {
    const v = naming()?.trim();
    if (v) p.onSave(v);
    setNaming(null);
  };
  return (
    <div class="presets" data-ui="saved-views" role="group" aria-label="Views">
      <For each={p.system ?? []}>
        {n => {
          const tip = () => p.systemTips?.[n];
          return (
            <span class={['preset', { on: n === p.active }]}>
              <button
                type="button"
                aria-pressed={n === p.active ? 'true' : 'false'}
                data-tip={tip()}
                aria-description={tip()}
                aria-keyshortcuts={p.onCount && n !== '-' ? 'Shift+F10' : undefined}
                onClick={() => p.onPick(n)}
                onContextMenu={e => onContext(n, e)}
                onKeyDown={e => onKey(n, e)}
              >
                {n}
              </button>
            </span>
          );
        }}
      </For>
      <For each={p.presets}>
        {n => (
          <span class={['preset', { on: n === p.active }]}>
            <button
              type="button"
              aria-pressed={n === p.active ? 'true' : 'false'}
              aria-keyshortcuts={p.onCount && n !== '-' ? 'Shift+F10' : undefined}
              onClick={() => p.onPick(n)}
              onContextMenu={e => onContext(n, e)}
              onKeyDown={e => onKey(n, e)}
            >
              <Icon name="pin" />{n}
            </button>
            <button type="button" class="x" aria-label={`Remove view ${n}`} data-tip="Remove" onClick={() => p.onUnpin(n)}>
              <Icon name="x" />
            </button>
          </span>
        )}
      </For>
      <Show
        when={naming() !== null}
        fallback={<Button label="Save view" icon="save" iconOnly kind="quiet" onClick={() => setNaming('')} />}
      >
        <span class="naming">
          <Input
            value={naming() ?? ''}
            placeholder="View name"
            aria-label="View name"
            ref={focusOnMount}
            onValueChange={v => setNaming(v)}
            onKeyDown={e => {
              if (isSubmitEnter(e)) submitName();
              if (e.key === 'Escape') setNaming(null);
            }}
          />
          <Button label="Save" kind="primary" onClick={submitName} />
          <Button label="Cancel" icon={actions.clear} iconOnly kind="quiet" onClick={() => setNaming(null)} />
        </span>
      </Show>
      <Show when={p.onCount ? menu() : null} keyed>
        {m => (
          <ContextMenu
            at={m.at}
            label={m.name}
            onClose={closeMenu}
            items={[p.counted === m.name
              ? { label: 'Stop counting in navigation', icon: 'circle-slash', onClick: () => p.onCount!(null) }
              : { label: 'Count in navigation', icon: 'list-ordered', onClick: () => p.onCount!(m.name) }]}
          />
        )}
      </Show>
    </div>
  );
}

/* ---------- Group and sort: shared list controls, part of a saved view (OR-35) ---------- */

/**
 * Group by (OR-35): one chip with the registry icon and the current grouping; a click opens the choices in a short menu
 * and the line never changes. The cross removes the grouping in one click; with no grouping only the icon shows.
 */
export function GroupControl<G extends string>(p: {
  value: G; options: readonly (readonly [G, string])[]; onChange: (g: G) => void;
  /** The option key that means no grouping; without it in the options there is no cross and no "No grouping". */ none?: G;
}) {
  let wrap: HTMLSpanElement | undefined;
  const none = createMemo(() => p.none ?? ('none' as G));
  const canNone = createMemo(() => p.options.some(([g]) => g === none()));
  const on = createMemo(() => !canNone() || p.value !== none());
  const cur = createMemo(() => p.options.find(([g]) => g === p.value)?.[1] ?? p.value);
  const nonNone = createMemo(() => p.options.filter(([g]) => g !== none()));
  return (
    <span ref={el => { wrap = el; }} class="pick" data-ui="group-control">
      <Menu container={wrap?.closest<HTMLElement>('.th-dark, .th-light') ?? undefined}>
        <MenuTrigger
          class={['pick-b', { on: on() }]}
          aria-label={on() ? `Group by ${cur()}` : 'Group by: none'}
          data-tip="Group by"
        >
          <Icon name={actions.group} />
          <Show when={on()}><span>{cur()}</span></Show>
          <Icon name="chevron-down" />
        </MenuTrigger>
        <MenuContent class="menu pick-menu" side="bottom" align="start" aria-label="Group by">
          <MenuRadioGroup value={p.value} onValueChange={v => p.onChange(v as G)}>
            <MenuGroupLabel class="menu-h">Group by</MenuGroupLabel>
            <For each={nonNone()}>
              {([g, l]) => (
                <MenuRadioItem value={g} closeOnSelect render={itemProps => <button {...itemProps} type="button" />}>
                  <span class="grow">{l}</span>
                  <Show when={g === p.value}><Icon name="check" /></Show>
                </MenuRadioItem>
              )}
            </For>
            <Show when={canNone()}>
              <MenuSeparator class="menu-sep" />
              <MenuRadioItem value={none()} closeOnSelect render={itemProps => <button {...itemProps} type="button" />}>
                <span class="grow">No grouping</span>
                <Show when={!on()}><Icon name="check" /></Show>
              </MenuRadioItem>
            </Show>
          </MenuRadioGroup>
        </MenuContent>
      </Menu>
      <Show when={on() && canNone()}>
        <button type="button" class="pick-x" aria-label="Remove grouping" data-tip="No grouping" onClick={() => p.onChange(none())}>
          <Icon name="x" />
        </button>
      </Show>
    </span>
  );
}

/**
 * Sort (OR-35): one chip led by the direction icon, a button that flips the direction in one click (as Group leads with
 * its icon), then the field, which opens the fields and the directions in a short menu. The value is the field, "-" before it for descending.
 * A field may name its directions (Newest first / Oldest first for times, the default).
 */
/** A sort field: value, name, direction names (descending, ascending; Newest first, Oldest first by default), and what the rows show:
 *  'age' (default: a moment shown as 4m, 3d) or 'value' (a number or text shown as it is). */
export type SortField = readonly [string, string] | readonly [string, string, readonly [string, string]] | readonly [string, string, readonly [string, string], 'age' | 'value'];
export function SortControl(p: { value: string; fields: readonly SortField[]; onChange: (v: string) => void }) {
  let wrap: HTMLSpanElement | undefined;
  const desc = createMemo(() => p.value.startsWith('-'));
  const field = createMemo(() => desc() ? p.value.slice(1) : p.value);
  const f = createMemo(() => p.fields.find(x => x[0] === field()) ?? p.fields[0]);
  const dirs = createMemo(() => (f()[2] ?? ['Newest first', 'Oldest first']) as readonly [string, string]);
  const dDesc = () => dirs()[0];
  const dAsc = () => dirs()[1];
  const ages = createMemo(() => (f()[3] ?? 'age') === 'age');
  const iconOf = (d: boolean) => (d !== ages()) ? actions.sortDesc : actions.sortAsc;

  return (
    <span ref={el => { wrap = el; }} class="pick" data-ui="sort-control">
      <button
        type="button"
        class="pick-x pick-dir"
        aria-label={desc() ? dDesc() : dAsc()}
        data-tip={`${desc() ? dDesc() : dAsc()}; click for ${(desc() ? dAsc() : dDesc()).toLowerCase()}`}
        onClick={() => p.onChange(desc() ? f()[0] : '-' + f()[0])}
      >
        <Icon name={iconOf(desc())} />
      </button>
      <Menu container={wrap?.closest<HTMLElement>('.th-dark, .th-light') ?? undefined}>
        <MenuTrigger class="pick-b on" aria-label={`Sort by ${f()[1]}`} data-tip="Sort by">
          <span>{f()[1]}</span>
          <Icon name="chevron-down" />
        </MenuTrigger>
        <MenuContent class="menu pick-menu" side="bottom" align="start" aria-label="Sort">
          <MenuRadioGroup value={f()[0]} onValueChange={v => p.onChange((desc() ? '-' : '') + v)}>
            <MenuGroupLabel class="menu-h">Sort by</MenuGroupLabel>
            <For each={p.fields}>
              {x => (
                <MenuRadioItem value={x[0]} closeOnSelect render={itemProps => <button {...itemProps} type="button" />}>
                  <span class="grow">{x[1]}</span>
                  <Show when={x[0] === f()[0]}><Icon name="check" /></Show>
                </MenuRadioItem>
              )}
            </For>
          </MenuRadioGroup>
          <MenuSeparator class="menu-sep" />
          <MenuRadioGroup value={desc() ? 'desc' : 'asc'} onValueChange={v => p.onChange(v === 'desc' ? '-' + f()[0] : f()[0])}>
            <MenuRadioItem value="desc" closeOnSelect render={itemProps => <button {...itemProps} type="button" />}>
              <Icon name={iconOf(true)} />
              <span class="grow">{dDesc()}</span>
              <Show when={desc()}><Icon name="check" /></Show>
            </MenuRadioItem>
            <MenuRadioItem value="asc" closeOnSelect render={itemProps => <button {...itemProps} type="button" />}>
              <Icon name={iconOf(false)} />
              <span class="grow">{dAsc()}</span>
              <Show when={!desc()}><Icon name="check" /></Show>
            </MenuRadioItem>
          </MenuRadioGroup>
        </MenuContent>
      </Menu>
    </span>
  );
}

/* ---------- Row facts (OR-37) ---------- */

/** Live facts of a row in short form, in the row's right zone (OR-21). Each fact is icon + compact value with a tooltip. */
export function RowFacts(p: { children: JSX.Element }) {
  return <span class="row-facts" data-ui="row-facts">{p.children}</span>;
}
export function Fact(p: { icon?: IconName; text?: string; tip: string; label?: string; children?: JSX.Element }) {
  return (
    <span class="fact" data-tip={p.tip} aria-label={p.children ? undefined : p.label ?? p.tip}>
      <Show when={p.icon} keyed>{ic => <Icon name={ic} />}</Show>
      {p.text}
      {p.children}
    </span>
  );
}

/* ---------- Work facts: the shared row anatomy of work in progress (controls/work-facts.md, OR-37, OR-63) ---------- */

/** Starts closer than this are one moment: the same fact is shown once. */
const SAME_MOMENT_MS = 60_000;

export type WorkFactsProps = {
  /** Facts of the row that are not work facts (codes, size, resources): before the work facts, so the time stays last. */
  before?: JSX.Element;
  /** Attempts so far (executions of a task, starts of a failing cause). `×N` from two: one attempt says nothing. */
  attempts?: number;
  /** The peers' fence for attempts (peerFence in format.ts): at or above it `×N` takes the attention tone. */
  attemptsFence?: number;
  /** The tooltip of `×N` when it is not "N attempts" (a run: "Attempt N of its task"). */
  attemptsTip?: string;
  /** Since when the item waits for the person (the oldest open question): the time on the row, before any other time. */
  waitAt?: number;
  /** The peers' fence for the waiting time, in seconds (peerFence). */
  waitFence?: number;
  /** Start of the latest attempt. In the tooltip of the time in work; on the row only when there is no time in work. */
  lastAt?: number;
  /** What the latest attempt is called: "Latest attempt" by default, "Latest failed start" for a failing slot. */
  lastLabel?: string;
  /** Start of the first attempt: the total time in work, on the row whenever known. */
  workAt?: number;
  /** The peers' fence for the time in work, in seconds (peerFence): at or above it the time takes the attention tone. */
  workFence?: number;
  /** The tooltip of the time in work when it is not since the first attempt (a run: "Running for"). */
  workTip?: string;
  /** The current state and since when: in the tooltip of the time in work ("<State> since …"). */
  state?: { label: string; at: number };
  /** The agent: its mark, the name in the tooltip. */
  agent?: Harness | null;
  /** A time the row shows in place of the work facts' own (an ended run's duration); still the last fact. */
  time?: JSX.Element;
  /** Server clock minus local clock, for ages read from the server. */
  clockOffsetMs?: number;
};

/**
 * Work facts in the row's right zone, quiet form (owner pick B, 25 Nov): `×N` attempts, the agent mark, then the time,
 * always the rightmost fact so times line up in one column down the list (owner, 25 Nov). The time is the wait for the
 * person when the item waits (hourglass), else the time in work (clock); the latest attempt and the time in the current
 * state are in its tooltip and in the item's detail. A row with no time in work shows the latest attempt instead (a
 * failing slot), else the time in its state (a finished task). `×N` from two attempts; a moment equal to the start of
 * work (within a minute) is left out of the tooltip. Outliers stand out against their peers, never against a fixed
 * number (peerFence).
 */
export function WorkFacts(p: WorkFactsProps) {
  const fin = (t: number | undefined): t is number => t !== undefined && Number.isFinite(t);
  const same = (a: number | undefined, b: number | undefined) => fin(a) && fin(b) && Math.abs(a - b) < SAME_MOMENT_MS;
  const n = () => p.attempts ?? 0;
  const nOut = () => p.attemptsFence !== undefined && n() > 1 && n() >= p.attemptsFence;
  const note = () => [
    fin(p.lastAt) && !same(p.lastAt, p.workAt) ? `${(p.lastLabel ?? 'Latest attempt').toLowerCase()} ${exact(p.lastAt)}` : '',
    p.state && fin(p.state.at) && !same(p.state.at, p.workAt) ? `${p.state.label} since ${exact(p.state.at)}` : '',
  ].filter(Boolean).join(' · ') || undefined;
  const nTip = () => p.attemptsTip ?? `${n().toLocaleString('en')} attempts`;
  return (
    <RowFacts>
      {p.before}
      <Show when={n() > 1}>
        <span class={['fact', 'attempts', nOut() ? 't-attention alert' : '']} data-tip={`${nTip()}${nOut() ? ', ten times the usual row or more' : ''}`}
          aria-label={nTip()}>×{n().toLocaleString('en')}</span>
      </Show>
      <Show when={p.agent && p.agent in harnesses}><HarnessMark h={p.agent!} /></Show>
      <Show when={!p.time} fallback={p.time}>
        <Show when={fin(p.waitAt)} fallback={
          <Show when={fin(p.workAt)} fallback={
            <Show when={fin(p.lastAt)} fallback={
              <Show when={p.state && fin(p.state.at)}>
                <Fact tip={`${p.state!.label} for`}><AgeChip t={p.state!.at} clockOffsetMs={p.clockOffsetMs} /></Fact>
              </Show>
            }>
              <Fact icon={actions.runs} tip={p.lastLabel ?? 'Latest attempt'}><AgeChip t={p.lastAt!} clockOffsetMs={p.clockOffsetMs} /></Fact>
            </Show>
          }>
            <Fact icon="clock" tip={p.workTip ?? 'In work since the first attempt'}>
              <AgeChip t={p.workAt!} clockOffsetMs={p.clockOffsetMs} alertAfter={p.workFence} tone="attention" alertLabel="ten times longer in work than the usual row or more" note={note()} />
            </Fact>
          </Show>
        }>
          <Fact icon="hourglass" tip="Waiting for you">
            <AgeChip t={p.waitAt!} clockOffsetMs={p.clockOffsetMs} alertAfter={p.waitFence} tone="attention" alertLabel="waiting ten times longer than the usual row or more" />
          </Fact>
        </Show>
      </Show>
    </RowFacts>
  );
}

/* ---------- Selection mode: a checkbox column and the action bar (OR-37) ---------- */

/**
 * The selection bar: its own line under the list tools while selection mode is on (FilterBar selection); group and sort
 * stay. The box selects every loaded row or none; actions apply to the selected rows; Esc or the cross leaves the mode.
 */
export function SelectBar(p: {
  count: number; total: number; onAll: () => void; onNone: () => void; actions: MenuItem[]; onExit: () => void;
}) {
  onSettled(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !e.defaultPrevented && !(e.target as Element).closest?.('input,textarea,[role=menu]')) p.onExit();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  });
  const all = createMemo(() => p.total > 0 && p.count >= p.total);
  let checkEl: HTMLInputElement | undefined;
  createEffect(() => [p.count, all()] as const, ([cnt, isAll]) => {
    if (checkEl) checkEl.indeterminate = cnt > 0 && !isAll;
  });
  // Left to right: select all (in the row checkbox column), leave, the count; the actions on the right.
  return (
    <div class="select-bar" data-ui="select-bar" role="toolbar" aria-label="Selection">
      <input
        ref={el => { checkEl = el; }}
        type="checkbox"
        class="row-check"
        checked={all()}
        aria-label={all() ? 'Select none' : 'Select all'}
        data-tip={all() ? 'Select none' : 'Select all'}
        onChange={() => all() ? p.onNone() : p.onAll()}
      />
      <Button label="Leave selection" icon="x" iconOnly kind="quiet" onClick={() => p.onExit()} />
      <span aria-live="polite"><span class="num">{p.count.toLocaleString('en')}</span> selected</span>
      <span class="grow" />
      <For each={p.actions}>
        {a => (
          <Button
            label={a.label}
            icon={a.icon}
            iconOnly
            kind={a.danger ? 'danger' : 'quiet'}
            disabledReason={p.count === 0 ? 'Select rows first' : a.disabledReason}
            onClick={a.onClick}
          />
        )}
      </For>
    </div>
  );
}

/** Turns selection mode on: a checkbox-shaped button at the left of the list tools, above the checkbox column it opens. */
export function SelectToggle(p: { on: boolean; onClick: () => void }) {
  return <span class="sel-toggle"><Button label="Select" icon="square" iconOnly kind="quiet" pressed={p.on} onClick={p.onClick} /></span>;
}

/* ---------- Row check: one checked row of a list, kept in the view URL as sel (fleet-view-urls-and-list-path.md §1) ---------- */

export function RowCheck(p: { checked: boolean; label: string; onToggle: () => void }) {
  return (
    <input
      type="checkbox"
      class="row-check"
      data-ui="row-check"
      checked={p.checked}
      aria-label={p.label}
      onClick={e => e.stopPropagation()}
      onChange={() => p.onToggle()}
    />
  );
}

/* ---------- Row pin: the pin mark of a pinned row, before its state icon (OR-12, OR-37) ---------- */

/**
 * On a pinned row, the first cell; the caller gives that row the class pin-row, which adds the one leading column
 * (styles.css), so other rows do not move. Both go together: the cell without the class shifts every cell. A click unpins
 * and the row leaves the Pinned group. With `off`, the pin of a row not pinned yet, after its title: shown on row hover and
 * keyboard focus only (never on touch screens, where the row menu pins); a click pins (nav-rail.md, Pinned).
 */
export function RowPin(p: { label: string; onToggle: () => void; /** Not pinned yet: shown on row hover and focus. */ off?: boolean }) {
  return (
    <button
      type="button"
      class={['row-pin', { off: !!p.off }]}
      data-ui="row-pin"
      aria-label={`${p.off ? 'Pin' : 'Unpin'} ${p.label}`}
      data-tip={p.off ? 'Pin' : 'Unpin'}
      onClick={e => { e.stopPropagation(); p.onToggle(); }}
    >
      <Icon name="pin" />
    </button>
  );
}
