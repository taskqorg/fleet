// Infinite lists (OR-64): a list loads its next page as its end scrolls near, with no Show more / Show less toggle.
// One shape everywhere: cursor pages behind a live first page, one request in flight, an abort on unmount,
// a quiet loading row, an error row with Retry, and nothing after the last row. Spec: patterns/infinite-list.md.
import { createEffect, createMemo, createSignal, onCleanup, Show, untrack } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Button, Icon, Skeleton } from './controls/primitives.tsx';
import { actions } from './registry.ts';

/** One page of rows and the cursor of the page after it (null: the last page). */
export type Page<R, C> = { rows: R[]; next: C | null };

/** What a list end needs: whether a page is loading, the last error, whether the list is complete, and the two actions. */
export type Pager = {
  readonly loading: boolean;
  readonly error: string | null;
  readonly done: boolean;
  more: () => void;
  retry: () => void;
};

export type Pages<R> = Pager & { readonly rows: R[] };

/**
 * Cursor pages behind a live first page. `first` is the first page as the view already reads it (useOp rereads it on
 * change rows, so new rows arrive at the top); `fetch` reads the page after a cursor. Loaded pages are never read again:
 * a first page reread after older pages loaded merges by key at the top, the first page's version of a row winning.
 * Only one page is in flight; changing `reset` (the filter) or unmounting aborts it and drops the older pages.
 */
export function createPages<R, C>(o: {
  first: () => Page<R, C> | null;
  fetch: (cursor: C, signal: AbortSignal) => Promise<Page<R, C>>;
  key: (row: R) => string;
  reset?: () => unknown;
}): Pages<R> {
  // Rows kept since older pages started loading: earlier first pages, then every older page, in order.
  const [kept, setKept] = createSignal<R[]>([], { ownedWrite: true });
  const [next, setNext] = createSignal<C | null | undefined>(undefined, { ownedWrite: true });
  const [loading, setLoading] = createSignal(false, { ownedWrite: true });
  const [error, setError] = createSignal<string | null>(null, { ownedWrite: true });
  let flight: AbortController | null = null;
  const stop = () => { flight?.abort(); flight = null; };
  onCleanup(stop);
  createEffect(() => o.reset?.(), () => { stop(); setKept([]); setNext(undefined); setLoading(false); setError(null); });
  const dedupe = (rows: R[]) => { const seen = new Set<string>(); return rows.filter(r => { const k = o.key(r); if (seen.has(k)) return false; seen.add(k); return true; }); };
  const rows = createMemo(() => {
    const f = o.first();
    if (!f) return [];
    const k = kept();
    return k.length ? dedupe([...f.rows, ...k]) : f.rows;
  });
  // Until an older page loads, the cursor is the live first page's.
  const cursor = createMemo(() => next() !== undefined ? next()! : o.first()?.next ?? null);
  const more = () => {
    if (flight || untrack(error) || !untrack(o.first)) return;
    const c = untrack(cursor);
    if (c === null) return;
    const ctl = new AbortController();
    flight = ctl;
    setLoading(true);
    o.fetch(c, ctl.signal).then(
      page => {
        if (flight !== ctl) return;
        flight = null;
        setKept(k => dedupe([...untrack(rows), ...k, ...page.rows]));
        setNext(() => page.next);
        setLoading(false);
      },
      (e: unknown) => {
        if (flight !== ctl) return;
        flight = null;
        setLoading(false);
        if (!ctl.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      },
    );
  };
  return {
    get rows() { return rows(); },
    get loading() { return loading(); },
    get error() { return error(); },
    get done() { return !!o.first() && cursor() === null; },
    more,
    retry: () => { setError(null); more(); },
  };
}

/**
 * Rows already in memory, built a page at a time as the end scrolls near (a long log, a board feed): the DOM never
 * holds rows nobody scrolled to. `reset` starts again from the first page.
 */
export function createWindow<R>(items: () => R[], step: number, reset?: () => unknown): Pages<R> {
  const [shown, setShown] = createSignal(step, { ownedWrite: true });
  createEffect(() => reset?.(), () => { setShown(step); });
  const rows = createMemo(() => items().slice(0, shown()));
  return {
    get rows() { return rows(); },
    loading: false,
    error: null,
    get done() { return shown() >= items().length; },
    more: () => setShown(n => n + step),
    retry: () => {},
  };
}

/** The nearest scrolling ancestor: the list end observes against it, so a list that scrolls inside a card loads too. */
function scrollRoot(el: Element): Element | null {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const y = getComputedStyle(p).overflowY;
    if (y === 'auto' || y === 'scroll') return p;
  }
  return null;
}

/**
 * The end of an infinite list: an invisible sentinel that asks for the next page when it comes within a few rows of
 * view, the quiet loading row while a page is in flight, one danger row with Retry when it failed, and nothing at all
 * once the list is complete. Place it right after the last row.
 */
export function ListEnd(p: { pages: Pager; what?: string; rows?: number }) {
  const [el, setEl] = createSignal<HTMLDivElement | undefined>(undefined, { ownedWrite: true });
  const [near, setNear] = createSignal(false, { ownedWrite: true });
  createEffect(el, node => {
    if (!node || typeof IntersectionObserver === 'undefined') return;
    // Five rows ahead of the end, so the next page is usually there before the person reaches it.
    const io = new IntersectionObserver(es => setNear(es.some(e => e.isIntersecting)), { root: scrollRoot(node), rootMargin: '0px 0px 200px 0px' });
    io.observe(node);
    return () => io.disconnect();
  });
  // One trigger per arrival: a page that leaves the end in view asks for the next as soon as it lands.
  createEffect(() => [near(), p.pages.loading, p.pages.error, p.pages.done] as const, ([n, loading, error, done]) => {
    if (n && !loading && !error && !done) p.pages.more();
  });
  return (
    <div class="list-end" data-ui="list-end">
      <div ref={setEl} class="list-sentinel" aria-hidden="true" />
      <Show when={p.pages.loading}><Skeleton rows={p.rows ?? 3} /></Show>
      <Show when={p.pages.error} keyed>{e => (
        <div class="list-error" role="alert">
          <Icon name="circle-alert" />
          <span class="grow">{p.what ?? 'Older rows'} did not load: {e}</span>
          <Button label="Retry" icon={actions.retry} onClick={p.pages.retry} />
        </div>
      )}</Show>
    </div>
  );
}

/** A list of rows followed by its end; rows past the first screen skip layout and paint until they scroll near. */
export function InfiniteList(p: { pages: Pager; children: JSX.Element; label: string; class?: string; endLabel?: string }) {
  return (
    <div class={['infinite-list', p.class]} data-ui="infinite-list" aria-label={p.label} aria-busy={p.pages.loading ? 'true' : undefined}>
      {p.children}
      <ListEnd pages={p.pages} what={p.endLabel} />
    </div>
  );
}
