// Change row: one definition change, or one Lab experiment, on Library › Changes (controls/change-row.md). The name opens
// the item and the whole row follows it (OR-26); the chip says where the change stands; the actions are the person's
// only when the row needs one (a conflict, a failed pull request, a definition made in Fleet); the time is last (OR-64).
import { For, Show } from 'solid-js';
import type { Tone } from '../states.ts';
import { Ago, Button } from './primitives.tsx';

export type ChangeRowAction = { label: string; primary?: boolean; busy?: boolean; onClick: () => void };

export function ChangeRow(p: {
  name: string;
  /** The definition's kind (agent, pipeline), quiet after the name. */
  kind?: string;
  /** What changed, or why the row needs a person. */
  summary?: string;
  /** Where it stands: the pull request's stage, Conflict, Failed, New in Fleet, the experiment's state. */
  chip?: { text: string; tone: Tone; tip?: string };
  /** attention: a person decides (a conflict); danger: it failed. */
  tone?: 'attention' | 'danger';
  actions?: ChangeRowAction[];
  /** Milliseconds; the row's last fact. */
  at?: number;
  href: string;
  onOpen: () => void;
}) {
  return (
    <li class="chrow-w">
      <article class={['chrow', p.tone && `t-${p.tone}`]} data-ui="change-row" aria-label={[p.name, p.kind, p.chip?.text].filter(Boolean).join(', ')}>
        <header>
          <h3><a class="chrow-link" href={p.href} onClick={e => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); p.onOpen(); } }}>{p.name}</a></h3>
          <Show when={p.kind}><span class="chrow-kind">{p.kind}</span></Show>
        </header>
        <Show when={p.summary}><p>{p.summary}</p></Show>
        <Show when={p.chip}>{c => <span class={['badge', `t-${c().tone}`]} data-tip={c().tip}>{c().text}</span>}</Show>
        <Show when={p.actions?.length}>
          <footer>
            <For each={p.actions}>{a => <Button label={a.label} kind={a.primary ? 'primary' : 'default'} busy={a.busy} onClick={a.onClick} />}</For>
          </footer>
        </Show>
        <Show when={p.at !== undefined}><Ago t={p.at!} /></Show>
      </article>
    </li>
  );
}
