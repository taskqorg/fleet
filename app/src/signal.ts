// Solid 2 reactive cell and accessor helpers bridging synchronous imperative code (tests, store, access) and Solid 2 tracking.
import { createSignal, getObserver, isEqual, runWithOwner, type Accessor } from 'solid-js';

export type Cell<T> = {
  get value(): T;
  set value(next: T);
  peek(): T;
};

/**
 * A reactive cell with synchronous imperative `.value` reads/writes and Solid 2 tracking inside reactive scopes.
 * Signals live outside component owners so module-level state and async callbacks never fail owned-write guards.
 */
export function cell<T>(initial: Exclude<T, Function>): Cell<T> {
  const [get, set] = runWithOwner(null, () => createSignal<T>(initial, { ownedWrite: true }));
  let cur: T = initial;
  return {
    get value() {
      return getObserver() ? get() : cur;
    },
    set value(next: T) {
      if (isEqual(cur, next)) return;
      cur = next;
      set(() => next);
    },
    peek() {
      return cur;
    },
  };
}

export type MaybeAccessor<T> = T | Accessor<T>;

export function readValue<T>(v: MaybeAccessor<T>): T {
  return typeof v === 'function' ? (v as Accessor<T>)() : v;
}
