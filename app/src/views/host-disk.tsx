// System › Hosts › one host: the Disk section (docs/design-system/pages/hosts.md, board drafts/host-disk frames A and B,
// approved 2026-11-27): what Fleet keeps on the host from its latest inventory (hosts_list disk), a 30-day trend from
// telemetry_disk, the share of each kind, the largest projects and the leftovers problem with the bot that owns it
// (docs/designs/fleet-host-cleanup.md sections 11 to 13). Passive: no buttons (OR-71).
import { createMemo, For, Show } from 'solid-js';
import { AgeChip, DetailSection, EntityCode, KvRow, ProblemRow, ShareBar, Sparkline, bytes, type SharePart } from '../../../packages/ui/src/index.ts';
import { useOp } from '../store.ts';
import type { HostRow } from '../../../contracts/generated/ops.ts';

type Disk = NonNullable<HostRow['disk']>;

/** Fleet's bytes on the host: the sum the disk-growth rule watches. */
export function fleetBytes(d: Disk['disk']): number {
  return (d.sessionsWorktree ?? 0) + (d.sessionsOther ?? 0) + (d.runTmp ?? 0) + (d.tmpOwned ?? 0) + (d.releases ?? 0)
    + (d.harnesses ?? 0) + (d.journal ?? 0) + (d.candidates ?? 0) + (d.spool ?? 0);
}

const DAY_KEYS = ['sessions_worktree', 'sessions_other', 'run_tmp', 'tmp_owned', 'releases', 'harnesses', 'journal', 'candidates', 'spool'];

export function HostDisk(p: { host: HostRow; clockOffsetMs?: number }) {
  const disk = () => p.host.disk;
  const trend = useOp('telemetry_disk', () => (p.host.disk ? { host: p.host.id, days: 30 } : null), { quiet: 300_000 });
  const days = createMemo(() => (trend.data?.points ?? []).map(x => DAY_KEYS.reduce((s, k) => s + (x.values[k] ?? 0), 0)));
  const problems = useOp('problems_list', () => (p.host.disk ? { limit: 200 } : null), { quiet: 60_000 });
  const leftovers = createMemo(() => problems.data?.problems.find(x => x.cause === `problem:host:${p.host.id}:leftovers` && x.state === 'open'));
  const gb = (v: number | null | undefined) => bytes(v ?? 0).text;
  return <Show when={disk()}>{d => {
    const k = () => d().disk;
    const total = () => fleetBytes(k());
    const growth = () => { const v = days(); return v.length >= 2 ? v[v.length - 1] - v[v.length - 2] : null; };
    const parts = (): SharePart[] => ([
      { label: `Worktrees · ${k().sessionsWorktreeCount ?? 0}`, v: k().sessionsWorktree ?? 0, tone: 'd1' },
      { label: 'Journal', v: k().journal ?? 0, tone: 'd2' },
      { label: 'Releases and CLIs', v: (k().releases ?? 0) + (k().harnesses ?? 0), tone: 'd3' },
      { label: `Other sessions · ${k().sessionsOtherCount ?? 0}`, v: (k().sessionsOther ?? 0) + (k().candidates ?? 0) + (k().spool ?? 0) + (k().runTmp ?? 0) + (k().tmpOwned ?? 0), tone: 'd4' },
    ] satisfies SharePart[]).filter(x => x.v > 0);
    const left = () => (d().teardown ?? 0) + (d().unknown ?? 0);
    return <DetailSection title="Disk" action={<span class="muted">inventory <AgeChip t={d().at} clockOffsetMs={p.clockOffsetMs} /></span>}>
      <div class="disk-head">
        <span class="disk-figure"><b>{gb(total())}</b> <span class="muted">Fleet on disk{k().free != null ? ` · ${gb(k().free)} free` : ''}</span></span>
        <Show when={growth() != null && growth()! > 0}>
          <span class="disk-figure attention"><b>+{gb(growth())}</b> <span class="muted">in 24 h</span></span>
        </Show>
        <Show when={days().length > 1}>
          <Sparkline values={days()} label="Fleet bytes on disk, daily maximum, last 30 days" alertFrom={growth() != null && growth()! > 0 ? days().length - 1 : undefined} />
        </Show>
      </div>
      <ShareBar label="Fleet on disk by kind" parts={parts()} value={v => bytes(v).text} />
      <Show when={d().projects.filter(x => x.projectId).length}>
        <h4 class="disk-sub">By project</h4>
        <For each={d().projects.filter(x => x.projectId).slice(0, 8)}>{x => (
          <KvRow lead={<EntityCode id={x.projectId} code={x.projectId} kind="project" cls="inline" />} label={`${x.worktrees} worktrees`} value={<span class="muted">{gb(x.bytes)}</span>} />
        )}</For>
      </Show>
      <Show when={left() > 0 || leftovers()}>
        <ProblemRow title={`Leftovers: ${left()} worktrees nothing needs, ${gb(d().leftoverBytes)}`} severity="warning" actions={[]} code={`problem:host:${p.host.id}:leftovers`}
          facts={leftovers() ? <span class="muted">owned by {leftovers()!.ownerName ?? 'no bot'}</span> : <span class="muted">removed by the next inventory</span>} />
      </Show>
    </DetailSection>;
  }}</Show>;
}
