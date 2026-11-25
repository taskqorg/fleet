// Virtual list with anchor keeping (principles §3, OR-6, OR-9, OR-11).
// Rendering window from TanStack Virtual core; anchor keeping and the new-rows pill are ours.
import { createEffect, createMemo, createRenderEffect, createSignal, For, onSettled, Show, untrack } from 'solid-js';
import type { JSX } from '@solidjs/web';
import tokens from '../tokens.json';
import { Virtualizer, defaultRangeExtractor, elementScroll, observeElementOffset, observeElementRect } from '@tanstack/virtual-core';

export type VirtualListProps<T> = {
  items: T[];
  /** Fill the owning column; the list is its only scroller. */
  fill?: boolean;
  getKey: (item: T) => string;
  render: (item: T, index: number) => JSX.Element;
  /** Fixed row height in px; with `dynamic`, the first estimate before measuring. */
  rowHeight: number;
  /** Variable-height rows (transcripts): measured once, cached by key. */
  dynamic?: boolean;
  /** Where new items arrive and the view follows: newest-first lists use 'top', logs use 'bottom'. */
  follow: 'top' | 'bottom';
  label: string;
  /** CSS grid columns shared by header and rows. */
  columns?: string;
  header?: JSX.Element;
  class?: string;
  /** Keep a focused row mounted and restore its focus if a live reorder moves it. */
  keepRowFocus?: boolean;
  /** Extra class per row, e.g. group headers. */
  rowClass?: (item: T) => string;
  /** Right click (or long press) on a row: the page opens its row menu (ContextMenu) at the pointer. */
  onRowMenu?: (item: T, at: { x: number; y: number }) => void;
  /** Infinite scroll (OR-11): called once per list length when the last rows come into view. */
  onEnd?: () => void;
  /** Data behind the end that the items do not show (rows loaded into a collapsed group): a change calls onEnd again while
   * the end is in view, so a collapsed group that fills the loaded page does not stop the loading. */
  endKey?: string;
  /** Upward loading (threads): called once per list length when the first rows come into view. */
  onStart?: () => void;
  /** New rows the owner knows of but has not loaded (a thread after a jump): shown in the same pill; a click calls onJump. */
  pending?: { count: number; onJump: () => void };
  /** Filled with the list's methods: scrollToKey for jumps (search hits). */
  handle?: { current: VirtualListHandle | null };
};
export type VirtualListHandle = {
  /** The list is scrolled to its follow edge (the newest row for a bottom-following list). */
  isAtEdge(): boolean;
  getAnchor(): { key: string; offsetPx: number } | null;
  restoreAnchor(key: string, offsetPx: number): void;
  scrollToKey: (key: string, align: 'start' | 'center' | 'end') => void;
};

// Touch devices get the 44 px row (owner exception to density, D14/M2).
const TOUCH_ROW = parseInt(tokens.size['row-phone']);
const coarse = () => typeof matchMedia !== 'undefined' && matchMedia('(pointer: coarse)').matches;

