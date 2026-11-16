// Browser error reporter (docs/designs/fleet-telemetry.md section 9): uncaught errors, unhandled rejections and render
// crashes go to client_error_report. One error (name, message, top frame) is sent once per window with the repeats
// counted; a tab sends at most maxReports reports, so a render loop cannot flood the coordinator. Sends use keepalive, so
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

const frameOf = (stack: string) => stack.split('\n').map(l => l.trim()).find(l => /^at\s|@\S+:\d+/.test(l)) ?? '';

export type ReporterOptions = {
  send: (report: ClientErrorReport) => void;
  path: () => string;
  version: () => string;
  userAgent: string;
  now?: () => number;
  /** One error is sent once per window; repeats inside it go out with the next report or flush as a count. */
  windowMs?: number;
  /** The most reports one tab sends; later errors are counted in dropped only. */
  maxReports?: number;
  setTimer?: (fn: () => void, ms: number) => unknown;
};

type Entry = { sentAt: number; pending: number; firstAt: number; report: Omit<ClientErrorReport, 'count' | 'at'> };

export function createReporter(o: ReporterOptions) {
  const now = o.now ?? Date.now, windowMs = o.windowMs ?? 5 * 60_000, max = o.maxReports ?? 30;
  const setTimer = o.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms));
  const entries = new Map<string, Entry>();
  let sent = 0, dropped = 0;
  const emit = (e: Entry, count: number, at: number) => {
    if (sent >= max) { dropped += count; return; }
    sent++;
    try { o.send({ ...e.report, count, at: new Date(at).toISOString() }); } catch { /* never throws */ }
  };
  const flushEntry = (e: Entry) => {
    if (e.pending <= 0) return;
    const n = e.pending; e.pending = 0; e.sentAt = now(); emit(e, n, e.firstAt);
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
        if (e && t - e.sentAt < windowMs) {
          e.report = report;
          if (e.pending++ === 0) { e.firstAt = t; setTimer(() => { try { flushEntry(e); } catch { /* never throws */ } }, e.sentAt + windowMs - t); }
          return;
        }
        const next: Entry = { sentAt: t, pending: 0, firstAt: t, report }, carried = e?.pending ?? 0;
        if (e) e.pending = 0; // its timer, if still due, then sends nothing
        entries.set(key, next);
        emit(next, 1 + carried, carried ? e!.firstAt : t);
      } catch { /* never throws */ }
    },
    /** Sends every counted repeat now (the tab is hidden or unloading). */
    flush() { try { for (const e of entries.values()) flushEntry(e); } catch { /* never throws */ } },
    stats: () => ({ sent, dropped }),
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
