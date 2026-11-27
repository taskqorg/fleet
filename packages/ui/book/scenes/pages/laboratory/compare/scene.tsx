import { createSignal, For, Show } from 'solid-js';
import { TaskListView, DecisionBlock, StateBadge, Num, SplitButton, Empty, Icon, TabBar } from '../../../../../src/index.ts';
import { NOW, hr } from '../../../../fixtures.ts';
import { PageHead, Panel, m } from '../../../../scene-parts.tsx';
import { VARIANTS, ALL } from "./fixture.ts";

function Laboratory() {
  const [note, setNote] = createSignal<string | null>(null);
  const [tab, setTab] = createSignal('Compare');
  return <>
    <PageHead title="Laboratory"><span class="muted">Experiment: hero copy method · 3 variants × 75</span><SplitButton kind="primary" main={{ label: 'Repeat trials', icon: 'rotate-ccw' }} alternatives={[{ label: 'Add variant', icon: 'plus' }, { label: 'Run on new task set', icon: 'list' }]} /></PageHead>
    <TabBar label="Experiment views" tabs={['Compare', 'Trials', 'Assessments'].map(x => ({ id: x, label: x }))} value={tab()} onChange={setTab} />
    <Show when={tab() === 'Compare'}><Panel title="Variants">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) 4rem 4rem 4.5rem 4rem 4.5rem 5rem' }}>
        <div class="vl-head" role="row"><span /><span>Variant</span><span class="r">Score</span><span class="r">Judge</span><span class="r">Tokens</span><span class="r">Time</span><span class="r">Cost</span><span class="r">Failed</span></div>
        <For each={VARIANTS}>{(x, i) => <div role="row" class="vl-row static">
          <span class="lead"><StateBadge family="execution" state={i() === 2 ? 'running' : 'succeeded'} reason={i() === 2 ? 'trials still running' : 'all trials finished'} compact /></span>
          <span class="title">{x.v}</span>
          <span class="r"><Num m={m(x.score)} kind="score" /></span>
          <span class="r"><Num m={m(x.judge, x.judge === null ? 'unavailable' : 'measured', 'judge not run')} kind="score" /></span>
          <span class="r"><Num m={m(x.tokens)} kind="count" /></span>
          <span class="r"><Num m={m(x.time)} kind="duration" /></span>
          <span class="r"><Num m={m(x.cost, x.cost === null ? 'unavailable' : x.est ? 'estimated' : 'measured', x.cost === null ? 'no meter' : 'tokens')} kind="money" /></span>
          <span class="r"><span class={`chip-age${x.failed > 5 ? ' t-danger alert' : ''}`} aria-label={`${x.failed} of ${x.trials} trials failed`}>{x.failed > 5 && <Icon name="triangle-alert" />}{x.failed}/{x.trials}</span></span>
        </div>}</For>
      </div>
    </Panel></Show>
    <Show when={tab() === 'Trials'}><Panel><TaskListView items={ALL.slice(0, 300)} now={NOW} onPin={() => {}} height="420px" /></Panel></Show>
    <Show when={tab() === 'Assessments'}><Empty text="No assessments yet. Write one from the comparison." /></Show>
    <Panel>
      <DecisionBlock question="Use B new skill for hero copy in cs?" askedBy="lab agent" addressedTo="architect" since={NOW - 3 * hr} now={NOW}
        context="B scores 0.83 against 0.71 with fewer tokens; 2 failed trials kept for review." options={[{ label: 'Yes', kbd: 'a', primary: true }, { label: 'Keep current' }]}
        answered={note() ? { by: 'you', answer: note()!, at: NOW, delivered: true } : undefined} onAnswer={setNote} />
    </Panel>
  </>;
}

export default function Scene() {
  return <Laboratory />;
}
