// Navigation counters (docs/design-system/controls/nav-rail.md, Counters): one nav_counts read gives every section's
// counter for this person; agents read the same operation. It is read through useOp, so it rereads when a change touches
// nav_counts (this person's focus, ignored projects and counter views included) at most once per 5 s, and every 30 s for
// hosts, whose liveness moves without a change row.
import { createEffect, createMemo, type Accessor } from 'solid-js';
import { cell, readValue, type MaybeAccessor } from './signal.ts';
import type { Ops } from './api.ts';
import { useOp } from './store.ts';
import { prefs, prefsRead } from './prefs.ts';
import { NAV_COUNTER_DEFAULTS } from './system-views.ts';
import type { NavCount, Tone } from '../../packages/ui/src/index.ts';

type Read = Ops['nav_counts']['reply'];
/** The last read, for the saved-views menu: which view each counter counts as the server resolved it. */
const lastRead = cell<Read | null>(null);
// Hosts offline needs a look (OR-18); the other counters are plain amounts of work.
const TONES: Record<string, Tone> = { hosts: 'attention' };

/** Each section's counter by section id, none while unread; ready: access is open and the person's settings are read. */
export function useNavCounts(ready: MaybeAccessor<boolean>): Accessor<Record<string, NavCount>> {
  // The server names nav_counts in every change that can move a count, this person's settings (focus, ignored projects,
  // counter views) included: reread via setting-change SSE at most once per 5 s, and every 30 s for hosts,
  // whose liveness moves without a change row.
  const doc = useOp('nav_counts', () => (readValue(ready) && prefsRead.value ? {} : null), { quiet: 5000, every: 30_000 });
  // The last counters stay while a changed setting is read (OR-23: nothing blinks out); none once access closes, so the
  // next person never sees them.
  const read = createMemo(() => (readValue(ready) ? doc.data : null));
  createEffect(() => read(), r => { lastRead.value = r; });
  return createMemo(() => {
    const r = read();
    if (!r) return {};
    return Object.fromEntries(Object.entries(r.sections).map(([id, c]) => [id, {
      n: c.inFocus, outside: c.split ? c.outside : undefined, tone: TONES[id] ?? 'neutral', label: c.label,
    }]));
  });
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
