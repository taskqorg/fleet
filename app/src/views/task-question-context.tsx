import { For, Match, Show, Switch, createMemo } from 'solid-js';
import { EntityCode } from '../../../packages/ui/src/index.ts';
import type { QuestionRow } from '../../../contracts/generated/ops.ts';

export function TaskQuestionContext(props: { question: QuestionRow }) {
  const q = () => props.question;
  const artifacts = createMemo(() => {
    const ids = q().artifactIds;
    return Array.isArray(ids) ? ids.filter((id): id is string => typeof id === 'string') : [];
  });
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
        </>
      }
    >
      <Match when={q().kind === 'acceptance'}>
        <>
          <strong>Result acceptance</strong>
          <Show when={q().executionId} keyed>
            {execId => (
              <>
                {' · '}
                <EntityCode id={execId} kind="execution" label cls="inline" />
              </>
            )}
          </Show>
          <Show when={artifacts().length > 0} fallback={<span class="muted"> · No stored output files</span>}>
            <For each={artifacts()}>
              {id => (
                <span>
                  {' · '}
                  <EntityCode id={id} kind="result" label cls="inline" />
                </span>
              )}
            </For>
          </Show>
          <p>Accepting records this result and completes the task. Rejecting keeps it open for rework.</p>
        </>
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
