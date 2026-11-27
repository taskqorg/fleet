import type { SceneProps } from "../../../../scene-shell.tsx";
import { createMemo, createSignal, Show } from 'solid-js';
import { TaskListView, ROW2, DecisionBlock, ProblemRow, StateBadge, Totals, AgeChip, ExecTree, Thread, ResultViewer, SplitButton, Button, Empty, TabBar, ListDetail, OutcomeBlock, type TaskRow } from '../../../../../src/index.ts';
import { NOW, min, tasks, TREE, transcript } from '../../../../fixtures.ts';
import { PageHead, Panel, m, useHashLocation } from '../../../../scene-parts.tsx';
import { ALL } from "./fixture.ts";

function Tasks(props: { id?: string }) {
  const [items, setItems] = createSignal(ALL);
  const [, go] = useHashLocation();
  const sel = createMemo(() => items().find(t => String(t.id) === props.id));
  return <ListDetail
    list={<>
      <PageHead title="Tasks"><Button label="New task" icon="plus" kind="primary" /></PageHead>
      <Panel><TaskListView items={items()} now={NOW} selected={sel()?.id} row={ROW2} onOpen={t => go(`/tasks/${t.id}`)}
        onPin={pid => setItems(xs => xs.map(x => x.id === pid ? { ...x, pinned: !x.pinned } : x))} /></Panel>
    </>}
    detail={sel() ? <Show when={sel()} keyed>{t => <TaskDetail t={t} onClose={() => go('/tasks')} />}</Show> : undefined} detailKey={sel()?.id} />;
}

function TaskDetail(props: { t: TaskRow; onClose: () => void }) {
  const [tab, setTab] = createSignal('Executions');
  const [rtab, setRtab] = createSignal('Diff');
  const [answer, setAnswer] = createSignal<string | null>(null);
  return <article class="detail" data-ui="detail" tabindex={-1} aria-label={props.t.title}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={props.onClose} />
      <span class="muted mono">{props.t.project} · T-{props.t.id}</span><span class="grow" />
      <SplitButton main={{ label: 'Pause', icon: 'pause' }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash' }, { label: 'Change account', icon: 'bot' }, { label: 'Hand off', icon: 'messages-square' }]} />
      <Button label="Share" icon="paperclip" iconOnly />
    </header>
    <h2 class="detail-title">{props.t.title}</h2>
    <div class="state-line"><StateBadge family="task" state={props.t.state} reason={props.t.reason} /><AgeChip t={props.t.started} /></div>
    <OutcomeBlock outcome="Release notes for 0.4 in releases/0.4.md, reviewed, linked from the changelog."
      requirements={[{ text: 'Covers every merged PR since 0.3', met: true }, { text: 'Reviewed by the reviewer role', met: false }, { text: 'Linked from CHANGELOG.md', met: false }]}
      inputs={[{ icon: 'file-text', label: 'template.md' }, { icon: 'git-pull-request', label: '14 merged PRs' }, { icon: 'messages-square', label: 'PM session' }]}
      depends="Depends on T-9976 (completed)" />
    <Totals items={[
      { label: 'Time', m: m(860), kind: 'duration' },
      { label: 'Tokens', m: { ...m(182000), partial: '2 of 3 measured' }, kind: 'count', note: '2 of 3 measured' },
      { label: 'Cost', cost: props.t.cost === null ? [{ value: null, currency: 'USD', basis: 'unavailable', reason: 'no meter' }] : [{ value: props.t.cost, currency: 'USD', basis: props.t.est ? 'estimated' : 'measured', reason: 'tokens' }, { value: 0.45, currency: 'USD', basis: 'subscription', note: 'Claude Max' }], showUnavailable: true },
      { label: 'API calls', m: m(null, 'unavailable', 'no gateway'), kind: 'count', note: 'no gateway' },
    ]} />
    <Show when={props.t.state === 'waiting'}><DecisionBlock question="Accept this result?" askedBy="reviewer (claude)" addressedTo="you" since={NOW - 12 * min} now={NOW}
      context={<>Checks passed: links, contrast, screenshots.</>} options={[{ label: 'Accept', kbd: 'a', primary: true }, { label: 'Request rework', kbd: 'r' }]}
      answered={answer() ? { by: 'you', answer: answer()!, at: NOW, delivered: false } : undefined} onAnswer={setAnswer} /></Show>
    <Show when={props.t.state === 'failed'}><ProblemRow severity="failure" title="render-07 failed" facts="exit 1 · dependants not started" actions={[{ label: 'Retry step', primary: true }, { label: 'Open log' }]} /></Show>
    <TabBar label="Task detail" cls="detail-tabs" tabs={['Executions', 'Transcript', 'Results'].map(x => ({ id: x, label: x }))} value={tab()} onChange={setTab} />
    <Show when={tab() === 'Executions'}><ExecTree nodes={TREE} now={NOW} label="Executions" /></Show>
    <Show when={tab() === 'Transcript'}><div class="pane"><Thread blocks={transcript()} label="Transcript" now={NOW} /></div></Show>
    <Show when={tab() === 'Results'}><ResultViewer name="index.html" type="diff" size={4200} source="Worker · codex" tab={rtab()} onTab={setRtab} tabs={['Diff', 'Preview', 'Checks 3']}
      copies={[{ where: 'windows', state: 'stored' }, { where: 'mac', state: 'pending' }]}>
      {rtab() === 'Diff' ? <pre class="diff"><span class="del">- &lt;h1&gt;Welcome&lt;/h1&gt;</span><span class="add">+ &lt;h1&gt;Good things work together.&lt;/h1&gt;</span></pre> : <Empty text={`${rtab()} viewer`} />}
    </ResultViewer></Show>
  </article>;
}

export default function Scene(props: SceneProps) {
  return <Tasks id={props.detail} />;
}
