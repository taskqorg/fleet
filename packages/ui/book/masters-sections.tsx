// rename State counters: Text 75 -> total
// rename State counters: Text 76 -> working
// rename State counters: Text 77 -> waiting
// rename State counters: Text 78 -> queued
// rename State counters: Text 79 -> failed
// rename Share bar: Text 83 -> bar 1
// rename Share bar: Text 84 -> bar 2
// rename Share bar: Text 85 -> bar 1
// rename Share bar: Text 86 -> bar 2
// Board masters: the sections blocks (masters.tsx).
import type { JSX } from '@solidjs/web';
import {
  CatalogueTree, SearchBox, Presets, StateCounters, FilterBar, GroupControl, SortControl, TabBar, DetailHead, Button, StateDot,
  KvRow, DetailSection, TextField, SettingField, DataTable, TableRow, TableName, DiffRow, InheritedRows, ShareBar, StepList,
  MatrixCell, ProblemRow, type CatalogueTreeNode, type StepItem,
} from '../src/index.ts';
import { NOW } from './fixtures.ts';
import { slot, type MasterSpec } from './masters-spec.ts';

const noop = () => {};
const m = (value: number) => ({ value, basis: 'measured' as const });
const usd = (value: number) => ({ value, basis: 'measured' as const, currency: 'USD' });
const none = { value: null, basis: 'unavailable' as const, reason: 'not started' };

/** Names the texts a control derives where they differ between a master's states (so one `texts` list cannot name them):
 *  after mount, each [selector, ...names] joins the matched element's adjacent texts and names its texts in document
 *  order (texts already marked by slot() are skipped). `click` presses an element first (a state the control enters on a
 *  click). The wrapper has no box, so the spec picks the control inside it. */
/** Joins each run of adjacent texts (Solid's markers between them included) into its first text. */
function merge(el: Element) {
  let first: Text | null = null;
  for (const n of [...el.childNodes]) {
    if (n.nodeType === 3) { if (first) { first.textContent += n.textContent ?? ''; n.textContent = ''; } else first = n as Text; }
    else if (n.nodeType === 8) continue;
    else { first = null; merge(n as Element); }
  }
}

function Named(p: { marks?: [string, ...string[]][]; click?: string; children: JSX.Element }) {
  const run = (el: HTMLElement) => {
    for (const [sel, ...names] of p.marks ?? []) {
      const t = el.querySelector(sel);
      if (!t) continue;
      merge(t);
      const walk = document.createTreeWalker(t, NodeFilter.SHOW_TEXT);
      let i = 0;
      for (let n = walk.nextNode(); n && i < names.length; n = walk.nextNode()) {
        const s = n.textContent ?? '';
        if (s.trim() && !s.includes('')) n.textContent = slot(names[i++], s);
      }
    }
  };
  return <div style={{ display: 'contents' }} ref={el => setTimeout(() => {
    if (p.click) el.querySelector<HTMLElement>(p.click)?.click();
    setTimeout(() => run(el), 30);
  }, 30)}>{p.children}</div>;
}

const facts = () => <><span class="mono">{slot('fact', '2 runs')}</span><span class="mono">{slot('age', '12m')}</span></>;
const LIFE_COUNTS = { working: 12, waiting: 3, queued: 31, failed: 2 };
const COUNTER_TEXTS = ['sigma', 'total', 'working', 'waiting', 'queued', 'failed'];

type Step = Extract<StepItem, { id: string }>;
const STEP: Record<string, Step> = {
  definition: { id: slot('name', 'Write the plan'), kind: 'agent', executor: slot('executor', 'planner · opus-5.5'), reads: 'task', writes: 'plan.md' },
  pending: { id: slot('name', 'Render the board'), kind: 'tool', executor: slot('executor', 'render_asset'), reads: '', writes: '',
    run: { state: 'waiting', note: 'waits for plan', time: none, cost: none } },
  running: { id: slot('name', 'Render the board'), kind: 'tool', executor: slot('executor', 'render_asset'), reads: '', writes: '',
    run: { state: 'running', note: '3 of 8', time: m(100), cost: none } },
  done: { id: slot('name', 'Write the plan'), kind: 'agent', executor: slot('executor', 'planner · opus-5.5'), reads: '', writes: '',
    run: { state: 'succeeded', note: 'reused', time: m(42), cost: usd(0.12) } },
  failed: { id: slot('name', 'Export PNGs'), kind: 'program', executor: slot('executor', 'export.sh'), reads: '', writes: '',
    run: { state: 'failed', time: m(8), cost: none, error: slot('error', 'export: canvas not found: screens/lab') } },
};

