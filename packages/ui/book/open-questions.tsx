// Open questions 01: visual variants for the owner to pick by looking. Built only from packages/ui.
import { render, type ComponentChildren } from 'preact';
import { useState } from 'preact/hooks';
import {
  Icon, Button, StateBadge, Num, AgeChip, FillBar, SettingField, NavRail, TooltipLayer, GroupHeader, SplitButton,
  type IconName,
} from '../src/index.ts';
import { NAV } from './nav.ts';
import { NOW, hr, hosts, tasks, type Task } from './fixtures.ts';

const T = tasks(8, 9989);
const PRI = (t: Task) => (t.id % 5 === 0 ? 0 : t.id % 2 ? 1 : 2);

function Variant({ id, title, note, children, phone }: { id: string; title: string; note: string; children: ComponentChildren; phone?: boolean }) {
  return <figure class={`variant${phone ? ' phone' : ''}`} aria-labelledby={`${id}-t`}>
    <figcaption><b id={`${id}-t`}>{id}</b> {title}<p>{note}</p></figcaption>
    <div class="variant-body">{children}</div>
  </figure>;
}
function Question({ id, title, why, children }: { id: string; title: string; why: string; children: ComponentChildren }) {
  return <section class="q" id={id}><h2>{title}</h2><p class="muted">{why}</p><div class="variants">{children}</div></section>;
}

const Row = ({ cols, children, cls }: { cols: string; children: ComponentChildren; cls?: string }) =>
  <div role="row" class={`vl-row static ${cls ?? ''}`} style={{ '--cols': cols, position: 'relative' }}>{children}</div>;
const Head = ({ cols, children }: { cols: string; children: ComponentChildren }) => <div class="vl-head" role="row" style={{ '--cols': cols }}>{children}</div>;

/* (a) priority */
const PriChip = ({ p }: { p: number }) => <span class={`chip-age${p === 0 ? ' t-danger alert' : ''}`} aria-label={`priority ${p}`}>p{p}</span>;
function A1() {
  const cols = 'minmax(0,1fr) 2rem 4.5rem 1.25rem 2.75rem';
  const rows = [...T].sort((a, b) => PRI(a) - PRI(b));
  return <div class="pane"><Head cols={cols}><span>Task</span><span>Pri</span><span>ID</span><span /><span class="r">Age</span></Head>
    {rows.map(t => <Row key={t.id} cols={cols}><span class="title">{t.title}</span><PriChip p={PRI(t)} /><span class="id svc">T-{t.id}</span><StateBadge family="task" state={t.state} compact /><span class="r"><AgeChip t={t.started} now={NOW} /></span></Row>)}</div>;
}
function A2() {
  const cols = 'minmax(0,1fr) 4.5rem 1.25rem 2.75rem';
  return <div class="pane">{[0, 1, 2].map(p => { const g = T.filter(t => PRI(t) === p); return <div key={p}>
    <Row cols="1fr" cls="grp"><GroupHeader icon={p === 0 ? 'triangle-alert' : 'list-ordered'} tone={p === 0 ? 'danger' : 'neutral'} label={`Priority ${p}`} count={g.length} open onToggle={() => {}} /></Row>
    {g.map(t => <Row key={t.id} cols={cols} cls={p === 0 ? 'p0' : ''}><span class="title">{t.title}</span><span class="id svc">T-{t.id}</span><StateBadge family="task" state={t.state} compact /><span class="r"><AgeChip t={t.started} now={NOW} /></span></Row>)}</div>; })}</div>;
}
function A3() {
  const [order, setOrder] = useState(T.slice(0, 6));
  const cols = '1.75rem 1.5rem minmax(0,1fr) 4.5rem 2.75rem';
  const move = (i: number, d: number) => { const o = [...order]; const [x] = o.splice(i, 1); o.splice(Math.max(0, Math.min(o.length, i + d)), 0, x); setOrder(o); };
  return <div class="pane"><Row cols="1fr" cls="grp"><GroupHeader icon="list-ordered" label="Queued, in launch order" count={order.length} open onToggle={() => {}} /></Row>
    {order.map((t, i) => <Row key={t.id} cols={cols}><Button label="Drag to reorder; Alt+Up or Alt+Down" icon="grip" iconOnly /><span class="mono muted">{i + 1}</span><span class="title">{t.title}</span><span class="id svc">T-{t.id}</span>
      <span class="r"><Button label="Move to top" icon="list-ordered" iconOnly onClick={() => move(i, -i)} /></span></Row>)}</div>;
}

