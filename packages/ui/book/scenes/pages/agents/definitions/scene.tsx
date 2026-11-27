import { createMemo, createSignal, For, Show } from 'solid-js';
import { StateBadge, Totals, SettingField, Button, ListDetail, DefinitionEditor, Choice, DraftBar, HarnessMark, type Harness, DetailSection, KvRow } from '../../../../../src/index.ts';
import { NOW, hr } from '../../../../fixtures.ts';
import { PageHead, Panel, m, kvs, CatRow, CAT_COLS } from '../../../../scene-parts.tsx';
import { AGENT_DEFS, HARNESS_TOOLS } from "./fixture.ts";

function Agents() {
  const [sel, setSel] = createSignal<string | null>('claude-reviewer');
  const [tab, setTab] = createSignal('Configuration');
  const [role, setRole] = createSignal('reviewer');
  const [tools, setTools] = createSignal(['Read', 'Glob', 'Grep', 'Bash (read-only)']);
  const a = createMemo(() => AGENT_DEFS.find(x => x.id === sel()));
  const list = <>
    <PageHead title="Agents"><Button label="New agent" icon="plus" iconOnly kind="primary" /></PageHead>
    <Panel><div class="vl-wrap" style={CAT_COLS}>
      <For each={AGENT_DEFS}>{x => <CatRow id={x.id} sel={x.id === sel()} onOpen={() => setSel(x.id)} right={x.right}
        state={<StateBadge family="definition" state={x.state} compact />}>
        <HarnessMark h={x.h} /><span class="id">{x.model}</span>{!x.id.includes(x.role) && <span class="muted">{x.role}</span>}</CatRow>}</For>
    </div></Panel>
  </>;
  return <ListDetail list={list} detailKey={sel() ?? undefined} detail={a() ? <Show when={a()} keyed>{ag => <DefinitionEditor label={ag.id} crumb={`shared · config/agents/${ag.id}.json · 4e5bf83`}
    title={<>{ag.id}<HarnessMark h={ag.h} /></>} tabs={['Configuration', 'Effective', 'Diff', 'Compatibility', 'Usage']} tab={tab()} onTab={setTab} onClose={() => setSel(null)}>
    <Show when={tab() === 'Configuration'}>
      <DraftBar changes={2} base="4e5bf83" note="Instructions and tools differ. New executions keep using the committed revision."
        actions={[{ label: 'Test', icon: 'play' }, { label: 'Diff', onClick: () => setTab('Diff') }, { label: 'Save as commit', icon: 'save', primary: true }]} />
      <DetailSection title="Identity">
        <SettingField label="Purpose" value="Reviews a finished change against the task and the project rules. Never edits." help="One line; shown wherever this agent is picked." now={NOW} status={{ kind: 'applied', at: NOW - 2 * hr }} />
        <KvRow icon="bot" label="Role" value={<Choice label="Role" value={role()} onChange={setRole} options={['PM', 'worker', 'reviewer', 'judge', 'custom'].map(r => ({ id: r, label: r }))} />} />
        <KvRow icon="settings" label="Harness" value={<Choice label="Harness" value={ag.h} options={[{ id: 'claude', label: 'Claude', icon: 'claude' }, { id: 'codex', label: 'Codex', icon: 'openai' }, { id: 'antigravity', label: 'Antigravity', icon: 'antigravity' }]} />} />
        <SettingField label="Model" value={ag.model} help="The harness lists the models it can run." now={NOW} status={{ kind: 'applied', at: NOW - 2 * hr }} />
      </DetailSection>
      <DetailSection title="Instructions">
        <pre class="diff block">{'Review the explicit supplied task and retained materials.\nDo not modify files or delegate work.\n'}<span class="add">Report each finding as: file, line, rule, severity (blocker | major | nit).</span><span class="add">Return "changes requested" when any blocker or major finding exists; otherwise "pass".</span></pre>
        <span class="muted">Three more layers reach the agent. The Effective tab lists all of them in order.</span>
      </DetailSection>
      <DetailSection title="Skills">{kvs([
        ['file-text', 'source-citations', <span class="mono">a41c9e0 · 4 agents</span>, <Button label="Remove" icon="x" iconOnly />],
        ['file-text', 'review-checklist', <span class="mono">9d02f71 · 2 agents</span>, <Button label="Remove" icon="x" iconOnly />],
      ])}<div><Button label="Add skill" icon="plus" /></div></DetailSection>
      <DetailSection title="Tools">{kvs([
        ['terminal', 'Harness', undefined, <Choice label="Harness tools" value={tools()} onChange={t => setTools(tools().includes(t) ? tools().filter(x => x !== t) : [...tools(), t])} options={HARNESS_TOOLS.map(t => ({ id: t, label: t }))} />],
        ['link', 'Connections', 'credentials stay in the connection', <Choice label="Connections" value={['fleet']} options={[{ id: 'fleet', label: 'fleet · questions, reply' }, { id: 'tavily', label: 'tavily · web_search' }, { id: 'github', label: 'github · pr_read' }]} />],
        ['panels-top-left', 'Environment', undefined, <Choice label="Environment needs" value={['checkout', 'os']} options={[{ id: 'checkout', label: 'checkout: read' }, { id: 'os', label: 'os: any' }, { id: 'gpu', label: 'gpu' }]} />],
      ])}</DetailSection>
    </Show>
    <Show when={tab() === 'Effective'}>
      <DetailSection title="Instruction layers, in the order the agent receives them">{kvs([
        ['shield', 'Shared rules', <span class="mono">specq @ 13772b7 · 3.1k tokens</span>],
        ['bot', 'Role', <span class="mono">config/roles/reviewer.md @ 77ab102 · 420 tokens</span>],
        ['file-plus-2', 'This configuration', <span class="mono">draft · 214 tokens</span>],
        ['folder-open', 'Project override', 'resolved when an execution starts'],
      ])}</DetailSection>
      <DetailSection title="Effective settings">{kvs([
        ['settings', 'Model', <span class="mono">sonnet</span>],
        ['terminal', 'Tools', "what this file allows, within the project grant and the task's choice"],
      ])}</DetailSection>
    </Show>
    <Show when={tab() === 'Diff'}>
      <pre class="diff block">{'  "instructions": "Review supplied task and retained files.'}<span class="add">{' Report findings with file, line, severity (blocker|major|nit).'}</span>{'",\n  "tools": ["Read", "Glob", '}<span class="del">{'"Grep"]'}</span><span class="add">{'"Grep", "Bash(read-only)"]'}</span></pre>
      <div><Button label="Compare in Lab" icon="chart-no-axes-column" /></div>
    </Show>
    <Show when={tab() === 'Compatibility'}><DetailSection title="Checked combinations">
      <For each={[['ready', 'claude 2.1.0 · sonnet · mac, pds18', 'at 4e5bf83'], ['ready', 'claude 2.0.4 · sonnet · windows', 'at 0c1d7aa'], ['unchecked', 'claude 2.1.0 · sonnet · draft', 'tools changed'], ['incompatible', 'claude 1.9.8 · sonnet · windows', 'this version does not know the Bash sandbox flag']] as const}>{r =>
        <div class="kv-row"><StateBadge family="definition" state={r[0]} compact /><span class="mono">{r[1]}</span><span class="muted">{r[2]}</span></div>}</For>
      <div><Button label="Check now" icon="play" /></div>
    </DetailSection></Show>
    <Show when={tab() === 'Usage'}>
      <DetailSection title="Used by">{kvs([
        ['network', 'issue-to-pr', 'pipeline · step review-code'],
        ['network', 'website-page', 'pipeline · step review'],
        ['rows-3', 'code-review', 'catalogue method · recommended for cs, fleet'],
      ])}</DetailSection>
      <Totals items={[{ label: 'Executions, 30 days', m: m(58), kind: 'count', note: '3 failed' }, { label: 'Median time', m: m(250), kind: 'duration' }, { label: 'Cost', m: m(41.2, 'estimated', 'tokens'), kind: 'money' }]} />
    </Show>
  </DefinitionEditor>}</Show> : undefined} />;
}

export default function Scene() {
  return <Agents />;
}
