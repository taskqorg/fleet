// Delivery work-area panels: overview (needs a look, moving now, pace) and pull/merge request detail.
import { createMemo, createSignal, For, Show } from 'solid-js';
import {
  EntityCode, StateBadge, AgeChip, Ago, Num, Totals, TimeStrip, ProblemRow, DetailSection, KvRow, Button,
  Choice, Empty, ErrorBanner, Skeleton, DayChart, ShareBar, NormBar, VirtualList, catalogueRow, count, states, capOf, day,
  type DayMark, type SharePart,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import { DELIVERY_FAILED } from '../system-views.ts';
import {
  MIN, DAY, FLEET, ORDER, DONE, RANGES, uiOnly, releaseLabel, idOf, providerLabel, ms, fmt, kept, failed, q, sec,
  alertOf, unrecorded, due, doneAt, stageMs, totalMark,
  type Stage, type Item, type PublishingCapability, type History, type HistoryPr, type Regressions, type Norms, type Bucket,
} from './delivery-model.tsx';

const FAILED = DELIVERY_FAILED as Stage[];

const KEYS: Record<string, string> = {
  pr_ci: 'PR checks', enqueue: 'Enqueue', queue: 'Merge queue', merge: 'Merge', release: 'Release', install: 'Install',
  ui: 'UI on site', lead: 'Lead', total: 'Open to installed', availability: 'Availability', time_to_available: 'Time to available',
  run: 'Queue run', availability_wait: 'Waiting for availability', ui_wait: 'Waiting for the UI on site', ui_install: 'UI on site (UI-only release)',
};
const keyLabel = (k: string) => KEYS[k] ?? k;

// Where the time goes: a pull request's stage durations folded into four parts, in pipeline order; a UI-only release's
// Install is its UI on site (stageMs).
const PARTS: { label: string; tone: SharePart['tone']; keys: string[] }[] = [
  { label: 'PR checks', tone: 'd1', keys: ['pr_ci'] }, { label: 'Queue', tone: 'd2', keys: ['enqueue', 'queue', 'merge'] },
  { label: 'Release', tone: 'd3', keys: ['release'] }, { label: 'Install', tone: 'd4', keys: ['install'] },
];
const partOf = (x: HistoryPr, keys: string[]) => keys.reduce((s, k) => s + (stageMs(x, k) ?? 0), 0);
// Measured through install: every part has its own duration (enqueue and merge may be absent when there was no wait).
const complete = (x: HistoryPr) => ['pr_ci', 'queue', 'release', 'install'].every(k => typeof stageMs(x, k) === 'number');

/** The work area while no pull request is open: what needs a look, what moves now, the pace. */
export function Overview(props: {
  now: number; items: Item[]; incomplete: boolean; norms: Norms; hist: History | null; regressions: Regressions['regressions']; histError: string | null; fleetShown: boolean;
  range: string; buckets: Bucket[]; picked: Bucket | null; onRange: (r: string) => void; onPick: (k: string) => void; onOpen: (it: Item) => void; href: (it: Item) => string;
}) {
  const moving = createMemo(() => ORDER.filter(s => !DONE.includes(s)).flatMap(s => props.items.filter(it => it.stage === s)));
  const over = (it: Item) => { const n = props.norms.stage(it); return !!n && props.now - ms(it.since) > n[1]; };
  const failedItems = createMemo(() => props.items.filter(it => FAILED.includes(it.stage)));
  const stuck = createMemo(() => moving().filter(over));
  const look = createMemo(() => {
    const regs = props.regressions;
    const h = props.hist;
    // One slowdown, one block (2026-11-20 decision): several metrics over their baseline are one cause to look at, named by the
    // pull requests they first went over with and what those took; one metric keeps its own row.
    const byPr = new Map<number, number>();
    for (const g of regs) if (g.firstPr) byPr.set(g.firstPr, (byPr.get(g.firstPr) ?? 0) + 1);
    const cause = [...byPr].sort((a, b) => b[1] - a[1]).map(([n, k]) => {
      const pr = h?.prs.find(x => x.number === n), t = pr?.durations.total;
      return `#${n}${pr ? ` ${pr.title}` : ''}${typeof t === 'number' ? `: ${fmt(t)} from open to installed` : ''}${pr?.queueRemovals ? `, ${pr.queueRemovals} queue removals` : ''} (${k} ${k === 1 ? 'metric' : 'metrics'})`;
    });
    const worst = regs.toSorted((a, b) => b.currentMs / (b.baselineMs || 1) - a.currentMs / (a.baselineMs || 1));
    return [
      ...(regs.length > 1 ? [<div class="stack">
        <ProblemRow severity="warning" title={`Delivery got slower: ${regs.length} metrics over their 14-day baseline`}
          facts={`worst ${keyLabel(worst[0].key)} ${worst[0].metric.toUpperCase()} ${fmt(worst[0].currentMs)} against ${fmt(worst[0].baselineMs)}`}
          actions={[...(worst[0]?.compareUrl ? [{ label: 'Compare', onClick: () => window.open(worst[0].compareUrl!, '_blank', 'noopener') }] : [])]} />
        <p class="muted small-note">{worst.map(g => `${keyLabel(g.key)} ${g.metric.toUpperCase()} ${fmt(g.currentMs)} / ${fmt(g.baselineMs)}`).join(' · ')}</p>
        {cause.length > 0 && <p class="small-note">First over with {cause.join('; ')}.</p>}
      </div>]
      : regs.map(g => <ProblemRow severity="warning" title={`${keyLabel(g.key)} ${g.metric.toUpperCase()} is ${fmt(g.currentMs)}, baseline ${fmt(g.baselineMs)}`}
        facts={`${g.firstPr ? `Since #${g.firstPr} · ` : ''}${g.n} pull requests in the last 24 h`} actions={[...(g.compareUrl ? [{ label: 'Compare', onClick: () => window.open(g.compareUrl!, '_blank', 'noopener') }] : [])]} />)),
      ...failedItems().map(it => <ProblemRow severity="failure" title={`#${it.number} ${it.stage === 'mq_failed' ? 'removed from the merge queue' : 'checks failed'}`} facts={<>{it.title} · <Ago t={ms(it.since)} /></>}
        actions={[{ label: 'Open', primary: true, onClick: () => props.onOpen(it) }]} />),
      ...stuck().map(it => <ProblemRow severity="warning" title={`#${it.number} ${it.stage === 'installing' && uiOnly(it.release) ? 'waiting for the UI on site' : states.delivery[it.stage].label.toLowerCase()} for ${fmt(props.now - ms(it.since))}, P90 ${fmt(props.norms.stage(it)![1])}`}
        facts={it.title} actions={[{ label: 'Open', primary: true, onClick: () => props.onOpen(it) }]} />),
    ];
  });
  const rowH = catalogueRow();
  return <div class="stack-pad" data-ui="delivery-overview">
    <Show when={look().length > 0}><DetailSection title="Needs a look">{look()}</DetailSection></Show>
    <DetailSection title="Moving now">
      <Show when={moving().length} fallback={<Empty text={props.incomplete ? 'Unavailable. GitHub data is incomplete.' : 'Nothing in flight.'} />}>
        <div style={{ '--list-height': `calc(${moving().length} * ${rowH > 32 ? 'var(--size-row-phone)' : 'var(--size-row)'})` }}>
          <VirtualList
            items={moving()}
            getKey={idOf}
            rowHeight={rowH}
            follow="top"
            label="Pull requests in flight"
            columns="1.25rem 3.5rem minmax(0,1fr) minmax(0,1.3fr) 2.75rem 4.5rem"
            render={it => {
              const n = props.norms.stage(it), el = Math.max(0, props.now - ms(it.since)), red = over(it);
              return <>
                <span class="lead"><StateBadge family="delivery" state={it.stage} compact /></span>
                <a class="mono row-link" href={props.href(it)} onClick={e => { e.preventDefault(); props.onOpen(it); }}>#{it.number}</a>
                <span class="muted" style={{ overflow: 'hidden', 'text-overflow': 'ellipsis' }}>{it.title}</span>
                <NormBar elapsed={el} p50={n?.[0]} p90={n?.[1]} label={`${states.delivery[it.stage].label} #${it.number}`} value={fmt} />
                <span class="r"><AgeChip t={ms(it.since)} alertAfter={alertOf(props.norms, it)} alertLabel="past the stage's P90" /></span>
                <span class={`r mono type-sm${red ? ' t-danger' : ' muted'}`} style={red ? { color: 'var(--tone-fg)' } : undefined}
                  data-tip={n ? `${states.delivery[it.stage].label}: P50 ${fmt(n[0])}, P90 ${fmt(n[1])} over 14 days` : undefined}>{n ? `P90 ${fmt(n[1])}` : 'no norm'}</span>
              </>;
            }}
          />
        </div>
      </Show>
      <Show when={props.incomplete && moving().length > 0}><p class="muted type-sm">The last rows read; GitHub coverage is incomplete.</p></Show>
    </DetailSection>
    <Show when={props.fleetShown}>
      <Pace hist={props.hist} error={props.histError} now={props.now} norms={props.norms} range={props.range} buckets={props.buckets} picked={props.picked} onRange={props.onRange} onPick={props.onPick} />
    </Show>
  </div>;
}

function Pace(props: {
  hist: History | null; error: string | null; now: number; norms: Norms; range: string; buckets: Bucket[]; picked: Bucket | null; onRange: (r: string) => void; onPick: (k: string) => void;
}) {
  const choice = () => <div class="state-line"><Choice label="Range" options={RANGES} value={props.range} onChange={props.onRange} />
    <Show when={props.hist?.backfilledFrom && props.buckets[0] && props.buckets[0].start <= ms(props.hist.backfilledFrom)}>
      <span class="muted type-sm">recorded from {day(ms(props.hist!.backfilledFrom!), props.now)}</span>
    </Show></div>;

  const stats = createMemo(() => {
    const hist = props.hist, buckets = props.buckets;
    if (!hist || !buckets.length) return null;
    const from = buckets[0].start, prs = hist.prs.filter(x => ms(x.merged) >= from);
    const inB = (b: Bucket) => prs.filter(x => ms(x.merged) >= b.start && ms(x.merged) < b.end);
    const installsFrom = hist.installsFrom ? ms(hist.installsFrom) : null;
    const total = buckets.map(b => totalMark(inB(b), b, installsFrom));
    const merged: DayMark[] = buckets.map(b => ({ v: inB(b).length, partial: b.today ? 'so far' : undefined }));
    const totals = prs.map(x => x.durations.total).filter((v): v is number => typeof v === 'number');
    const removals = prs.reduce((n, x) => n + x.queueRemovals, 0), flakes = prs.reduce((n, x) => n + x.flakes, 0);
    // Pull requests still on their way are not counted as unmeasured, as on the chart.
    const scope = (props.picked ? inB(props.picked) : prs).filter(x => due(x, installsFrom)), done = scope.filter(complete);
    const mean = (keys: string[]) => done.reduce((s, x) => s + partOf(x, keys), 0) / (done.length || 1);
    const share: SharePart[] = PARTS.map(p => ({ label: p.label, v: mean(p.keys), tone: p.tone, tip: `${p.label}: mean ${fmt(mean(p.keys))}, P50 ${fmt(q(done.map(x => partOf(x, p.keys)), 0.5) ?? 0)}` }));
    const days = Math.max(1, (props.now - from) / DAY), base = props.norms.total;
    return { prs, total, merged, totals, removals, flakes, scope, done, share, days, base };
  });
  // The panel reads the last computed stats, not Show's accessor: when stats turns null (history or
  // buckets go empty), the charts' memos re-run once before Show removes them and would read null.
  const st = createMemo<ReturnType<typeof stats>>(prev => stats() ?? prev ?? null) as () => NonNullable<ReturnType<typeof stats>>;

  return (
    <Show when={!(props.error && !props.hist)} fallback={<DetailSection title="Pace">{choice()}<ErrorBanner text={`Delivery history did not load: ${props.error}`} /></DetailSection>}>
      <Show when={stats()} fallback={<DetailSection title="Pace">{choice()}<Skeleton rows={3} /></DetailSection>}>{_ => (
        <DetailSection title="Pace"><div style={{ display: 'grid', gap: 'var(--space-4)', 'min-width': 0 }}>
          {choice()}
          <Totals items={[
            { label: 'Open to installed P50', m: sec(q(st().totals, 0.5)), kind: 'duration', note: st().base ? `baseline ${fmt(st().base![0])}` : undefined },
            { label: 'Open to installed P90', m: sec(q(st().totals, 0.9)), kind: 'duration', note: st().base ? `baseline ${fmt(st().base![1])}` : undefined },
            { label: 'Merged', m: { value: st().prs.length, basis: 'measured' }, kind: 'count', note: props.range === '24h' ? undefined : `${st().prs.length / st().days < 10 ? (st().prs.length / st().days).toFixed(1) : count(Math.round(st().prs.length / st().days)).text} a day` },
            { label: 'Queue removals', m: { value: st().removals, basis: 'measured' }, kind: 'count', note: st().flakes ? `${count(st().flakes).text} flaky` : undefined },
          ]} />
          <DayChart title="Open to installed" label="Open to installed per day, P50 and P90" kind="range" cols={props.buckets} marks={st().total} value={fmt}
            cap={capOf(st().total.flatMap(m => [m.v, m.hi]), { floor: 10 * MIN, unit: MIN })} capNote="about P95 of the figures shown; a figure over four times their median is drawn cut, with its value"
            names={['P50', 'P90']} values alertAbove={st().base?.[1]} alertLabel={st().base ? `above the 14-day P90 ${fmt(st().base![1])}` : undefined} selected={props.picked?.key ?? null} onSelect={k => props.onPick(k)} />
          <DayChart title="Merged" label="Pull requests merged per day" kind="bar" size="sm" cols={props.buckets} marks={st().merged} value={v => count(Math.round(v)).text}
            cap={capOf(st().merged.map(m => m.v), { floor: 4, fence: Infinity })} tone="d2" axis selected={props.picked?.key ?? null} onSelect={k => props.onPick(k)} />
          <ShareBar title="Where the time goes" label="Where the time goes" parts={st().share} value={fmt}
            note={`mean per pull request, ${count(st().done.length).text} of ${count(st().scope.length).text} with every stage measured${props.picked ? ` · ${props.picked.tip ?? props.picked.label}` : ''}`} />
        </div></DetailSection>
      )}</Show>
    </Show>
  );
}

/** The opened pull/merge request: its stages on one time axis, publishing state/actions, queue runs, release and installs per host. */
export function PrDetail(props: { id: string; item?: Item; capability?: PublishingCapability; now: number; norms: Norms; onReload?: () => void; onClose: () => void }) {
  const parts = createMemo(() => {
    const idx = props.id.lastIndexOf('~');
    const repoSlug = idx > 0 ? props.id.slice(0, idx).replaceAll('~', '/') : props.id;
    const number = idx > 0 ? Number(props.id.slice(idx + 1)) : NaN;
    return { repoSlug, number, fleet: repoSlug === FLEET };
  });
  const [busyAction, setBusyAction] = createSignal<string | null>(null);
  const [actionError, setActionError] = createSignal<string | null>(null);
  const hd = useOp('delivery_history', () => (parts().fleet ? { number: parts().number } : null), { every: 600000 });
  const h = createMemo(() => ({ data: kept(hd), error: failed(hd), loading: parts().fleet && hd.data === null && !failed(hd) }));
  const x = createMemo(() => h().data?.prs.find(p => p.number === parts().number));
  // The merge queue splits at its last successful run's start: waiting for a runner before, its checks after.
  const qStart = createMemo(() => h().data?.runs.filter(r => r.conclusion === 'success' && r.started).map(r => r.started!).sort().at(-1));
  const title = createMemo(() => props.item?.title || x()?.title || `#${parts().number}`);
  const steps = createMemo<(readonly [string, Stage, string | undefined, string | undefined])[]>(() => {
    const cur = x();
    if (!cur) return [];
    const qs = qStart();
    return [
      ['PR checks', 'pr_ci', cur.opened, cur.prCiDone],
      ...(qs && cur.enqueued ? [['Waiting in queue', 'mq_wait', cur.enqueued, qs], ['Queue checks', 'mq_ci', qs, cur.mqCiDone ?? cur.merged]] as const : [['Merge queue', 'mq_ci', cur.enqueued, cur.mqCiDone ?? cur.merged]] as const),
      ['Releasing', 'releasing', cur.merged, cur.released], [uiOnly(cur.release) ? 'UI on site' : 'Installing', 'installing', cur.released, doneAt(cur)],
    ];
  });
  const done = createMemo(() => { const cur = x(); return cur ? doneAt(cur) : undefined; });
  const rel = createMemo(() => props.item?.release || x()?.release);
  // A stage without its own end ends where a later one starts; only the last stage reached runs to now.
  const known = createMemo(() => steps().filter(([, , a]) => a));
  const ended = createMemo(() => known().map(([l, s, a, b], i) => [l, s, a, b ?? known().slice(i + 1).map(k => k[2]).find(Boolean)] as const));
  const t0 = createMemo(() => ms(x()?.opened ?? props.item?.opened ?? props.item?.since));
  const url = createMemo(() => props.item?.url || x()?.url);
  const isOpen = createMemo(() => {
    const it = props.item;
    return !!it && !DONE.includes(it.stage) && it.stage !== 'merged' && it.stage !== 'releasing' && it.stage !== 'installing';
  });
  const canAction = (name: string) => !!props.capability?.publicationSupported && props.capability.supportedActions.includes(name);
  const runPub = async (kind: 'mark_ready' | 'request_merge' | 'cancel_merge_request') => {
    const it = props.item;
    if (!it || busyAction()) return;
    setBusyAction(kind);
    setActionError(null);
    const reqId = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `ui-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const body = {
      requestId: reqId,
      projectId: it.projectId,
      repo: it.repo,
      number: it.number,
      branch: it.branch ?? undefined,
      expectedHeadSha: it.headSha ?? undefined,
    };
    try {
      if (kind === 'mark_ready') await op('publishing_mark_ready', body);
      else if (kind === 'request_merge') await op('publishing_request_merge', body);
      else await op('publishing_cancel_merge_request', body);
      props.onReload?.();
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyAction(null);
    }
  };
  return <article class="detail" data-ui="detail" tabindex={-1} aria-label={title()}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => props.onClose()} /><EntityCode code={`${parts().repoSlug}#${parts().number}`} id={props.id} kind="pull_request" form="code" /><span class="grow" />
      <Show when={url()}>{u => <Button label={props.item?.provider === 'gitlab' || props.item?.kind === 'merge_request' ? 'Open on GitLab' : 'Open on GitHub'} icon="external-link" iconOnly onClick={() => window.open(u(), '_blank', 'noopener')} />}</Show></header>
    <h2 class="detail-title">{title()}</h2>
    <div class="state-line">
      <Show when={props.item}>{it => <StateBadge family="delivery" state={it().stage} />}</Show>
      <Show when={props.item}>{it => <span class="muted">{providerLabel(it().provider, it().kind)}{it().host && it().host !== 'github.com' ? ` (${it().host})` : ''}</span>}</Show>
      <Show when={x()?.author}>{author => <span class="muted">by {author()}</span>}</Show>
      <Show when={t0() > 0}><AgeChip t={t0()} /></Show>
      <Show when={rel()}>{r => <EntityCode code={releaseLabel(r())} id={r()} kind="release" />}</Show>
    </div>
    <div class="stack-pad">
      <Show when={actionError()}>{err => <ErrorBanner text={`Publishing action failed: ${err()}`} />}</Show>
      <Show when={props.item}>{it => (
        <DetailSection title="Publishing">
          <KvRow icon="git-merge" label="Target" meta={it().baseBranch ? `${it().branch ?? 'head'} → ${it().baseBranch}` : (it().branch ?? undefined)} value={<code>{providerLabel(it().provider, it().kind)}</code>} />
          <Show when={it().headSha}>{sha => <KvRow icon="circle-check" label="Head commit" value={<code class="mono">{sha()}</code>} />}</Show>
          <Show when={typeof it().isDraft === 'boolean' || typeof it().autoMergeEnabled === 'boolean'}>
            <KvRow icon="git-pull-request" label={it().isDraft ? 'Draft' : 'Ready for review'}
              meta={it().autoMergeEnabled ? `Auto-merge active${it().mergeMethod ? ` (${it().mergeMethod})` : ''}` : 'Auto-merge inactive'}
              value={isOpen() ? <span class="acts">
                <Show when={it().isDraft && canAction('mark_ready')}>
                  <Button label={busyAction() === 'mark_ready' ? 'Marking ready…' : 'Mark ready'} kind="primary" busy={busyAction() === 'mark_ready'} disabledReason={busyAction() ? 'Action in progress' : undefined} onClick={() => runPub('mark_ready')} />
                </Show>
                <Show when={!it().isDraft && !it().autoMergeEnabled && canAction('request_merge')}>
                  <Button label={busyAction() === 'request_merge' ? 'Requesting merge…' : 'Request merge'} kind="primary" busy={busyAction() === 'request_merge'} disabledReason={busyAction() ? 'Action in progress' : undefined} onClick={() => runPub('request_merge')} />
                </Show>
                <Show when={it().autoMergeEnabled && canAction('cancel_merge_request')}>
                  <Button label={busyAction() === 'cancel_merge_request' ? 'Cancelling…' : 'Cancel merge'} busy={busyAction() === 'cancel_merge_request'} disabledReason={busyAction() ? 'Action in progress' : undefined} onClick={() => runPub('cancel_merge_request')} />
                </Show>
              </span> : undefined} />
          </Show>
          <Show when={props.capability && !props.capability.publicationSupported && props.capability.reason}>
            {reason => <p class="muted type-sm">{reason()}</p>}
          </Show>
        </DetailSection>
      )}</Show>
      <Show when={h().error && !h().data}><ErrorBanner text={`History did not load: ${h().error}`} /></Show>
      <Show when={x()} fallback={
        <Show when={props.item} fallback={
          <Show when={h().loading} fallback={<Empty text="This pull request is not in flight and has no recorded history." />}><Skeleton rows={4} /></Show>
        }>{it => <>
          <DetailSection title="Now">
            <KvRow icon={states.delivery[it().stage].icon} label={states.delivery[it().stage].label}
              meta={it().stage === 'pr_failed' ? 'the last checks on its head failed' : it().stage === 'mq_failed' ? 'its last merge-queue run failed; it left the queue' : undefined}
              value={<AgeChip t={ms(it().since)} alertAfter={alertOf(props.norms, it())} alertLabel="past the stage's P90" />} />
            <Show when={it().opened}>{op => <KvRow icon="git-pull-request" label="Opened" value={<AgeChip t={ms(op())} />} />}</Show>
          </DetailSection>
          <p class="muted type-sm">{parts().fleet ? 'The stage history is recorded once the pull request merges.' : 'Stage history is recorded for the fleet repository only.'}</p>
        </>}</Show>
      }>{cur => <>
        <Totals items={[
          { label: done() ? 'Open to installed' : 'Lead so far', m: sec((ms(done()) || props.now) - ms(cur().opened)), kind: 'duration' },
          { label: 'Queue runs', m: { value: cur().queueRuns, basis: 'measured' }, kind: 'count' },
          { label: 'Removed', m: { value: cur().queueRemovals, basis: 'measured' }, kind: 'count', note: cur().flakes ? `${cur().flakes} flaky` : undefined },
        ]} />
        <DetailSection title="Stages">
          <For each={ended()}>{([label, s, a, b]) => (
            <KvRow icon={states.delivery[s].icon} label={label}
              value={<span class="state-line"><TimeStrip from={t0()} to={Math.max(props.now, t0() + MIN)} start={ms(a)} end={b ? ms(b) : null} now={props.now} state={b ? 'succeeded' : 'running'} label={label} />
                <Num m={sec((b ? ms(b) : props.now) - ms(a))} kind="duration" /></span>} />
          )}</For>
        </DetailSection>
        <DetailSection title="Queue runs">
          <Show when={h().data!.runs.length} fallback={<Empty text="No merge-queue runs recorded." />}>
            <For each={h().data!.runs}>{r => (
              <KvRow
                icon={r.conclusion === 'success' ? 'circle-check' : r.conclusion === 'cancelled' ? 'circle-slash' : 'octagon-x'}
                label={`Run ${r.attempt > 1 ? `${r.id} · attempt ${r.attempt}` : r.id}`}
                meta={r.conclusion === 'success' ? 'all checks passed' : `${r.conclusion}: ${r.jobs.filter(j => j.conclusion !== 'success').map(j => j.name).join(', ') || 'no failed job recorded'}`}
                value={<Num m={sec(r.wallMs ?? null)} kind="duration" />} />
            )}</For>
          </Show>
        </DetailSection>
        <DetailSection title="Installed">
          <Show when={!uiOnly(cur().release)} fallback={
            <Show when={cur().uiOnSite} fallback={<Empty text={`UI-only release: hosts install nothing; ${unrecorded(cur(), h().data?.installsFrom ? ms(h().data!.installsFrom!) : null) ? `its UI on site was not recorded (recorded from ${day(ms(h().data!.installsFrom!), props.now)})` : 'the site does not serve it yet'}.`} />}>
              {onSite => <KvRow icon="panels-top-left" label="Site" meta="serves this UI or a newer one" value={<AgeChip t={ms(onSite())} />} />}
            </Show>
          }>
            <Show when={Object.keys(cur().installed ?? {}).length > 0} fallback={<Empty text={cur().released ? 'Not installed on any host yet.' : 'Not released yet.'} />}>
              <For each={Object.entries(cur().installed)}>{([host, t]) => (
                <KvRow icon="terminal" label={host} meta={cur().installedVersions?.[host]} value={<AgeChip t={ms(t)} />} />
              )}</For>
            </Show>
          </Show>
        </DetailSection>
      </>}</Show>
    </div>
  </article>;
}
