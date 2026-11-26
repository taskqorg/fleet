// Board masters page: every state of every master the board's screens are drawn with, rendered by the real
// packages/ui controls, light and dark side by side. Built by scripts/build-book.mjs into
// docs/design-system/book/masters.html (not committed); scripts/board-extract.mjs --masters reads it and
// scripts/board-masters.py turns it into the component sets of board/masters.bl (docs/design-system/README.md#design-board).
//
// A spec names the master (the name screens use in inst(...)), its axes (component properties: each axis is a prop
// of the control and its values), and render(v), the control in the variant v. `slot(name, text)` marks a text so the
// board names it `name`: screens override a master's texts by these names, so a slot keeps its name across
// regenerations. A text the control derives (a number, a sliced letter) is named by `texts`, in document order.
// `pick` selects the master inside what render draws (one row of a list); without it the first element is the master.
// Atoms that other masters contain (NEST, by data-ui) become nested instances, so a screen switches their state.
import { For } from 'solid-js';
import { render } from '@solidjs/web';
import { MASTERS_ATOMS } from './masters-atoms.tsx';
import { MASTERS_HOME } from './masters-home.tsx';
import { MASTERS_LIVE } from './masters-live.tsx';
import { MASTERS_SECTIONS } from './masters-sections.tsx';
import { NEST } from './masters-spec.ts';
import type { MasterSpec } from './masters-spec.ts';

export const MASTERS: MasterSpec[] = [...MASTERS_ATOMS, ...MASTERS_HOME, ...MASTERS_LIVE, ...MASTERS_SECTIONS];

/** Every combination of the axes, first axis slowest: {kind: [a, b]} gives [{kind: a}, {kind: b}]. */
export function variants(axes: MasterSpec['axes']): Record<string, string>[] {
  let out: Record<string, string>[] = [{}];
  for (const [k, vals] of Object.entries(axes ?? {})) out = out.flatMap(o => vals.map(v => ({ ...o, [k]: v })));
  return out;
}

function Master(p: { spec: MasterSpec }) {
  return <section class="bm" data-master={p.spec.name} data-texts={(p.spec.texts ?? []).join('\n')} data-w={p.spec.w ?? ''}>
    <h2>{p.spec.name}</h2>
    <For each={variants(p.spec.axes)}>{v =>
      <div class="bm-v" data-variant={Object.entries(v).map(([k, x]) => `${k}=${x}`).join(';')} data-pick={p.spec.pick ?? ''}>
        <For each={['light', 'dark'] as const}>{t =>
          <div class={`frame th-${t}`}><div class="bm-box" style={p.spec.w ? { width: `${p.spec.w}px` } : undefined}>{p.spec.render(v)}</div></div>}
        </For>
      </div>}
    </For>
  </section>;
}

(window as unknown as { __boardNest: typeof NEST }).__boardNest = NEST;
const app = document.getElementById('app');
if (app) render(() => <div class="page"><h1>Board masters</h1><For each={MASTERS}>{s => <Master spec={s} />}</For></div>, app);
