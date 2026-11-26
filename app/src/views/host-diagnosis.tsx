// System › Hosts › host: Diagnosis (docs/design-system/pages/hosts.md, board/screens/host-diagnosis.bl, approved without a
// button, owner 2026-11-26). The host's latest report (host_diagnosis) beside the coordinator's facts: the queued work
// this host skipped and why, uploads Fleet refused, failed transfers, rows the coordinator refused, commands not
// confirmed, and the executor log folded. Opening the host asks it for a fresh report (host_diagnose, wait 0); the
// coordinator also asks every online host in the background, and a stored report rereads through the change stream.
// Nothing to report: the section is hidden.
import { createMemo, For, onSettled, Show } from 'solid-js';
import { AgeChip, DetailSection, Disclosure, EntityCode, Icon, LiveRow, timeOf } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';

export function HostDiagnosis(p: { hostId: string; clockOffsetMs: number }) {
  onSettled(() => { void op('host_diagnose', { host: p.hostId, wait: 0 }).catch(() => {}); });
  const read = useOp('host_diagnosis', () => ({ host: p.hostId }));
  const d = () => read.data;
  const report = () => d()?.report;
  const refusedRoutes = createMemo(() => (report()?.outboxFailed ?? []).filter(f => f.route !== '/api/hosts/artifacts/failed'));
  const transfers = () => report()?.transfersFailed ?? [];
  const waiting = () => (d()?.waiting ?? []).filter(w => w.skippedHere);
  const commands = () => d()?.coordinatorCommands ?? [];
  const queued = () => report()?.outbox.rows ?? 0;
  const problems = createMemo(() => waiting().length + transfers().length + refusedRoutes().length + commands().length + (queued() > 0 ? 1 : 0));
  const log = () => report()?.logTail ?? [];
  const at = (ms: number) => timeOf(ms, p.clockOffsetMs);
  return <Show when={d() && problems() > 0}>
    <DetailSection title="Diagnosis" action={<Show when={d()!.reportedAt}>{t => <span class="muted">report <AgeChip t={t()} clockOffsetMs={p.clockOffsetMs} /></span>}</Show>}>
      <p class="host-versions" data-ui="host-diagnosis-facts">
        <Show when={d()!.lastSeenAt}>{t => <span class="hv"><Icon name={d()!.online ? 'circle-check' : 'circle-slash'} /> polled <AgeChip t={t()} clockOffsetMs={p.clockOffsetMs} /></span>}</Show>
        <Show when={transfers().length}><span class="hv t-attention"><Icon name="circle-alert" /> {transfers().length} {transfers().length === 1 ? 'upload' : 'uploads'} failed today</span></Show>
        <Show when={waiting().length}><span class="hv t-attention"><Icon name="hourglass" /> {waiting().length} {waiting().length === 1 ? 'run waits' : 'runs wait'}, skipped here</span></Show>
        <Show when={queued() > 0}><span class="hv">{queued()} rows to send</span></Show>
        <Show when={commands().length}><span class="hv">{commands().length} {commands().length === 1 ? 'command' : 'commands'} not confirmed</span></Show>
      </p>
      <ul class="live-list" data-ui="host-diagnosis">
        <For each={waiting()}>{w => (
          <LiveRow kind="event" state="queued" at={at(w.decidedAt ?? w.since)} text="waits for a host"
            facts={<span class="lr-fact"><EntityCode id={w.sessionId ?? w.executionId} kind={w.sessionId ? 'conversation' : 'execution'} /> <span class="t-attention">skipped here: {w.skippedHere}</span></span>} />
        )}</For>
        <For each={transfers()}>{t => (
          <LiveRow kind="event" state="failed" trouble at={at(t.at)} text={t.name ?? t.kind}
            facts={<span class="lr-fact"><Show when={t.executionId}>{x => <EntityCode id={x()} kind="execution" />}</Show> {t.error}</span>} />
        )}</For>
        <For each={refusedRoutes()}>{f => (
          <LiveRow kind="event" state="failed" trouble at={at(f.at)} text={`${f.route.replace('/api/hosts/', '')} refused`} facts={<span class="lr-fact">{f.status} · {f.reason}</span>} />
        )}</For>
        <For each={commands()}>{c => (
          <LiveRow kind="event" state="working" at={at(c.updatedAt)} text={`${c.kind} not confirmed`}
            facts={<Show when={c.executionId}>{x => <span class="lr-fact"><EntityCode id={x()} kind="execution" /> {c.state}</span>}</Show>} />
        )}</For>
      </ul>
      <Show when={log().length}>
        <Disclosure summary={<>Executor log <span class="muted">{log().length} lines</span></>}>
          <pre class="host-log" data-ui="host-log">{log().join('\n')}</pre>
        </Disclosure>
      </Show>
    </DetailSection>
  </Show>;
}
