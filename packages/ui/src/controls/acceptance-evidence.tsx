import { For, Show } from 'solid-js';
import type { AcceptanceContext } from '../../../../contracts/generated/ops.ts';
import { EntityCode, linkCodes } from './overlays.tsx';

export type AcceptanceEvidenceRow = {
  acceptance?: AcceptanceContext;
  taskId?: string | null;
  artifactIds?: unknown;
};

/** The evidence offered for acceptance. Each reference uses Fleet's standard detail card. */
export function AcceptanceEvidence(p: { q: AcceptanceEvidenceRow }) {
  const evidence = () => p.q.acceptance;
  const artifacts = () => Array.isArray(p.q.artifactIds)
    ? p.q.artifactIds.filter((id): id is string => typeof id === 'string' && Boolean(id))
    : [];
  return (
    <>
      <Show when={evidence()?.summary} keyed>{text => <p>{linkCodes(text, 0)}</p>}</Show>
      <Show when={evidence()?.verification} keyed>{text => <p>{linkCodes(text, 0)}</p>}</Show>
      <Show when={p.q.taskId || evidence()?.taskCode || evidence()?.pullRequest || evidence()?.release || artifacts().length}>
        <p>
          <Show when={p.q.taskId || evidence()?.taskCode}>
            <EntityCode kind="task" id={p.q.taskId} code={evidence()?.taskCode} label cls="inline" />{' '}
          </Show>
          <Show when={evidence()?.pullRequest} keyed>
            {pr => <><EntityCode kind="pull_request" id={`${pr.repo}#${pr.number}`} code={`#${pr.number}`} cls="inline" />{' '}</>}
          </Show>
          <Show when={evidence()?.release} keyed>
            {tag => <><EntityCode kind="release" id={tag} code={tag} cls="inline" />{' '}</>}
          </Show>
          <For each={artifacts()}>
            {id => <><EntityCode kind="result" id={id} label cls="inline" />{' '}</>}
          </For>
        </p>
      </Show>
    </>
  );
}
