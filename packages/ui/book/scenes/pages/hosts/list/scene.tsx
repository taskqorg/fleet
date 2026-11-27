import type { SceneProps } from "../../../../scene-shell.tsx";
import { createEffect, createMemo, createSignal, For, Show } from 'solid-js';
import { AreaChart, ProblemRow, FillBar, StateBadge, Num, AgeChip, SettingField, Button, Icon, Stale, ListDetail, Choice, DetailSection, KvRow, HarnessCounts, DataTable } from '../../../../../src/index.ts';
import { NOW, min, hr } from '../../../../fixtures.ts';
import { hosts } from './fixture.ts';
import { PageHead, Panel, m, kvs } from '../../../../scene-parts.tsx';

/** Limits (pages/hosts.md "Limits", D24): the work area while no host is open. */
function LimitsMock() {
  const d = 24 * hr, from = NOW - 7 * d, to = NOW + 7 * d;
  return <section class="limits" aria-label="Limits">
    <PageHead title="Limits"><Choice label="Burn window" options={[{ id: '1', label: '1h' }, { id: '3', label: '3h' }, { id: '6', label: '6h' }]} value="3" />
      <Choice label="Chart" options={[{ id: 'left', label: 'Left' }, { id: 'burn', label: 'Burn' }]} value="left" />
      <Button label="Earlier" icon="arrow-left" iconOnly /><Button label="Today" pressed /><Button label="Later" icon="arrow-right" iconOnly /><Button label="Refresh all" icon="rotate-cw" iconOnly /></PageHead>
    <div class="limit-cards">
      <article class="panel limit-card" aria-label="codex weekly"><header class="panel-h"><h2>codex <span class="id">weekly</span></h2><span class="grow" /><span class="num">45%</span></header>
        <dl class="limit-facts"><dt>Next reset</dt><dd><time>2d</time> <span class="num">+120</span></dd><dt>First empty</dt><dd><time>1d</time></dd><dt>Pool empty</dt><dd><span class="muted">not before the line ends</span></dd><dt>Burn</dt><dd><span class="num">2.0/h</span></dd></dl>
        <AreaChart label="codex weekly left" from={from} to={to} now={NOW} max={200} value={v => `${Math.round(v / 2)}%`}
          series={[{ label: 'recorded', points: [{ at: from, v: 180 }, { at: NOW - 4 * d, v: 200 }, { at: NOW, v: 90 }], tone: 'd1' },
            { label: 'projected', points: [{ at: NOW, v: 90 }, { at: NOW + 2 * d, v: 20 }, { at: NOW + 2 * d, v: 120 }, { at: NOW + 6 * d, v: 40 }], tone: 'd1', projected: true }]} /></article>
    </div>
    <Panel title="Subscriptions"><DataTable label="Subscriptions" head={<tr><th scope="col">Subscription</th><th scope="col">5h</th><th scope="col">weekly</th><th scope="col">weekly-opus</th><th scope="col"><span class="sr-only">Refresh</span></th></tr>}>
      <For each={[
        ['alex-pro', 'codex', '', [[62, '3h', 'ahead', 4, 'execution', ''], [55, '2d', 'on', 40, 'probe', ''], null]],
        ['team-max', 'claude', '×2.5', [[91, '1h', 'Low', 2, 'execution', 't-danger'], [30, '5d', 'under', 2, 'execution', ''], [70, '5d', 'ahead', 2, 'execution', '']]],
      ] as const}>{([sub, har, mul, cells]) => <tr><th scope="row"><span class="title">{sub}</span> <span class="id">{har}</span>{mul && <span class="num muted"> {mul}</span>}</th>
        <For each={cells}>{c => c ? <td><span class="limit-cell"><FillBar total={100} segments={[{ label: 'used', bytes: c[0] }]} warnAt={20} unit="percent" /><time>{c[1]}</time><span class={`chip ${c[5] || (c[2] === 'ahead' ? 't-attention' : '')}`}>{c[5] && <Icon name="triangle-alert" />}{c[2]}</span><AgeChip t={NOW - c[3] * min} /><span class="muted">{c[4]}</span></span></td> : <td class="muted">—</td>}</For>
        <td><Button label={`Refresh ${sub}`} icon="rotate-cw" iconOnly /></td></tr>}</For>
    </DataTable></Panel>
  </section>;
}

