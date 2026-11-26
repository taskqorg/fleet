// System › Agents (docs/design-system/pages/system.md, board drafts/agent-garden frames 1, 2, 4 and 5): what agents could
// not do and what fixes it. One read, agent_gaps (a view: the change stream rereads it): the window's head figures, one
// gap row per class, and the opened class: the finding's steps, its before/after around the change, the pull request
// that made it, where it fails, the setting that controls it and examples. Investigate and Dismiss answer the finding's
// question; idle, the work area shows the first class with a finding (UR-35).
import { createMemo, createSignal, For, Show } from 'solid-js';
import {
  Button, DetailHead, DetailSection, Empty, ErrorBanner, GapCompare, GapRow, KvRow, ListDetail, OfflineMarker, PickMenu,
  ShellStatus, Skeleton, StepTrail, Totals, day, moment, useTick, type TrailStep, type Tone,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp, useView } from '../store.ts';
import type { AgentGapClass, AgentGapFinding, AgentGapsReply } from '../../../contracts/generated/ops.ts';

const DAY = 864e5;
const t = (s?: string | null) => (s ? Date.parse(s) : NaN);
const plural = (n: number, one: string) => `${n.toLocaleString('en')} ${one}${n === 1 ? '' : 's'}`;
const listOf = (xs: string[]) => xs.length <= 2 ? xs.join(' and ') : `${xs.slice(0, -1).join(', ')} and ${xs.at(-1)}`;
const path = (c: string) => `/agent-gaps/${encodeURIComponent(c)}`;
const OPEN_STATES = ['open', 'investigating', 'still-failing'];
const WATCH_STATES = ['linked', 'watching'];

/** "743 runs · 3 hosts · codex, claude" or "91 calls · tavily". */
function detailOf(c: AgentGapClass): string {
  const n = c.source === 'gateway' ? plural(c.count, 'call') : plural(c.executions, 'run');
  const hosts = c.hosts.filter(h => h.key !== '-').map(h => h.key);
  const where = hosts.length > 2 ? `${hosts.length} hosts` : hosts.join(', ');
  const tail = c.source === 'gateway' ? c.tools.slice(0, 2).map(x => x.key.split('/')[0]) : c.harnesses.filter(h => h.key !== '-').map(h => h.key);
  return [n, where, [...new Set(tail)].join(', ')].filter(Boolean).join(' · ');
}

/** The day index of the change point in the window, when it falls inside it. */
function changeIndex(c: AgentGapClass): number | undefined {
  const at = t(c.finding?.changeAt);
  if (Number.isNaN(at)) return undefined;
  const day0 = Date.parse(`${c.daily[0]?.day}T00:00:00Z`);
  const i = Math.floor((at - day0) / DAY);
  return i >= 0 && i < c.daily.length ? i : undefined;
}

/** Where a class stands, as its row's chip. */
function chipOf(c: AgentGapClass): { text: string; tone: Tone; tip?: string } {
  const f = c.finding;
  const pr = f?.pr ? `#${f.pr}` : '';
  switch (f?.state) {
    case 'open': return { text: 'Open', tone: 'attention' };
    case 'investigating': return { text: 'Investigating', tone: 'running' };
    case 'linked': return { text: `Waiting · ${pr}`, tone: 'running', tip: f.note ?? undefined };
    case 'watching': return { text: `Watching · ${pr} on hosts`, tone: 'running' };
    case 'fixed': return { text: 'Fixed', tone: 'success' };
    case 'still-failing': return { text: `Still failing after ${pr}`, tone: 'danger' };
    case 'dismissed': return { text: 'Dismissed', tone: 'neutral' };
  }
  if (c.class === 'other') return { text: 'Add a rule', tone: 'neutral', tip: 'config/agent-gaps.json names its class and setting' };
  return { text: c.aboveThreshold ? 'Above threshold' : 'Below threshold', tone: 'neutral' };
}

