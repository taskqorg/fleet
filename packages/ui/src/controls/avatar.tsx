// Avatar and project chip: who a card, tile or row is about (controls/session-card.md, board master `Avatar`, `Project chip`).
import { Show } from 'solid-js';
import { type Harness, type Tone, harnesses } from '../registry.ts';
import { HarnessMark } from './primitives.tsx';

/** What an avatar shows: a bot's harness mark, or a letter (`P` for a project PM) in a tone. */
export type AvatarMark = { letter?: string; harness?: Harness; tone?: Tone };

/** Decorative by default: the name next to it carries the meaning. With `label` it is named on its own. */
export function Avatar(p: AvatarMark & { size?: 'md' | 'sm'; label?: string }) {
  const mark = () => !!p.harness && p.harness in harnesses;
  return (
    <span
      class={['avatar', p.size ?? 'md', `t-${p.tone ?? 'running'}`, { mark: mark() }]}
      data-ui="avatar"
      role={p.label ? 'img' : undefined}
      aria-label={p.label}
      aria-hidden={p.label ? undefined : 'true'}
    >
      <Show when={mark()} fallback={<span class="letter">{(p.letter ?? '?').slice(0, 1)}</span>}>
        <HarnessMark h={p.harness!} />
      </Show>
    </span>
  );
}

/** The project a session belongs to, as a small pill. */
export function ProjectChip(p: { name: string }) {
  return <span class="pchip" data-ui="project-chip">{p.name}</span>;
}
