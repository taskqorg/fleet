// The installed version and the app update (docs/design-system/controls/nav-rail.md, Version).
//
// Update detection compares what this tab runs with what the site serves now: the hashed entry script and stylesheet
// named in the page this tab loaded against the ones in a fresh copy of the page. It does not depend on service worker
// events, so it gives the same answer on every check, and after an update the reloaded tab matches the site.
import { signal } from '@preact/signals';
import { type Tone, type VersionInfo, type VersionRow } from '../../packages/ui/src/index.ts';
import { op, type Ops } from './api.ts';
import { accessGeneration, isAccessCurrent, onAccessInvalidated } from './access.ts';

type Versions = Ops['system_versions']['reply'];
export const versions = signal<Versions | null>(null);
export const updateReady = signal(false);
export const updating = signal(false);
export const updateChecked = signal(false);
export const versionObservedAt = signal<number | null>(null);
export const versionError = signal<string | null>(null);
export const updateError = signal<string | null>(null);
onAccessInvalidated.add(() => {
  versions.value = null; tabVersion.value = null; versionRead++; updateRead++; versionObservedAt.value = null;
  versionError.value = null; updateChecked.value = false; updateReady.value = false; updateError.value = null;
});
/** The site's UI version at a moment this tab was known to run the site's app (its assets matched before and after the read). */
export const tabVersion = signal<string | null>(null);
let versionRead = 0, updateRead = 0;

// The entry script and the stylesheet carry content hashes (app/build.mjs), so a new app always changes one of them.
const assetsOf = (html: string) => [...html.matchAll(/(?:src|href)="(\/assets\/(?:main|app)-[^"]+\.(?:js|css))"/g)].map(m => m[1]).sort().join(' ');
const own = () => assetsOf(document.head.innerHTML);

/** A fresh copy of the page the site serves now; null when it cannot be read (offline, signed out, not the app). */
async function servedAssets(): Promise<string | null> {
  try {
    const r = await fetch('/overview', { cache: 'no-store', credentials: 'same-origin', headers: { accept: 'text/html' } });
    if (!r.ok || !r.headers.get('content-type')?.startsWith('text/html')) return null;
    return assetsOf(await r.text()) || null;
  } catch { return null; }
}

export async function checkUpdate() {
  const read = ++updateRead, generation = accessGeneration(), mine = own(), served = await servedAssets();
  if (!isAccessCurrent(generation) || read !== updateRead) return;
  updateChecked.value = !!mine && !!served;
  if (mine && served) updateReady.value = served !== mine;
}

export async function loadVersions() {
  const read = ++versionRead, generation = accessGeneration();
  try {
    const mine = own(), before = await servedAssets();
    if (!isAccessCurrent(generation) || read !== versionRead) return;
    const v = await op('system_versions', {});
    if (!isAccessCurrent(generation) || read !== versionRead) return;
    versions.value = v; versionObservedAt.value = Date.now(); versionError.value = null;
    const after = await servedAssets();
    if (!isAccessCurrent(generation) || read !== versionRead) return;
    if (mine && before === mine && after === mine) tabVersion.value = v.ui.version;
  } catch (error) { if (isAccessCurrent(generation) && read === versionRead) versionError.value = error instanceof Error ? error.message : String(error); }
}

/** Release versions compare by numeric parts (1.261115.6); anything else is not ordered. */
export const olderThan = (a: string | null | undefined, b: string | null | undefined) => {
  const pa = a?.split('.').map(Number), pb = b?.split('.').map(Number);
  if (!a || !b || !/^\d+\.\d+\.\d+$/.test(a) || !/^\d+\.\d+\.\d+$/.test(b) || !pa || !pb || [...pa, ...pb].some(n => !Number.isSafeInteger(n))) return null;
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] < pb[i];
  return false;
};

/**
 * Reload into the new app on the first click: the waiting service worker (if any) takes over first, so the reload is
 * served by the new worker and its cache; without one the reload alone fetches the new page (navigations go to the network).
 */
