// Virtual list with anchor keeping (principles §3, OR-6, OR-9, OR-11).
// Rendering window from TanStack Virtual core; anchor keeping and the new-rows pill are ours.
import type { ComponentChildren } from 'preact';
import { useLayoutEffect, useReducer, useRef, useState } from 'preact/hooks';
import tokens from '../tokens.json';
import { Virtualizer, elementScroll, observeElementOffset, observeElementRect } from '@tanstack/virtual-core';

export type VirtualListProps<T> = {
  items: T[];
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
  /** Infinite scroll (OR-11): called once per list length when the last rows come into view. */
  onEnd?: () => void;
  /** Upward loading (threads): called once per list length when the first rows come into view. */
  onStart?: () => void;
  /** New rows the owner knows of but has not loaded (a thread after a jump): shown in the same pill; a click calls onJump. */
  pending?: { count: number; onJump: () => void };
  /** Filled with the list's methods: scrollToKey for jumps (search hits). */
  handle?: { current: VirtualListHandle | null };
};
export type VirtualListHandle = { scrollToKey: (key: string, align: 'start' | 'center' | 'end') => void };

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
  const prev = useRef<{ keys: Set<string>; atEdge: boolean }>({ keys: new Set(p.items.map(p.getKey)), atEdge: true });

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

  const atEdge = () => {
    const el = scroller.current;
    if (!el) return true;
    return p.follow === 'top' ? el.scrollTop < 2 : el.scrollHeight - el.scrollTop - el.clientHeight < 2;
  };
  const capture = () => {
    const el = scroller.current;
    const first = el ? v.getVirtualItemForOffset(el.scrollTop) : undefined;
    if (el && first) anchor.current = { key: String(first.key), delta: first.start - el.scrollTop };
    prev.current.atEdge = atEdge();
    if (prev.current.atEdge && unseen) setUnseen(0);
  };

  // Runs before paint whenever items change: keep the anchor row where it was, or follow the edge.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    // ponytail: O(n) key diff per update; switch to a server-provided "new since" count if lists reach millions client-side.
    // New rows counted for the pill are the ones after the last row already shown; rows loaded above are not new.
    let added = 0, lastOld = -1;
    p.items.forEach((it, i) => { if (prev.current.keys.has(p.getKey(it))) lastOld = i; });
    p.items.forEach((it, i) => { if (i > lastOld && !prev.current.keys.has(p.getKey(it))) added++; });
    // A window replaced as a whole (a jump, a reload) has no row in common with the last one: nothing in it is "new".
    const replaced = lastOld < 0 && prev.current.keys.size > 0;
    if (replaced) { added = 0; setUnseen(0); }
    const wasAtEdge = prev.current.atEdge;
    prev.current.keys = new Set(p.items.map(p.getKey));
    if (wasAtEdge) {
      if (p.follow === 'bottom' && p.items.length) v.scrollToIndex(p.items.length - 1, { align: 'end' });
      return;
    }
    const a = anchor.current;
    if (a) {
      const i = p.items.findIndex(it => p.getKey(it) === a.key);
      if (i >= 0) el.scrollTop = v.getOffsetForIndex(i, 'start')![0] - a.delta;
      // The anchor left with the old window (reload to newest): show the followed edge.
      else if (replaced && p.follow === 'bottom' && p.items.length) { v.scrollToIndex(p.items.length - 1, { align: 'end' }); prev.current.atEdge = true; }
    }
    if (added) setUnseen(n => n + added);
  }, [p.items]);

  const jump = () => {
    setUnseen(0);
    if (p.follow === 'top') scroller.current?.scrollTo({ top: 0 });
    else if (p.items.length) v.scrollToIndex(p.items.length - 1, { align: 'end' });
  };

  // Asked once per window edge (length and edge row), so a replaced window of the same length asks again.
  const edgeKey = (side: 'start' | 'end') => `${p.items.length}:${p.items.length ? p.getKey(p.items[side === 'start' ? 0 : p.items.length - 1]) : ''}`;
  const endFor = useRef('');
  const lastVisible = v.getVirtualItems().at(-1)?.index ?? -1;
  useLayoutEffect(() => { if (p.onEnd && p.items.length && lastVisible >= p.items.length - 5 && endFor.current !== edgeKey('end')) { endFor.current = edgeKey('end'); p.onEnd(); } });

  // Read from the scroll position after anchor keeping ran, not from the render: rows just loaded above are not "in view".
  const startFor = useRef('');
  const checkStart = () => { const el = scroller.current; if (p.onStart && el && p.items.length && el.scrollTop < p.rowHeight * 5 && startFor.current !== edgeKey('start')) { startFor.current = edgeKey('start'); p.onStart(); } };
  useLayoutEffect(checkStart);
  if (p.handle) p.handle.current = {
    scrollToKey: (key, align) => { const i = p.items.findIndex(it => p.getKey(it) === key); if (i >= 0) { prev.current.atEdge = false; v.scrollToIndex(i, { align }); const el = scroller.current, start = v.getOffsetForIndex(i, 'start')?.[0]; if (el && start !== undefined) anchor.current = { key, delta: start - el.scrollTop }; } },
  };

  const style = p.columns ? { '--cols': p.columns } : undefined;
  return (
    <div data-ui="data-list" class={`vl-wrap ${p.class ?? ''}`} style={style}>
      {p.header && <div class="vl-head" role="row">{p.header}</div>}
      <div ref={scroller} class="vl" role="grid" aria-label={p.label} aria-rowcount={p.items.length} tabIndex={0} onScroll={() => { capture(); checkStart(); }}>
        <div class="vl-space" style={{ height: `${v.getTotalSize()}px` }}>
          {v.getVirtualItems().map(vi => (
            <div key={vi.key} data-index={vi.index} ref={p.dynamic ? v.measureElement : undefined} role="row" aria-rowindex={vi.index + 1}
              class={`vl-row${p.dynamic ? ' dyn' : ''} ${p.rowClass?.(p.items[vi.index]) ?? ''}`} style={{ transform: `translateY(${vi.start}px)`, height: p.dynamic ? undefined : `${p.rowHeight}px` }}>
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