/** The finding's steps: Found, Investigating, Change, On hosts, Watching, Fixed. */
function stepsOf(f: AgentGapFinding | undefined | null): TrailStep[] {
  const names = ['Found', 'Investigating', 'Change', 'On hosts', 'Watching', 'Fixed'];
  const reached = !f ? -1 : ({ open: 0, dismissed: 0, investigating: 1, linked: 2, watching: 4, 'still-failing': 5, fixed: 6 } as Record<string, number>)[f.state] ?? 0;
  return names.map((label, i): TrailStep => {
    if (f?.state === 'still-failing' && i === 5) return { label: 'Still failing', state: 'current', tone: 'danger' };
    if (f?.state === 'dismissed' && i === 1) return { label: 'Dismissed', state: 'current' };
    return { label, state: i < reached ? 'done' : i === reached ? 'current' : 'todo' };
  });
}

/** Failures a day before and after the change, from the class's days (the finding's verdict when it has one). */
function compareOf(c: AgentGapClass, now: number) {
  const f = c.finding, i = changeIndex(c), unit = c.source === 'gateway' ? 'calls' : 'failures';
  if (f?.verdictAt && f.beforeCount != null && f.afterCount != null) {
    return {
      before: { value: `${f.beforeCount}`, label: `before, in ${plural(f.beforeRuns ?? 0, 'run')}` },
      after: { value: `${f.afterCount}`, label: `after, in ${plural(f.afterRuns ?? 0, 'run')}`, tone: (f.state === 'fixed' ? 'success' : 'danger') as Tone },
      note: f.state === 'fixed' ? `Closed as fixed ${day(t(f.verdictAt), now)}.` : `Still failing at ${day(t(f.verdictAt), now)}; asked again.`,
    };
  }
  if (i === undefined) return null;
  const avg = (xs: number[]) => xs.length ? Math.round(xs.reduce((a, b) => a + b, 0) / xs.length) : 0;
  const counts = c.daily.map(d => d.count);
  const at = t(f!.changeAt);
  return {
    before: { value: `${avg(counts.slice(0, i))} ${unit} a day`, label: `before, ${day(at - DAY, now)}` },
    after: { value: `${avg(counts.slice(i))}`, label: `after, from ${moment(at, now)}`, tone: 'success' as Tone },
    note: `The change reached every host ${moment(at, now)}${f?.release ? ` (${f.release})` : ''}. Fleet compares failures per agent run over the same days on each side, then closes the finding as fixed or asks again.`,
  };
}

