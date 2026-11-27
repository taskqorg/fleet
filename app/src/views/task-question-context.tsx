import { Match, Show, Switch, createMemo } from 'solid-js';
import { AcceptanceEvidence, EntityCode } from '../../../packages/ui/src/index.ts';
import type { QuestionRow } from '../../../contracts/generated/ops.ts';

/** Whether a typed answer can be accepted: acceptance and a decision with options take their options only (the server
 * refuses other text for a failed run's decision), so their field would only mislead. */
export function typedAnswer(q: QuestionRow): boolean {
  if (q.kind === 'acceptance') return false;
  return !(q.kind === 'operator_decision' && Array.isArray(q.options) && q.options.length > 0);
}

export function TaskQuestionContext(props: { question: QuestionRow }) {
  const q = () => props.question;
  const isIdle = createMemo(() => {
    const rawOpts = q().options;
    const opts = Array.isArray(rawOpts) ? rawOpts.map(String) : [];
    return opts.includes('fail task') || opts.includes('accept earlier result');
  });

  return (
    <Switch
      fallback={
        <>
          <strong>Blocking decision</strong>
          <p>
            {isIdle()
              ? 'Your answer unblocks this wait: fail the task, accept an eligible earlier result, or retry in the conversation.'
              : 'Your answer resolves this wait and unblocks the task.'}
          </p>
          <Show when={isIdle() && q().sessionId} keyed>
            {sessionId => <p>To retry, write in <EntityCode id={sessionId} kind="conversation" label cls="inline" />.</p>}
          </Show>
        </>
      }
    >
      <Match when={q().kind === 'acceptance'}>
        <AcceptanceEvidence q={q()} />
      </Match>
      <Match when={q().cause}>
        <>
          <strong>System problem</strong>
          <p>System alert. Answering records your action without completing a task.</p>
        </>
      </Match>
      <Match when={q().kind === 'agent_question'}>
        <>
          <strong>Blocking decision</strong>
          <Show when={q().sessionId} keyed>
            {sessionId => (
              <>
                {' · '}
                <EntityCode id={sessionId} kind="conversation" label cls="inline" />
              </>
            )}
          </Show>
          <p>Your answer goes to the waiting conversation and unblocks execution.</p>
        </>
      </Match>
      <Match when={q().kind === 'secret'}>
        <>
          <strong>Blocking decision</strong>
          <p>Adding the requested key (or declining) unblocks this access wait.</p>
        </>
      </Match>
    </Switch>
  );
}
