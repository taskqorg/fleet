// Drag to reorder (nav-rail.md, Pinned; owner 26 Nov: no Move up / Move down): one primitive for every ordered pin list,
// the rail's Pinned zone and pinned sections, the Pinned group of lists and the phone's More strip.
// modern-web-guidance drag-and-drop: the real item lifts and follows the pointer (translate, no clone), the destination comes
// from the other items' layout midpoints, a drop line shows in the gap, the order changes once on release. Keyboard:
// Space grabs, arrows move, Space or Enter drops, Escape cancels, focus leaving drops; a polite status announces each step.
// Touch: a long press lifts the item, then it drags; until then the page scrolls as usual.
import { createSignal, onCleanup, Show } from 'solid-js';
import { Portal, type JSX } from '@solidjs/web';
import { reorderTo } from './reorder-keys.ts';
export { reorderTo };

export type ReorderOptions = {
  /** The keys in their current order. */ keys: () => string[];
  /** The new order on a drop or a keyboard drop; not called when nothing moved. */ onOrder: (keys: string[]) => void;
  /** The item's name for announcements. */ label: (key: string) => string;
  /** The element that holds the items (each carries data-reorder-key). */ root: () => HTMLElement | undefined;
  /** Focus the item's focusable after a keyboard move. */ focus: (key: string) => void;
  /** Rows run down (y) or across (x). */ axis?: 'x' | 'y';
};

const LONG_PRESS = 450, SLOP = 6;
const reduced = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;


