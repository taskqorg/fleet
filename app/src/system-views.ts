// System presets of the lists with saved views (OR-33): fixed, shown before the person's own views. A system preset sets
// the state filter only; grouping, sort, projects and search stay as the person set them. One source,
// contracts/vocabulary.json: the coordinator's nav_counts counts the same presets.
import vocabulary from '../../contracts/vocabulary.json' with { type: 'json' };
import type { View, ViewName } from '../../packages/view-url/view-url.ts';

type System = { name: string; view: View };
type Preset = { name: string; state?: string[]; notState?: string[] };
const presets = (list: 'tasks' | 'conversations' | 'delivery'): System[] => (vocabulary.systemViews[list] as Preset[]).map(p => ({
  name: p.name,
  // notState: every state of the list except these, so a new state joins the preset.
  view: { view: list as ViewName, state: p.state ?? vocabulary.lists[list].states.filter(s => !p.notState?.includes(s)) },
}));
export const SYSTEM_VIEWS = { tasks: presets('tasks'), conversations: presets('conversations'), delivery: presets('delivery') };

/** The view each section's navigation counter counts while the person has chosen none. */
export const NAV_COUNTER_DEFAULTS: Record<string, string> = vocabulary.navDefaults;
/** Delivery stages that are done (the Installed preset) and failed (Needs me): a new stage is in flight unless named there. */
export const DELIVERY_DONE: string[] = SYSTEM_VIEWS.delivery.find(p => p.name === 'Installed')!.view.state!;
export const DELIVERY_FAILED: string[] = SYSTEM_VIEWS.delivery.find(p => p.name === 'Needs me')!.view.state!;
