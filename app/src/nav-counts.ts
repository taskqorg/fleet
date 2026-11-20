// Navigation counters (docs/design-system/controls/nav-rail.md, Counters): one nav_counts read gives every section's
// counter for this person; agents read the same operation. It is read through useOp, so it rereads when a change touches
// nav_counts (this person's focus, ignored projects and counter views included) at most once per 5 s, and every 30 s for
// hosts, whose liveness moves without a change row.
import { useEffect, useRef } from 'preact/hooks';
import { signal } from '@preact/signals';
import type { Ops } from './api.ts';
import { useOp } from './store.ts';
import { prefs, prefsRead, prefsSaved } from './prefs.ts';
import { NAV_COUNTER_DEFAULTS } from './system-views.ts';
import type { NavCount, Tone } from '../../packages/ui/src/index.ts';

type Read = Ops['nav_counts']['reply'];
/** The last read, for the saved-views menu: which view each counter counts as the server resolved it. */
const lastRead = signal<Read | null>(null);
// Hosts offline needs a look (OR-18); the other counters are plain amounts of work.
const TONES: Record<string, Tone> = { hosts: 'attention' };

/** Each section's counter by section id, none while unread; ready: access is open and the person's settings are read. */
export function useNavCounts(ready: boolean): Record<string, NavCount> {
  // The server names nav_counts in every change that can move a count, this person's settings (focus, ignored projects,
  // counter views) included: reread at most once per 5 s, so busy executions never storm it, and every 30 s for hosts,
  // whose liveness moves without a change row.
  const doc = useOp('nav_counts', ready && prefsRead.value ? {} : null, { quiet: 5000, every: 30_000 });
  // A changed focus, ignored project or counter view is read as soon as its save lands, not at the end of the quiet window.
  const { focus, ignored, navCounters, views } = prefs.value;
  const settings = JSON.stringify([focus, ignored, Object.entries(navCounters ?? {}).sort().map(([list, name]) => [list, name, views.find(v => v.list === list && v.name === name)?.view ?? null])]);
  const first = useRef(true), reload = useRef(doc.reload); reload.current = doc.reload;
  useEffect(() => { if (first.current) { first.current = false; return; } void prefsSaved().then(() => reload.current()); }, [settings]);
  // The last counters stay while a changed setting is read (OR-23: nothing blinks out); none once access closes, so the
  // next person never sees them.
  const read = ready ? doc.data : null;
  useEffect(() => { lastRead.value = read; }, [read]);
  if (!read) return {};
  return Object.fromEntries(Object.entries(read.sections).map(([id, c]) => [id, {
    n: c.inFocus, outside: c.split ? c.outside : undefined, tone: TONES[id] ?? 'neutral', label: c.label,
  }]));
}

/**
 * The view a section's counter counts, for the saved-views menu; null when the counter is off. The server's answer when
 * it was read for the person's current choice (a removed or refused saved view counts the default); while a new choice
 * is still being read, that choice when its view exists, else the default.
 */
export function navCounted(section: string, names: string[]): string | null {
  const v = prefs.value.navCounters?.[section], counted = lastRead.value?.sections[section]?.view;
  if (v === '-') return null;
  const wanted = v && names.includes(v) ? v : NAV_COUNTER_DEFAULTS[section] ?? null;
  // The server's answer once it counts the current choice; a removed or refused view counts the default.
  return counted !== undefined && (counted === wanted || counted === (v || NAV_COUNTER_DEFAULTS[section])) ? counted : wanted;
}
