import type { SceneProps } from "../../../../scene-shell.tsx";
import { createSignal, For, Show } from 'solid-js';
import { ProblemRow, StateBadge, Totals, SettingField, Button, Empty, ListDetail, DefinitionEditor, Choice, DraftBar, StepList, FlowStrip, type StepItem, type FlowNode, type StateOf, DetailSection } from '../../../../../src/index.ts';
import { PIPE_DEFS } from './fixture.ts';
import { NOW, hr } from '../../../../fixtures.ts';
import { PageHead, Panel, m, kvs, CatRow, CAT_COLS, useHashLocation } from '../../../../scene-parts.tsx';

const FACTORY: StepItem[] = [
  { id: 'intake', kind: 'agent', executor: <><b>intake</b> accepts, narrows, rejects or asks</>, reads: 'issue', writes: 'intake.md, risk, surfaces' },
  { id: 'spec', kind: 'agent', executor: <><b>spec-writer</b> records assumptions, never stops to ask</>, reads: 'issue, intake.md', writes: 'spec.md' },
  { band: 'loop', label: 'Repeat until every review passes · at most 3 rounds · then ask the operator' },
  { id: 'build', kind: 'agent', executor: <><b>claude-worker</b> in its own worktree</>, reads: 'spec.md, reviews, checks log', writes: 'change, build.md', inset: true },
  { id: 'gate', kind: 'program', executor: <><b>checks.sh</b> a fail goes back to build and no reviewer starts</>, reads: 'change', writes: 'checks.md', inset: true },
  { band: 'parallel', label: 'In parallel · intake decides which reviewers run', inset: true },
  { id: 'review-code', kind: 'agent', executor: <><b>claude-reviewer</b> model chosen by risk</>, reads: 'change, spec.md', writes: 'review, verdict', inset: true },
  { id: 'review-ui', kind: 'agent', executor: <><b>website-reviewer</b> only when the surface is ui</>, reads: 'change, spec.md', writes: 'review, verdict', inset: true },
  { id: 'review-security', kind: 'agent', executor: <><b>security-reviewer</b> only when the surface is security</>, reads: 'change, spec.md', writes: 'review, verdict', inset: true },
  { id: 'approve', kind: 'agent', executor: <><b>approver</b> approves, or escalates to the operator</>, reads: 'spec.md, reviews, checks.md', writes: 'decision.md' },
  { id: 'deliver', kind: 'tool', executor: <><b>session_deliver</b> syncs with main, reruns the gate, merges</>, reads: 'change, decision.md', writes: 'pull request' },
];
const FACTORY_FLOW = (cur?: string): FlowNode[] => [
  { id: 'intake', kind: 'agent' }, { id: 'spec', kind: 'agent' },
  { loop: 'until all pass · max 3', nodes: [{ id: 'build', kind: 'agent' }, { id: 'gate', kind: 'program' }, { parallel: [{ id: 'review-code', kind: 'agent', current: cur === 'review-code' }, { id: 'review-ui', kind: 'agent', optional: true, current: cur === 'review-ui' }, { id: 'review-security', kind: 'agent', optional: true, current: cur === 'review-security' }] }] },
  { id: 'approve', kind: 'agent' }, { id: 'deliver', kind: 'tool' }];
const RENDER_RUN: StepItem[] = [
  { id: 'prepare', kind: 'program', executor: <b>prepare-inputs.sh</b>, reads: 'batch', writes: 'inputs/', run: { state: 'succeeded', note: 'reused', reason: 'from run 40: unchanged', time: m(0), cost: m(null, 'unavailable', 'no meter') } },
  { band: 'parallel', label: 'In parallel · 8 assets' },
  { id: 'render', kind: 'tool', executor: <><b>render_asset</b> (blender)</>, reads: 'inputs/', writes: 'renders/', inset: true,
    run: { state: 'failed', note: '7 of 8', reason: 'asset-07 failed', time: m(372), cost: m(3.4, 'estimated', 'tokens'), error: 'asset-07 · exit 1 after 2m 04s on windows · CUDA out of memory (2.1 GB). 7 finished renders kept.' } },
  { id: 'assemble', kind: 'agent', executor: <><b>codex-designer</b></>, reads: 'renders/', writes: 'catalogue.html', run: { state: 'waiting', note: 'waits for render', time: m(null, 'unavailable', 'not started'), cost: m(null, 'unavailable', 'not started') } },
];
function StepEditor(props: { onAgent: () => void }) {
  return <>
    {kvs([
      ['settings', 'Kind', undefined, <Choice label="Kind" value="agent" options={[{ id: 'agent', label: 'Agent', icon: 'bot' }, { id: 'tool', label: 'Tool', icon: 'settings' }, { id: 'program', label: 'Program', icon: 'terminal' }, { id: 'pipeline', label: 'Pipeline', icon: 'network' }]} />],
      ['bot', 'Agent', 'claude-reviewer', <Button label="Open configuration" icon="external-link" onClick={props.onAgent} />],
      ['play', 'Runs', undefined, <Choice label="Runs" value="always" options={[{ id: 'always', label: 'always' }, { id: 'when', label: 'when an earlier output says so' }]} />],
      ['sliders-horizontal', 'Model', 'haiku when intake rates the risk low, otherwise sonnet', <Button label="Add rule" icon="plus" iconOnly />],
    ])}
    <SettingField label="Assignment" value="Review the change against spec.md and project rules." help="Task goal." now={NOW} status={{ kind: 'applied', at: NOW - 3 * hr }} />
    <h3 class="sub-h">Inputs</h3>
    {kvs([['link', 'change', 'from build'], ['link', 'spec.md', 'from spec'], ['file-plus-2', 'checks.md', 'from gate · optional']])}
    <h3 class="sub-h">Outputs</h3>
    {kvs([['file-text', 'review', 'markdown findings'], ['check', 'verdict', 'pass | changes requested']])}
    <h3 class="sub-h">Then</h3>
    {kvs([
      ['circle-check', 'Pass', 'proceed to approve'],
      ['rotate-cw', 'Changes requested', 'back to build with reviews'],
      ['clock', 'After 3 rounds', 'stop and ask the operator'],
      ['octagon-x', 'Fail', 'stop and keep outputs'],
    ])}
    <div><Button label="Remove step" icon="trash-2" kind="danger" /></div>
  </>;
}

