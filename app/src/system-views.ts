// System presets of the lists with saved views (OR-33): fixed, shown before the person's own views. A system preset sets
// the state filter only; grouping, sort, projects and search stay as the person set them. The coordinator counts the same
// presets for the navigation counters (go/coordinator/nav_counts.go navSystemViews; tests/go/nav-counts.test.ts compares).
import { DELIVERY_STAGES, type View } from '../../packages/view-url/view-url.ts';

type System = { name: string; view: View };
/** Delivery stages that are done (merged, installed) and failed: a new stage is in flight unless named here. */
export const DELIVERY_DONE: string[] = ['installed', 'merged'], DELIVERY_FAILED: string[] = ['pr_failed', 'mq_failed'];
export const SYSTEM_VIEWS = {
  tasks: [{ name: 'Needs me', view: { view: 'tasks', state: ['failed', 'waiting'] } }, { name: 'Running', view: { view: 'tasks', state: ['queued', 'in_progress', 'waiting', 'held', 'paused'] } }],
  conversations: [{ name: 'Needs me', view: { view: 'conversations', state: ['failed', 'waiting'] } }, { name: 'Running', view: { view: 'conversations', state: ['preparing', 'running', 'waiting'] } }],
  delivery: [
    { name: 'Needs me', view: { view: 'delivery', state: DELIVERY_FAILED } },
    { name: 'In flight', view: { view: 'delivery', state: DELIVERY_STAGES.filter(s => !DELIVERY_DONE.includes(s)) } },
    { name: 'Installed', view: { view: 'delivery', state: DELIVERY_DONE } },
  ],
} satisfies Record<string, System[]>;

/** The view each section's navigation counter counts while the person has chosen none (navDefaults in nav_counts.go). */
export const NAV_COUNTER_DEFAULTS: Record<string, string> = { tasks: 'Running', conversations: 'Running', delivery: 'In flight', results: 'Last 24 h', hosts: 'Offline' };