export function AgentGaps(p: { id?: string; onOpen: (path: string) => void }) {
  const [days, setDays] = createSignal('7');
  const [host, setHost] = createSignal('');
  const [profile, setProfile] = createSignal('');
  const read = useView('agent_gaps', () => ({ days: Number(days()), ...(host() ? { hostId: host() } : {}), ...(profile() ? { profileId: profile() } : {}) }), { quiet: 30_000 });
  const data = () => read.data;
  const classes = createMemo(() => data()?.classes ?? []);
  // The picks keep every host and agent the window has shown, so switching from one to another needs no detour.
  const seen = { hosts: new Set<string>(), profiles: new Set<string>() };
  const keys = (pick: (c: AgentGapClass) => { key: string }[]) => [...new Set(classes().flatMap(c => pick(c).map(x => x.key)))].filter(k => k !== '-').toSorted();
  const hostOptions = createMemo(() => { keys(c => c.hosts).forEach(h => seen.hosts.add(h)); return [['', 'All hosts'] as const, ...[...seen.hosts].toSorted().map(h => [h, h] as const)]; });
  const profileOptions = createMemo(() => { keys(c => c.profiles).forEach(x => seen.profiles.add(x)); return [['', 'All agents'] as const, ...[...seen.profiles].toSorted().map(x => [x, x] as const)]; });
  const open = createMemo(() => classes().find(c => c.class === p.id));
  const idleClass = createMemo(() => classes().find(c => c.finding && [...WATCH_STATES, ...OPEN_STATES].includes(c.finding.state)) ?? classes()[0]);
  const tick = useTick(60_000);
  const nowMs = () => tick();

  const head = (d: AgentGapsReply) => {
    const findings = d.classes.map(c => c.finding?.state).filter(Boolean) as string[];
    return <Totals items={[
      { label: `failed runs, ${d.days} days`, m: { value: d.runs, basis: 'measured' }, kind: 'count' },
      { label: 'failed gateway calls', m: { value: d.calls, basis: 'measured' }, kind: 'count' },
      { label: 'findings open', m: { value: findings.filter(s => OPEN_STATES.includes(s)).length, basis: 'measured' }, kind: 'count' },
      { label: 'changes being watched', m: { value: findings.filter(s => WATCH_STATES.includes(s)).length, basis: 'measured' }, kind: 'count' },
      { label: 'not classified', m: { value: d.other, basis: 'measured' }, kind: 'count' },
    ]} />;
  };

  const list = (
    <>
      <ShellStatus><OfflineMarker state={read.state} now={nowMs()} /></ShellStatus>
      <Show when={read.error && !data()}><ErrorBanner text={read.error!} onRetry={read.reload} /></Show>
      <Show when={data()} fallback={<Show when={!read.error}><Skeleton rows={8} /></Show>}>{d => (
        <section class="panel stack" aria-label="What agents could not do">
          {head(d())}
          <div class="fbar-line">
            <PickMenu label="Host" icon="panels-top-left" value={host()} options={hostOptions()} onChange={setHost} />
            <PickMenu label="Agent" icon="bot" value={profile()} options={profileOptions()} onChange={setProfile} />
            <PickMenu label="Window" icon="clock" value={days()} options={[['7', '7 days'], ['14', '14 days'], ['30', '30 days']]} onChange={setDays} />
          </div>
          <Show when={classes().length} fallback={<Empty text={`No agent tool call failed in the last ${d().days} days.`} />}>
            <ul class="gap-rows" aria-label="Classes of failure">
              <For each={classes()}>{c => {
                const last = t(c.lastAt);
                return <GapRow name={c.label} detail={c.class === 'other' ? `${detailOf(c)} · ${c.examples[0]?.line ?? ''}` : detailOf(c)}
                  days={c.daily.map(x => x.count)} changeAt={changeIndex(c)} chip={chipOf(c)}
                  fact={Number.isNaN(last) ? undefined : (nowMs() - last < DAY ? moment(last, nowMs()) : day(last, nowMs()))}
                  factTip={c.lastAt ? `last failure ${moment(last, nowMs())}` : undefined}
                  href={path(c.class)} current={c.class === p.id} onOpen={() => p.onOpen(path(c.class))} />;
              }}</For>
            </ul>
          </Show>
        </section>
      )}</Show>
    </>
  );

  return (
    <ListDetail
      list={list}
      detailKey={p.id ? `gap:${p.id}` : undefined}
      detail={p.id ? (
        <Show when={open()} fallback={
          <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={p.id}>
            <DetailHead crumb="system · agents" title={p.id} onClose={() => p.onOpen('/agent-gaps')} />
            <Show when={data()} fallback={<Skeleton rows={6} />}><Empty text={`No failure of ${p.id} in the last ${days()} days.`} /></Show>
          </article>
        }>{c => <GapDetail c={c()} days={Number(days())} onClose={() => p.onOpen('/agent-gaps')} onOpen={p.onOpen} reload={read.reload} />}</Show>
      ) : undefined}
      idle={<Show when={idleClass()} fallback={<Show when={data()}><Empty text="Agents had everything they needed in this window." /></Show>}>
        {c => <GapDetail c={c()} days={Number(days())} onOpen={p.onOpen} reload={read.reload} />}
      </Show>}
    />
  );
}