const SHARE = [{ label: 'PR checks', v: 41, tone: 'd1' as const }, { label: 'Queue', v: 24, tone: 'd2' as const }, { label: 'Release', v: 8, tone: 'd3' as const }, { label: 'Install', v: 9, tone: 'd4' as const }];
const SHARE_LEGEND = SHARE.flatMap((_, i) => [`part ${i + 1}`, `part ${i + 1} value`, `part ${i + 1} share`]);

const MATRIX: Record<string, Parameters<typeof MatrixCell>[0]> = {
  pass: { state: 'pass', label: 'Example 3, variant codeMode', score: 0.91, k: 3, n: 3, time: m(252), cost: usd(1.84), confounders: 'pds18-native · claude · opus-5.5', onOpen: noop },
  partial: { state: 'partial', label: 'Example 3, variant baseline', score: 0.64, k: 2, n: 3, note: '1 repeat left out: host changed', onOpen: noop },
  fail: { state: 'fail', label: 'Example 4, variant codeMode', k: 0, n: 3, note: 'timeout at step 4/6', onOpen: noop, onRetry: noop },
  running: { state: 'running', label: 'Example 4, variant baseline', k: 1, n: 3, step: '3/6', time: m(125), onOpen: noop },
  excluded: { state: 'excluded', label: 'Example 5, variant toolMode', k: 0, n: 3, onOpen: noop },
};

const SETTING = {
  applied: { value: '500 GB', help: 'Warns below 25 GB free. Changed by Alex, 10:40.', status: { kind: 'applied', at: NOW - 2 * 3600_000 } },
  editing: { value: '750 GB', help: 'Warns below 25 GB free.', status: { kind: 'applied', at: NOW - 2 * 3600_000 } },
  pending: { value: '750 GB', help: 'Saved; waits for the host to apply it.', status: { kind: 'pending' } },
  error: { value: '750 GB', help: 'Host: not enough free disk for 750 GB.', status: { kind: 'apply_error', error: 'not enough free disk' } },
} as const;

const tableHead = (cls?: string) => <tr class={cls}>
  <th scope="col">{slot('name', 'Variant')}</th><th scope="col" class="num">{slot('c1', 'Score')}</th>
  <th scope="col" class="num">{slot('c2', 'Time')}</th><th scope="col" class="num">{slot('c3', 'Cost')}</th>
</tr>;

