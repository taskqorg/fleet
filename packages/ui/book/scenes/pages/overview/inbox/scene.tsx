import type { SceneProps } from "../../../../scene-shell.tsx";
import { createMemo, For, Show } from 'solid-js';
import { StatStrip, DecisionBlock, ProblemRow, FillBar, StateBadge, Num, AgeChip, Button, Icon, Stale, StateCounters, ListDetail, NeedsYou, OpsSummary, HarnessCounts } from '../../../../../src/index.ts';
import { NOW, min, hr, tasks, hosts, PROJECTS } from '../../../../fixtures.ts';
import { PageHead, Panel, m, useHashLocation } from '../../../../scene-parts.tsx';
import { ALL } from "./fixture.ts";

function Overview(props: { id?: string }) {
  const [, go] = useHashLocation();
  const byProject = PROJECTS.map(p => { const ts = ALL.filter(t => t.project === p); const c: Record<string, number> = {}; for (const t of ts) c[t.state] = (c[t.state] ?? 0) + 1; return { p, c }; });
  // Overview B (2026-11-20 decision): the Needs-you inbox is the control column; with nothing picked the work area shows the fleet.
  const inbox = [
    { cause: 'Acceptance', rows: [{ id: 'q1', icon: 'circle-check' as const, title: 'Accept this result?', code: 'Q-FLT-201', at: NOW - 12 * min }] },
    { cause: 'Decisions', rows: [{ id: 'q2', icon: 'bell' as const, title: 'Use the Windows store for the game bundle?', code: 'Q-FLT-198', at: NOW - 40 * min }] },
    { cause: 'Accounts', rows: [{ id: 'p1', icon: 'triangle-alert' as const, title: 'Claude quota exhausted', code: 'T-9937', at: NOW - 2 * hr }] },
    { cause: 'Stores', rows: [{ id: 'p2', icon: 'triangle-alert' as const, title: 'Low space on windows store', code: 'windows', at: NOW - 3 * hr }] },
  ] as const;
  const list = <NeedsYou><For each={inbox}>{g => <div class="ny-group" role="group" aria-label={g.cause}>
    <div class="ny-h"><span class="mono">{g.cause}</span><span class="grow" /><span class="mono">{g.rows.length}</span></div>
    <For each={g.rows}>{r => <a class={`ny-row${r.id === props.id ? ' sel' : ''}`} href={`#/overview/${r.id}`}><Icon name={r.icon} label={r.icon === 'triangle-alert' ? 'Problem' : 'Question'} />
      <span class="ny-t"><span class="title">{r.title}</span><span class="id">{r.code}</span></span><AgeChip t={r.at} /></a>}</For>
  </div>}</For></NeedsYou>;
  const idle = <article class="detail" aria-label="Fleet now">
    <PageHead title="Fleet now"><Stale at={NOW - 20e3} now={NOW} /></PageHead>
    <OpsSummary>
    <StatStrip label="Operations" stats={[
      { label: 'need you', icon: 'bell', value: m(4), tone: 'attention' },
      { label: 'failed today', icon: 'octagon-x', value: m(2), tone: 'danger', href: '#/tasks' },
      { label: 'running', icon: 'activity', value: m(10), of: 13, href: '#/hosts' },
      { label: 'queued', icon: 'list-ordered', value: m(7) },
      { label: 'spent today', icon: 'chart-no-axes-column', value: m(41.2, 'estimated', 'tokens'), kind: 'money' },
    ]} />
    <div class="cols">
        <Panel title="Workers per host" actions={<Button label="Hosts" icon="external-link" iconOnly onClick={() => go('/hosts')} />}>
          <ul class="kv"><For each={hosts}>{h => <li>
            <StateBadge family="host" state={h.online ? 'online' : 'offline'} compact />
            <span class="k">{h.id}</span>
            <span class="v"><Num m={{ value: h.running, basis: 'measured', partial: h.online ? undefined : 'last known' }} kind="count" /><span class="muted">/{h.slots}</span></span>
            <HarnessCounts counts={h.byHarness} label={`${h.id} workers by harness`} />
            <span class="v muted">{h.queued ? `${h.queued} queued` : ''}</span>
          </li>}</For></ul>
        </Panel>
        <Panel title="Accounts">
          <ul class="kv">
            <For each={[{ a: 'claude-max', used: 0.97, reset: NOW - 3 * hr }, { a: 'codex-pro', used: 0.41, reset: NOW - 1 * hr }, { a: 'antigravity', used: null as number | null, reset: 0 }]}>{x => <li>
              <Icon name="bot" /><span class="k">{x.a}</span>
              <FillBar total={x.used === null ? null : 100} segments={x.used === null ? [] : [{ label: 'used', bytes: x.used * 100 }]} warnAt={10} unit="percent" />
            </li>}</For>
          </ul>
        </Panel>
        <Panel title="Projects">
          <ul class="kv"><For each={byProject}>{({ p, c }) => <li><Icon name="folder-open" /><span class="k">{p}</span>
            <StateCounters family="task" label={`${p} by state`} counts={{ waiting: c.waiting ?? 0, failed: c.failed ?? 0, in_progress: c.in_progress ?? 0, queued: c.queued ?? 0 }} active={[]} onToggle={() => go('/tasks')} /></li>}</For></ul>
        </Panel>
    </div>
    </OpsSummary>
  </article>;
  const picked = createMemo(() => inbox.flatMap(g => g.rows.map(r => ({ ...r, cause: g.cause }))).find(r => r.id === props.id));
  const detail = <Show when={picked()} keyed>{pk => <article class="detail" data-ui="detail" aria-label={pk.title}>
    <h2 class="detail-title">{pk.title}</h2>
    {pk.icon === 'triangle-alert'
      ? <ProblemRow severity="failure" title={pk.title} facts={<>resets in <span class="mono">2h</span> · T-9937 interrupted</>} actions={[{ label: 'Continue on codex', primary: true }, { label: 'Wait for reset' }]} />
      : <DecisionBlock question={pk.title} askedBy="reviewer (claude)" addressedTo="you" since={pk.at} now={NOW} waits="T-9989 Write release notes 95" options={[{ label: 'Accept', kbd: 'a', primary: true }, { label: 'Request rework', kbd: 'r' }]} />}
  </article>}</Show>;
  return <ListDetail list={list} detail={picked() ? detail : undefined} detailKey={props.id} idle={idle} />;
}

export default function Scene(props: SceneProps) {
  return <Overview id={props.detail} />;
}