function GapDetail(p: { c: AgentGapClass; days: number; onClose?: () => void; onOpen: (path: string) => void; reload: () => void }) {
  const tick = useTick(60_000);
  const now = () => tick();
  const c = () => p.c;
  const f = () => c().finding ?? undefined;
  const cmp = createMemo(() => compareOf(c(), now()));
  const delivery = useOp('delivery_history', () => (f()?.pr ? { number: f()!.pr! } : null));
  const pr = createMemo(() => delivery.data?.prs.find(x => x.number === f()?.pr));
  const [busy, setBusy] = createSignal('');
  const [err, setErr] = createSignal('');
  const answer = async (a: 'investigate' | 'dismiss') => {
    const qid = f()?.questionId;
    if (!qid) return;
    setBusy(a); setErr('');
    try { await op('question_answer', { questionId: qid, answer: a }); p.reload(); } catch (e) { setErr(e instanceof Error ? e.message : String(e)); } finally { setBusy(''); }
  };
  const summary = createMemo(() => {
    const x = c(), e = x.examples[0];
    const hosts = x.hosts.filter(h => h.key !== '-').map(h => h.key);
    const who = x.source === 'gateway' ? plural(x.count, 'gateway call') : `${plural(x.executions, `${x.harnesses.filter(h => h.key !== '-').map(h => h.key).join(' and ')} run`)}`;
    return `${who}${hosts.length ? ` on ${listOf(hosts)}` : ''} failed this way ${plural(x.count, 'time')} in ${p.days} days${e ? `: "${e.line}"` : ''}.`;
  });
  return (
    <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={c().label}>
      <DetailHead crumb="system · agents" title={c().label} onClose={p.onClose} />
      <StepTrail steps={stepsOf(f())} label="Where the finding stands" />
      <Show when={cmp()} fallback={<p>{summary()}</p>}>{x => (
        <GapCompare before={x().before} after={x().after} days={c().daily.map(d => d.count)} changeAt={changeIndex(c())} note={x().note}
          label={`${c().label}: failures a day before and after the change`} />
      )}</Show>
      <Show when={err()}><ErrorBanner text={err()} /></Show>
      <Show when={f()?.state === 'open' || f()?.state === 'still-failing'}>
        <div class="fbar-line">
          <Button label="Investigate" kind="primary" busy={busy() === 'investigate'} onClick={() => void answer('investigate')} />
          <Button label="Dismiss" busy={busy() === 'dismiss'} onClick={() => void answer('dismiss')} />
        </div>
      </Show>
      <Show when={f()?.sessionId}>{sid => (
        <DetailSection title="Investigation">
          <KvRow icon="messages-square" label="PM conversation" meta={<span class="muted">started from this finding</span>}
            value={<a href={`/conversations/${encodeURIComponent(sid())}`} onClick={e => { e.preventDefault(); p.onOpen(`/conversations/${encodeURIComponent(sid())}`); }}>Open conversation</a>} />
          <Show when={f()?.note}><p class="muted">{f()!.note}</p></Show>
        </DetailSection>
      )}</Show>
      <Show when={f() && f()!.state !== 'dismissed'}>
        <DetailSection title="What changed">
          <Show when={f()?.pr} fallback={<p class="muted">The pull request will link here (agent_gaps_link). Setting: {c().setting.ref}.</p>}>
            <KvRow icon="git-pull-request" label={pr() ? `#${pr()!.number} ${pr()!.title}` : `#${f()!.pr}`}
              meta={<span class="muted">{[c().setting.kind, pr()?.merged ? 'merged' : 'open', f()?.release].filter(Boolean).join(' · ')}</span>}
              value={<a href={f()!.prUrl ?? '#'} target="_blank" rel="noreferrer">{pr()?.merged ? day(t(pr()!.merged), now()) : 'Open'}</a>} />
            <Show when={f()?.state === 'linked' && f()?.note}><p class="muted">{f()!.note}</p></Show>
          </Show>
        </DetailSection>
      </Show>
      <DetailSection title="Where">
        <For each={c().hosts}>{h => (
          <KvRow icon="panels-top-left" label={h.key === '-' ? 'coordinator' : h.key} value={<span>{plural(h.count, c().source === 'gateway' ? 'call' : 'failure')}</span>} />
        )}</For>
      </DetailSection>
      <DetailSection title="Controlled by">
        <KvRow icon="settings" label={c().setting.ref} meta={<span class="muted">{c().setting.text}</span>} />
      </DetailSection>
      <DetailSection title="Examples" count={c().examples.length}>
        <For each={c().examples}>{e => (
          <KvRow icon="activity" label={[e.profileId, e.hostId && `on ${e.hostId}`].filter(Boolean).join(' ') || e.tool}
            meta={<span class="mono">{e.line}</span>}
            value={e.path ? <a href={e.path} onClick={ev => { ev.preventDefault(); p.onOpen(e.path!); }}>{day(t(e.at), now())}</a> : <span>{day(t(e.at), now())}</span>} />
        )}</For>
      </DetailSection>
    </article>
  );
}