export function VirtualList<T>(props: VirtualListProps<T>) {
  const rowHeight = createMemo(() => coarse() ? Math.max(props.rowHeight, TOUCH_ROW) : props.rowHeight);
  let scroller: HTMLDivElement | undefined;
  let spaceEl: HTMLDivElement | undefined;
  const [tick, setTick] = createSignal(0, { ownedWrite: true });
  const [unseen, setUnseen] = createSignal(0, { ownedWrite: true });
  // Anchor = first visible item and its distance from the viewport top, captured before each update.
  let anchor: { key: string; delta: number } | null = null;
  let requested: { key: string; offsetPx?: number; align?: 'start' | 'center' | 'end' } | null = null;
  let prevKeys = new Set<string>();
  let prevAtEdge = true;
  let prevTop = 0;
  let initialized = false;
  let endFor = '';
  let startFor = '';
  let pendingFocus: { el: HTMLElement; key: string; rowLink: boolean } | null = null;

  const atEdge = () => {
    if (!scroller) return true;
    return props.follow === 'top' ? scroller.scrollTop < 2 : scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 2;
  };
  // TanStack also scrolls to compensate for measured rows. Record those writes before their scroll events.
  const scrollTo: typeof elementScroll = (offset, options, instance) => {
    elementScroll(offset, options, instance);
    const el = instance.scrollElement;
    if (el) prevTop = el.scrollTop;
  };

  const buildOpts = (items: T[], rh: number, keepFocus: boolean | undefined) => {
    const active = keepFocus && document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const activeRow = active?.closest<HTMLElement>('.vl-row');
    const focused = activeRow && scroller?.contains(activeRow) ? active : null;
    const focusIndex = focused ? items.findIndex(item => props.getKey(item) === activeRow?.dataset.key) : -1;
    return {
      focused,
      opts: {
        count: items.length,
        getScrollElement: () => scroller ?? null,
        estimateSize: () => rh,
        getItemKey: (i: number) => props.getKey(items[i]),
        overscan: 10,
        rangeExtractor: focusIndex < 0 ? defaultRangeExtractor : (range: Parameters<typeof defaultRangeExtractor>[0]) => {
          const indices = defaultRangeExtractor(range);
          if (indices.includes(focusIndex)) return indices;
          indices.push(focusIndex);
          return indices.sort((a, b) => a - b);
        },
        scrollToFn: scrollTo,
        observeElementRect,
        observeElementOffset,
        onChange: () => setTick(n => n + 1),
      },
    };
  };

  const v = new Virtualizer<HTMLDivElement, HTMLDivElement>(
    untrack(() => buildOpts(props.items, rowHeight(), props.keepRowFocus).opts),
  );

  const capture = () => {
    const first = scroller ? v.getVirtualItemForOffset(scroller.scrollTop) : undefined;
    if (scroller && first) anchor = { key: String(first.key), delta: first.start - scroller.scrollTop };
    prevAtEdge = atEdge();
    if (scroller) prevTop = scroller.scrollTop;
    if (prevAtEdge && untrack(unseen)) setUnseen(0);
  };

  const applyRequested = (): boolean => {
    const r = requested;
    if (!r || !scroller) return false;
    requested = null;
    const items = untrack(() => props.items);
    const i = items.findIndex(item => props.getKey(item) === r.key);
    if (i < 0) {
      if (!items.length) {
        v.scrollToOffset(0);
        anchor = null;
      }
      return false;
    }
    const start = v.getOffsetForIndex(i, 'start')?.[0];
    if (start === undefined) return false;
    if (spaceEl) spaceEl.style.height = `${v.getTotalSize()}px`;
    if (r.align) v.scrollToIndex(i, { align: r.align });
    else v.scrollToOffset(Math.max(0, start - (r.offsetPx ?? 0)));
    v._willUpdate();
    anchor = { key: r.key, delta: start - scroller.scrollTop };
    prevAtEdge = false;
    setTick(n => n + 1);
    return true;
  };

  const edgeKey = (side: 'start' | 'end') => {
    const items = untrack(() => props.items);
    return `${items.length}:${items.length ? props.getKey(items[side === 'start' ? 0 : items.length - 1]) : ''}${side === 'end' ? ':' + (untrack(() => props.endKey) ?? '') : ''}`;
  };

  const checkStart = () => {
    const items = untrack(() => props.items), rh = untrack(rowHeight);
    if (scroller && scroller.scrollTop >= rh * 5) startFor = '';
    if (props.onStart && scroller && items.length && scroller.scrollTop < rh * 5 && startFor !== edgeKey('start')) {
      startFor = edgeKey('start');
      props.onStart();
    }
  };

  const checkEnd = () => {
    const items = untrack(() => props.items);
    const lastVisible = scroller ? v.getVirtualItemForOffset(scroller.scrollTop + scroller.clientHeight)?.index ?? -1 : -1;
    if (lastVisible < items.length - 5) endFor = '';
    if (props.onEnd && items.length && lastVisible >= items.length - 5 && endFor !== edgeKey('end')) {
      endFor = edgeKey('end');
      props.onEnd();
    }
  };

  let prevItemsRef: T[] | undefined;

  onSettled(() => {
    const cleanup = v._didMount();
    const items = untrack(() => props.items);
    prevKeys = new Set(items.map(props.getKey));
    prevItemsRef = items;
    initialized = true;
    v._willUpdate();
    if (props.follow === 'bottom' && items.length) {
      v.scrollToIndex(items.length - 1, { align: 'end' });
    }
    applyRequested();
    checkEnd();
    checkStart();
    setTick(n => n + 1);
    return () => {
      cleanup();
      if (props.handle) props.handle.current = null;
    };
  });

  // Keep Virtualizer options & scroll anchor in sync whenever items, rowHeight, or keepRowFocus change.
  createRenderEffect(
    () => ({ items: props.items, rh: rowHeight(), keepFocus: props.keepRowFocus, dyn: props.dynamic }),
    ({ items, rh, keepFocus }) => {
      if (scroller) {
        const top = scroller.scrollTop;
        if (top !== prevTop) {
          const row = v.getVirtualItemForOffset(top);
          anchor = row ? { key: String(row.key), delta: row.start - top } : null;
          prevAtEdge = atEdge();
          prevTop = top;
        }
      }
      const { focused, opts } = buildOpts(items, rh, keepFocus);
      if (focused) {
        const key = focused.closest<HTMLElement>('.vl-row')?.dataset.key ?? '';
        pendingFocus = { el: focused, key, rowLink: focused.classList.contains('row-link') };
      }
      v.setOptions(opts);
      v._willUpdate();
      if (spaceEl) spaceEl.style.height = `${v.getTotalSize()}px`;

      if (initialized && scroller && items !== prevItemsRef) {
        prevItemsRef = items;
        // New rows counted for the pill: for 'bottom' (threads) the rows after the last row already shown; for 'top'
        // (newest-first lists, possibly grouped) the rows placed before the last old row.
        let added = 0, lastOld = -1;
        items.forEach((it, i) => { if (prevKeys.has(props.getKey(it))) lastOld = i; });
        items.forEach((it, i) => { if ((props.follow === 'top' ? i < lastOld : i > lastOld) && !prevKeys.has(props.getKey(it))) added++; });
        const replaced = lastOld < 0 && prevKeys.size > 0;
        if (replaced) { added = 0; setUnseen(0); }
        const wasAtEdge = prevAtEdge;
        prevKeys = new Set(items.map(props.getKey));
        if (wasAtEdge && !requested) {
          if (props.follow === 'bottom' && items.length) v.scrollToIndex(items.length - 1, { align: 'end' });
        } else if (!requested) {
          if (anchor) {
            const i = items.findIndex(it => props.getKey(it) === anchor!.key);
            if (i >= 0) v.scrollToOffset(v.getOffsetForIndex(i, 'start')![0] - anchor.delta);
            else if (replaced && props.follow === 'bottom' && items.length) {
              v.scrollToIndex(items.length - 1, { align: 'end' });
              prevAtEdge = true;
            }
          }
          if (added) setUnseen(n => n + added);
        }
      }

      setTick(n => n + 1);
    },
  );

  // After DOM commit: keep bottom edge when dynamic rows resize, restore lost row focus, apply pending anchor requests, and trigger edge loading.
  createEffect(
    () => [tick(), props.items, props.endKey] as const,
    () => {
      if (!initialized) return;
      v._willUpdate();
      const items = untrack(() => props.items);
      if (!requested && props.follow === 'bottom' && prevAtEdge && !atEdge() && items.length) v.scrollToEnd();
      const pf = pendingFocus;
      pendingFocus = null;
      if (pf) {
        const restore = () => {
          if (document.activeElement !== document.body) return;
          let target: HTMLElement | null = pf.el.isConnected ? pf.el : null;
          if (!target && pf.key && scroller) {
            const rowEl = scroller.querySelector<HTMLElement>(`.vl-row[data-key="${CSS.escape(pf.key)}"]`);
            if (rowEl) {
              target = (pf.rowLink ? rowEl.querySelector<HTMLElement>('.row-link') : null)
                ?? rowEl.querySelector<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), [tabindex]');
            }
          }
          (target ?? scroller)?.focus({ preventScroll: true });
        };
        restore();
        queueMicrotask(restore);
      }
      if (applyRequested()) return;
      checkEnd();
      checkStart();
    },
  );

  const jump = () => {
    setUnseen(0);
    const items = untrack(() => props.items);
    if (props.follow === 'top') scroller?.scrollTo({ top: 0 });
    else if (items.length) v.scrollToIndex(items.length - 1, { align: 'end' });
    capture();
  };

  createEffect(() => props.handle, h => {
    if (!h) return;
    h.current = {
      isAtEdge: atEdge,
      getAnchor: () => {
        const first = scroller ? v.getVirtualItemForOffset(scroller.scrollTop) : undefined;
        return scroller && first ? { key: String(first.key), offsetPx: first.start - scroller.scrollTop } : null;
      },
      restoreAnchor: (key, offsetPx) => {
        requested = { key, offsetPx };
        setTick(n => n + 1);
      },
      scrollToKey: (key, align) => {
        requested = { key, align };
        setTick(n => n + 1);
      },
    };
  });

  const virtualItems = createMemo(() => {
    tick();
    props.items;
    return v.getVirtualItems();
  });
  const totalSize = createMemo(() => {
    tick();
    props.items;
    return v.getTotalSize();
  });

  return (
    <div
      data-ui="data-list"
      class={['vl-wrap', props.class]}
      style={{ ...(props.columns ? { '--cols': props.columns } : {}), ...(props.fill ? { flex: '1' } : {}) }}
    >
      <Show when={props.header} keyed>{h => <div class="vl-head" role="row">{h}</div>}</Show>
      <div
        ref={el => { scroller = el; }}
        style={props.fill ? { flex: '1', height: 'auto' } : undefined}
        class="vl"
        role="grid"
        aria-label={props.label}
        aria-rowcount={props.items.length}
        tabindex={0}
        onScroll={() => { capture(); checkStart(); }}
      >
        <div ref={el => { spaceEl = el; }} class="vl-space" style={{ height: `${totalSize()}px` }}>
          <For each={virtualItems()} keyed={vi => String(vi.key)}>
            {vi => {
              const item = createMemo(() => props.items[vi().index]);
              let rowEl: HTMLDivElement | undefined;
              createEffect(() => [props.dynamic, vi().index] as const, ([dyn]) => {
                if (dyn && rowEl && rowEl.isConnected) v.measureElement(rowEl);
              });
              onSettled(() => {
                if (props.dynamic && rowEl) v.measureElement(rowEl);
              });
              return (
                <div
                  data-key={String(vi().key)}
                  data-index={vi().index}
                  ref={el => { rowEl = el; }}
                  role="row"
                  aria-rowindex={vi().index + 1}
                  class={['vl-row', { dyn: !!props.dynamic }, props.rowClass && item() !== undefined ? props.rowClass(item()!) : '']}
                  style={{ transform: `translateY(${vi().start}px)`, height: props.dynamic ? undefined : `${rowHeight()}px` }}
                  onContextMenu={e => {
                    if (!props.onRowMenu) return;
                    const it = item();
                    if (it === undefined) return;
                    e.preventDefault();
                    props.onRowMenu(it, { x: e.clientX, y: e.clientY });
                  }}
                >
                  {item() !== undefined ? props.render(item()!, vi().index) : null}
                </div>
              );
            }}
          </For>
        </div>
      </div>
      <span aria-live="polite">
        <Show
          when={props.pending && props.pending.count > 0}
          fallback={
            <Show when={unseen() > 0}>
              <button type="button" class={['pill', `pill-${props.follow}`]} onClick={jump}>{unseen()} new</button>
            </Show>
          }
        >
          <button
            type="button"
            class={['pill', `pill-${props.follow}`]}
            onClick={() => { setUnseen(0); props.pending?.onJump(); }}
          >
            {props.pending?.count} new
          </button>
        </Show>
      </span>
    </div>
  );
}