export function useReorder(o: ReorderOptions) {
  const axis = () => o.axis ?? 'y';
  // kbd: the order while a keyboard grab moves the item (shown, not saved until the drop).
  const [kbd, setKbd] = createSignal<{ key: string; order: string[]; start: string[] } | null>(null);
  const [lifted, setLifted] = createSignal<string | null>(null);
  const [line, setLine] = createSignal<{ x: number; y: number; w: number; h: number } | null>(null);
  const [say, setSay] = createSignal('');
  let sayTimer: ReturnType<typeof setTimeout> | undefined;
  const announce = (t: string) => { setSay(''); clearTimeout(sayTimer); sayTimer = setTimeout(() => setSay(t), 120); };
  let movingFocus = false;

  const order = () => kbd()?.order ?? o.keys();
  const items = () => [...(o.root()?.querySelectorAll<HTMLElement>('[data-reorder-key]') ?? [])];
  const el = (key: string) => items().find(e => e.dataset.reorderKey === key);
  const pos = (key: string, keys = order()) => `${keys.indexOf(key) + 1} of ${keys.length}`;

  /* ---------- pointer ---------- */
  let drag: { key: string; id: number; x: number; y: number; to: number; el: HTMLElement; timer?: ReturnType<typeof setTimeout>; live: boolean; touch: boolean } | null = null;

  /** The slot the pointer is over: before the first other item whose midpoint lies past the pointer. */
  const slotAt = (px: number, py: number, key: string) => {
    const others = items().filter(e => e.dataset.reorderKey !== key);
    const i = others.findIndex(e => { const r = e.getBoundingClientRect(); return axis() === 'y' ? py < r.top + r.height / 2 : px < r.left + r.width / 2; });
    return { to: i < 0 ? others.length : i, others };
  };
  const showLine = (to: number, others: HTMLElement[], key: string) => {
    const keys = o.keys();
    if (reorderTo(keys, key, to).join('\n') === keys.join('\n')) return setLine(null); // its own slot: no marker
    const a = others[to - 1]?.getBoundingClientRect(), b = others[to]?.getBoundingClientRect(), r = (a ?? b)!;
    if (axis() === 'y') {
      const y = a && b ? (a.bottom + b.top) / 2 : a ? a.bottom : b!.top;
      setLine({ x: r.left, y: y - 1, w: r.width, h: 2 });
    } else {
      const x = a && b ? (a.right + b.left) / 2 : a ? a.right : b!.left;
      setLine({ x: x - 1, y: r.top, w: 2, h: r.height });
    }
  };
  const start = () => {
    if (!drag) return;
    drag.live = true;
    setLifted(drag.key);
    drag.el.setPointerCapture?.(drag.id);
    document.documentElement.classList.add('reordering');
  };
  const end = (commit: boolean) => {
    const d = drag;
    drag = null;
    if (!d) return;
    clearTimeout(d.timer);
    if (!d.live) return;
    document.documentElement.classList.remove('reordering');
    d.el.style.translate = '';
    setLifted(null);
    setLine(null);
    if (!commit) return announce(`${o.label(d.key)}: move cancelled.`);
    const keys = o.keys(), next = reorderTo(keys, d.key, d.to);
    // The click that ends a drag must not open the item.
    const guard = (e: Event) => { e.preventDefault(); e.stopPropagation(); };
    window.addEventListener('click', guard, { capture: true, once: true });
    setTimeout(() => window.removeEventListener('click', guard, { capture: true }), 0);
    if (next.join('\n') === keys.join('\n')) return;
    o.onOrder(next);
    announce(`${o.label(d.key)} moved to ${pos(d.key, next)}.`);
  };
  const onMove = (e: PointerEvent) => {
    if (!drag || e.pointerId !== drag.id) return;
    const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    if (!drag.live) {
      if (Math.hypot(dx, dy) > SLOP) { if (drag.touch) return end(false); start(); } // a touch that moves before the press is a scroll
      else return;
    }
    e.preventDefault();
    drag.el.style.translate = axis() === 'y' ? `0 ${dy}px` : `${dx}px 0`;
    const { to, others } = slotAt(e.clientX, e.clientY, drag.key);
    drag.to = to;
    showLine(to, others, drag.key);
  };
  const onUp = (e: PointerEvent) => { if (drag && e.pointerId === drag.id) end(e.type === 'pointerup'); };
  const onKeyEsc = (e: KeyboardEvent) => { if (e.key === 'Escape' && drag?.live) { e.preventDefault(); end(false); } };
  // While a touch drag is live the page must not scroll under it.
  const noScroll = (e: TouchEvent) => { if (drag?.live) e.preventDefault(); };
  // A long press on touch also raises the context menu (a row menu, the browser's link menu): not while it lifts an item.
  const noMenu = (e: Event) => { if (drag && (drag.live || drag.touch)) { e.preventDefault(); e.stopImmediatePropagation(); } };
  document.addEventListener('pointermove', onMove, { passive: false });
  document.addEventListener('pointerup', onUp);
  document.addEventListener('pointercancel', onUp);
  document.addEventListener('keydown', onKeyEsc, true);
  document.addEventListener('touchmove', noScroll, { passive: false });
  window.addEventListener('contextmenu', noMenu, true);
  onCleanup(() => {
    if (drag?.live) document.documentElement.classList.remove('reordering');
    if (drag) clearTimeout(drag.timer);
    drag = null;
    window.removeEventListener('contextmenu', noMenu, true);
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    document.removeEventListener('pointercancel', onUp);
    document.removeEventListener('keydown', onKeyEsc, true);
    document.removeEventListener('touchmove', noScroll);
    clearTimeout(sayTimer);
  });

  const pointerDown = (key: string) => (e: PointerEvent) => {
    if (drag || kbd() || (e.pointerType === 'mouse' && e.button !== 0)) return;
    const target = e.currentTarget as HTMLElement;
    const at = target.closest<HTMLElement>('[data-reorder-key]') ?? target;
    const touch = e.pointerType !== 'mouse';
    drag = { key, id: e.pointerId, x: e.clientX, y: e.clientY, to: o.keys().indexOf(key), el: at, live: false, touch };
    if (touch) drag.timer = setTimeout(() => { if (drag && !drag.live) { start(); navigator.vibrate?.(10); } }, LONG_PRESS);
  };

  /* ---------- keyboard ---------- */
  const commitKbd = (cancel: boolean) => {
    const k = kbd();
    if (!k) return;
    setKbd(null);
    if (cancel) { o.focus(k.key); return announce(`${o.label(k.key)}: move cancelled, back at ${pos(k.key, k.start)}.`); }
    if (k.order.join('\n') !== k.start.join('\n')) o.onOrder(k.order);
    announce(`${o.label(k.key)} dropped at ${pos(k.key, k.order)}.`);
  };
  const keyDown = (key: string) => (e: KeyboardEvent) => {
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    // Space on a control inside the item (its pin, its checkbox) is that control's own.
    const t = e.target as HTMLElement;
    if (t !== e.currentTarget && t.matches('button, input, textarea, select') && !t.matches('[data-reorder-key]')) return;
    const k = kbd();
    if (!k) {
      if (e.key !== ' ') return;
      e.preventDefault(); e.stopPropagation();
      const keys = o.keys();
      setKbd({ key, order: keys, start: keys });
      return announce(`${o.label(key)} grabbed, ${pos(key, keys)}. Arrow keys move, Space drops, Escape cancels.`);
    }
    if (k.key !== key) return;
    const back = axis() === 'y' ? 'ArrowUp' : 'ArrowLeft', fwd = axis() === 'y' ? 'ArrowDown' : 'ArrowRight';
    if (e.key === back || e.key === fwd) {
      e.preventDefault(); e.stopPropagation();
      const i = k.order.indexOf(key), next = reorderTo(k.order, key, i + (e.key === fwd ? 1 : -1));
      if (next.join('\n') === k.order.join('\n')) return;
      movingFocus = true;
      setKbd({ ...k, order: next });
      requestAnimationFrame(() => { o.focus(key); el(key)?.scrollIntoView({ block: 'nearest', inline: 'nearest' }); movingFocus = false; });
      announce(`${o.label(key)}, ${pos(key, next)}.`);
    } else if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); commitKbd(false); }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); commitKbd(true); }
  };
  const focusOut = (key: string) => () => { if (!movingFocus && kbd()?.key === key) commitKbd(false); };

  /** Props for an item's element: its key, the pointer and keyboard handlers, its state for styling. */
  const item = (key: string) => ({
    'data-reorder-key': key,
    'data-reorder': lifted() === key ? 'lifted' : kbd()?.key === key ? 'grabbed' : undefined,
    onPointerDown: pointerDown(key),
    onKeyDown: keyDown(key),
    onFocusOut: focusOut(key),
    // The pointer drag replaces the browser's own link and image drag.
    onDragStart: (e: DragEvent) => e.preventDefault(),
  });

  /** The drop line (fixed, over everything) and the status line; render once next to the items. */
  const view = (): JSX.Element => (
    <>
      <p class="sr-only" role="status">{say()}</p>
      <Show when={line()}>
        {l => <Portal><div class="drop-line" aria-hidden="true" style={{ left: `${l().x}px`, top: `${l().y}px`, width: `${l().w}px`, height: `${l().h}px`, transition: reduced() ? 'none' : undefined }} /></Portal>}
      </Show>
    </>
  );
  return { order, item, view, grabbed: () => kbd()?.key ?? lifted() };
}
