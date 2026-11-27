// Message attachments in the browser (docs/designs/fleet-conversations.md, Attachments): each file is uploaded into the
// artifact store as soon as it is added (artifact_upload_begin, then PUT parts with Content-Range), with its progress
// shown on its chip; a send waits until every file is stored and passes their artifact ids.
import { createEffect, createMemo, createSignal } from 'solid-js';
import type { ComposerAttachment } from '../../packages/ui/src/index.ts';
import { readValue, type MaybeAccessor } from './signal.ts';
import { op, OpError } from './api.ts';

export type UploadTarget = { sessionId?: string; projectId?: string | null };
type Item = ComposerAttachment & { artifactId?: string; done: Promise<string>; abort: AbortController };

type PartBody = { id?: string; received?: number; finishing?: boolean; message?: string } | null;

let activePutParts = 0;
const attachmentSources = new Set<() => readonly Item[]>();
/** Whether any attachment XHR or upload is currently running. */
export const uploadsInFlight = () => {
  if (activePutParts > 0) return true;
  for (const read of attachmentSources) if (read().some(x => x.state === 'uploading')) return true;
  return false;
};
/** Whether any composer currently holds an uploaded attachment chip waiting in memory to be sent. */
export const hasReadyAttachments = () => {
  for (const read of attachmentSources) if (read().some(x => x.state === 'ready')) return true;
  return false;
};

/** PUT one part; progress reports the bytes of this part sent so far. */
function putPart(url: string, body: Blob, range: string, signal: AbortSignal, progress: (sent: number) => void): Promise<{ status: number; body: PartBody }> {
  activePutParts++;
  return new Promise<{ status: number; body: PartBody }>((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open('PUT', url);
    x.setRequestHeader('content-range', range);
    // One listener per part, removed when the part ends, so a long upload keeps no finished requests alive.
    const stop = () => x.abort(), done = () => signal.removeEventListener('abort', stop);
    x.upload.onprogress = e => progress(e.loaded);
    x.onload = () => { done(); let b: PartBody = null; try { const parsed: unknown = JSON.parse(x.responseText); if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) b = parsed as PartBody; } catch { /* not JSON: the status says enough */ } resolve({ status: x.status, body: b }); };
    x.onerror = () => { done(); resolve({ status: 0, body: null }); }; // a dropped connection: sent again, resuming at the server's received
    x.onabort = () => { done(); reject(new OpError('aborted', 0)); };
    signal.addEventListener('abort', stop, { once: true });
    x.send(body);
  }).finally(() => { activePutParts--; });
}

const wait = (ms: number, signal: AbortSignal) => new Promise<void>((ok, no) => {
  if (signal.aborted) return no(new OpError('aborted', 0));
  const t = setTimeout(() => { signal.removeEventListener('abort', stop); ok(); }, ms);
  const stop = () => { clearTimeout(t); no(new OpError('aborted', 0)); };
  signal.addEventListener('abort', stop, { once: true });
});

// At most this many files upload at once; the rest wait their turn (each part is held whole by the coordinator).
const PARALLEL = 3;
let running = 0;
const queue: (() => void)[] = [];
async function turn<T>(signal: AbortSignal, fn: () => Promise<T>): Promise<T> {
  // A slot is handed over to a waiter as it frees, so nobody else takes it in between.
  if (running >= PARALLEL) await new Promise<void>(ok => queue.push(ok)); else running++;
  try { if (signal.aborted) throw new OpError('aborted', 0); return await fn(); }
  finally { const next = queue.shift(); if (next) next(); else running--; }
}

/**
 * Upload one file; resolves to its artifact id. Parts go in order on the server's grid; a refused or cut-off part
 * resumes where the server's file ends, a transient failure is sent again with backoff, and a finish still running
 * (or a lost reply to the last part) is asked again by resending the last part. Stops as soon as signal aborts.
 */