/* (b) outcome and inputs */
const OUTCOME = 'Release notes for 0.4 in releases/0.4.md, reviewed, linked from the changelog.';
const REQS = [['Covers every merged PR since 0.3', true], ['Reviewed by the reviewer role', false], ['Linked from CHANGELOG.md', false]] as const;
const INPUTS: [IconName, string][] = [['file-text', 'releases/template.md'], ['git-pull-request', '14 merged PRs'], ['messages-square', 'PM session 12 Nov']];
const Inputs = () => <span class="chips">{INPUTS.map(([i, l]) => <span key={l} class="chip"><Icon name={i} />{l}</span>)}</span>;
function Detail({ children, tabs, tab, setTab }: { children: ComponentChildren; tabs: string[]; tab: string; setTab: (t: string) => void }) {
  return <article class="detail mini">
    <div class="detail-h"><span class="muted mono">fleet · T-9989</span><span class="grow" /><SplitButton main={{ label: 'Pause', icon: 'pause' }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash' }]} /></div>
    <h2 class="detail-title">Write release notes 95</h2>
    <div class="state-line"><StateBadge family="task" state="waiting" reason="acceptance by you" /><AgeChip t={NOW - 9 * hr} now={NOW} /></div>
    {children}
    <div class="tabs detail-tabs" role="tablist">{tabs.map(x => <button type="button" role="tab" key={x} aria-selected={tab === x} onClick={() => setTab(x)}>{x}</button>)}</div>
  </article>;
}
function B1() {
  const [tab, setTab] = useState('Outcome');
  return <Detail tabs={['Outcome', 'Executions', 'Transcript', 'Results']} tab={tab} setTab={setTab}>{null}</Detail>;
}
function B1Body() {
  return <div class="outcome"><p>{OUTCOME}</p><ul class="reqs">{REQS.map(([r, ok]) => <li key={r}><Icon name={ok ? 'circle-check' : 'square'} />{r}</li>)}</ul><Inputs /><p class="muted type-sm">Depends on T-9976 (completed)</p></div>;
}
function B2() {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState('Executions');
  return <Detail tabs={['Executions', 'Transcript', 'Results']} tab={tab} setTab={setTab}>
    <div class="outcome compact">
      <button type="button" class="tw" aria-expanded={open} aria-label={open ? 'Collapse outcome' : 'Expand outcome'} onClick={() => setOpen(!open)}><span class={open ? 'chev open' : 'chev'}><Icon name="chevron-down" /></span></button>
      <div><p class={open ? '' : 'clamp'}>{OUTCOME}</p>
        <span class="row"><span class="chip-age">1/3</span><span class="muted type-sm">requirements met</span><Inputs /></span>
        {open && <ul class="reqs">{REQS.map(([r, ok]) => <li key={r}><Icon name={ok ? 'circle-check' : 'square'} />{r}</li>)}</ul>}</div>
    </div>
  </Detail>;
}

/* (c) host detail */
const h = hosts[1];
const ENVS = [['game-worktree-3', 'codex · T-9976', 4.2e9], ['design-main', 'idle', 1.1e9]] as const;
const ACCTS = [['fleet-codex', 'codex-pro'], ['fleet-claude', 'claude-max']] as const;
const HARN = [['codex', '0.48.1', 'ready'], ['claude', '2.1.0', 'ready'], ['agy', '0.9.2', 'unchecked']] as const;
const HostSections = ({ only }: { only?: string }) => <>
  {(!only || only === 'Environments') && <div class="sub"><h3>Environments</h3>{ENVS.map(([n, u, b]) => <div key={n} class="kv-row"><Icon name="folder-open" /><b>{n}</b><span class="muted">{u}</span><span class="grow" /><Num m={{ value: b, basis: 'measured' }} kind="bytes" /></div>)}</div>}
  {(!only || only === 'Accounts') && <div class="sub"><h3>Account users</h3>{ACCTS.map(([u, a]) => <div key={u} class="kv-row"><Icon name="bot" /><b>{u}</b><span class="muted">{a}</span></div>)}</div>}
  {(!only || only === 'Harnesses') && <div class="sub"><h3>Harnesses</h3>{HARN.map(([n, v, s]) => <div key={n} class="kv-row"><Icon name={s === 'ready' ? 'circle-check' : 'circle-question-mark'} /><b>{n}</b><span class="mono muted">{v}</span><span class="muted">{s}</span></div>)}</div>}
  {(!only || only === 'Store') && <div class="sub"><h3>Store</h3><div class="kv-row"><FillBar total={h.store.total} segments={h.store.segments} warnAt={h.store.warnAt} /></div><div class="kv-row"><Icon name="shield" /><b>Tunnel</b><span class="muted">connected · 12m</span></div>
    <SettingField label="Capacity" value={h.capacity.value} help="Garage layout capacity." now={NOW} status={h.capacity.status} /></div>}
</>;
function C1() {
  const [tab, setTab] = useState('Environments');
  return <article class="detail mini"><h2 class="detail-title">windows <span class="id">windows</span></h2>
    <div class="tabs detail-tabs" role="tablist">{['Environments', 'Accounts', 'Harnesses', 'Store'].map(x => <button type="button" role="tab" key={x} aria-selected={tab === x} onClick={() => setTab(x)}>{x}</button>)}</div>
    <HostSections only={tab} /></article>;
}
function C2() {
  const [open, setOpen] = useState('windows');
  const cols = '1.25rem minmax(0,1fr) 1.25rem 3.5rem minmax(0,9rem)';
  return <div class="pane">{hosts.map(x => <div key={x.id}>
    <Row cols={cols} cls={open === x.id ? 'sel' : ''}><button type="button" class="tw" aria-expanded={open === x.id} aria-label="Toggle details" onClick={() => setOpen(open === x.id ? '' : x.id)}><span class={open === x.id ? 'chev open' : 'chev'}><Icon name="chevron-down" /></span></button>
      <span class="title">{x.id}</span><StateBadge family="host" state={x.online ? 'online' : 'offline'} compact /><span class="r mono">{x.running}/{x.slots}</span>
      <FillBar total={x.store.total} segments={x.store.segments} warnAt={x.store.warnAt} /></Row>
    {open === x.id && <div class="expanded"><HostSections /></div>}</div>)}</div>;
}
function C3() {
  return <article class="detail mini"><h2 class="detail-title">windows <span class="id">windows</span></h2><HostSections /></article>;
}

/* (d) phone navigation */
function PhoneFrame({ children, bottom }: { children: ComponentChildren; bottom?: ComponentChildren }) {
  return <div class="phone-frame"><header class="topbar"><b class="brand">Fleet</b><span class="grow" /><span class="nav-count t-attention">3</span></header>{children}{bottom}</div>;
}
const PhoneList = () => <div class="phone-list">{T.slice(0, 7).map(t => <div key={t.id} class="phone-row"><span class="title">{t.title}</span><span class="row"><span class="id">T-{t.id}</span><StateBadge family="task" state={t.state} compact /><AgeChip t={t.started} now={NOW} /></span></div>)}</div>;
function D1() {
  const [pinned, setPinned] = useState(['tasks', 'hosts']);
  return <PhoneFrame><div class="phone-body force-narrow"><NavRail items={NAV} pinned={pinned} current="tasks" onGo={() => {}} onPins={setPinned} /><PhoneList /></div></PhoneFrame>;
}
function D2() {
  const pins = NAV.filter(n => ['overview', 'tasks', 'hosts', 'results'].includes(n.id));
  return <PhoneFrame bottom={<nav class="bottom-bar" aria-label="Pinned sections">{pins.map(n => <a key={n.id} href="#d2" class={n.id === 'tasks' ? 'cur' : ''} aria-label={n.label}><Icon name={n.icon} /><span>{n.label}</span>{n.count && <span class={`nav-count t-${n.count.tone}`}>{n.count.n}</span>}</a>)}
    <a href="#d2" aria-label="All sections"><Icon name="ellipsis" /><span>Sections</span></a></nav>}><PhoneList /></PhoneFrame>;
}
function D3() {
  const [open, setOpen] = useState(true);
  const [pinned, setPinned] = useState(['tasks', 'hosts']);
  return <PhoneFrame><button type="button" class="switcher" aria-expanded={open} onClick={() => setOpen(!open)}><Icon name="list" /><b>Tasks</b><span class="nav-count t-danger">2</span><span class="grow" /><span class={open ? 'chev open' : 'chev'}><Icon name="chevron-down" /></span></button>
    {open && <div class="inline-rail"><NavRail items={NAV} pinned={pinned} current="tasks" onGo={() => setOpen(false)} onPins={setPinned} /></div>}
    <PhoneList /></PhoneFrame>;
}

function Page() {
  return <div class="page">
    <header class="bk-head"><h1>Open questions 01</h1><p class="muted">Pick one variant per question. All variants use packages/ui and follow OR-1 to OR-21. Synthetic data.</p></header>
    <Question id="a" title="(a) Task priority" why="The inventory needs priority (change priority on Overview and Tasks). Where does it live in the task list?">
      <Variant id="A1" title="Priority chip in the service zone" note="p0, p1, p2 chip next to the ID; p0 red (OR-18). Lists sort by priority inside each state group."><A1 /></Variant>
      <Variant id="A2" title="Group by priority" note="Priority becomes a grouping option; P0 rows get a red edge. No extra column."><A2 /></Variant>
      <Variant id="A3" title="Launch order in the queue" note="Priority is the order of queued work: drag (or Alt+Up/Down) and 'Move to top'. Rank numbers, no levels."><A3 /></Variant>
    </Question>
    <Question id="b" title="(b) Task outcome and inputs" why="Task detail lacks expected outcome, requirements, inputs and dependencies.">
      <Variant id="B1" title="Outcome tab, first" note="A first tab holds outcome, requirement checklist, inputs and dependencies. Detail head stays short."><B1 /><B1Body /></Variant>
      <Variant id="B2" title="Outcome block above the tabs" note="Two clamped lines, requirements met as 1/3, inputs as chips; expands inline. Always visible (OR-19)."><B2 /></Variant>
    </Question>
    <Question id="c" title="(c) Host detail" why="Environments, account users, harness versions and tunnel state per host.">
      <Variant id="C1" title="Tabs in a host detail panel" note="List and detail like Tasks; one tab per area."><C1 /></Variant>
      <Variant id="C2" title="Rows expand inline" note="The host table row expands in place to show all areas; no detail panel."><C2 /></Variant>
      <Variant id="C3" title="Stacked sections, no tabs" note="Detail panel shows every area at once, top to bottom."><C3 /></Variant>
    </Question>
    <Question id="d" title="(d) Phone navigation" why="Below 760 px the full rail does not fit. Currently (interim) it shrinks to icons.">
      <Variant id="D1" title="Icon rail (current interim)" note="Same rail, icons only, pinned first, counters on icons. Names in tooltips." phone><D1 /></Variant>
      <Variant id="D2" title="Bottom bar of pinned sections" note="Pinned sections become a bottom bar with names; 'Sections' opens the full list as its own route." phone><D2 /></Variant>
      <Variant id="D3" title="Section switcher, rail inline" note="Top line names the current section; tapping expands the full rail inline, pushing content down (no popup, OR-14)." phone><D3 /></Variant>
    </Question>
    <TooltipLayer />
  </div>;
}

render(<Page />, document.getElementById('app')!);