function Pipelines(props: { id?: string }) {
  const id = () => props.id ?? 'issue-to-pr';
  const [, go] = useHashLocation();
  const [open, setOpen] = createSignal<string | undefined>('review-code');
  const [tab, setTab] = createSignal(id() === 'render-props' ? 'Runs' : 'Definition');
  const failed = () => id() === 'render-props';
  const list = <>
    <PageHead title="Pipelines"><Button label="New pipeline" icon="plus" iconOnly kind="primary" /></PageHead>
    <Panel><div class="vl-wrap" style={CAT_COLS}>
      <For each={PIPE_DEFS}>{([pid, isDef, st, facts, right]) => <CatRow id={pid} sel={pid === id()} onOpen={() => go(pid === 'render-props' ? '/pipelines/render-props' : '/pipelines')} state={isDef ? <StateBadge family="definition" state={st as StateOf<'definition'>} compact /> : <StateBadge family="execution" state={st as StateOf<'execution'>} compact />} right={right}><span class="facts">{facts}</span></CatRow>}</For>
    </div></Panel>
  </>;
  return <ListDetail list={list} detailKey={id()} detail={<DefinitionEditor label={id()} crumb={failed() ? 'csgo · config/pipelines/render-props.json · run 41 at 94e588a' : 'shared · config/pipelines/issue-to-pr.json · 5eefade'}
    title={id()} tabs={['Definition', 'Runs', 'Used by', 'History']} tab={tab()} onTab={setTab} onClose={() => go('/pipelines')}>
    <Show when={tab() === 'Definition' && !failed()}>
      <DraftBar changes={1} base="5eefade" note="The review-code step reads one more input." actions={[{ label: 'Run', icon: 'play' }, { label: 'Diff' }, { label: 'Save as commit', icon: 'save', primary: true }]} />
      <DetailSection title="Starts when">
        <Choice label="Triggers" value={['label', 'hand']} options={[{ id: 'label', label: 'an issue gets the label factory', icon: 'github' }, { id: 'hand', label: 'a person or an agent starts it' }, { id: 'add', label: 'Add trigger', icon: 'plus' }]} />
      </DetailSection>
      <DetailSection title="Flow"><FlowStrip nodes={FACTORY_FLOW(open())} label="intake, spec, then a loop of build, gate and parallel reviews, then approve and deliver" /></DetailSection>
      <DetailSection title="Steps">
        <StepList items={FACTORY} open={open()} onOpen={s => setOpen(open() === s ? undefined : s)} label="Steps of issue-to-pr"><StepEditor onAgent={() => go('/agents')} /></StepList>
      </DetailSection>
    </Show>
    <Show when={tab() === 'Definition' && failed()}><DetailSection title="Steps"><StepList items={RENDER_RUN.map(s => 'band' in s ? s : { ...s, run: undefined })} label="Steps of render-props" /></DetailSection></Show>
    <Show when={tab() === 'Runs' && failed()}>
      <ProblemRow severity="failure" title="Run 41 stopped at render" facts="Repeating asset-07 and assemble is enough: prepare and 7 renders are kept."
        actions={[{ label: 'Rerun failed', icon: 'rotate-ccw', primary: true }, { label: 'Repeat all steps' }, { label: 'Edit definition' }]} />
      <DetailSection title="Flow"><FlowStrip label="prepare, eight parallel renders with one failed, then assemble waiting"
        nodes={[{ id: 'prepare', kind: 'program', state: 'succeeded' }, { parallel: [{ id: 'render × 7', kind: 'tool', state: 'succeeded' }, { id: 'asset-07', kind: 'tool', state: 'failed', current: true }] }, { id: 'assemble', kind: 'agent', state: 'waiting' }]} /></DetailSection>
      <DetailSection title="Steps of run 41"><StepList items={RENDER_RUN} label="Steps of run 41" /></DetailSection>
      <Totals items={[{ label: 'Wall time', m: m(374), kind: 'duration' }, { label: 'Cost', m: m(3.4, 'estimated', 'tokens'), kind: 'money' }, { label: 'Renders kept', m: m(7), kind: 'count', note: 'of 8' }]} />
    </Show>
    <Show when={tab() === 'Runs' && !failed()}><Empty text="41 runs. Open render-props to see a run with a failed step." /></Show>
    <Show when={tab() === 'Used by' || tab() === 'History'}><Empty text={`${tab()} is not mocked up.`} /></Show>
  </DefinitionEditor>} />;
}

export default function Scene(props: SceneProps) {
  return <Pipelines id={props.detail} />;
}
