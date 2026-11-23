// Cross-message selection (docs/designs/fleet-conversations.md, "Copy", Stage 3 item 22). Selection is app state over block keys,
// so it spans rows outside the rendering window; Cmd+C asks the server for the markdown of the range (one builder, thread_export).
// Pointer ranges can cut the endpoint rows. Native browser selection stays for copying part of one row.
import { cmpKey } from './threads.ts';

export type Point = { key: string; offset: number };
export type Selection = { start: Point; end: Point; partial?: true } | null;
/** End offset that covers a whole text block (the server cuts text by its plain offsets and clamps). */
const WHOLE = Number.MAX_SAFE_INTEGER;

/** A click starts a selection at a row; Shift+click extends it from the start; the two ends are kept in key order. (The view
 * starts it without a tint on a plain click and shows it on Shift+click.) */
export function click(sel: Selection, key: string, shift: boolean): Selection {
  if (!shift || !sel) return { start: { key, offset: 0 }, end: { key, offset: WHOLE } };
  const anchor = sel.start.key;
  return cmpKey(key, anchor) < 0 ? { start: { key, offset: 0 }, end: { key: anchor, offset: WHOLE } } : { start: { key: anchor, offset: 0 }, end: { key, offset: WHOLE } };
}
/** A drag across replies, in either direction. Offsets are Unicode code points in each reply's source. */
export function drag(anchor: Point, focus: Point): Selection {
  if (anchor.key === focus.key) return null;
  return cmpKey(anchor.key, focus.key) < 0
    ? { start: anchor, end: focus, partial: true }
    : { start: focus, end: anchor, partial: true };
}

/** Each partial endpoint uses an in-text highlight. Rows between the endpoints keep the whole-row tint. */
export function textBounds(sel: Selection, key: string) {
  if (!sel?.partial) return;
  if (key === sel.start.key) return { from: sel.start.offset, to: WHOLE };
  if (key === sel.end.key) return { from: 0, to: sel.end.offset };
}

/** Cmd+A: the whole thread, loaded or not: keys below and above every real key. */
export const all = (): Selection => ({ start: { key: '0:0', offset: 0 }, end: { key: `${Number.MAX_SAFE_INTEGER}:0`, offset: WHOLE } });
/** Whether a row is inside the selection (for the highlight of rendered rows). */
export const inside = (sel: Selection, key: string) => !!sel && cmpKey(key, sel.start.key) >= 0 && cmpKey(key, sel.end.key) <= 0;
/** The range for thread_export. */
export const range = (sel: NonNullable<Selection>) => ({ from: sel.start, to: sel.end });
