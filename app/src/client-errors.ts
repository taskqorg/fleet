// Browser error reporter (docs/designs/fleet-telemetry.md section 9): uncaught errors, unhandled rejections and render
// crashes go to client_error_report. One error (name, message, top frame) is sent once per window with the repeats
// counted; a tab sends at most maxReports first reports and maxRepeats repeat counts, so a render loop cannot flood the coordinator. Sends use keepalive, so
// an error right before unload still arrives. The person comes from the session on the server; the reporter never throws.
import type { Ops } from '../../contracts/generated/ops.ts';
import { parseView, formatView, VIEW_EXTRA } from '../../packages/view-url/view-url.ts';

export type ClientErrorReport = Ops['client_error_report']['request'];
export type ClientErrorSource = ClientErrorReport['source'];

/** The server's byte limits (ClientErrorReportRequest); the reporter trims to them so a report is never refused. */
export const LIMITS = { name: 200, message: 2000, stack: 8192, path: 2048, version: 200, userAgent: 512 } as const;

const encoder = new TextEncoder(), decoder = new TextDecoder();
/** s cut to at most max UTF-8 bytes, never inside a character. */
export function trimBytes(s: string, max: number): string {
  const b = encoder.encode(s);
  if (b.length <= max) return s;
  return decoder.decode(b.subarray(0, max)).replace(/�+$/, '');
}

/**
 * The page path the report carries: an operator-app view keeps its path and the query keys the app itself writes
 * (view-url's table and VIEW_EXTRA, values as found, so a malformed one that broke the page stays visible); any other
 * key is dropped, and so is the whole query of a path that is not a view.
 */
export function reportPath(pathname: string, search: string): string {
  const v = parseView(pathname + search);
  if (!v) return pathname || '/';
  const own = VIEW_EXTRA[v.view] ?? [];
  return formatView({ ...v, extra: Object.fromEntries(Object.entries(v.extra ?? {}).filter(([k]) => own.includes(k))) });
}

/** The fields of an error-like value; a thrown non-Error becomes its text. */
export function describe(error: unknown): { name: string; message: string; stack: string } {
  if (error instanceof Error || (error && typeof error === 'object' && 'message' in error)) {
    const e = error as { name?: unknown; message?: unknown; stack?: unknown };
    return { name: String(e.name || 'Error'), message: String(e.message ?? ''), stack: typeof e.stack === 'string' ? e.stack : '' };
  }
  let message: string;
  try { message = typeof error === 'string' ? error : JSON.stringify(error) ?? String(error); } catch { message = String(error); }
  return { name: 'NonError', message, stack: '' };
}

/** The top frame as the server's fingerprint takes it: the first script frame, else the first built-in one. */
export function frameOf(stack: string): string {
  const frames = stack.split('\n').map(l => l.trim()).filter(l => /^at\s|^[^@\s][^@]*@\S+:\d+|^@\S+:\d+/.test(l));
  return frames.find(l => l.includes('/')) ?? frames[0] ?? '';
}

/** The server's repeat count limit; more repeats in one window are counted in dropped. */
const MAX_COUNT = 1_000_000;
/** A repeat-count send carries only the stack's head: enough for the top frame, small enough for the keepalive quota. */
const REPEAT_STACK = 1024;

export type ReporterOptions = {
  send: (report: ClientErrorReport) => void;
  path: () => string;
  version: () => string;
  userAgent: string;
  now?: () => number;
  /** One error is sent once per window; repeats inside it go out as a count when it ends or on flush. */
  windowMs?: number;
  /** The most first reports (one per new error) a tab sends; later new errors are counted in dropped only. */
  maxReports?: number;
  /** The most repeat-count sends a tab makes (window ends and flushes); later repeats are counted in dropped only. */
  maxRepeats?: number;
  /** The most repeat counts one flush sends (keepalive bodies share a 64 KiB budget on unload). */
  flushEntries?: number;
  setTimer?: (fn: () => void, ms: number) => unknown;
};

type Entry = { sentAt: number; pending: number; firstAt: number; timer: number; report: Omit<ClientErrorReport, 'count' | 'at'> };

