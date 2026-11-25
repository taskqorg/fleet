// Semantic data tables (OR-25, modern-web-guidance responsive-table & sortable-table), tabs, and disclosures.
import { createEffect, createMemo, createSignal, For, onSettled, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Menu, MenuContent, MenuItem, MenuTrigger, Tabs, TabsList, TabsTab } from '@ui-sdk/solid';
import { Icon } from './primitives.tsx';

/**
 * Semantic table region that never scrolls sideways (OR-66, modern-web-guidance responsive-table): while its columns fit
 * the frame it is a table; when they do not, each row becomes a stacked block (`.stacked`): the row header on top, every
 * other cell as `label value`, the label taken from its column header. The table measures itself on resize and on new
 * rows, so a fixed breakpoint neither leaves a wide table overflowing nor stacks a narrow one that fits.
 */
export function DataTable(p: {
  label: string;
  head: JSX.Element;
  children: JSX.Element;
  cls?: string;
}) {
  let frame: HTMLDivElement | undefined;
  onSettled(() => {
    if (!frame || typeof ResizeObserver !== 'function') return;
    const f = frame;
    let queued = 0;
    // One measure per frame, however many rows or texts changed.
    const later = () => { if (!queued) queued = requestAnimationFrame(() => { queued = 0; fit(); }); };
    const fit = () => {
      const table = f.querySelector(':scope > table') as HTMLTableElement | null;
      if (!table) return;
      labelCells(table);
      f.classList.remove('stacked');
      if (table.getBoundingClientRect().width > f.clientWidth + 1) f.classList.add('stacked');
    };
    const ro = new ResizeObserver(fit);
    const mo = new MutationObserver(later);
    ro.observe(f);
    mo.observe(f, { childList: true, subtree: true, characterData: true });
    fit();
    return () => { ro.disconnect(); mo.disconnect(); cancelAnimationFrame(queued); };
  });
  return (
    <div ref={el => { frame = el; }} class="table-frame" data-ui="data-table" tabindex={0} role="region" aria-label={p.label}>
      <table class={['limit-table', p.cls]}>
        <thead>{p.head}</thead>
        <tbody>{p.children}</tbody>
      </table>
    </div>
  );
}

/** Gives each body cell its column header as `data-label` (colspans counted), for the stacked layout. */
function labelCells(table: HTMLTableElement) {
  const heads: string[] = [];
  for (const th of table.tHead?.rows[0]?.cells ?? []) for (let i = 0; i < th.colSpan; i++) heads.push(th.textContent?.trim() ?? '');
  for (const body of table.tBodies) for (const row of body.rows) {
    let col = 0;
    for (const cell of row.cells) {
      const label = cell.colSpan === 1 && cell.tagName === 'TD' ? heads[col] ?? '' : '';
      if ((cell.dataset.label ?? '') !== label) { if (label) cell.dataset.label = label; else delete cell.dataset.label; }
      col += cell.colSpan;
    }
  }
}

/**
 * One body row of a DataTable (controls/data-list.md#table-row): the whole row opens its item (OR-26). The name cell holds
 * the row's one focusable control, so the row is reached by Tab and opened with Enter; `selected` marks the open item.
 */
export function TableRow(p: { selected?: boolean; onOpen?: () => void; children: JSX.Element }) {
  return (
    <tr class={['t-row', { sel: !!p.selected, opens: !!p.onOpen }]} onClick={() => p.onOpen?.()}>
      {p.children}
    </tr>
  );
}

/** The name cell of a TableRow: state icon (OR-22), then the name as the row's button; it takes the free width (OR-21). */
/** `current` marks the open item for assistive technology (aria-current on the name button). */
export function TableName(p: { lead?: JSX.Element; label: string; onOpen?: () => void; current?: boolean }) {
  return (
    <th scope="row" class="t-name">
      <span class="t-name-in">
        {p.lead}
        <Show when={p.onOpen} keyed fallback={<span class="t-label">{p.label}</span>}>
          {open => <button type="button" class="t-label" aria-current={p.current ? 'true' : undefined} onClick={e => { e.stopPropagation(); open(); }}>{p.label}</button>}
        </Show>
      </span>
    </th>
  );
}