export const MASTERS_SECTIONS: MasterSpec[] = [
  { name: 'Catalogue row', axes: { state: ['rest', 'selected', 'pinned'] }, w: 520, pick: '.vl-row:has([data-node-id="row"])',
    render: v => {
      const nodes: CatalogueTreeNode[] = [{ id: 'row', label: slot('title', 'Fix receipt factory'), sub: slot('code', 'T-FLT-51'),
        state: { family: 'life', state: v.state === 'pinned' ? 'waiting' : 'working' }, right: facts(), pinned: v.state === 'pinned' }];
      return <CatalogueTree label="Sessions" nodes={nodes} selectedId={v.state === 'selected' ? 'row' : null} onSelect={noop} onPin={noop} />;
    } },
  { name: 'List group', axes: { state: ['open', 'folded'] }, w: 520, pick: '.vl-row:has([data-node-id="g"])', texts: ['count'],
    render: v => {
      const open = v.state === 'open';
      const nodes: CatalogueTreeNode[] = [{ id: 'g', label: slot('title', open ? 'Running' : 'Done'), count: open ? 12 : 230, defaultCollapsed: !open,
        children: [{ id: 'leaf', label: 'Fix receipt factory', sub: 'T-FLT-51', state: { family: 'life', state: 'working' } }] }];
      return <CatalogueTree label="Sessions" nodes={nodes} onSelect={noop} />;
    } },
  { name: 'Search', axes: { state: ['icon', 'open'] }, w: 220, texts: ['text'],
    render: v => <SearchBox label="Search" value={v.state === 'open' ? 'agent:claude host:mac' : ''} onInput={noop} /> },
  { name: 'View chip', axes: { state: ['default', 'active'] }, pick: '.preset',
    render: v => {
      const name = slot('label', v.state === 'active' ? 'Needs me' : 'Running');
      return <Presets system={[name]} presets={[]} active={v.state === 'active' ? name : null} onPick={noop} onSave={noop} onUnpin={noop} />;
    } },
  { name: 'State counters', texts: COUNTER_TEXTS,
    render: () => <StateCounters family="life" label="Sessions by state" counts={LIFE_COUNTS} active={[]} onToggle={noop} onAll={noop} hideZero /> },
  { name: 'Filter bar', w: 520, texts: ['count', ...COUNTER_TEXTS],
    render: () => <FilterBar query="" onQuery={noop} placeholder="Search sessions" shown={84} total={1240}
      extra={<Presets system={[slot('view 1', 'Needs me'), slot('view 2', 'Running')]} presets={[]} active={slot('view 1', 'Needs me')} onPick={noop} onSave={noop} onUnpin={noop} />}
      counters={<StateCounters family="life" label="Sessions by state" counts={LIFE_COUNTS} active={[]} onToggle={noop} onAll={noop} hideZero />}
      tools={<><GroupControl value="none" none="none" options={[['none', 'None'], ['state', 'State'], ['project', 'Project']]} onChange={noop} />
        <SortControl value="-age" fields={[['age', slot('sort', 'Age')]]} onChange={noop} /></>} /> },
  { name: 'Tab', axes: { state: ['default', 'current'] }, pick: '[role=tab]',
    render: v => <TabBar label="Facets" value={v.state === 'current' ? 'a' : 'b'} onChange={noop}
      tabs={[{ id: 'a', label: slot('label', v.state === 'current' ? 'Sessions' : 'Tasks') }, { id: 'b', label: 'Delivery' }]} /> },
  { name: 'Tab bar', w: 800, pick: '.tabs',
    render: () => <TabBar label="Project facets" value="sessions" onChange={noop}
      tabs={[{ id: 'sessions', label: slot('sessions', 'Sessions') }, { id: 'tasks', label: slot('tasks', 'Tasks') }, { id: 'delivery', label: slot('delivery', 'Delivery') },
        { id: 'results', label: slot('results', 'Results') }, { id: 'rules', label: slot('rules', 'Rules & methods') }]} /> },
  { name: 'Detail head', w: 880,
    render: () => <DetailHead crumb={slot('code', 'fleet · T-FLT-51')} title={slot('title', 'Fix receipt factory')} onClose={noop}
      state={<><StateDot state="working" /> {slot('reason', 'step 2 of 3 · taken 12m ago')}</>}
      actions={<><Button label="Pause" kind="primary" /><Button label="Share" icon="link" iconOnly kind="quiet" /></>} /> },
  { name: 'Key row', w: 560,
    render: () => <KvRow icon="network" label={slot('name', 'pds18-native')} meta={slot('meta', 'linux · 8 slots')} value={<span class="mono">{slot('value', '44 runs')}</span>} /> },
  { name: 'Detail section', w: 560,
    render: () => <DetailSection title={slot('title', 'Harnesses')}>
      <KvRow icon="network" label={slot('name', 'pds18-native')} meta={slot('meta', 'linux · 8 slots')} value={<span class="mono">{slot('value', '44 runs')}</span>} />
      <KvRow icon="network" label={slot('name 2', 'owner-mac')} meta={slot('meta 2', 'darwin · 4 slots')} value={<span class="mono">{slot('value 2', '30 runs')}</span>} />
    </DetailSection> },
  { name: 'Text field', axes: { state: ['editable', 'invalid', 'disabled'] }, w: 320, texts: ['value'],
    render: v => <TextField label={slot('label', 'Experiment name')} value="codeMode on 3 variants" onChange={noop}
      help={v.state === 'editable' ? slot('help', 'Shown in Lab runs.') : undefined}
      error={v.state === 'invalid' ? slot('help', 'A run with this name exists.') : undefined}
      disabledReason={v.state === 'disabled' ? slot('help', 'Read only: the run has started.') : undefined} /> },
  { name: 'Setting field', axes: { state: ['applied', 'editing', 'pending', 'error'] }, w: 420, pick: '.setting', texts: ['value'],
    render: v => {
      const s = SETTING[v.state as keyof typeof SETTING];
      return <Named click={v.state === 'editing' ? '.setting button.value' : undefined}>
        <SettingField label={slot('label', 'Store capacity')} value={v.state === 'editing' ? s.value : slot('value', s.value)} help={slot('help', s.help)} now={NOW} status={s.status} onSave={noop} />
      </Named>;
    } },
  { name: 'Table row', axes: { kind: ['header', 'row', 'selected'] }, w: 640, pick: ':is(tr.m-head, tbody tr)',
    render: v => v.kind === 'header'
      ? <DataTable label="Leaderboard" head={tableHead('m-head')}>{[]}</DataTable>
      : <DataTable label="Leaderboard" head={tableHead()}>
        <TableRow selected={v.kind === 'selected'} onOpen={noop}>
          <TableName label={slot('name', 'codeMode · opus-5.5')} current={v.kind === 'selected'} onOpen={noop} />
          <td class="num">{slot('c1', '0.83')}</td><td class="num">{slot('c2', '4m 12s')}</td><td class="num">{slot('c3', '$1.84')}</td>
        </TableRow>
      </DataTable> },
  { name: 'Diff row', axes: { state: ['changed', 'inherited'] }, w: 560, pick: ':is(.diff-row, .diff-inherited)',
    render: v => v.state === 'changed'
      ? <DiffRow field={slot('field', 'model')} value={slot('value', 'opus-5.5')} base="sonnet-5.5" />
      : <Named marks={[['.dr-fold', 'label']]}><InheritedRows count={14}><DiffRow field="harness" value="claude" base="claude" /></InheritedRows></Named> },
  { name: 'Share bar', axes: { state: ['normal', 'partial'] }, w: 480, pick: '.sb',
    render: v => <Named marks={[['.sb-bar', ...SHARE.map((_, i) => `bar ${i + 1}`)], ['.sb-legend', ...SHARE_LEGEND]]}>
      <ShareBar label="Time per pull request" parts={SHARE} value={x => `${x}m`} partial={v.state === 'partial'}
        note={slot('note', v.state === 'partial' ? '12 of 389 measured' : 'mean per pull request, 76 of 389 measured')} />
    </Named> },
  { name: 'Step row', axes: { state: ['definition', 'pending', 'running', 'done', 'failed'] }, w: 560, pick: '.step-item',
    render: v => <Named marks={[['.no', 'number'], ['.io', 'io', 'writes'], ['.chip-age', 'note'], ['.svc > :nth-last-child(2)', 'time'], ['.svc > :last-child', 'cost']]}>
      <StepList label="Steps" items={[STEP[v.state]]} />
    </Named> },
  { name: 'Matrix cell', axes: { state: ['pass', 'partial', 'fail', 'running', 'excluded'] }, w: 200, pick: '.mcell',
    render: v => <Named marks={[['.mc-head', 'score'], ['.mc-k', 'repeats'], ['.mc-l2', 'meta', 'meta 2', 'meta 3']]}>
      <MatrixCell {...MATRIX[v.state]} />
    </Named> },
  { name: 'Problem row', axes: { tone: ['attention', 'danger'] }, w: 720, pick: '.problem',
    render: v => <Named marks={[['.pr-n', 'count']]}>{v.tone === 'attention'
      ? <ProblemRow severity="warning" title={slot('title', 'Low space on windows store')} facts={slot('facts', '18 GB free, warning at 25 GB · 4 tasks waiting')} onDetails={noop}
        actions={[{ label: 'Raise capacity', primary: true }, { label: 'Change location' }, { label: 'Add retention rule' }]} />
      : <ProblemRow severity="failure" title={slot('title', 'Codex cannot start on pds18-native')} count={22} facts={slot('facts', 'coding-tool-unready (browser) since 18:40 · runs continue on claude')} onDetails={noop}
        actions={[{ label: 'Reinstall browser tool', primary: true }, { label: 'Pause Codex here' }, { label: 'Open host' }]} />}</Named> },
];
