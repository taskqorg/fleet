// Identity colour and fact glyphs (controls/project-tile.md, controls/fact-count.md, OR-72, OR-73).
// A project's tile carries its code in its identity colour (never a state); a fact is a glyph and a number on its tone.
import { For, Show, createSignal } from 'solid-js';
import vocabulary from '../../../../contracts/vocabulary.json' with { type: 'json' };
import { Icon } from './primitives.tsx';
import { factKinds, type FactKind } from '../registry.ts';

export type IdentityColor = (typeof vocabulary.identity.colors)[number];
export type IdentityPattern = 'solid' | 'hatch';
export const identityColors = vocabulary.identity.colors as IdentityColor[];

/** The colour a project or bot shows: the one its settings name (or the stored automatic one the caller passes), else,
 * before one is stored, one picked from its code. */
export function identityColor(code: string, color?: string | null): IdentityColor {
  if (color && (identityColors as string[]).includes(color)) return color as IdentityColor;
  let h = 0;
  for (const ch of code.toUpperCase()) h = (h * 31 + ch.charCodeAt(0)) % 9973;
  return identityColors[h % identityColors.length];
}

// The identity each project names, by code: the app sets it from project_list so every project reference (EntityCode,
// the rail) draws the same tile.
const [projectIds, setProjectIds] = createSignal<ReadonlyMap<string, { color?: string; pattern?: string }>>(new Map());
export function setProjectIdentities(list: { code: string; color?: string | null; pattern?: string | null }[]) {
  setProjectIds(new Map(list.filter(x => x.code).map(x => [x.code.toUpperCase(), { color: x.color ?? undefined, pattern: x.pattern ?? undefined }])));
}
export const projectIdentity = (code: string) => projectIds().get(code.toUpperCase());

export type TileSize = 'xs' | 'sm' | 'md' | 'lg';

/** A project's tile: its code in its identity colour over a bar in the same colour, solid or hatched. Decorative
 * beside the project's name (the name carries the meaning); `label` names it when it stands alone. */
export function ProjectTile(p: { code: string; color?: string | null; pattern?: string | null; size?: TileSize; label?: string }) {
  return (
    <span
      class={['ptile', p.size ?? 'md', `id-${identityColor(p.code, p.color)}`, { hatch: p.pattern === 'hatch' }]}
      data-ui="project-tile"
      role={p.label ? 'img' : undefined}
      aria-label={p.label}
      aria-hidden={p.label ? undefined : 'true'}
    >
      <span class="ptile-code">{p.code}</span>
    </span>
  );
}

/** One fact as its glyph and count on its tone (OR-73): the word is the tooltip and the accessible name. A count of
 * zero shows nothing (OR-29); a fact without a count (needs setup) shows its glyph alone. */
export function FactCount(p: { kind: FactKind; n?: number | null; tip?: string }) {
  const k = () => factKinds[p.kind];
  const name = () => (p.n == null ? k().label : `${k().label}: ${p.n.toLocaleString('en')}`);
  return (
    <Show when={p.n !== 0}>
      <span class={['fcount', `t-${k().tone}`]} data-ui="fact-count" data-tip={p.tip ?? k().label} role="img" aria-label={name()}>
        <Icon name={k().icon} />
        <Show when={p.n != null}><span class="num">{p.n!.toLocaleString('en')}</span></Show>
      </span>
    </Show>
  );
}

/** A row's facts in the one fixed order (registry factKinds); zero and absent facts are left out. */
export function FactCounts(p: { facts: Partial<Record<FactKind, number | null | true | undefined>>; tips?: Partial<Record<FactKind, string | undefined>> }) {
  const shown = () => (Object.keys(factKinds) as FactKind[]).filter(k => p.facts[k] !== undefined && p.facts[k] !== 0);
  return (
    <Show when={shown().length}>
      <span class="fcounts" data-ui="fact-counts">
        <For each={shown()}>{k => <FactCount kind={k} n={p.facts[k] === true ? null : (p.facts[k] as number | null)} tip={p.tips?.[k]} />}</For>
      </span>
    </Show>
  );
}

/** The identity picker of Setup (project) and Settings & Memory (bot): Auto and the eight colours, then the pattern.
 * A radio group each; Auto clears the setting. */
export function IdentityPicker(p: { code: string; auto?: string; color?: string | null; pattern?: string | null; onColor: (c: IdentityColor | null) => void; onPattern: (x: IdentityPattern | null) => void }) {
  const auto = () => !p.color || !(identityColors as string[]).includes(p.color);
  return (
    <div class="idpick" data-ui="identity-picker">
      <div class="idpick-row" role="radiogroup" aria-label="Colour">
        <button type="button" role="radio" aria-checked={auto() ? 'true' : 'false'} class={['idpick-sw auto', `id-${identityColor(p.code, p.auto)}`, { on: auto() }]} data-tip="Auto: the colour Fleet gave it" onClick={() => p.onColor(null)}>
          <span class="idpick-auto">A</span>
        </button>
        <For each={identityColors}>
          {c => (
            <button type="button" role="radio" aria-checked={p.color === c ? 'true' : 'false'} aria-label={c} data-tip={c} class={['idpick-sw', `id-${c}`, { on: p.color === c }]} onClick={() => p.onColor(c)} />
          )}
        </For>
      </div>
      <div class="idpick-row" role="radiogroup" aria-label="Pattern">
        <For each={['solid', 'hatch'] as IdentityPattern[]}>
          {x => (
            <button type="button" role="radio" aria-checked={(p.pattern ?? 'solid') === x ? 'true' : 'false'} class={['idpick-pat', { on: (p.pattern ?? 'solid') === x }]} onClick={() => p.onPattern(x === 'solid' ? null : x)}>
              <ProjectTile code={p.code} color={p.color || p.auto} pattern={x} size="sm" />
              <span>{x === 'solid' ? 'Solid' : 'Hatched'}</span>
            </button>
          )}
        </For>
      </div>
    </div>
  );
}
