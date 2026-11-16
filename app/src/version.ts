// The installed version and the app update (docs/design-system/controls/nav-rail.md, Version).
//
// Update detection compares what this tab runs with what the site serves now: the hashed entry script and stylesheet
// named in the page this tab loaded against the ones in a fresh copy of the page. It does not depend on service worker
// events, so it gives the same answer on every check, and after an update the reloaded tab matches the site.
import { signal } from '@preact/signals';
import { age, type Tone, type VersionInfo, type VersionRow } from '../../packages/ui/src/index.ts';
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
    rows.push({ key: 'ui', part: 'Site app', icon: 'house', version: v.ui.version, state: v.ui.kind === 'ui' ? 'UI release' : 'bundled', tone: 'neutral' });
    const started = Date.parse(v.coordinator.started);
    rows.push({ key: 'coordinator', part: 'Coordinator', icon: 'network', version: v.coordinator.version, state: 'running', tone: 'success',
      note: [Number.isFinite(started) ? `since ${age(started, now).text}` : '', v.coordinator.offered && v.coordinator.offered !== v.coordinator.version ? `offers ${v.coordinator.offered}` : ''].filter(Boolean).join(', ') || undefined });
    if (v.gateway) rows.push({ key: 'gateway', part: 'Gateway', icon: 'shield', version: v.gateway.version ?? null, state: v.gateway.observedAt === null || v.serverNow + Math.max(0, now - (versionObservedAt.value ?? now)) - v.gateway.observedAt > 60_000 ? 'unknown (probe pending)' : v.gateway.state, note: v.gateway.observedAt ? `observed ${new Date(v.gateway.observedAt).toISOString()}` : 'No probe result yet', tone: v.gateway.observedAt === null || v.serverNow + Math.max(0, now - (versionObservedAt.value ?? now)) - v.gateway.observedAt > 60_000 ? 'attention' : v.gateway.state === 'ok' ? 'success' : v.gateway.state === 'draining' || v.gateway.state === 'pending' ? 'attention' : 'danger' });
    for (const h of v.hosts) rows.push({ key: 'host:' + h.id, part: h.id, icon: 'terminal', version: h.version, ...hostState(h, v.serverNow + Math.max(0, now - (versionObservedAt.value ?? now)), v.staleAfterMs) });
  }
  if (v && (versionError.value || versionObservedAt.value === null || now - versionObservedAt.value > 300_000)) {
    for (const row of rows) if (row.key !== 'tab') {
      row.state = `unknown (last: ${row.state})`; if (row.tone !== 'danger') row.tone = 'attention';
      row.note = [row.note, versionObservedAt.value === null ? 'Never observed' : `observed ${new Date(versionObservedAt.value).toISOString()}`, versionError.value].filter(Boolean).join('; ');
    }
  }
  if (updateError.value) rows.unshift({ key: 'update', part: 'App update', icon: 'rotate-cw', version: null, state: 'pending / failed', tone: 'attention', note: updateError.value });
  const worst = rows.reduce((t, r) => RANK[r.tone] > RANK[t] ? r.tone : t, 'neutral' as Tone), n = rows.filter(r => RANK[r.tone] > 0).length;
  return { current: v?.coordinator.version ?? null, tone: RANK[worst] > 0 ? worst : 'neutral', summary: n ? `${n} ${n === 1 ? 'part needs' : 'parts need'} a look` : undefined, updateReady: updateChecked.value && updateReady.value, updating: updating.value, onUpdate: applyUpdate, rows, onOpen: () => { void loadVersions(); void checkUpdate(); } };
}

export function hostState(h: Versions['hosts'][number], now: number, staleMs: number): { state: string; tone: Tone; note?: string } {
  const offline = h.lastSeenAt === null || now - h.lastSeenAt > staleMs;
  const u = (h.update ?? null) as { state?: string; version?: string; error?: string } | null;
  // A failed update stops mattering once the host runs that release or a newer one another way.
  const stale = olderThan(h.version, u?.version) === false;
  if (u && !stale && ['error', 'failed', 'refused'].includes(u.state ?? '')) return { state: `${offline ? 'offline; ' : ''}update ${u.state}`, tone: 'danger', note: [u.error, offline ? (h.lastSeenAt === null ? 'never seen' : `last seen ${new Date(h.lastSeenAt).toISOString()}`) : ''].filter(Boolean).join('; ') };
  if (offline) return { state: 'offline', tone: 'neutral', note: h.lastSeenAt === null ? 'never seen' : `last seen ${new Date(h.lastSeenAt).toISOString()}` };
  if (u?.state === 'pending') return { state: 'update pending', tone: 'running', note: u.version };
  const auto = typeof h.autoUpdate === 'string' ? h.autoUpdate : 'on';
  const held = auto === 'off' || (auto !== 'on' && Date.parse(auto) > now);
  if (h.behind === null || !h.version) return { state: 'unknown', tone: 'attention', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : 'No comparable offered release' };
  if (h.behind) return { state: 'behind', tone: 'attention', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : undefined };
  return { state: 'current', tone: 'success', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : undefined };
}
