// Virtual list with anchor keeping (principles §3, OR-6, OR-9, OR-11).
// Rendering window from TanStack Virtual core; anchor keeping and the new-rows pill are ours.
import type { ComponentChildren } from 'preact';
import { useLayoutEffect, useReducer, useRef, useState } from 'preact/hooks';
import tokens from '../tokens.json';
import { Virtualizer, elementScroll, observeElementOffset, observeElementRect } from '@tanstack/virtual-core';

export type VirtualListProps<T> = {
  items: T[];
  /** Fill the owning column; the list is its only scroller. */
  fill?: boolean;
  getKey: (item: T) => string;
  render: (item: T, index: number) => ComponentChildren;
  /** Fixed row height in px; with `dynamic`, the first estimate before measuring. */
  rowHeight: number;
  /** Variable-height rows (transcripts): measured once, cached by key. */
  dynamic?: boolean;
  /** Where new items arrive and the view follows: newest-first lists use 'top', logs use 'bottom'. */
  follow: 'top' | 'bottom';
  label: string;
  /** CSS grid columns shared by header and rows. */
  columns?: string;
  header?: ComponentChildren;
  class?: string;
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
export type VirtualListHandle = { getAnchor(): { key: string; offsetPx: number } | null; restoreAnchor(key: string, offsetPx: number): void; scrollToKey: (key: string, align: 'start' | 'center' | 'end') => void };

// Touch devices get the 44 px row (owner exception to density, D14/M2).
const TOUCH_ROW = parseInt(tokens.size['row-phone']);
const coarse = () => typeof matchMedia !== 'undefined' && matchMedia('(pointer: coarse)').matches;

export function VirtualList<T>(props: VirtualListProps<T>) {
  const p = coarse() ? { ...props, rowHeight: Math.max(props.rowHeight, TOUCH_ROW) } : props;
  const scroller = useRef<HTMLDivElement>(null);
  const [, rerender] = useReducer((x: number) => x + 1, 0);
  const [unseen, setUnseen] = useState(0);
  // Anchor = first visible item and its distance from the viewport top, captured before each update.
  const anchor = useRef<{ key: string; delta: number } | null>(null);
  const requested = useRef<{ key: string; offsetPx?: number; align?: 'start' | 'center' | 'end' } | null>(null);
  const prev = useRef<{ keys: Set<string>; atEdge: boolean; top: number }>({ keys: new Set(p.items.map(p.getKey)), atEdge: true, top: 0 });

  const atEdge = () => {
    const el = scroller.current;
    if (!el) return true;
    return p.follow === 'top' ? el.scrollTop < 2 : el.scrollHeight - el.scrollTop - el.clientHeight < 2;
  };
  // Read the old DOM before rendering the new window. A scroll event may still be pending, and shrinking the
  // window can clamp scrollTop during the DOM update. Clamping must not be counted as a second scroll.
  const elBeforeRender = scroller.current;
  if (elBeforeRender) {
    const top = elBeforeRender.scrollTop;
    if (Math.abs(top - prev.current.top) > 1) {
      if (anchor.current) anchor.current.delta -= top - prev.current.top;
      prev.current.atEdge = atEdge();
      prev.current.top = top;
    }
  }

  const opts = {
    count: p.items.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => p.rowHeight,
    getItemKey: (i: number) => p.getKey(p.items[i]),
    overscan: 10,
    scrollToFn: elementScroll,
    observeElementRect,
    observeElementOffset,
    onChange: () => rerender(0),
  };
  const [v] = useState(() => new Virtualizer<HTMLDivElement, HTMLDivElement>(opts));
  v.setOptions(opts);
  useLayoutEffect(() => v._didMount(), []);
  useLayoutEffect(() => { v._willUpdate(); });

  const capture = () => {
    const el = scroller.current;
    const first = el ? v.getVirtualItemForOffset(el.scrollTop) : undefined;
    if (el && first) anchor.current = { key: String(first.key), delta: first.start - el.scrollTop };
    prev.current.atEdge = atEdge();
    if (el) prev.current.top = el.scrollTop;
    if (prev.current.atEdge && unseen) setUnseen(0);
  };
  // This control's own scroll writes (to the end, an anchor kept, a restore) move the position before their scroll
  // event too. Record them so the next render does not count them twice.
  const wrote = () => { const el = scroller.current; if (el) prev.current.top = el.scrollTop; };

  // Runs before paint whenever items change: keep the anchor row where it was, or follow the edge.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    // ponytail: O(n) key diff per update; switch to a server-provided "new since" count if lists reach millions client-side.
    // New rows counted for the pill: for 'bottom' (threads) the rows after the last row already shown; for 'top' (newest-first
    // lists, possibly grouped) the rows placed before the last old row. Rows paged in on the far side (older history) are not new.
    let added = 0, lastOld = -1;
    p.items.forEach((it, i) => { if (prev.current.keys.has(p.getKey(it))) lastOld = i; });
    p.items.forEach((it, i) => { if ((p.follow === 'top' ? i < lastOld : i > lastOld) && !prev.current.keys.has(p.getKey(it))) added++; });
    // A window replaced as a whole (a jump, a reload) has no row in common with the last one: nothing in it is "new".
    const replaced = lastOld < 0 && prev.current.keys.size > 0;
    if (replaced) { added = 0; setUnseen(0); }
    const wasAtEdge = prev.current.atEdge;
    prev.current.keys = new Set(p.items.map(p.getKey));
    if (wasAtEdge) {
      if (p.follow === 'bottom' && p.items.length) { v.scrollToIndex(p.items.length - 1, { align: 'end' }); wrote(); }
      return;
    }
    const a = anchor.current;
    if (a) {
      const i = p.items.findIndex(it => p.getKey(it) === a.key);
      if (i >= 0) { v.scrollToOffset(v.getOffsetForIndex(i, 'start')![0] - a.delta); wrote(); }
      // The anchor left with the old window (reload to newest): show the followed edge.
      else if (replaced && p.follow === 'bottom' && p.items.length) { v.scrollToIndex(p.items.length - 1, { align: 'end' }); prev.current.atEdge = true; wrote(); }
    }
    if (added) setUnseen(n => n + added);
  }, [p.items]);

  // Viewport and measured heights can change without a new items array.
  useLayoutEffect(() => {
    if (p.follow === 'bottom' && prev.current.atEdge && !atEdge() && p.items.length) { v.scrollToEnd(); wrote(); }
  });

  const jump = () => {
    setUnseen(0);
    if (p.follow === 'top') scroller.current?.scrollTo({ top: 0 });
    else if (p.items.length) v.scrollToIndex(p.items.length - 1, { align: 'end' });
    capture();
  };

  // Apply imperative restoration after this control's own anchor/layout work, before edge loading.
  // A parent layout effect can run before or after ours; a local render also handles the latter.
  useLayoutEffect(() => {
    const r = requested.current, el = scroller.current; if (!r || !el) return;
    requested.current = null;
    const i = p.items.findIndex(item => p.getKey(item) === r.key);
    if (i < 0) { if (!p.items.length) { v.scrollToOffset(0); anchor.current = null; wrote(); } return; }
    const start = v.getOffsetForIndex(i, 'start')?.[0];
    const offset = r.align ? v.getOffsetForIndex(i, r.align)?.[0] : start === undefined ? undefined : start - (r.offsetPx ?? 0);
    if (offset !== undefined && start !== undefined) { v.scrollToOffset(Math.max(0, offset)); anchor.current = { key: r.key, delta: start - el.scrollTop }; prev.current.atEdge = false; wrote(); }
  });

  // Asked once per window edge (length and edge row), so a replaced window of the same length asks again.
  const edgeKey = (side: 'start' | 'end') => `${p.items.length}:${p.items.length ? p.getKey(p.items[side === 'start' ? 0 : p.items.length - 1]) : ''}${side === 'end' ? ':' + (p.endKey ?? '') : ''}`;
  const endFor = useRef('');
  useLayoutEffect(() => { const el = scroller.current, lastVisible = el ? v.getVirtualItemForOffset(el.scrollTop + el.clientHeight)?.index ?? -1 : -1; if (lastVisible < p.items.length - 5) endFor.current = ''; if (p.onEnd && p.items.length && lastVisible >= p.items.length - 5 && endFor.current !== edgeKey('end')) { endFor.current = edgeKey('end'); p.onEnd(); } });

  // Read from the scroll position after anchor keeping ran, not from the render: rows just loaded above are not "in view".
  const startFor = useRef('');
  const checkStart = () => { const el = scroller.current; if (el && el.scrollTop >= p.rowHeight * 5) startFor.current = ''; if (p.onStart && el && p.items.length && el.scrollTop < p.rowHeight * 5 && startFor.current !== edgeKey('start')) { startFor.current = edgeKey('start'); p.onStart(); } };
  useLayoutEffect(checkStart);
  if (p.handle) p.handle.current = {
    getAnchor: () => { const el = scroller.current, first = el ? v.getVirtualItemForOffset(el.scrollTop) : undefined; return el && first ? { key: String(first.key), offsetPx: first.start - el.scrollTop } : null; },
    restoreAnchor: (key, offsetPx) => { requested.current = { key, offsetPx }; rerender(0); },
    scrollToKey: (key, align) => { requested.current = { key, align }; rerender(0); },
  };

  const style = { ...(p.columns ? { '--cols': p.columns } : {}), ...(p.fill ? { flex: 1 } : {}) };
  return (
    <div data-ui="data-list" class={`vl-wrap ${p.class ?? ''}`} style={style}>
      {p.header && <div class="vl-head" role="row">{p.header}</div>}
      <div ref={scroller} style={p.fill ? { flex: 1, height: 'auto' } : undefined} class="vl" role="grid" aria-label={p.label} aria-rowcount={p.items.length} tabIndex={0} onScroll={() => { capture(); checkStart(); }}>
        <div class="vl-space" style={{ height: `${v.getTotalSize()}px` }}>
          {v.getVirtualItems().map(vi => (
            <div key={vi.key} data-key={String(vi.key)} data-index={vi.index} ref={p.dynamic ? v.measureElement : undefined} role="row" aria-rowindex={vi.index + 1}
              class={`vl-row${p.dynamic ? ' dyn' : ''} ${p.rowClass?.(p.items[vi.index]) ?? ''}`} style={{ transform: `translateY(${vi.start}px)`, height: p.dynamic ? undefined : `${p.rowHeight}px` }}
              onContextMenu={p.onRowMenu ? e => { e.preventDefault(); p.onRowMenu!(p.items[vi.index], { x: e.clientX, y: e.clientY }); } : undefined}>
              {p.render(p.items[vi.index], vi.index)}
            </div>
          ))}
        </div>
      </div>
      <span aria-live="polite">{p.pending && p.pending.count > 0 ? <button type="button" class={`pill pill-${p.follow}`} onClick={() => { setUnseen(0); p.pending!.onJump(); }}>{p.pending.count} new</button>
        : unseen > 0 && <button type="button" class={`pill pill-${p.follow}`} onClick={jump}>{unseen} new</button>}</span>
    </div>
  );
}
