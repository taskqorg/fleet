// The installed version and the app update (docs/design-system/controls/nav-rail.md, Version).
//
// Update detection compares what this tab runs with what the site serves now: the hashed entry script and stylesheet
// named in the page this tab loaded against the ones in a fresh copy of the page. It does not depend on service worker
// events, so it gives the same answer on every check, and after an update the reloaded tab matches the site.
import { signal } from '@preact/signals';
import { age, type Tone, type VersionInfo, type VersionRow } from '../../packages/ui/src/index.ts';
import { op, type Ops } from './api.ts';

type Versions = Ops['system_versions']['reply'];
export const versions = signal<Versions | null>(null);
export const updateReady = signal(false);
export const updating = signal(false);
/** The site's UI version when this tab was known to match the site (first check that found no update). */
let tabVersion: string | null = null;

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
  const mine = own(), served = await servedAssets();
  if (!mine || !served) return;
  updateReady.value = served !== mine;
}

export async function loadVersions() {
  try {
    const v = await op('system_versions', {});
    versions.value = v;
    if (!updateReady.value && tabVersion === null) tabVersion = v.ui.version;
  } catch { /* the mark keeps its last answer */ }
}

/**
 * Reload into the new app on the first click: the waiting service worker (if any) takes over first, so the reload is
 * served by the new worker and its cache; without one the reload alone fetches the new page (navigations go to the network).
 */
export async function applyUpdate() {
  if (updating.value) return;
  updating.value = true;
  try {
    const reg = 'serviceWorker' in navigator ? await navigator.serviceWorker.getRegistration() : undefined;
    if (reg) {
      await reg.update().catch(() => {});
      const w = reg.waiting ?? reg.installing;
      if (w) await new Promise<void>(done => {
        navigator.serviceWorker.addEventListener('controllerchange', () => done(), { once: true });
        setTimeout(done, 4000);
        w.postMessage('skip-waiting');
      });
    }
  } finally { location.reload(); }
}

/** Starts the checks: soon after load, when the tab gets focus (at most once a minute), and every 5 minutes. */
export function startVersionChecks(): () => void {
  let last = 0;
  const run = () => { if (Date.now() - last < 60_000) return; last = Date.now(); void checkUpdate(); void loadVersions(); };
  const first = setTimeout(run, 3000), every = setInterval(() => { last = 0; run(); }, 300_000);
  addEventListener('focus', run);
  return () => { clearTimeout(first); clearInterval(every); removeEventListener('focus', run); };
}

const RANK: Record<Tone, number> = { neutral: 0, success: 0, running: 0, attention: 1, danger: 2 } as Record<Tone, number>;

/** The navigation's version mark and the rows of its overview. */
export function versionInfo(now = Date.now()): VersionInfo {
  const v = versions.value, rows: VersionRow[] = [];
  rows.push({ key: 'tab', part: 'This tab', icon: 'panels-top-left', version: tabVersion, state: updateReady.value ? 'update ready' : 'current', tone: updateReady.value ? 'running' : 'success' });
  if (v) {
    rows.push({ key: 'ui', part: 'Site app', icon: 'house', version: v.ui.version, state: v.ui.kind === 'ui' ? 'UI release' : 'bundled', tone: 'neutral' });
    const started = Date.parse(v.coordinator.started);
    rows.push({ key: 'coordinator', part: 'Coordinator', icon: 'network', version: v.coordinator.version, state: 'running', tone: 'success',
      note: [Number.isFinite(started) ? `since ${age(started, now).text}` : '', v.coordinator.offered && v.coordinator.offered !== v.coordinator.version ? `offers ${v.coordinator.offered}` : ''].filter(Boolean).join(', ') || undefined });
    if (v.gateway) rows.push({ key: 'gateway', part: 'Gateway', icon: 'shield', version: v.gateway.version ?? null, state: v.gateway.state === 'ok' ? 'ok' : 'unreachable', tone: v.gateway.state === 'ok' ? 'success' : 'danger' });
    for (const h of v.hosts) rows.push({ key: 'host:' + h.id, part: h.id, icon: 'terminal', version: h.version, ...hostState(h, v.serverNow, v.staleAfterMs) });
  }
  const worst = rows.reduce((t, r) => RANK[r.tone] > RANK[t] ? r.tone : t, 'neutral' as Tone), n = rows.filter(r => RANK[r.tone] > 0).length;
  return { current: v?.coordinator.version ?? null, tone: RANK[worst] > 0 ? worst : 'neutral', summary: n ? `${n} ${n === 1 ? 'part needs' : 'parts need'} a look` : undefined, updateReady: updateReady.value, updating: updating.value, onUpdate: applyUpdate, rows, onOpen: () => { void loadVersions(); void checkUpdate(); } };
}

function hostState(h: Versions['hosts'][number], now: number, staleMs: number): { state: string; tone: Tone; note?: string } {
  if (h.lastSeenAt === null || now - h.lastSeenAt > staleMs) return { state: 'offline', tone: 'neutral', note: h.lastSeenAt ? `seen ${age(h.lastSeenAt, now).text}` : undefined };
  const u = (h.update ?? null) as { state?: string; version?: string; error?: string } | null;
  if (u && ['error', 'failed', 'refused'].includes(u.state ?? '')) return { state: `update ${u.state}`, tone: 'danger', note: u.error };
  if (u?.state === 'pending') return { state: 'updating', tone: 'running', note: u.version };
  const auto = typeof h.autoUpdate === 'string' ? h.autoUpdate : 'on';
  const held = auto === 'off' || (auto !== 'on' && Date.parse(auto) > now);
  if (h.behind) return { state: 'behind', tone: 'attention', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : undefined };
  return { state: 'current', tone: 'success', note: held ? (auto === 'off' ? 'auto-update off' : 'update held') : undefined };
}