/** Sortable `<th scope="col">` header cell following modern-web-guidance sortable-table. */
export function SortColHead(p: {
  label: string;
  active: boolean;
  dir: 'ascending' | 'descending';
  numeric?: boolean;
  onSort: () => void;
}) {
  return (
    <th scope="col" class={p.numeric ? 'num' : undefined} aria-sort={p.active ? p.dir : undefined}>
      <button type="button" class="sort-btn" onClick={() => p.onSort()}>
        <span>{p.label}</span>
        <Show when={p.active}>
          <Icon name={p.dir === 'ascending' ? 'arrow-up-narrow-wide' : 'arrow-down-wide-narrow'} />
        </Show>
      </button>
    </th>
  );
}

/**
 * Accessible single-row tablist powered by @ui-sdk/solid Tabs (selection follows click/focus, arrow-key navigation).
 * The bar never wraps and never scrolls sideways (OR-25, tab-bar.md): tabs that do not fit fold into a last More menu,
 * and the current tab always stays in the bar.
 */
export function TabBar<T extends string>(p: {
  label: string;
  tabs: readonly { id: T; label: string; count?: number }[];
  value: T;
  onChange: (id: T) => void;
  cls?: string;
}) {
  let wrap: HTMLDivElement | undefined;
  const [fit, setFit] = createSignal(Infinity);
  let widths: number[] = [];
  const shown = createMemo(() => {
    const n = fit();
    if (n >= p.tabs.length) return p.tabs;
    const head = p.tabs.slice(0, n);
    const cur = p.tabs.find(t => t.id === p.value);
    if (cur && !head.includes(cur)) head[n - 1] = cur;
    return head;
  });
  const rest = createMemo(() => p.tabs.filter(t => !shown().includes(t)));
  const layout = () => {
    if (!wrap) return;
    const list = wrap.querySelector<HTMLElement>('[role=tablist]');
    if (!list) return;
    const tabs = [...list.querySelectorAll<HTMLElement>('[role=tab]')];
    if (tabs.length === p.tabs.length) widths = tabs.map(t => t.offsetWidth);
    if (widths.length !== p.tabs.length) return;
    const gap = parseFloat(getComputedStyle(list).columnGap) || 0;
    const room = wrap.clientWidth;
    const all = widths.reduce((a, b) => a + b, 0) + gap * (widths.length - 1);
    if (all <= room) { setFit(Infinity); return; }
    const more = (parseFloat(getComputedStyle(wrap).getPropertyValue('--size-clock')) || 64) + gap;
    let n = 0, used = 0;
    while (n < widths.length && used + widths[n] + (n ? gap : 0) + more <= room) used += widths[n] + (n++ ? gap : 0);
    setFit(Math.max(1, n));
  };
  // New tabs, labels or counts change the widths: show every tab again, measure on the next frame, fold again.
  createEffect(() => p.tabs.map(t => `${t.id}:${t.label}:${t.count ?? ''}`).join('|'), () => {
    widths = [];
    setFit(Infinity);
    const raf = requestAnimationFrame(() => layout());
    return () => cancelAnimationFrame(raf);
  });
  onSettled(() => {
    if (typeof ResizeObserver !== 'function' || !wrap) return;
    const ro = new ResizeObserver(() => layout());
    ro.observe(wrap);
    return () => ro.disconnect();
  });
  return (
    <div ref={el => { wrap = el; }} class={['tabs-fit', p.cls]} data-ui="tab-bar">
      <Tabs
        style={{ display: 'contents' }}
        value={p.value}
        loop={true}
        onValueChange={v => {
          if (v !== null) p.onChange(v as T);
        }}
      >
        <TabsList class="tabs" data-ui="tabs" aria-label={p.label}>
          <For each={shown()}>
            {t => (
              <TabsTab value={t.id}>
                <span>{t.label}</span>
                <Show when={t.count}><span class="tab-n mono">{t.count}</span></Show>
              </TabsTab>
            )}
          </For>
        </TabsList>
      </Tabs>
      <Show when={rest().length > 0}>
        <Menu container={wrap?.closest<HTMLElement>('.th-dark, .th-light') ?? undefined}>
          <MenuTrigger class="tab-more">More<Icon name="chevron-down" /></MenuTrigger>
          <MenuContent class="menu" side="bottom" align="end">
            <For each={rest()}>
              {t => (
                <MenuItem render={itemProps => <button {...itemProps} type="button" />} onSelect={() => p.onChange(t.id)}>
                  {t.label}<Show when={t.count}> <span class="tab-n mono">{t.count}</span></Show>
                </MenuItem>
              )}
            </For>
          </MenuContent>
        </Menu>
      </Show>
    </div>
  );
}