export async function applyUpdate() {
  if (updating.value) return;
  updating.value = true;
  updateError.value = null;
  try {
    // A reload after an unreadable page could return an old offline shell, so verify the site before proceeding.
    if (!await servedAssets()) throw Error('The current site app is unavailable. Retry when connected.');
    const reg = 'serviceWorker' in navigator ? await navigator.serviceWorker.getRegistration() : undefined;
    if (reg) {
      await reg.update();
      const installing = reg.installing;
      if (installing && !reg.waiting) await new Promise<void>((resolve, reject) => {
        const finish = () => {
          if (installing.state === 'installed' || installing.state === 'activated') { clearTimeout(timer); installing.removeEventListener('statechange', finish); resolve(); }
          else if (installing.state === 'redundant') { clearTimeout(timer); installing.removeEventListener('statechange', finish); reject(Error('App installation failed. Retry update.')); }
        };
        const timer = setTimeout(() => { installing.removeEventListener('statechange', finish); reject(Error('App installation is still pending. Retry update.')); }, 8000);
        installing.addEventListener('statechange', finish); finish();
      });
      const w = reg.waiting;
      if (w) await new Promise<void>((resolve, reject) => {
        const changed = () => { if (navigator.serviceWorker.controller === w) { clearTimeout(timer); navigator.serviceWorker.removeEventListener('controllerchange', changed); resolve(); } };
        const timer = setTimeout(() => { navigator.serviceWorker.removeEventListener('controllerchange', changed); reject(Error('App activation is still pending. Retry update.')); }, 4000);
        navigator.serviceWorker.addEventListener('controllerchange', changed); w.postMessage('skip-waiting'); changed();
      });
    }
    location.reload(); // navigation is network-first; the next tab confirms its assets against the site
  } catch (error) { updateError.value = error instanceof Error ? error.message : String(error); }
  finally { updating.value = false; }
}

/** Starts the checks: soon after load, when the tab gets focus (at most once a minute), and every 5 minutes. */
export function startVersionChecks(): () => void {
  let last = 0;
  const run = () => { if (document.hidden || Date.now() - last < 60_000) return; last = Date.now(); void checkUpdate(); void loadVersions(); };
  const first = setTimeout(run, 3000), every = setInterval(() => { last = 0; run(); }, 300_000);
  addEventListener('focus', run);
  return () => { clearTimeout(first); clearInterval(every); removeEventListener('focus', run); };
}

const RANK: Record<Tone, number> = { neutral: 0, success: 0, running: 0, attention: 1, danger: 2 } as Record<Tone, number>;