export function createReporter(o: ReporterOptions) {
  const now = o.now ?? Date.now, windowMs = o.windowMs ?? 5 * 60_000, max = o.maxReports ?? 30, maxRepeats = o.maxRepeats ?? 100, perFlush = o.flushEntries ?? 10;
  const setTimer = o.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms));
  const entries = new Map<string, Entry>();
  let sent = 0, repeats = 0, dropped = 0;
  const send = (report: ClientErrorReport) => { try { o.send(report); } catch { /* never throws */ } };
  const emitFirst = (e: Entry) => {
    if (sent >= max) { dropped++; return; }
    sent++; send({ ...e.report, count: 1, at: new Date(e.firstAt).toISOString() });
  };
  const flushEntry = (e: Entry) => {
    if (e.pending <= 0) return;
    const n = Math.min(e.pending, MAX_COUNT);
    dropped += e.pending - n; e.pending = 0; e.timer++; e.sentAt = now();
    if (repeats >= maxRepeats) { dropped += n; return; }
    repeats++; send({ ...e.report, stack: trimBytes(e.report.stack, REPEAT_STACK), count: n, at: new Date(e.firstAt).toISOString() });
  };
  return {
    capture(error: unknown, source: ClientErrorSource) {
      try {
        const d = describe(error), t = now();
        const name = trimBytes(d.name, LIMITS.name) || 'Error', message = trimBytes(d.message, LIMITS.message);
        const key = name + '\n' + message + '\n' + frameOf(d.stack);
        const report = { name, message, stack: trimBytes(d.stack, LIMITS.stack), source, path: trimBytes(o.path(), LIMITS.path) || '/',
          version: trimBytes(o.version(), LIMITS.version), userAgent: trimBytes(o.userAgent, LIMITS.userAgent) };
        const e = entries.get(key);
        if (!e) {
          const next: Entry = { sentAt: t, pending: 0, firstAt: t, timer: 0, report };
          entries.set(key, next); emitFirst(next); return;
        }
        e.report = report;
        if (t - e.sentAt >= windowMs && e.pending === 0) { e.sentAt = t; e.firstAt = t; e.pending = 1; flushEntry(e); return; }
        if (e.pending++ === 0) {
          e.firstAt = t;
          const token = ++e.timer;
          // A flush in between (tab hidden) moves the window: this timer then sends nothing.
          setTimer(() => { try { if (e.timer === token) flushEntry(e); } catch { /* never throws */ } }, Math.max(0, e.sentAt + windowMs - t));
        }
      } catch { /* never throws */ }
    },
    /** Sends the largest counted repeats now (the tab is hidden or unloading). */
    flush() { try { [...entries.values()].filter(e => e.pending > 0).sort((a, b) => b.pending - a.pending).slice(0, perFlush).forEach(flushEntry); } catch { /* never throws */ } },
    stats: () => ({ sent, repeats, dropped }),
  };
}

export type Reporter = ReturnType<typeof createReporter>;

/** Same-origin keepalive POST: not op(), so a report neither waits for access validation nor marks the app offline or signed out. */
export function sendReport(report: ClientErrorReport) {
  try {
    fetch('/api/ops/client_error_report', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(report),
      credentials: 'same-origin', redirect: 'manual', keepalive: true }).catch(() => {});
  } catch { /* never throws */ }
}

/**
 * Installs the window handlers. ignore: errors already reported elsewhere (an operation's own failure is in the
 * coordinator's spans).
 */
export function installReporter(r: Reporter, ignore: (error: unknown) => boolean = () => false) {
  addEventListener('error', ev => {
    const error = ev.error ?? { name: 'Error', message: ev.message || 'Script error', stack: ev.filename ? `at ${ev.filename}:${ev.lineno}:${ev.colno}` : '' };
    if (!ignore(error)) r.capture(error, 'error');
  });
  addEventListener('unhandledrejection', ev => { if (!ignore(ev.reason)) r.capture(ev.reason, 'rejection'); });
  addEventListener('pagehide', () => r.flush());
  addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') r.flush(); });
}
