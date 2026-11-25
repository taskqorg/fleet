// The installed version and the app update (docs/design-system/controls/nav-rail.md, Version).
//
// Update detection compares what this tab runs with what the site serves now: the hashed entry script and stylesheet
// named in the page this tab loaded against the ones in a fresh copy of the page. It does not depend on service worker
// events, so it gives the same answer on every check, and after an update the reloaded tab matches the site.
import { cell } from './signal.ts';
import type { Tone, VersionInfo, VersionRow } from '../../packages/ui/src/index.ts';
import { op, requestsInFlight, type Ops } from './api.ts';
import { access, accessGeneration, isAccessCurrent, onAccessInvalidated, onAccessOpen } from './access.ts';
import { navigate } from './router.ts';

type Versions = Ops['system_versions']['reply'];
export const versions = cell<Versions | null>(null);
export const updateReady = cell(false);
export const updating = cell(false);
export const updateChecked = cell(false);
export const versionObservedAt = cell<number | null>(null);
export const versionError = cell<string | null>(null);
export const updateError = cell<string | null>(null);
/** Seconds until the tab updates itself (null: not counting), and what holds the count (UpdateHold). */
export const updateIn = cell<number | null>(null);
export const updateHold = cell<UpdateHold | null>(null);
onAccessInvalidated.add(() => {
  versions.value = null; tabVersion.value = null; versionRead++; updateRead++; versionObservedAt.value = null;
  versionError.value = null; updateChecked.value = false; updateReady.value = false; updateError.value = null;
});
/** The site's UI version at a moment this tab was known to run the site's app (its assets matched before and after the read). */
export const tabVersion = cell<string | null>(null);
let versionRead = 0, updateRead = 0;

// The entry script and the stylesheet carry content hashes (app/build.mjs), so a new app always changes one of them.
const assetsOf = (html: string) => [...html.matchAll(/(?:src|href)="(\/assets\/(?:main|app)-[^"]+\.(?:js|css))"/g)].map(m => m[1]).sort().join(' ');
const own = () => assetsOf(document.head.innerHTML);

/** A fresh copy of the page the site serves now; null when it cannot be read (offline, signed out, not the app). */
async function servedAssets(): Promise<string | null> {
  try {
    const r = await fetch('/home', { cache: 'no-store', credentials: 'same-origin', headers: { accept: 'text/html' } });
    if (!r.ok || !r.headers.get('content-type')?.startsWith('text/html')) return null;
    return assetsOf(await r.text()) || null;
  } catch { return null; }
}

export async function checkUpdate() {
  const read = ++updateRead, generation = accessGeneration(), mine = own(), served = await servedAssets();
  if (!isAccessCurrent(generation) || read !== updateRead) return;
  updateChecked.value = !!mine && !!served;
  if (!mine || !served) return;
  const was = updateReady.value;
  updateReady.value = served !== mine;
  // A newly served app: read its version now, so the strip names it.
  if (updateReady.value && !was) void loadVersions();
  if (!updateReady.value) return;
  // Newly offered to a person looking at the tab: the full count shows first. A tab the person is away from waits for
  // their return (countdown).
  if (!was && !away()) lastActive = Date.now();
  // A reload this tab made itself that brought back the same old app (a cache in between): no second automatic reload.
  if (!updating.value && reloadedInto() === mine) { updateError.value = 'The reload kept the old app. Retry.'; return; }
  // A failed update counts down again at the next check, from a full count.
  if (updateError.value && !updating.value) { updateError.value = null; lastActive = Date.now(); }
  countdown();
}

/*
 * The idle update (nav-rail.md, Update; board screens/shell). While a new app is ready, the tab reloads into it after
 * IDLE_MS with no activity: a pointer move, key, wheel, touch or focus change in this tab sets the one timestamp
 * back (passive listeners, written at most every ACTIVE_STEP). A tab the person is away from (hidden, or its window
 * without focus) never reloads: it waits for the person to come back, then updates at once when their last action was
 * IDLE_MS or more ago, and otherwise counts down the rest. Coming back is not activity. One 1 s timer runs only while a new app is ready. Work is never lost: input a person typed and did not send,
 * an open modal dialog, or a request still in flight hold the count (holding was chosen over restoring drafts after the
 * reload: it cannot lose what a page did not save). The reload keeps the route (location.reload) and the scroll of the
 * page's scroll areas.
 */
