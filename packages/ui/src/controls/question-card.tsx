import type { JSX } from '@solidjs/web';
// Question card: the card form of a decision (controls/decision-block.md#card, OR-53, board master `Question card`).
import { createEffect, createSignal, For, Show } from 'solid-js';
import { createDraft } from '../drafts.ts';
import { isSubmitEnter } from '../keys.ts';
import { Button, Ago } from './primitives.tsx';
import { EntityCode, linkCodes, QuestionImages } from './overlays.tsx';
import type { QuestionChoice } from '../answer.ts';

/**
 * Open: the question, its choices as buttons in the asker's order (the asker's default is primary), then a one-line
 * answer that sends on Enter. Failed: the recovery question in the danger tone (Retry, Open), with no typed answer.
 * Answered: one muted line; the card's owner then shows the next open question in its place.
 */
export function QuestionCard(p: {
  code?: string | null; questionId?: string | null; artifactIds?: unknown; showImages?: boolean;
  question: string; context?: JSX.Element; choices: QuestionChoice[]; tone?: 'attention' | 'danger';
  /** The chosen label while its answer is on the way: that button is busy, the others wait. */
  answering?: string;
  answered?: { answer: string; at: number };
  onAnswer?: (answer: string) => void;
  /** A key request: the one-line field takes the key (write-only) and Add key sends it here, never to onAnswer. */
  onSecret?: (value: string) => void;
  /** Where the typed answer is kept (drafts.ts), e.g. `question:<id>`: a remount or reload keeps it. A key is never kept. */
  draftKey?: string;
}) {
  const [answer, setAnswer, clearAnswer] = createDraft(() => p.draftKey);
  const [secret, setSecret] = createSignal('');
  const danger = () => p.tone === 'danger';
  // An answered question has no draft left.
  createEffect(() => !!p.answered, done => { if (done) clearAnswer(); });
  const send = (s: string) => { if (s.trim() && !p.answering) p.onAnswer?.(s.trim()); };
  const addKey = () => { const v = secret().trim(); if (v && !p.answering) p.onSecret?.(v); };
  const ref = () => (p.code || p.questionId)
    ? <div class="qcard-head"><EntityCode kind="question" code={p.code} id={p.questionId} form="code" /></div>
    : null;
  return (
    <Show
      when={p.answered}
      keyed
      fallback={
        <div class={['qcard', { danger: danger() }]} data-ui="question-card" role="group" aria-label={p.question} data-question-images={p.showImages ? p.questionId ?? '' : undefined} onClick={e => e.stopPropagation()}>
          {ref()}
          {/* Codes, Fleet links and UUIDs in the text are entity references (OR-38, OR-44), never raw ids or URLs. */}
          <p class="qcard-q">{linkCodes(p.question, 0)}</p>
          <Show when={p.showImages}><QuestionImages text={p.question} questionId={p.questionId} artifactIds={p.artifactIds} /></Show>
          {p.context}
          <div class="qcard-choices">
            <For each={p.choices}>
              {c => (
                <Button
                  label={c.label}
                  kind={c.primary ? (danger() ? 'danger' : 'primary') : danger() ? 'default' : 'attention'}
                  busy={p.answering === c.label}
                  disabledReason={p.answering && p.answering !== c.label ? 'Sending the answer' : undefined}
                  onClick={() => send(c.label)}
                />
              )}
            </For>
          </div>
          <Show when={p.onSecret}>
            <div class="qcard-choices">
              <input
                class="qcard-answer"
                type="password"
                autocomplete="off"
                spellcheck={false}
                value={secret()}
                placeholder="Paste key…"
                aria-label={`Key: ${p.question}`}
                disabled={!!p.answering}
                onInput={e => setSecret(e.currentTarget.value)}
                onKeyDown={e => { if (isSubmitEnter(e)) { e.preventDefault(); addKey(); } }}
              />
              <Button label="Add key" icon="shield" kind="primary" busy={p.answering === 'key added'} disabledReason={secret().trim() ? undefined : 'Paste the key first'} onClick={addKey} />
            </div>
          </Show>
          <Show when={!danger() && !p.onSecret}>
            <input
              class="qcard-answer"
              value={answer()}
              placeholder="Answer…"
              aria-label={`Answer: ${p.question}`}
              disabled={!!p.answering}
              onInput={e => setAnswer(e.currentTarget.value)}
              onKeyDown={e => { if (isSubmitEnter(e)) { e.preventDefault(); send(answer()); } }}
            />
          </Show>
        </div>
      }
    >
      {a => <div class="qcard answered" data-ui="question-card" role="status">{ref()}<span>Answered · {a.answer} · <Ago t={a.at} /></span></div>}
    </Show>
  );
}
