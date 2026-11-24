// State dot and word: the one state vocabulary on cards, slots and the context panel (OR-55, controls/state-badge.md#dot-and-word).
import { createMemo } from 'solid-js';
import { type Life, type LifeFamily, type StateOf, states, lifeOf } from '../registry.ts';

/** A state of the vocabulary itself, or a state of a family that maps to it (lifeOf). */
export type LifeRef = { state: Life; family?: undefined } | { [F in LifeFamily]: { family: F; state: StateOf<F> } }[LifeFamily];

/** The vocabulary state, or undefined for a state the mapping does not know (a newer server state). */
export const toLife = (r: LifeRef): Life | undefined => {
  const s = r.family ? (lifeOf[r.family] as Record<string, Life>)[r.state as string] : r.state;
  return s in states.life ? s : undefined;
};

/**
 * A 6 px dot in the state's tone and the state word. Working pulses; Stopped is hollow; Asleep is a dashed ring.
 * `bare` keeps the word for screen readers only: the dot alone where the row already says what it is (Live slots, events).
 */
export function StateDot(p: LifeRef & { reason?: string; bare?: boolean }) {
  const life = createMemo(() => toLife(p as LifeRef));
  // An unknown state shows its own word in the neutral tone, never a healthy one.
  const d = createMemo(() => { const l = life(); return l ? states.life[l] : { label: String(p.state), tone: 'neutral' as const }; });
  return (
    <span class={['sdot', `t-${d().tone}`]} data-ui="state-dot" data-state={life() ?? 'unknown'} aria-label={p.reason ? `${d().label}, ${p.reason}` : undefined}>
      <i aria-hidden="true" />
      <span class={p.bare ? 'sr-only' : undefined}>{d().label}</span>
    </span>
  );
}