export type UpdateHold = 'draft' | 'dialog' | 'save';
export const IDLE_MS = 30_000;
const ACTIVE_STEP = 250, RELOADED_KEY = 'fleet-update:reloaded', SCROLL_KEY = 'fleet-update:scroll';
let lastActive = Date.now(), timer: ReturnType<typeof setInterval> | undefined, left = false;
/** The person is away: the tab is hidden or its window has no focus. */
const away = () => document.visibilityState !== 'visible' || !document.hasFocus();

/**
 * Fields a person typed into, with the value they had before the first keystroke: a field whose value differs and is not
 * empty is a draft (a composer emptied by its send is not). Search and filter fields hold no work, so they are not kept.
 */
const edited = new Map<HTMLElement, string>();
const valueOf = (el: HTMLElement) => el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement ? el.value : el.isContentEditable ? el.textContent ?? '' : '';
function holdReason(): UpdateHold | null {
  for (const [el, before] of edited) {
    if (!el.isConnected) { edited.delete(el); continue; }
    const now = valueOf(el);
    if (now.trim() && now !== before) return 'draft';
  }
  // A side sheet is the phone's detail view, not a dialog with state of its own.
  if (document.querySelector(':modal:not(.side-sheet)')) return 'dialog';
  return null;
}

function countdown() {
  if (!timer) timer = setInterval(countdown, 1000);
  if (!updateReady.peek() || updating.peek() || updateError.peek()) { clearInterval(timer); timer = undefined; updateIn.value = null; updateHold.value = null; return; }
  if (away()) { updateIn.value = null; updateHold.value = null; return; }
  const now = Date.now(), hold = holdReason();
  if (hold) { lastActive = now; updateIn.value = null; updateHold.value = hold; return; }
  const left = Math.max(0, Math.ceil((IDLE_MS - (now - lastActive)) / 1000));
  updateIn.value = left;
  // At 0 a request still in flight (a save, a send) finishes first; past 2 s the strip says what it waits for.
  if (left === 0 && requestsInFlight() > 0) { updateHold.value = now - lastActive > IDLE_MS + 2000 ? 'save' : null; return; }
  updateHold.value = null;
  if (left === 0) void applyUpdate(true);
}

function active(e: Event) {
  // A pointer that did not move (the page moved under a resting pointer) is not activity.
  if (e instanceof PointerEvent && e.type === 'pointermove' && !e.movementX && !e.movementY) return;
  const now = Date.now();
  // The focus that comes back with the window or the tab is the return, not an action.
  if (e.type === 'focusin' && left) { left = false; return; }
  left = false;
  if (now - lastActive >= ACTIVE_STEP) lastActive = now;
}

/** The assets of the tab that reloaded itself in the last 10 minutes: when this tab has them too, the reload brought back the old app. */
function reloadedInto(): string | null {
  try { const r = JSON.parse(sessionStorage.getItem(RELOADED_KEY) ?? 'null'); return r && Date.now() - r.at < 600_000 ? r.assets : null; } catch { return null; }
}

