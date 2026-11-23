import { EntityCode } from '../../../packages/ui/src/index.ts';
import type { QuestionRow } from '../../../contracts/generated/ops.ts';

export function TaskQuestionContext({ question: q }: { question: QuestionRow }) {
  if (q.kind === 'acceptance') {
    const artifacts = Array.isArray(q.artifactIds) ? q.artifactIds.filter((id): id is string => typeof id === 'string') : [];
    return (
      <>
        <strong>Result acceptance</strong>
        {q.executionId && <> · <EntityCode id={q.executionId} kind="execution" label cls="inline" /></>}
        {artifacts.length > 0
          ? artifacts.map(id => <span key={id}> · <EntityCode id={id} kind="result" label cls="inline" /></span>)
          : <span class="muted"> · No stored output files</span>}
        <p>Accepting records this result and completes the task. Rejecting keeps it open for rework.</p>
      </>
    );
  }
  if (q.cause) {
    return (
      <>
        <strong>System problem</strong>
        <p>System alert. Answering records your action without completing a task.</p>
      </>
    );
  }
  if (q.kind === 'agent_question') {
    return (
      <>
        <strong>Blocking decision</strong>
        {q.sessionId && <> · <EntityCode id={q.sessionId} kind="conversation" label cls="inline" /></>}
        <p>Your answer goes to the waiting conversation and unblocks execution.</p>
      </>
    );
  }
  if (q.kind === 'secret') {
    return (
      <>
        <strong>Blocking decision</strong>
        <p>Adding the requested key (or declining) unblocks this access wait.</p>
      </>
    );
  }
  const opts = Array.isArray(q.options) ? q.options.map(String) : [];
  const isIdle = opts.includes('fail task') || opts.includes('accept earlier result');
  return (
    <>
      <strong>Blocking decision</strong>
      <p>
        {isIdle
          ? 'Your answer unblocks this wait: fail the task, accept an eligible earlier result, or retry in the conversation.'
          : 'Your answer resolves this wait and unblocks the task.'}
      </p>
    </>
  );
}
