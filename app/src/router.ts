// Lightweight History API router for Solid 2 replacing wouter-preact.
import type { Accessor } from 'solid-js';
import { cell } from './signal.ts';
import { toInternal, toTarget } from './routes.ts';

// The address bar shows target routes (routes.ts); views see the path they understand. An earlier path in the address bar
// (a bookmark, an outside link, Back to an old entry) is replaced by its target route, keeping the query and hash.
const redirect = () => {
  if (typeof location === 'undefined' || typeof history === 'undefined') return;
  const target = toTarget(location.pathname || '/');
  if (target !== location.pathname) history.replaceState(history.state, '', target + location.search + location.hash);
};
redirect();
const readPath = () => (typeof location !== 'undefined' ? toInternal(location.pathname || '/') : '/');
const readQuery = () => (typeof location !== 'undefined' ? location.search.slice(1) : '');

const locCell = cell(readPath());
const searchCell = cell(readQuery());

export function syncLocation() {
  redirect();
  locCell.value = readPath();
  searchCell.value = readQuery();
}

if (typeof window !== 'undefined') {
  window.addEventListener('popstate', syncLocation);
}

export const pathname: Accessor<string> = () => locCell.value;
/** The view's path and query as they are now (not tracked): for a change built from the URL at the moment of the change. */
export const currentUrl = () => readPath() + (typeof location !== 'undefined' ? location.search : '');
export const search: Accessor<string> = () => searchCell.value;

export function navigate(to: string, opts?: { replace?: boolean }) {
  if (typeof location === 'undefined' || typeof history === 'undefined') {
    const [pathPart, queryPart = ''] = to.split('?');
    locCell.value = toInternal(pathPart || '/');
    searchCell.value = queryPart.split('#')[0] ?? '';
    return;
  }
  const url = new URL(to, location.origin);
  const next = toTarget(url.pathname) + url.search + url.hash;
  if (opts?.replace) history.replaceState(null, '', next);
  else history.pushState(null, '', next);
  locCell.value = toInternal(url.pathname || '/');
  searchCell.value = url.search.slice(1);
}

/** Reactive pathname accessor and imperative navigator, matching Solid 2 `[getter, action]` conventions. */
export function useLocation(): readonly [Accessor<string>, typeof navigate] {
  return [pathname, navigate] as const;
}

/** Reactive query string accessor (without leading `?`). */
export function useSearch(): Accessor<string> {
  return search;
}
