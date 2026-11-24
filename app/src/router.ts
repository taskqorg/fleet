// Lightweight History API router for Solid 2 replacing wouter-preact.
import type { Accessor } from 'solid-js';
import { cell } from './signal.ts';
import { internalUrl, targetUrl } from './routes.ts';

// The address bar shows target routes (routes.ts); views see the path they understand. An earlier path in the address bar
// (a bookmark, an outside link, Back to an old entry) is replaced by its target route, keeping the query and hash.
// A project's facet keeps its project in the path (routes.ts), so the query takes part in both translations.
const address = () => (location.pathname || '/') + location.search;
const redirect = () => {
  if (typeof location === 'undefined' || typeof history === 'undefined') return;
  const target = targetUrl(address());
  if (target !== address()) history.replaceState(history.state, '', target + location.hash);
};
redirect();
const readInternal = () => (typeof location !== 'undefined' ? internalUrl(address()) : '/');
const readPath = () => readInternal().split('?')[0];
const readQuery = () => readInternal().split('?').slice(1).join('?');

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
export const currentUrl = () => (typeof location !== 'undefined' ? readInternal() : locCell.value + (searchCell.value ? '?' + searchCell.value : ''));
export const search: Accessor<string> = () => searchCell.value;

export function navigate(to: string, opts?: { replace?: boolean }) {
  if (typeof location === 'undefined' || typeof history === 'undefined') {
    const [pathPart, ...rest] = internalUrl(to.split('#')[0] || '/').split('?');
    locCell.value = pathPart || '/';
    searchCell.value = rest.join('?');
    return;
  }
  const url = new URL(to, location.origin);
  const next = targetUrl(url.pathname + url.search) + url.hash;
  if (opts?.replace) history.replaceState(null, '', next);
  else history.pushState(null, '', next);
  syncLocation();
}

/** Reactive pathname accessor and imperative navigator, matching Solid 2 `[getter, action]` conventions. */
export function useLocation(): readonly [Accessor<string>, typeof navigate] {
  return [pathname, navigate] as const;
}

/** Reactive query string accessor (without leading `?`). */
export function useSearch(): Accessor<string> {
  return search;
}
