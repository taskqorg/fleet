// Navigation counters (docs/design-system/controls/nav-rail.md, Counters): one nav_counts read gives every section's
// counter for this person; agents read the same operation. The read is repeated after a change that can move a count
// (at most once per QUIET ms, the last change included, so busy executions never storm it), after this person's
// settings change (focus, ignored projects, the counter views), and every REFRESH ms for hosts, whose liveness moves
// without a change row.
import { useEffect, useRef } from 'preact/hooks';
import { op, type Ops } from './api.ts';
import { useDoc } from './store.ts';
import { subscribe, type Change } from './feed.ts';
import { access } from './access.ts';
import { preferencesTouched } from './invalidation.ts';
import { prefs, prefsRead, prefsSaved } from './prefs.ts';
import type { NavCount, Tone } from '../../packages/ui/src/index.ts';

type Reply = Ops['nav_counts']['reply'];
const QUIET = 3000, REFRESH = 30_000;
const COUNTED: Change['kind'][] = ['task', 'session', 'execution', 'artifact', 'delivery', 'host'];
// Hosts offline needs a look (OR-18); the other counters are plain amounts of work.
const TONES: Record<string, Tone> = { hosts: 'attention' };

/** Each section's counter by section id, none while unread; ready: access is open and the person's settings are read. */
export function useNavCounts(ready: boolean): Record<string, NavCount> {
  const { focus, ignored, navCounters } = prefs.value;
  // Sorted, so the server's copy of the same settings (its own key order) names the same read.
  const key = ready && prefsRead.value ? 'nav-counts:' + JSON.stringify([focus, ignored, Object.entries(navCounters ?? {}).sort()]) : null;
  // A changed focus or counter view shows at once in prefs; the read waits for its save, so it counts the new settings.
  const doc = useDoc<Reply>(key, () => prefsSaved().then(() => op('nav_counts', {})), () => false, REFRESH);
  const reload = useRef(doc.reload); reload.current = doc.reload;
  useEffect(() => {
    if (!key) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const off = subscribe({ resync: () => {}, changes: cs => {
      // This person's settings only: other people's setting changes reach the feed too.
      if (timer || !(cs.some(c => COUNTED.includes(c.kind)) || preferencesTouched(cs, access.value.identity?.identityKey))) return;
      timer = setTimeout(() => { timer = undefined; reload.current(); }, QUIET);
    } });
    return () => { off(); clearTimeout(timer); };
  }, [key]);
  // The last counters stay while a changed setting is read (OR-23: nothing blinks out); none once access closes, so the
  // next person never sees them.
  const last = useRef<Reply | null>(null);
  if (!key) last.current = null; else if (doc.data) last.current = doc.data;
  const r = last.current;
  if (!r) return {};
  return Object.fromEntries(Object.entries(r.sections).map(([id, c]) => [id, {
    n: c.inFocus, outside: r.focused && id !== 'hosts' ? c.outside : undefined, tone: TONES[id] ?? 'neutral', label: c.label,
  }]));
}
