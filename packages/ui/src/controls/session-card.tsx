// Session card: one active session on Home (controls/session-card.md, OR-50, OR-53, board master `Session card`).
import { createMemo, For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { type Life, states } from '../registry.ts';
import { age } from '../format.ts';
import { StateDot } from './state-dot.tsx';
import { Avatar, ProjectChip, type AvatarMark } from './avatar.tsx';
import { OfflineMarker } from './primitives.tsx';

/** Card borders per state (OR-56): the tone sits on the border and the question, never on the fill. */
const BORDER: Partial<Record<Life, string>> = { waiting: 'attention', failed: 'danger' };

/**
 * Head (avatar, name, project chip, state dot and word), the "now" line in the person's words, a 2 px progress bar when
 * the session has a plan, the meta line (its facts, a missing one left out, then the work facts at its right end with
 * the time last) and at most one question. The whole card opens
 * the session (OR-26); the question acts in place. No model, tokens, cost or ids (OR-54).
 */
export function SessionCard(p: {
  name: string; project?: string; avatar: AvatarMark; state: Life; now: string;
  progress?: { step: number; of: number };
  /** Facts in order: step, host, next timer. */
  meta?: JSX.Element[];
  /** The work facts (WorkFacts) at the right end of the meta line, the time last (controls/work-facts.md). */
  facts?: JSX.Element;
  /** The oldest open question, as a QuestionCard. */
  question?: JSX.Element;
  /** Last confirmed time when the card's data is not current: the card dims and shows the offline marker age. */
  staleSince?: number; clock?: number;
  onOpen?: () => void;
}) {
  const word = createMemo(() => states.life[p.state].label);
  const meta = createMemo(() => p.meta ?? []);
  const open = (e: KeyboardEvent | MouseEvent) => {
    if (e instanceof KeyboardEvent && (e.key !== 'Enter' || e.target !== e.currentTarget)) return;
    p.onOpen?.();
  };
  return (
    <article
      class={['scard', BORDER[p.state] ? `b-${BORDER[p.state]}` : '', { 'is-stale': p.staleSince !== undefined }]}
      data-ui="session-card"
      data-state={p.state}
      tabindex={0}
      aria-label={`${p.name}, ${word()}${p.staleSince !== undefined ? `, last seen ${age(p.staleSince, p.clock ?? Date.now()).long}` : ''}`}
      onClick={open}
      onKeyDown={open}
    >
      <div class="scard-head">
        <Avatar {...p.avatar} />
        <div class="scard-who">
          <div class="scard-name"><b>{p.name}</b><Show when={p.project} keyed>{pr => <ProjectChip name={pr} />}</Show></div>
          <StateDot state={p.state} />
        </div>
        <Show when={p.staleSince !== undefined}><OfflineMarker state="offline" since={p.staleSince} now={p.clock ?? Date.now()} /></Show>
      </div>
      <p class="scard-now">{p.now}</p>
      <Show when={p.progress} keyed>
        {g => <progress class="scard-progress" max={g.of} value={g.step} aria-label={`Step ${g.step} of ${g.of}`} />}
      </Show>
      <Show when={meta().length > 0 || p.facts}>
        <div class="scard-meta">
          <span class="scard-meta-text"><For each={meta()}>{(m, i) => <>{i() > 0 ? ' · ' : ''}{m}</>}</For></span>
          {p.facts}
        </div>
      </Show>
      <Show when={p.question} keyed>{q => <div class="scard-q">{q}</div>}</Show>
    </article>
  );
}

/** A loading card of the same size as a session card. */
export function SessionCardSkeleton() {
  return <div class="scard skeleton" data-ui="session-card" aria-hidden="true"><i /><i /><i /></div>;
}
