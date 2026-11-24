// View state in the URL (docs/designs/fleet-view-urls-and-list-path.md §1). Every change is built from the URL as it is at
// the moment of the change, never from a value captured earlier (a debounced search must not undo a click made meanwhile).
import { createEffect, createMemo, createSignal, untrack } from 'solid-js';
import { readValue, type MaybeAccessor } from './signal.ts';
import { currentUrl, useLocation, useSearch } from './router.ts';
import { parseView, formatView, type View, type ViewName } from '../../packages/view-url/view-url.ts';

/** The URL after applying patch to the view in current (path and query). Pure, for the hooks below and tests. */
export const nextUrl = (current: string, name: ViewName, patch: Partial<View>) => formatView({ ...(parseView(current) ?? { view: name }), ...patch });
const here = currentUrl;

/** Discrete changes push a history entry; a search pushes once when typing starts and replaces while it goes on. */
export function useUrlView(name: ViewName) {
  const [loc, go] = useLocation();
  const search = useSearch();
  const currentUrl = createMemo(() => loc() + (search() ? '?' + search() : ''));
  const view = createMemo<View>(() => parseView(currentUrl()) ?? { view: name });
  // typing: the last change was a search commit made here, so the next one replaces it. Any other navigation (Back, Forward,
  // a link) moves the URL away from what we wrote and ends the search session.
  let typing = false;
  let wrote: string | null = null;
  createEffect(() => currentUrl(), url => {
    if (wrote !== null && url !== wrote) { typing = false; wrote = null; }
  });
  const set = (patch: Partial<View>, how: 'push' | 'type' = 'push') => {
    const cur = here(), next = nextUrl(cur, name, patch);
    if (next !== formatView(parseView(cur) ?? { view: name })) go(next, { replace: how === 'type' && typing });
    typing = how === 'type';
    wrote = how === 'type' ? next : null;
  };
  return {
    get view() { return view(); },
    set,
    href: (patch: Partial<View>) => formatView({ ...view(), ...patch }),
  };
}

/** Search box text: updates at once; the URL (and so the request) follows after 300 ms without a key press. */
export function useSearchBox(q: MaybeAccessor<string | undefined>, commit: (q: string) => void) {
  const initial = untrack(() => readValue(q)) ?? '';
  const [text, setText] = createSignal(initial, { ownedWrite: true });
  let last = initial;
  createEffect(
    () => readValue(q) ?? '',
    next => {
      if (next !== last) {
        last = next;
        if (untrack(text) !== next) setText(next); // Back/Forward or Clear filters
      }
    },
    { defer: true },
  );
  createEffect(
    () => text(),
    t => {
      const committed = untrack(() => readValue(q)) ?? '';
      if (t === committed) return;
      const timer = setTimeout(() => { last = t; commit(t); }, 300);
      return () => clearTimeout(timer);
    },
    { defer: true },
  );
  return [text, setText] as const;
}