/** The navigation's version mark and the rows of its overview. */
export function versionInfo(now = Date.now()): VersionInfo {
  const v = versions.value, rows: VersionRow[] = [];
  rows.push({ key: 'tab', part: 'This tab', icon: 'panels-top-left', version: tabVersion.value, state: updateChecked.value ? (updateReady.value ? 'update ready' : 'current') : 'unknown', tone: updateChecked.value ? (updateReady.value ? 'running' : 'success') : 'attention' });
  if (v) {
    // Server moments shift by the clock offset seen when the reply arrived, so ages count on this tab's clock.
    const skew = (versionObservedAt.value ?? now) - v.serverNow, serverNow = now - skew, local = (t: number) => t + skew;
    rows.push({ key: 'ui', part: 'Site app', icon: 'house', version: v.ui.version, state: v.ui.kind === 'ui' ? 'UI release' : 'bundled', tone: 'neutral' });
    const started = Date.parse(v.coordinator.started);
    rows.push({ key: 'coordinator', part: 'Coordinator', icon: 'network', version: v.coordinator.version, state: 'running', tone: 'success',
      times: Number.isFinite(started) ? [{ label: 'started', at: local(started) }] : undefined,
      note: v.coordinator.offered && v.coordinator.offered !== v.coordinator.version ? `offers ${v.coordinator.offered}` : undefined });
    if (v.gateway) {
      const g = v.gateway, stale = g.observedAt === null || serverNow - g.observedAt > 60_000;
      rows.push({ key: 'gateway', part: 'Gateway', icon: 'shield', version: g.version ?? null, state: stale ? 'unknown (probe pending)' : g.state,
        times: g.observedAt ? [{ label: 'checked', at: local(g.observedAt) }] : undefined, note: g.observedAt ? undefined : 'No probe result yet',
        tone: stale ? 'attention' : g.state === 'ok' ? 'success' : g.state === 'draining' || g.state === 'pending' ? 'attention' : 'danger' });
    }
    for (const h of v.hosts) {
      const s = hostState(h, serverNow, v.staleAfterMs);
      rows.push({ key: 'host:' + h.id, part: h.id, icon: 'terminal', version: h.version, ...s, times: s.seen ? [{ label: 'last seen', at: local(s.seen) }] : undefined });
    }
  }
  if (v && (versionError.value || versionObservedAt.value === null || now - versionObservedAt.value > 300_000)) {
    for (const row of rows) if (row.key !== 'tab') {
      row.state = `unknown (last: ${row.state})`; if (row.tone !== 'danger') row.tone = 'attention';
      if (versionObservedAt.value !== null) row.times = [...row.times ?? [], { label: 'observed', at: versionObservedAt.value }];
      row.note = [row.note, versionObservedAt.value === null ? 'Never observed' : '', versionError.value].filter(Boolean).join('; ') || undefined;
    }
  }
  if (updateError.value) rows.unshift({ key: 'update', part: 'App update', icon: 'rotate-cw', version: null, state: 'pending / failed', tone: 'attention', note: updateError.value });
  const worst = rows.reduce((t, r) => RANK[r.tone] > RANK[t] ? r.tone : t, 'neutral' as Tone), n = rows.filter(r => RANK[r.tone] > 0).length;
  return { current: v?.coordinator.version ?? null, tone: RANK[worst] > 0 ? worst : 'neutral', summary: n ? `${n} ${n === 1 ? 'part needs' : 'parts need'} a look` : undefined, updateReady: updateChecked.value && updateReady.value, updating: updating.value, onUpdate: applyUpdate, rows, onOpen: () => { void loadVersions(); void checkUpdate(); } };
}

export function hostState(h: Versions['hosts'][number], now: number, staleMs: number): { state: string; tone: Tone; note?: string; seen?: number } {
  const offline = h.lastSeenAt === null || now - h.lastSeenAt > staleMs, seen = offline && h.lastSeenAt !== null ? h.lastSeenAt : undefined;
  const u = (h.update ?? null) as { state?: string; version?: string; error?: string } | null;
  // A failed update stops mattering once the host runs that release or a newer one another way.
  const stale = olderThan(h.version, u?.version) === false;
  if (u && !stale && ['error', 'failed', 'refused'].includes(u.state ?? '')) return { state: `${offline ? 'offline; ' : ''}update ${u.state}`, tone: 'danger', seen, note: [u.error, offline && h.lastSeenAt === null ? 'never seen' : ''].filter(Boolean).join('; ') || undefined };
  if (offline) return { state: 'offline', tone: 'neutral', seen, note: h.lastSeenAt === null ? 'never seen' : undefined };
  if (u?.state === 'pending') return { state: 'update pending', tone: 'running', note: u.version };
  const auto = typeof h.autoUpdate === 'string' ? h.autoUpdate : 'on';
  const held = auto === 'off' || (auto !== 'on' && Date.parse(auto) > now);
  if (h.behind === null || !h.version) return { state: 'unknown', tone: 'attention', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : 'No comparable offered release' };
  if (h.behind) return { state: 'behind', tone: 'attention', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : undefined };
  return { state: 'current', tone: 'success', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : undefined };
}
