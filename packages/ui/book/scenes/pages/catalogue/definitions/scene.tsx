import { createSignal, For, Show } from 'solid-js';
import { StateBadge, Num, Button, Empty, ListDetail, DefinitionEditor, DetailSection } from '../../../../../src/index.ts';
import { PageHead, Panel, m, kvs, CatRow, CAT_COLS } from '../../../../scene-parts.tsx';
import { CAPS } from "./fixture.ts";

function Catalogue() {
  const [sel, setSel] = createSignal<string | null>('web-research');
  const [tab, setTab] = createSignal('Methods');
  const list = <>
    <PageHead title="Catalogue" />
    <Panel><div class="vl-wrap" style={CAT_COLS}>
      <For each={CAPS}>{c => <CatRow id={c.id} sel={c.id === sel()} onOpen={() => setSel(c.id)} state={<StateBadge family="definition" state={c.state} compact />} right={c.right}><span class="facts">{c.facts}</span></CatRow>}</For>
    </div></Panel>
  </>;
  return <ListDetail list={list} detailKey={sel() ?? undefined} detail={sel() ? <Show when={sel()} keyed>{s => <DefinitionEditor label={s} crumb="shared · config/catalogue.json · 5eefade" title={s}
    tabs={['Methods', 'Contract', 'Assessments']} tab={tab()} onTab={setTab} onClose={() => setSel(null)} actions={<Button label="Add method" icon="plus" />}>
    <Show when={tab() === 'Methods'}>
      <DetailSection title="What the caller gets">{kvs([
        ['arrow-down', 'Input', <span class="mono">prompt · max sources</span>],
        ['arrow-up', 'Result', <span class="mono">summary.md · sources · pages (optional)</span>],
      ])}</DetailSection>
      <DetailSection title="Methods">
        <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1.4fr) 4rem minmax(0,1fr) 4rem 4rem 4.5rem minmax(0,1.2fr)' }}>
          <div class="vl-head" role="row"><span /><span>Method</span><span>Scope</span><span>Runs</span><span class="r">Quality</span><span class="r">Time</span><span class="r">Cost</span><span>For</span></div>
          <For each={[
            ['ready', 'Source discovery', 'shared', 'search-basic', m(0.82), m(8), m(0.02), 'everyone', false],
            ['ready', 'Summary with sources', 'shared', 'web-research', m(0.91), m(130), m(0.38, 'estimated', 'tokens'), 'cs, fleet', false],
            ['unchecked', 'Guided delivery', 'csgo', 'prop-search', m(null, 'unavailable', 'not assessed'), m(null, 'unavailable', 'not assessed'), m(null, 'unavailable', 'not assessed'), 'nobody yet', true],
          ] as const}>{([st, title, scope, pipe, q, tm, c, forW, mute]) => <div role="row" class="vl-row static">
            <StateBadge family="definition" state={st} compact /><span class="title">{title}</span><span class="muted">{scope}</span><span class="mono" data-tip={`config/pipelines/${pipe}.json`}>{pipe}</span>
            <span class="r"><Num m={q} kind="score" /></span><span class="r"><Num m={tm} kind="duration" /></span><span class="r"><Num m={c} kind="money" /></span><span class={mute ? 'muted' : undefined}>{forW}</span></div>}</For>
        </div>
      </DetailSection>
      <DetailSection title="Which method a call uses">{kvs([
        ['list', 'The task names one', 'that method, never replaced without permission'],
        ['folder-open', 'Else the project recommends one', 'cs, fleet: Summary with sources'],
        ['rows-3', 'Else the shared default', 'Source discovery'],
      ])}</DetailSection>
      <div class="state-line"><Button label="Recommend for a project" icon="check-check" /><Button label="Compare in Lab" icon="chart-no-axes-column" /></div>
    </Show>
    <Show when={tab() !== 'Methods'}><Empty text={`${tab()} is not mocked up.`} /></Show>
  </DefinitionEditor>}</Show> : undefined} />;
}

export default function Scene() {
  return <Catalogue />;
}
