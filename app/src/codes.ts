// An item in the URL by its human code (OR-38, UR-69; D17): `/projects/FLT/tasks/T-FLT-42` names the task by its code.
// Views read entities by id; this resolves a code to the id through entity_resolve and passes an id through unchanged.
import { createMemo, type Accessor } from 'solid-js';
import { useOp } from './store.ts';

/** A D17 code (T-FLT-42, R-FLT-7, …), as entity_resolve takes it. */
export const isCode = (s: string | undefined): s is string => !!s && /^(?:[TQECRB]-[A-Z][A-Z0-9]{1,4}-[1-9][0-9]*|S-[A-Z][A-Z0-9]{1,4}-[A-DF-PRU-Z][1-9][0-9]*)$/i.test(s);

/**
 * The id the URL item names: a code resolved in the person's scope, else the item itself. Undefined while a code resolves;
 * an unknown or forbidden code stays the code, so the detail reports it as not found.
 */
export function useItemId(item: Accessor<string | undefined>): Accessor<string | undefined> {
  const doc = useOp('entity_resolve', () => (isCode(item()) ? { code: item()! } : null));
  return createMemo(() => {
    const x = item();
    if (!isCode(x)) return x;
    if (doc.data && doc.data.code.toUpperCase() === x.toUpperCase()) return doc.data.id;
    return doc.error ? x : undefined;
  });
}
