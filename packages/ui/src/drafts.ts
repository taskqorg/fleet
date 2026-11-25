// Drafts (controls/text-field.md#drafts): typed text lives here, keyed by what it is for (a question, a form field),
// not in the component that shows it. A remount, a route change or a reload brings the text back. The app installs a
// persistent, person-scoped backend (setDraftBackend); without one, drafts live in memory for the page's life.
import { createEffect, createSignal, runWithOwner, untrack, type Accessor } from 'solid-js';

export type DraftBackend = { read(key: string): string; write(key: string, text: string): void };

const memory = new Map<string, string>();
const inMemory: DraftBackend = { read: k => memory.get(k) ?? '', write: (k, t) => { if (t) memory.set(k, t); else memory.delete(k); } };
let backend: DraftBackend = inMemory;
const [epoch, setEpoch] = runWithOwner(null, () => createSignal(0, { ownedWrite: true }));

/** The app's backend (person-scoped storage); a failing backend falls back to memory for that call. */
export function setDraftBackend(b: DraftBackend | null) { backend = b ?? inMemory; refreshDrafts(); }
/** The backend can answer now (the person became known): mounted drafts load what is stored, or store what was typed. */
export function refreshDrafts() { setEpoch(n => n + 1); }
/** Tracks refreshDrafts: a box that keeps its own text (Composer) reloads it when this changes. */
export const draftEpoch = epoch;

/** A Composer persistence over the draft store: Composer's `draftKey`. */
export function draftPersistence(key: string) {
  return {
    key: 'draft:' + key,
    read: () => readDraft(key),
    write: (text: string) => writeDraft(key, text),
    clearIfUnchanged: (text: string) => { if (readDraft(key) === text) clearDraft(key); },
  };
}

function readDraft(key: string): string {
  try { return backend.read(key); } catch { return inMemory.read(key); }
}
function writeDraft(key: string, text: string) {
  inMemory.write(key, text);
  try { backend.write(key, text); } catch { /* kept in memory */ }
}
const EMPTIED = '\u0000';
/** Forget a draft, after its text was sent or saved. */
export function clearDraft(key: string) { writeDraft(key, ''); }

/**
 * A text signal kept in the draft store under `key` (a string, or an accessor when the target can change). Reads start
 * from the stored text, else `initial`; every write is stored; `clear` empties it after a send or save. An undefined
 * key keeps the text in the component only (a secret, a search box).
 */
export function createDraft(key: string | Accessor<string | undefined>, initial = ''): [Accessor<string>, (text: string) => void, () => void] {
  const keyOf = typeof key === 'function' ? key : () => key;
  // An emptied field with a default is stored as EMPTIED, so a reload does not bring the default back.
  const load = (k: string | undefined) => { const v = k ? readDraft(k) : ''; return v === EMPTIED ? '' : v || initial; };
  const [text, setText] = createSignal(untrack(() => load(keyOf())), { ownedWrite: true });
  let current = untrack(keyOf);
  createEffect(() => [keyOf(), epoch()] as const, ([k]) => {
    if (k !== current) { current = k; setText(load(k)); return; }
    if (!k) return;
    const typed = untrack(text);
    if (typed === initial) setText(load(k)); else writeDraft(k, typed || EMPTIED);
  });
  const set = (t: string) => { setText(t); const k = untrack(keyOf); if (k) writeDraft(k, t === initial ? '' : t || EMPTIED); };
  const clear = () => { setText(initial); const k = untrack(keyOf); if (k) clearDraft(k); };
  return [text, set, clear];
}