export function uploadFile(file: File, target: UploadTarget, signal: AbortSignal, progress: (fraction: number) => void): Promise<string> {
  return turn(signal, async () => {
    const begun = await op('artifact_upload_begin', { name: file.name || 'file', size: file.size, ...(file.type ? { mediaType: file.type } : {}), ...target }, { signal });
    const part = begun.partBytes;
    let first = 0, tries = 0, same = 0;
    for (;;) {
      if (signal.aborted) throw new OpError('aborted', 0);
      // With the whole file on the server (or an empty file) a bodyless probe asks for the artifact.
      const probe = first >= file.size, last = Math.min(file.size, first + part) - 1;
      const range = probe ? `bytes */${file.size}` : `bytes ${first}-${last}/${file.size}`;
      const r = await putPart(begun.url, probe ? new Blob([]) : file.slice(first, last + 1), range, signal, sent => progress(file.size ? Math.min(1, (first + sent) / file.size) : 1));
      if (r.status === 200 && r.body?.id) { progress(1); return r.body.id; }
      const received = r.body?.received;
      if ((r.status === 200 || r.status === 409) && typeof received === 'number') {
        // Progress resets the counters; the same answer again (a cut-off part, a finish running) waits a little longer.
        same = received > first && !r.body?.finishing ? 0 : same + 1;
        if (same > (r.body?.finishing ? 1000 : 8)) throw new OpError(r.body?.message ?? 'upload does not advance', r.status);
        if (same) await wait(Math.min(10_000, 500 * 2 ** same), signal);
        first = received; tries = 0;
        continue;
      }
      // A transient failure is sent again; anything else is the file's error.
      if ([0, 500, 502, 503, 504, 520, 521, 522, 523, 524].includes(r.status) && tries < 6) { await wait(500 * 2 ** tries++, signal); continue; }
      throw new OpError(r.body?.message ?? `upload failed (${r.status})`, r.status);
    }
  });
}

/** The composer's attachments for one target: add, remove, wait for all, clear after a send. */
export function useAttachments(targetInput: MaybeAccessor<UploadTarget>) {
  const [items, setItems] = createSignal<Item[]>([], { ownedWrite: true });
  let live: Item[] = [];
  const updateItems = (fn: (xs: Item[]) => Item[]) => {
    live = fn(live);
    setItems(live);
  };
  let currentTarget: UploadTarget = {};
  const readLive = () => live;
  attachmentSources.add(readLive);
  const key = createMemo(() => JSON.stringify(readValue(targetInput)));
  // A new target starts empty; unmounting stops uploads and frees previews.
  createEffect(
    () => [key(), readValue(targetInput)] as const,
    ([, target]) => {
      currentTarget = target;
      return () => {
        attachmentSources.delete(readLive);
        for (const i of live) { i.abort.abort(); if (i.preview) URL.revokeObjectURL(i.preview); }
        updateItems(() => []);
      };
    },
  );
  const patch = (id: string, p: Partial<Item>) => updateItems(xs => xs.map(x => x.id === id ? { ...x, ...p } : x));
  const add = (files: File[]) => {
    const target = currentTarget;
    const fresh = files.map(file => {
      const id = crypto.randomUUID(), abort = new AbortController();
      const item: Item = { id, name: file.name || 'file', size: file.size, mediaType: file.type || 'application/octet-stream', state: 'uploading', progress: 0, abort,
        preview: /^image\//.test(file.type) ? URL.createObjectURL(file) : undefined, done: null as unknown as Promise<string> };
      item.done = uploadFile(file, target, abort.signal, progress => patch(id, { progress }))
        .then(artifactId => { patch(id, { state: 'ready', artifactId, progress: 1 }); return artifactId; },
          e => { if (!abort.signal.aborted) patch(id, { state: 'failed', error: e?.message ?? String(e) }); throw e; });
      item.done.catch(() => { /* shown on the chip */ });
      return item;
    });
    updateItems(xs => [...xs, ...fresh]);
  };
  const remove = (id: string) => updateItems(xs => xs.filter(x => { if (x.id !== id) return true; x.abort.abort(); if (x.preview) URL.revokeObjectURL(x.preview); return false; }));
  /** The artifact ids of every file, once all are stored; a failed file fails the send. */
  const ready = async (): Promise<string[]> => {
    // A file removed while the send waits drops out of it; the files still there decide.
    for (;;) {
      const now = live;
      const settled = await Promise.allSettled(now.map(x => x.done));
      if (now.every(x => live.includes(x)) && live.length === now.length) {
        const failed = settled.find(r => r.status === 'rejected') as PromiseRejectedResult | undefined;
        if (failed) throw failed.reason;
        return settled.map(r => (r as PromiseFulfilledResult<string>).value);
      }
    }
  };
  const clear = (sent: string[]) => updateItems(xs => xs.filter(x => { if (!x.artifactId || !sent.includes(x.artifactId)) return true; if (x.preview) URL.revokeObjectURL(x.preview); return false; }));
  return { get items() { return items() as ComposerAttachment[]; }, add, remove, ready, clear };
}
