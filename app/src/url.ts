// View state in the URL (docs/designs/fleet-view-urls-and-list-path.md §1). Every change is built from the URL as it is at
// the moment of the change, never from a value captured earlier (a debounced search must not undo a click made meanwhile).
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation, useSearch } from 'wouter-preact';
import { parseView, formatView, type View, type ViewName } from '../../packages/view-url/view-url';

/** The URL after applying patch to the view in current (path and query). Pure, for the hooks below and tests. */
export const nextUrl = (current: string, name: ViewName, patch: Partial<View>) => formatView({ ...(parseView(current) ?? { view: name }), ...patch });
const here = () => location.pathname + location.search;

/** Discrete changes push a history entry; a search pushes once when typing starts and replaces while it goes on. */
export function useUrlView(name: ViewName) {
  const search = useSearch(), [loc, go] = useLocation();
  const view: View = parseView(loc + (search ? '?' + search : '')) ?? { view: name };
  // typing: the last change was a search commit made here, so the next one replaces it. Any other navigation (Back, Forward,
  // a link) moves the URL away from what we wrote and ends the search session.
  const typing = useRef(false), wrote = useRef<string | null>(null);
  const url = loc + (search ? '?' + search : '');
  if (wrote.current !== null && url !== wrote.current) { typing.current = false; wrote.current = null; }
  const set = (patch: Partial<View>, how: 'push' | 'type' = 'push') => {
    const cur = here(), next = nextUrl(cur, name, patch);
    if (next !== formatView(parseView(cur) ?? { view: name })) go(next, { replace: how === 'type' && typing.current });
    typing.current = how === 'type'; wrote.current = how === 'type' ? next : null;
  };
  return { view, set, href: (patch: Partial<View>) => formatView({ ...view, ...patch }) };
}

/** Search box text: updates at once; the URL (and so the request) follows after 300 ms without a key press. */
export function useSearchBox(q: string | undefined, commit: (q: string) => void) {
  const [text, setText] = useState(q ?? '');
  const last = useRef(q ?? '');
  if ((q ?? '') !== last.current) { last.current = q ?? ''; if (text !== last.current) setText(last.current); } // Back/Forward
  useEffect(() => {
    if (text === (q ?? '')) return;
    const t = setTimeout(() => { last.current = text; commit(text); }, 300);
    return () => clearTimeout(t);
  }, [text]);
  return [text, setText] as const;
}
