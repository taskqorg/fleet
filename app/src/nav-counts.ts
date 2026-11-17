// Navigation counters (docs/design-system/controls/nav-rail.md, Counters): one nav_counts read gives every section's
// counter for this person; agents read the same operation. The read is repeated after a change that can move a count
// (at most once per QUIET ms, the last change included, so busy executions never storm it), after this person's
// settings change (focus, ignored projects, the counter views), and every REFRESH ms for hosts, whose liveness moves
// without a change row.
import { useEffect, useRef } from 'preact/hooks';
import { signal } from '@preact/signals';
import { op, type Ops } from './api.ts';
import { useDoc } from './store.ts';
import { subscribe, type Change } from './feed.ts';
import { access } from './access.ts';
import { preferencesTouched } from './invalidation.ts';
import { prefs, prefsRead, prefsSaved } from './prefs.ts';
import { NAV_COUNTER_DEFAULTS } from './system-views.ts';
import type { NavCount, Tone } from '../../packages/ui/src/index.ts';

type Reply = Ops['nav_counts']['reply'];
const QUIET = 5000, REFRESH = 30_000;
// Execution changes come also with every token and cost total; a session's state moves with them, so they count, but
// a batch of only those waits longer.
const QUIET_EXECUTIONS = 15_000;
const COUNTED: Change['kind'][] = ['task', 'session', 'artifact', 'delivery', 'host'];
/** The view each section's counter counts now, as the server resolved it (a removed or refused saved view counts the default). */
const countedViews = signal<Record<string, string>>({});
// Hosts offline needs a look (OR-18); the other counters are plain amounts of work.
const TONES: Record<string, Tone> = { hosts: 'attention' };

/** Each section's counter by section id, none while unread; ready: access is open and the person's settings are read. */
export function useNavCounts(ready: boolean): Record<string, NavCount> {
  const { focus, ignored, navCounters } = prefs.value;
  // Sorted, so the server's copy of the same settings (its own key order) names the same read.
  // The counted saved views' definitions too: editing one reads at once.
  const saved = Object.entries(navCounters ?? {}).sort().map(([list, name]) => [list, name, prefs.value.views.find(v => v.list === list && v.name === name)?.view ?? null]);
  const key = ready && prefsRead.value ? 'nav-counts:' + JSON.stringify([focus, ignored, saved]) : null;
  // A changed focus or counter view shows at once in prefs; the read waits for its save, so it counts the new settings.
  const doc = useDoc<Reply>(key, () => prefsSaved().then(() => op('nav_counts', {})), () => false, REFRESH);
  const reload = useRef(doc.reload); reload.current = doc.reload;
  useEffect(() => {
    if (!key) return;
    // One pending read; a counted change brings a later one forward. This person's settings only: other people's setting
    // changes reach the feed too.
    let timer: ReturnType<typeof setTimeout> | undefined, due = Infinity;
    const off = subscribe({ resync: () => {}, changes: cs => {
      const wait = cs.some(c => COUNTED.includes(c.kind)) || preferencesTouched(cs, access.value.identity?.identityKey) ? QUIET
        : cs.some(c => c.kind === 'execution') ? QUIET_EXECUTIONS : 0;
      if (!wait || Date.now() + wait >= due) return;
      clearTimeout(timer); due = Date.now() + wait;
      timer = setTimeout(() => { timer = undefined; due = Infinity; reload.current(); }, wait);
    } });
    return () => { off(); clearTimeout(timer); };
  }, [key]);
  // The last counters stay while a changed setting is read (OR-23: nothing blinks out); none once access closes, so the
  // next person never sees them.
  const last = useRef<Reply | null>(null);
  if (!key) last.current = null; else if (doc.data) last.current = doc.data;
  const r = last.current;
  useEffect(() => { countedViews.value = r ? Object.fromEntries(Object.entries(r.sections).map(([id, c]) => [id, c.view])) : {}; }, [r]);
  if (!r) return {};
  return Object.fromEntries(Object.entries(r.sections).map(([id, c]) => [id, {
    n: c.inFocus, outside: c.split ? c.outside : undefined, tone: TONES[id] ?? 'neutral', label: c.label,
  }]));
}

/**
 * The view a section's counter counts, for the saved-views menu: the server's answer once read, else the person's choice
 * while that view exists, else the default; null when the counter is off.
 */
export function navCounted(section: string, names: string[]): string | null {
  const v = prefs.value.navCounters?.[section];
  if (v === '-') return null;
  return countedViews.value[section] ?? (v && names.includes(v) ? v : NAV_COUNTER_DEFAULTS[section] ?? null);
}
