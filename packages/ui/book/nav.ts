import type { NavItem } from '../src/controls';
import { workspaces } from '../src/registry';

// The book's nav: every registry workspace, with demo count badges.
const COUNTS: Record<string, NavItem['count']> = {
  overview: { n: 3, tone: 'attention', label: 'need you' },
  tasks: { n: 2, tone: 'danger', label: 'failed' },
  hosts: { n: 1, tone: 'attention', label: 'low space' },
};
export const NAV: NavItem[] = workspaces.map(w => ({ ...w, count: COUNTS[w.id] }));