/** The scroll areas of the page, by their class and place, so the reloaded page can find them again. */
const SCROLLERS = '.shell-main, .split-list, .detail, .work-idle, .side-body, .limits, .runners';
function keepScroll() {
  const all = [...document.querySelectorAll<HTMLElement>(SCROLLERS)];
  const at = all.map((el, i) => ({ i, cls: el.className, top: el.scrollTop })).filter(x => x.top > 0);
  try { sessionStorage.setItem(SCROLL_KEY, JSON.stringify({ path: location.pathname + location.search, window: scrollY, at })); } catch { /* the scroll is not kept */ }
}
/** After a reload into a new app: puts the scroll areas back as they render, until each is restored, the person acts, or 5 s pass. */
function restoreScroll() {
  let kept: { path: string; window: number; at: { i: number; cls: string; top: number }[] } | null = null;
  try { kept = JSON.parse(sessionStorage.getItem(SCROLL_KEY) ?? 'null'); sessionStorage.removeItem(SCROLL_KEY); } catch { return; }
  if (!kept || kept.path !== location.pathname + location.search) return;
  const k = kept;
  let left = [...k.at], win = k.window;
  const done = () => { obs.disconnect(); clearTimeout(stop); removeEventListener('pointerdown', done, true); removeEventListener('keydown', done, true); removeEventListener('wheel', done, true); };
  const apply = () => {
    if (win > 0 && document.documentElement.scrollHeight - innerHeight >= win) { scrollTo(0, win); win = 0; }
    const all = [...document.querySelectorAll<HTMLElement>(SCROLLERS)];
    left = left.filter(x => { const el = all[x.i]; if (!el || el.className !== x.cls || el.scrollHeight - el.clientHeight < x.top) return true; el.scrollTop = x.top; return false; });
    if (!left.length && !win) done();
  };
  const obs = new MutationObserver(apply), stop = setTimeout(done, 5000);
  obs.observe(document.body, { childList: true, subtree: true });
  for (const t of ['pointerdown', 'keydown', 'wheel']) addEventListener(t, done, { capture: true, passive: true });
  apply();
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
 * Once the reload is asked for, `updating` stays on until the page unloads: the control never turns back into Update in
 * between (a busy state cleared before the reload landed showed the button again). A failure says why and allows Retry.
 */
export async function applyUpdate(auto = false) {
  if (updating.value) return;
  updating.value = true;
  updateError.value = null;
  try {
    // A reload after an unreadable page could return an old offline shell, so verify the site before proceeding.
    if (!await servedAssets()) throw Error('The site app is unreachable. Retry when connected.');
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
    keepScroll();
    try { if (auto) sessionStorage.setItem(RELOADED_KEY, JSON.stringify({ assets: own(), at: Date.now() })); else sessionStorage.removeItem(RELOADED_KEY); } catch { /* no loop guard */ }
    location.reload(); // navigation is network-first; the next tab confirms its assets against the site
  } catch (error) {
    updateError.value = error instanceof Error ? error.message : String(error);
    updating.value = false;
  }
}

/**
 * Starts the checks: as soon as access opens (at load, and again after access was validated anew, which clears the
 * versions), when the tab gets focus (at most once a minute), and every 5 minutes. A read before access is open would be
 * discarded, so none is made.
 */
export function startVersionChecks(): () => void {
  let last = 0;
  const run = () => { if (document.hidden || access.peek().state !== 'open' || Date.now() - last < 60_000) return; last = Date.now(); void checkUpdate(); void loadVersions(); };
  const opened = () => { last = 0; run(); };
  // Access opening is one synchronous continuation with the baseline's activation (access.ts): the reads start after it.
  const afterOpen = () => { setTimeout(opened, 0); };
  const first = setTimeout(opened, 0), every = setInterval(opened, 300_000);
  onAccessOpen.add(afterOpen);
  // The person comes back: an update ready since their last action IDLE_MS ago installs now, else the count goes on.
  const leave = () => { left = true; };
  const back = () => { if (away()) { left = true; return; } if (updateReady.peek()) countdown(); };
  addEventListener('focus', run); addEventListener('focus', back); addEventListener('blur', leave);
  // Activity for the idle update: passive and captured, so a handler that stops the event still counts it.
  const opts = { capture: true, passive: true } as const, typed = (e: Event) => {
    const t = e.target;
    if (!(t instanceof HTMLElement) || edited.has(t) || t.matches('input[type=search], [role=searchbox], [role=combobox]') || t.closest('[role=search], .spotlight')) return;
    edited.set(t, valueOf(t));
  };
  // Scrolling counts through its input (wheel, keys, touch, the scrollbar's pointer); a scroll event alone may be the page's own.
  const ACTIVITY = ['pointermove', 'pointerdown', 'keydown', 'wheel', 'touchstart', 'focusin'];
  for (const t of ACTIVITY) addEventListener(t, active, opts);
  addEventListener('beforeinput', typed, opts);
  document.addEventListener('visibilitychange', back);
  restoreScroll();
  return () => {
    clearTimeout(first); clearInterval(every); onAccessOpen.delete(afterOpen); removeEventListener('focus', run); removeEventListener('focus', back); removeEventListener('blur', leave);
    for (const t of ACTIVITY) removeEventListener(t, active, opts);
    removeEventListener('beforeinput', typed, opts); document.removeEventListener('visibilitychange', back);
    clearInterval(timer); timer = undefined;
  };
}

/** Only a confirmed problem tones the mark (nav-rail.md, Version): attention and danger rows; every other tone is the row's own. */
const RANK: Record<Tone, number> = { neutral: 0, success: 0, running: 0, attention: 1, danger: 2 } as Record<Tone, number>;

/**
 * The navigation's version mark and the rows of its overview. A row is toned attention or danger only for a confirmed
 * problem that is true now: a host update that failed, a host whose poll problem is open, a gateway that failed a probe
 * and its re-probe, a host behind with auto-update off or held. Not yet checked, a probe not yet repeated, a failed or
 * late read (the overview says when it last checked) and an offline host are facts, never warnings.
 */
export function versionInfo(now = Date.now()): VersionInfo {
  const v = versions.value, rows: VersionRow[] = [];
  rows.push({ key: 'tab', part: 'This tab', icon: 'panels-top-left', version: tabVersion.value,
    state: updateChecked.value ? (updateReady.value ? 'older than the site' : 'current') : 'not checked yet',
    tone: updateChecked.value ? (updateReady.value ? 'running' : 'success') : 'neutral' });
  if (v) {
    // Server moments keep their true time (the tooltip); their age counts from the server's present as this tab estimates it.
    const serverNow = v.serverNow + Math.max(0, now - (versionObservedAt.value ?? now)), at = (label: string, t: number) => [{ label, at: t, now: serverNow }];
    rows.push({ key: 'ui', part: 'Site app', icon: 'house', version: v.ui.version, state: v.ui.kind === 'ui' ? 'UI release' : 'bundled', tone: 'neutral' });
    const started = Date.parse(v.coordinator.started);
    rows.push({ key: 'coordinator', part: 'Coordinator', icon: 'network', version: v.coordinator.version, state: 'running', tone: 'success',
      times: Number.isFinite(started) ? at('started', started) : undefined,
      note: v.coordinator.offered && v.coordinator.offered !== v.coordinator.version ? `offers ${v.coordinator.offered}` : undefined });
    if (v.gateway) {
      // The coordinator probes the gateway when read and the last probe is older than a minute, and repeats a failed probe
      // at once (go/coordinator/versions.go): an older probe is being repeated, so it reads "checking". The last confirmed
      // outage (probe and re-probe failed) stays danger until a newer probe answers; an older "ok" is no warning.
      const g = v.gateway, aged = g.observedAt == null || serverNow - g.observedAt > 60_000, down = g.state === 'unreachable';
      rows.push({ key: 'gateway', part: 'Gateway', icon: 'shield', version: g.version ?? null,
        state: down ? 'unreachable' : aged || g.state === 'pending' ? 'checking…' : g.state,
        times: g.observedAt != null ? at(aged ? 'last checked' : 'checked', g.observedAt) : undefined,
        note: aged && g.observedAt != null && !down && g.state !== 'ok' ? `last: ${g.state}` : undefined,
        tone: down ? 'danger' : aged || g.state === 'pending' ? 'neutral' : g.state === 'ok' ? 'success' : 'running' });
    }
    for (const h of v.hosts) {
      const { seen, failingSince, ...s } = hostState(h, serverNow, v.staleAfterMs);
      const times = [...seen !== undefined ? at('last seen', seen) : [], ...failingSince !== undefined ? at('failing since', failingSince) : []];
      rows.push({ key: 'host:' + h.id, part: h.id, icon: 'terminal', version: h.version, ...s, times: times.length ? times : undefined,
        open: s.tone === 'attention' || s.tone === 'danger' || s.state === 'offline' || s.state === 'polls failing' ? () => navigate(`/hosts/${encodeURIComponent(h.id)}`) : undefined });
    }
  }
  const worst = rows.reduce((t, r) => RANK[r.tone] > RANK[t] ? r.tone : t, 'neutral' as Tone), n = rows.filter(r => RANK[r.tone] > 0).length;
  const ready = updateChecked.value && updateReady.value;
  return { current: v?.coordinator.version ?? null, tone: worst, summary: n ? `${n} ${n === 1 ? 'part needs' : 'parts need'} a look` : undefined,
    // The version the site serves, once a read shows one other than this tab's (a read older than the check names the old one).
    updateReady: ready, newVersion: ready && v && v.ui.version !== tabVersion.value ? v.ui.version : null, updating: updating.value, updateError: updateError.value, onUpdate: () => void applyUpdate(),
    updateIn: updateIn.value, updateHold: updateHold.value,
    checkedAt: versionObservedAt.value, checkError: versionError.value, rows, at: versionInfo, onOpen: () => { void loadVersions(); void checkUpdate(); } };
}

/** One host's row. Danger and attention only for confirmed problems (versionInfo). */
export function hostState(h: Versions['hosts'][number], now: number, staleMs: number): { state: string; tone: Tone; note?: string; seen?: number; failingSince?: number } {
  const offline = h.lastSeenAt === null || now - h.lastSeenAt > staleMs, seen = offline && h.lastSeenAt !== null ? h.lastSeenAt : undefined;
  // The coordinator answers this host's polls with an error (go/coordinator/host_poll_health.go): it gets no commands and
  // no update target while lastSeenAt still moves (the server sends it only while the host polls; an aged snapshot of a
  // host gone since shows offline). It is a problem once the coordinator has opened the host's poll problem (pollProblem);
  // before that the next good poll may clear it. The note is the reason code, never the message.
  const pe = h.pollError;
  if (pe && !offline) return { state: 'polls failing', tone: h.pollProblem ? 'danger' : 'neutral', failingSince: pe.since, note: `${pe.reason} (${pe.status})` };
  const u = (h.update ?? null) as { state?: string; version?: string; error?: string } | null;
  // A failed update stops mattering once the host runs that release or a newer one another way.
  const stale = olderThan(h.version, u?.version) === false;
  if (u && !stale && ['error', 'failed', 'refused'].includes(u.state ?? '')) return { state: `${offline ? 'offline; ' : ''}update ${u.state}`, tone: 'danger', seen, note: [u.error, offline && h.lastSeenAt === null ? 'never seen' : ''].filter(Boolean).join('; ') || undefined };
  if (offline) return { state: 'offline', tone: 'neutral', seen, note: h.lastSeenAt === null ? 'never seen' : undefined };
  if (u?.state === 'pending') return { state: 'updating', tone: 'running', note: u.version };
  const auto = typeof h.autoUpdate === 'string' ? h.autoUpdate : 'on';
  const held = auto === 'off' || (auto !== 'on' && Date.parse(auto) > now), heldNote = auto === 'off' ? 'auto-update off' : 'update held';
  if (h.behind === null || !h.version) return { state: 'unknown', tone: 'neutral', note: held ? heldNote : 'No comparable offered release' };
  // Behind with auto-update on updates itself; behind while held waits for a person.
  if (h.behind) return held ? { state: 'behind', tone: 'attention', note: heldNote } : { state: 'behind', tone: 'neutral', note: 'updates itself' };
  return { state: 'current', tone: 'success', note: held ? heldNote : undefined };
}
