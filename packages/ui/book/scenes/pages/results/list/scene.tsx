import { createMemo, createSignal } from 'solid-js';
import { StateBadge, Num, AgeChip, ResultViewer, EntityCode, Button, Empty, VirtualList, StateCounters, FilterBar, ListDetail, ListFrame } from '../../../../../src/index.ts';
import { PageHead, Panel, m } from '../../../../scene-parts.tsx';
import tokens from '../../../../../tokens.json';
const ROW = parseInt(tokens.size.row);
import { RESULTS, type Res } from "./fixture.ts";

function Results() {
  const [sel, setSel] = createSignal<Res>(RESULTS[0]);
  const [tab, setTab] = createSignal('View');
  const [query, setQuery] = createSignal('');
  const [copies, setCopies] = createSignal<Res['copy'][]>([]);
  const counts = { stored: 0, placed: 0, pending: 0, unavailable: 0 } as Record<Res['copy'], number>;
  for (const r of RESULTS) counts[r.copy]++;
  const shown = createMemo(() => {
    const c = copies(), q = query();
    return RESULTS.filter(r => (!c.length || c.includes(r.copy)) && (!q || r.name.includes(q) || `t-${r.task}`.includes(q.toLowerCase())));
  });
  return <ListDetail
    list={<>
      <PageHead title="Results" />
      <Panel><ListFrame bar={<FilterBar query={query()} onQuery={setQuery} placeholder="Search results"
          shown={shown().length} total={RESULTS.length}
          counters={<StateCounters family="copy" label="Results by copy state" counts={counts} active={copies()} onAll={() => setCopies([])} onToggle={c => setCopies(copies().includes(c) ? copies().filter(x => x !== c) : [...copies(), c])} />} />}>
        <VirtualList items={shown()} getKey={r => r.id} rowHeight={ROW} follow="top" label="Results" rowClass={r => r.id === sel().id ? 'sel' : ''}
          columns="1.25rem minmax(0,1fr) 4rem 4.5rem 2.75rem"
          header={<><span /><span>Result</span><span>Project</span><span class="r">Size</span><span class="r">Age</span></>}
          render={r => <>
            <span class="lead"><StateBadge family="copy" state={r.copy} compact /></span>
            <span class="tt"><a class="title row-link" href={`#/results/${r.id}`} onClick={e => { e.preventDefault(); setSel(r); }}>{r.name}</a><EntityCode code={`T-FLT-${r.task}`} id={`00000000-0000-4000-8000-${String(r.task).padStart(12, '0')}`} /></span>
            <span class="svc">{r.project}</span>
            <span class="r"><Num m={{ value: r.size, basis: 'measured' }} kind="bytes" /></span>
            <span class="r"><AgeChip t={r.at} /></span>
          </>} />
      </ListFrame></Panel>
    </>}
    detailKey={sel().id}
    detail={<article class="detail" data-ui="detail" tabindex={-1} aria-label={sel().name}>
      <ResultViewer name={sel().name} type={sel().type} size={sel().size} source={`T-${sel().task}`} tab={tab()} onTab={setTab} tabs={['View', 'Checks 2', 'Versions 3']}
        copies={[{ where: 'windows', state: sel().copy }, { where: 'mac', state: 'pending' }]}>
        {sel().copy === 'unavailable' ? <Empty text="No holding store is online. Last copy on mac, seen 2h ago." action={<Button label="Notify when online" icon="bell" />} /> : <Empty text={`${sel().type} viewer for ${sel().name}`} />}
      </ResultViewer>
    </article>} />;
}

export default function Scene() {
  return <Results />;
}