function Hosts(props: { limits?: boolean }) {
  const [sel, setSel] = createSignal<string | null>(props.limits ? null : hosts[1].id);
  createEffect(() => props.limits, lim => { setSel(lim ? null : hosts[1].id); }, { defer: true });
  const h = createMemo(() => hosts.find(x => x.id === sel()));
  // Control column: the section's problem, then the hosts catalogue (patterns/list-detail.md). Rows are two-line; the store fill wraps under them.
  const list = <>
    <PageHead title="Hosts"><Stale at={NOW - 40e3} now={NOW} /></PageHead>
    <ProblemRow severity="warning" title="Low space on windows store" facts="18 GB free, warning at 25 GB · 4 uploads waiting"
      actions={[{ label: 'Raise capacity', primary: true }, { label: 'Change game locations' }, { label: 'Add retention rule' }]} />
    <Panel>
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
        <For each={hosts}>{x => <div role="row" class={`vl-row static${x.id === sel() ? ' sel' : ''}`}>
          <span class="lead"><StateBadge family="host" state={x.online ? 'online' : 'offline'} compact lastSeen={x.online ? undefined : x.lastSeen} now={NOW} /></span>
          <span class="tt"><a class="title row-link" href="#/hosts" onClick={e => { e.preventDefault(); setSel(x.id); }}>{x.id}</a>
            {!x.id.includes(x.platform.slice(0, 3)) && <span class="id">{x.platform}</span>}
            <span data-tip="Workers running of slots"><Num m={{ value: x.running, basis: 'measured', partial: x.online ? undefined : 'last known' }} kind="count" /><span class="muted">/{x.slots}</span></span>
            <HarnessCounts counts={x.byHarness} label={`${x.id} workers by harness`} />
            {x.queued > 0 && <span class="hc"><Icon name="list-ordered" label="Queued commands" /><Num m={{ value: x.queued, basis: 'measured' }} kind="count" /></span>}
            <FillBar total={x.store.total} segments={x.store.segments} warnAt={x.store.warnAt} stale={x.store.stale} now={NOW} /></span>
          <span><AgeChip t={x.lastSeen} alertAfter={15 * 60} tone="attention" /></span>
        </div>}</For>
      </div>
    </Panel>
  </>;
  return <ListDetail list={list} detailKey={sel() ?? undefined} idle={<LimitsMock />} detail={h() ? <Show when={h()} keyed>{host => <article class="detail" data-ui="detail" tabindex={-1} aria-label={host.id}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => setSel(null)} />{!host.id.includes(host.platform.slice(0, 3)) && <span class="muted mono">{host.platform}</span>}</header>
    <h2 class="detail-title">{host.id}</h2>

      <div class="stack-pad">
        <DetailSection title="Environments">{kvs([
          ['folder-open', 'game-worktree-3', 'codex · T-9976', <Num m={{ value: 4.2e9, basis: 'measured' }} kind="bytes" />],
          ['folder-open', 'design-main', 'idle', <Num m={{ value: 1.1e9, basis: 'measured' }} kind="bytes" />],
        ])}</DetailSection>
        <DetailSection title="Account users">{kvs([['bot', 'fleet-codex', 'codex-pro'], ['bot', 'fleet-claude', 'claude-max']])}</DetailSection>
        <DetailSection title="Harnesses">{kvs([
          ['openai', 'codex', <span class="mono">0.48.1</span>, <span class="muted">ready</span>],
          ['claude', 'claude', <span class="mono">2.1.0</span>, <span class="muted">ready</span>],
          ['antigravity', 'agy', <span class="mono">0.9.2</span>, <span class="muted">unchecked</span>],
        ])}</DetailSection>
        <DetailSection title="Store and tunnel">
          <KvRow icon="shield" label="Tunnel" meta="connected" value={<AgeChip t={NOW - 12 * min} />} />
          <For each={[
            ['Store capacity', host.capacity.value, 'Garage layout capacity.', host.capacity.status, { by: 'Alex', at: NOW - 20 * min }],
            ['Free-space warning', '25 GB', 'Below this, the store shows a problem.', { kind: 'applied', at: NOW - 2 * hr }],
            ['ClickHouse memory', '4 GB', 'systemd MemoryMax for the Fleet unit.', { kind: 'applied', at: NOW - 5 * hr }],
          ] as const}>{([l, v, help, st, ch]) => <SettingField label={l} value={v} help={help} now={NOW} status={st} changed={ch} />}</For>
        </DetailSection>
      </div>
  </article>}</Show> : undefined} />;
}

export default function Scene(props: SceneProps) {
  return <Hosts limits={props.detail === "limits"} />;
}
