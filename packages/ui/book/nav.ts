import type { NavItem, NavCount } from '../src/controls.tsx';
import { workspaces } from '../src/registry.ts';

// The book's nav: the registry's seven rail sections, each counting its first screen, with the counters of nav-rail.md (Counters) as the app shows them without a
// focus; NAV_FOCUS shows the same with a project focus: in focus, then the remainder outside it.
const COUNTS: Record<string, NavCount> = {
  tasks: { n: 42, tone: 'neutral', label: 'running' },
  conversations: { n: 7, tone: 'neutral', label: 'running' },
  results: { n: 18, tone: 'neutral', label: 'new in 24 h' },
  delivery: { n: 5, tone: 'neutral', label: 'in flight' },
  hosts: { n: 1, tone: 'attention', label: 'offline' },
};
const FOCUSED: Record<string, NavCount> = {
  tasks: { n: 12, outside: 30, tone: 'neutral', label: 'running' },
  conversations: { n: 0, outside: 7, tone: 'neutral', label: 'running' },
  results: { n: 4, outside: 14, tone: 'neutral', label: 'new in 24 h' },
  delivery: { n: 5, outside: 0, tone: 'neutral', label: 'in flight' },
  hosts: { n: 1, tone: 'attention', label: 'offline' },
};
const sections = workspaces.filter(w => w.nav).map(w => ({ id: w.id, label: w.label, icon: w.icon, root: (w as { root?: string }).root ?? w.id }));
export const NAV: NavItem[] = sections.map(({ root, ...w }) => ({ ...w, count: COUNTS[root] }));
export const NAV_FOCUS: NavItem[] = sections.map(({ root, ...w }) => ({ ...w, count: FOCUSED[root] }));