/** Native disclosure widget (`details`/`summary`) for secondary or advanced content. */
export function Disclosure(p: {
  summary: JSX.Element;
  summaryClass?: string;
  open?: boolean;
  children: JSX.Element;
}) {
  return (
    <details class="disclosure" data-ui="disclosure" open={p.open}>
      <summary class={p.summaryClass}>{p.summary}</summary>
      {p.children}
    </details>
  );
}

/** Where a ReflowRow cell goes in a narrow frame: beside the name (state), on the second line (meta), or last on it (time). */
export type ReflowPart = 'state' | 'meta' | 'time';

/**
 * A table that never scrolls sideways (owner rule; controls/data-list.md#reflow-table): columns while its frame is wide;
 * in a narrow frame (a phone, a narrow work area) each row becomes two lines, the name with its state, then the meta
 * facts joined by `·` with the time last. `cols` is the wide grid-template-columns, one track per header
 * (modern-web-guidance responsive-table: a container query on the table's own width; the headers stay column headers).
 */
export function ReflowTable(p: { label: string; head: string[]; cols: string; children: JSX.Element }) {
  return (
    <div class="reflow-frame" data-ui="reflow-table">
      <div class="reflow" role="table" aria-label={p.label} style={{ '--reflow-cols': p.cols }}>
        <div class="reflow-head" role="row"><For each={p.head}>{h => <span role="columnheader">{h}</span>}</For></div>
        {p.children}
      </div>
    </div>
  );
}

/** One row of a ReflowTable: the whole row opens it (OR-26) and its name is the row's button; while `selected`, `detail` shows under it at full width. */
export function ReflowRow(p: {
  name: string; lead?: JSX.Element; selected?: boolean; onOpen?: () => void;
  cells: { part: ReflowPart; el: JSX.Element }[]; detail?: JSX.Element;
}) {
  return (
    <>
      <div class={['reflow-row', { sel: !!p.selected, opens: !!p.onOpen }]} role="row" onClick={() => p.onOpen?.()}>
        <span class="reflow-name" role="rowheader">
          {p.lead}
          <Show when={p.onOpen} keyed fallback={<span class="t-label">{p.name}</span>}>
            {open => <button type="button" class="t-label" aria-expanded={p.selected ? 'true' : 'false'} onClick={e => { e.stopPropagation(); open(); }}>{p.name}</button>}
          </Show>
        </span>
        <For each={p.cells}>{c => <span class={[`reflow-${c.part}`, { 'reflow-empty': c.el == null || c.el === '' }]} role="cell">{c.el}</span>}</For>
      </div>
      <Show when={p.selected}><div class="reflow-detail" role="row"><div role="cell">{p.detail}</div></div></Show>
    </>
  );
}
