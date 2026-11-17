// Navigation counters (docs/design-system/controls/nav-rail.md, Counters): one nav_counts read gives every section's
// counter for this person; agents read the same operation. The read is repeated after a change that can move a count
// (at most once per QUIET ms, the last change included, so busy executions never storm it), when this person's settings
// change (focus, ignored projects, the counter views: they name the read, and main.tsx rereads the settings when another
// device changes them), and every REFRESH ms for hosts, whose liveness moves without a change row.
import { useEffect, useRef } from 'preact/hooks';
import { signal } from '@preact/signals';
import { op, type Ops } from './api.ts';
import { useDoc } from './store.ts';
import { subscribe, type Change } from './feed.ts';
import { prefs, prefsRead, prefsSaved } from './prefs.ts';
import { NAV_COUNTER_DEFAULTS } from './system-views.ts';
import type { NavCount, Tone } from '../../packages/ui/src/index.ts';

/** The reply and the counter choices it was read for. */
type Read = { reply: Ops['nav_counts']['reply']; asked: Record<string, string> };
const QUIET = 5000, REFRESH = 30_000;
// Execution changes come also with every token and cost total; a session's state moves with them, so they count, but
// a batch of only those waits longer.
const QUIET_EXECUTIONS = 15_000;
const COUNTED: Change['kind'][] = ['task', 'session', 'artifact', 'delivery', 'host'];
/** The last read, for the saved-views menu: which view each counter counts as the server resolved it. */
const lastRead = signal<Read | null>(null);
// Hosts offline needs a look (OR-18); the other counters are plain amounts of work.
const TONES: Record<string, Tone> = { hosts: 'attention' };
/** JSON with object keys sorted: the server's copy of the same settings (its own key order) names the same read. */
const stable = (v: unknown) => JSON.stringify(v, (_k, x) => x && typeof x === 'object' && !Array.isArray(x) ? Object.fromEntries(Object.entries(x).sort()) : x);

/** Each section's counter by section id, none while unread; ready: access is open and the person's settings are read. */
export function useNavCounts(ready: boolean): Record<string, NavCount> {
  const { focus, ignored, navCounters, views } = prefs.value;
  // The counted saved views' definitions too: editing one reads at once.
  const saved = Object.entries(navCounters ?? {}).map(([list, name]) => [list, name, views.find(v => v.list === list && v.name === name)?.view ?? null]);
  const key = ready && prefsRead.value ? 'nav-counts:' + stable([focus, ignored, saved]) : null;
  // A changed focus or counter view shows at once in prefs; the read waits for its save, so it counts the new settings.
  const doc = useDoc<Read>(key, () => prefsSaved().then(async () => {
    const asked = { ...prefs.value.navCounters };
    return { reply: await op('nav_counts', {}), asked };
  }), () => false, REFRESH);
  const reload = useRef(doc.reload); reload.current = doc.reload;
  useEffect(() => {
    if (!key) return;
    // One pending read; a counted change brings a later one forward.
    let timer: ReturnType<typeof setTimeout> | undefined, due = Infinity;
    const off = subscribe({ resync: () => {}, changes: cs => {
      const wait = cs.some(c => COUNTED.includes(c.kind)) ? QUIET : cs.some(c => c.kind === 'execution') ? QUIET_EXECUTIONS : 0;
      if (!wait || Date.now() + wait >= due) return;
      clearTimeout(timer); due = Date.now() + wait;
      timer = setTimeout(() => { timer = undefined; due = Infinity; reload.current(); }, wait);
    } });
    return () => { off(); clearTimeout(timer); };
  }, [key]);
  // The last counters stay while a changed setting is read (OR-23: nothing blinks out); none once access closes, so the
  // next person never sees them.
  const last = useRef<Read | null>(null);
  if (!key) last.current = null; else if (doc.data?.reply) last.current = doc.data;
  const read = last.current;
  useEffect(() => { lastRead.value = read; }, [read]);
  if (!read) return {};
  return Object.fromEntries(Object.entries(read.reply.sections).map(([id, c]) => [id, {
    n: c.inFocus, outside: c.split ? c.outside : undefined, tone: TONES[id] ?? 'neutral', label: c.label,
  }]));
}

/**
 * The view a section's counter counts, for the saved-views menu; null when the counter is off. The server's answer when
 * it was read for the person's current choice (a removed or refused saved view counts the default); while a new choice
 * is still being read, that choice when its view exists, else the default.
 */
export function navCounted(section: string, names: string[]): string | null {
  const v = prefs.value.navCounters?.[section], read = lastRead.value;
  if (v === '-') return null;
  if (read && (read.asked[section] ?? '') === (v ?? '') && read.reply.sections[section]) return read.reply.sections[section].view;
  return v && names.includes(v) ? v : NAV_COUNTER_DEFAULTS[section] ?? null;
}
