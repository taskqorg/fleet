// Lightweight History API router for Solid 2 replacing wouter-preact.
import type { Accessor } from 'solid-js';
import { cell } from './signal.ts';

const readPath = () => (typeof location !== 'undefined' ? location.pathname || '/' : '/');
const readQuery = () => (typeof location !== 'undefined' ? location.search.slice(1) : '');

const locCell = cell(readPath());
const searchCell = cell(readQuery());

export function syncLocation() {
  locCell.value = readPath();
  searchCell.value = readQuery();
}

if (typeof window !== 'undefined') {
  window.addEventListener('popstate', syncLocation);
}

export const pathname: Accessor<string> = () => locCell.value;
export const search: Accessor<string> = () => searchCell.value;

export function navigate(to: string, opts?: { replace?: boolean }) {
  if (typeof location === 'undefined' || typeof history === 'undefined') {
    const [pathPart, queryPart = ''] = to.split('?');
    locCell.value = pathPart || '/';
    searchCell.value = queryPart.split('#')[0] ?? '';
    return;
  }
  const url = new URL(to, location.origin);
  const next = url.pathname + url.search + url.hash;
  if (opts?.replace) history.replaceState(null, '', next);
  else history.pushState(null, '', next);
  locCell.value = url.pathname || '/';
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
