// Delivery work-area panels: overview (needs a look, moving now, pace) and pull request detail.
import {
  EntityCode, StateBadge, AgeChip, Ago, Num, Totals, TimeStrip, ProblemRow, DetailSection, KvRow, Button,
  Choice, Empty, ErrorBanner, Skeleton, Latest, DayChart, ShareBar, NormBar, states, capOf, day,
  type DayMark, type SharePart,
} from '../../../packages/ui/src/index.ts';
import { useOp } from '../store.ts';
import { DELIVERY_FAILED } from '../system-views.ts';
import {
  MIN, DAY, FLEET, ORDER, DONE, RANGES, uiOnly, releaseLabel, idOf, ms, fmt, kept, failed, q, sec,
  alertOf, unrecorded, due, doneAt, stageMs, totalMark,
  type Stage, type Item, type History, type HistoryPr, type Regressions, type Norms, type Bucket,
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
export function Overview({ now, items, incomplete, norms, hist, regressions, histError, fleetShown, range, buckets, picked, onRange, onPick, onOpen, href }: {
  now: number; items: Item[]; incomplete: boolean; norms: Norms; hist: History | null; regressions: Regressions['regressions']; histError: string | null; fleetShown: boolean;
  range: string; buckets: Bucket[]; picked: Bucket | null; onRange: (r: string) => void; onPick: (k: string) => void; onOpen: (it: Item) => void; href: (it: Item) => string;
}) {
  const moving = ORDER.filter(s => !DONE.includes(s)).flatMap(s => items.filter(it => it.stage === s));
  const over = (it: Item) => { const n = norms.stage(it); return !!n && now - ms(it.since) > n[1]; };
  const failedItems = items.filter(it => FAILED.includes(it.stage)), stuck = moving.filter(over);
  // One slowdown, one block (2026-11-20 decision): several metrics over their baseline are one cause to look at, named by the
  // pull requests they first went over with and what those took; one metric keeps its own row.
  const byPr = new Map<number, number>(); for (const g of regressions) if (g.firstPr) byPr.set(g.firstPr, (byPr.get(g.firstPr) ?? 0) + 1);
  const cause = [...byPr].sort((a, b) => b[1] - a[1]).map(([n, k]) => {
    const pr = hist?.prs.find(x => x.number === n), t = pr?.durations.total;
    return `#${n}${pr ? ` ${pr.title}` : ''}${typeof t === 'number' ? `: ${fmt(t)} from open to installed` : ''}${pr?.queueRemovals ? `, ${pr.queueRemovals} queue removals` : ''} (${k} ${k === 1 ? 'metric' : 'metrics'})`;
  });
  const worst = regressions.toSorted((a, b) => b.currentMs / (b.baselineMs || 1) - a.currentMs / (a.baselineMs || 1));
  const look = [
    ...(regressions.length > 1 ? [<div key="slower" class="stack">
      <ProblemRow severity="warning" title={`Delivery got slower: ${regressions.length} metrics over their 14-day baseline`}
        facts={`worst ${keyLabel(worst[0].key)} ${worst[0].metric.toUpperCase()} ${fmt(worst[0].currentMs)} against ${fmt(worst[0].baselineMs)}`}
        actions={[...(worst[0]?.compareUrl ? [{ label: 'Compare', onClick: () => window.open(worst[0].compareUrl!, '_blank', 'noopener') }] : [])]} />
      <p class="muted small-note">{worst.map(g => `${keyLabel(g.key)} ${g.metric.toUpperCase()} ${fmt(g.currentMs)} / ${fmt(g.baselineMs)}`).join(' · ')}</p>
      {cause.length > 0 && <p class="small-note">First over with {cause.join('; ')}.</p>}
    </div>]
    : regressions.map(g => <ProblemRow key={'r' + g.key + g.metric} severity="warning" title={`${keyLabel(g.key)} ${g.metric.toUpperCase()} is ${fmt(g.currentMs)}, baseline ${fmt(g.baselineMs)}`}
      facts={`${g.firstPr ? `Since #${g.firstPr} · ` : ''}${g.n} pull requests in the last 24 h`} actions={[...(g.compareUrl ? [{ label: 'Compare', onClick: () => window.open(g.compareUrl!, '_blank', 'noopener') }] : [])]} />)),
    ...failedItems.map(it => <ProblemRow key={'f' + idOf(it)} severity="failure" title={`#${it.number} ${it.stage === 'mq_failed' ? 'removed from the merge queue' : 'checks failed'}`} facts={<>{it.title} · <Ago t={ms(it.since)} /></>}
      actions={[{ label: `Open #${it.number}`, primary: true, onClick: () => onOpen(it) }]} />),
    ...stuck.map(it => <ProblemRow key={'s' + idOf(it)} severity="warning" title={`#${it.number} ${it.stage === 'installing' && uiOnly(it.release) ? 'waiting for the UI on site' : states.delivery[it.stage].label.toLowerCase()} for ${fmt(now - ms(it.since))}, P90 ${fmt(norms.stage(it)![1])}`}
      facts={it.title} actions={[{ label: `Open #${it.number}`, primary: true, onClick: () => onOpen(it) }]} />),
  ];
  return <div class="stack-pad" data-ui="delivery-overview">
    {look.length > 0 && <DetailSection title="Needs a look"><Latest items={look} n={3} /></DetailSection>}
    <DetailSection title="Moving now">
      {!moving.length ? <Empty text={incomplete ? 'Unavailable. GitHub data is incomplete.' : 'Nothing in flight.'} />
        : <div class="vl-wrap" role="grid" aria-label="Pull requests in flight" style={{ '--cols': '1.25rem 3.5rem minmax(0,1fr) minmax(0,1.3fr) 2.75rem 4.5rem' }}>{moving.map(it => {
          const n = norms.stage(it), el = Math.max(0, now - ms(it.since)), red = over(it);
          return <div key={idOf(it)} class="vl-row static" role="row">
            <span class="lead"><StateBadge family="delivery" state={it.stage} compact /></span>
            <a class="mono row-link" href={href(it)} onClick={e => { e.preventDefault(); onOpen(it); }}>#{it.number}</a>
            <span class="muted" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{it.title}</span>
            <NormBar elapsed={el} p50={n?.[0]} p90={n?.[1]} label={`${states.delivery[it.stage].label} #${it.number}`} value={fmt} />
            <span class="r"><AgeChip t={ms(it.since)} alertAfter={alertOf(norms, it)} alertLabel="past the stage's P90" /></span>
            <span class={`r mono type-sm${red ? ' t-danger' : ' muted'}`} style={red ? { color: 'var(--tone-fg)' } : undefined}
              data-tip={n ? `${states.delivery[it.stage].label}: P50 ${fmt(n[0])}, P90 ${fmt(n[1])} over 14 days` : undefined}>{n ? `P90 ${fmt(n[1])}` : 'no norm'}</span>
          </div>;
        })}</div>}
      {incomplete && moving.length > 0 && <p class="muted type-sm">The last rows read; GitHub coverage is incomplete.</p>}
    </DetailSection>
    {fleetShown && <Pace hist={hist} error={histError} now={now} norms={norms} range={range} buckets={buckets} picked={picked} onRange={onRange} onPick={onPick} />}
  </div>;
}

function Pace({ hist, error, now, norms, range, buckets, picked, onRange, onPick }: {
  hist: History | null; error: string | null; now: number; norms: Norms; range: string; buckets: Bucket[]; picked: Bucket | null; onRange: (r: string) => void; onPick: (k: string) => void;
}) {
  const choice = <div class="state-line"><Choice label="Range" options={RANGES} value={range} onChange={onRange} />
    {hist?.backfilledFrom && buckets[0] && buckets[0].start <= ms(hist.backfilledFrom) && <span class="muted type-sm">recorded from {day(ms(hist.backfilledFrom), now)}</span>}</div>;
  if (error && !hist) return <DetailSection title="Pace">{choice}<ErrorBanner text={`Delivery history did not load: ${error}`} /></DetailSection>;
  if (!hist || !buckets.length) return <DetailSection title="Pace">{choice}<Skeleton rows={3} /></DetailSection>;
  const from = buckets[0].start, prs = hist.prs.filter(x => ms(x.merged) >= from);
  const inB = (b: Bucket) => prs.filter(x => ms(x.merged) >= b.start && ms(x.merged) < b.end);
  const installsFrom = hist.installsFrom ? ms(hist.installsFrom) : null;
  const total = buckets.map(b => totalMark(inB(b), b, installsFrom));
  const merged: DayMark[] = buckets.map(b => ({ v: inB(b).length, partial: b.today ? 'so far' : undefined }));
  const totals = prs.map(x => x.durations.total).filter((v): v is number => typeof v === 'number');
  const removals = prs.reduce((n, x) => n + x.queueRemovals, 0), flakes = prs.reduce((n, x) => n + x.flakes, 0);
  // Pull requests still on their way are not counted as unmeasured, as on the chart.
  const scope = (picked ? inB(picked) : prs).filter(x => due(x, installsFrom)), done = scope.filter(complete);
  const mean = (keys: string[]) => done.reduce((s, x) => s + partOf(x, keys), 0) / (done.length || 1);
  const share: SharePart[] = PARTS.map(p => ({ label: p.label, v: mean(p.keys), tone: p.tone, tip: `${p.label}: mean ${fmt(mean(p.keys))}, P50 ${fmt(q(done.map(x => partOf(x, p.keys)), 0.5) ?? 0)}` }));
  const days = Math.max(1, (now - from) / DAY), base = norms.total;
  const pick = (k: string) => onPick(k);
  return <DetailSection title="Pace"><div style={{ display: 'grid', gap: 'var(--space-4)', minWidth: 0 }}>
    {choice}
    <Totals items={[
      { label: 'Open to installed P50', m: sec(q(totals, 0.5)), kind: 'duration', note: base ? `baseline ${fmt(base[0])}` : undefined },
      { label: 'Open to installed P90', m: sec(q(totals, 0.9)), kind: 'duration', note: base ? `baseline ${fmt(base[1])}` : undefined },
      { label: 'Merged', m: { value: prs.length, basis: 'measured' }, kind: 'count', note: range === '24h' ? undefined : `${(prs.length / days).toFixed(prs.length / days < 10 ? 1 : 0)} a day` },
      { label: 'Queue removals', m: { value: removals, basis: 'measured' }, kind: 'count', note: flakes ? `${flakes} flaky` : undefined },
    ]} />
    <DayChart title="Open to installed" label="Open to installed per day, P50 and P90" kind="range" cols={buckets} marks={total} value={fmt}
      cap={capOf(total.flatMap(m => [m.v, m.hi]), { floor: 10 * MIN, unit: MIN })} capNote="about P95 of the figures shown; a figure over four times their median is drawn cut, with its value"
      names={['P50', 'P90']} values alertAbove={base?.[1]} alertLabel={base ? `above the 14-day P90 ${fmt(base[1])}` : undefined} selected={picked?.key ?? null} onSelect={pick} />
    <DayChart title="Merged" label="Pull requests merged per day" kind="bar" size="sm" cols={buckets} marks={merged} value={v => String(Math.round(v))}
      cap={capOf(merged.map(m => m.v), { floor: 4, fence: Infinity })} tone="d2" axis selected={picked?.key ?? null} onSelect={pick} />
    <ShareBar title="Where the time goes" label="Where the time goes" parts={share} value={fmt}
      note={`mean per pull request, ${done.length} of ${scope.length} with every stage measured${picked ? ` · ${picked.tip ?? picked.label}` : ''}`} />
  </div></DetailSection>;
}

/** The opened pull request: its stages on one time axis, queue runs, release and installs per host (the fleet repository
 *  has a recorded history; other repositories show what the current read knows). */
export function PrDetail({ id, item, now, norms, onClose }: { id: string; item?: Item; now: number; norms: Norms; onClose: () => void }) {
  const [owner, repo, num] = id.split('~'), number = Number(num), fleet = `${owner}/${repo}` === FLEET;
  const hd = useOp('delivery_history', fleet ? { number } : null, { every: 600000 });
  const h = { data: kept(hd), error: failed(hd), loading: fleet && hd.data === null && !failed(hd) };
  const x = h.data?.prs.find(p => p.number === number);
  // The merge queue splits at its last successful run's start: waiting for a runner before, its checks after.
  const qStart = h.data?.runs.filter(r => r.conclusion === 'success' && r.started).map(r => r.started!).sort().at(-1);
  const title = item?.title || x?.title || `#${number}`;
  const steps: (readonly [string, Stage, string | undefined, string | undefined])[] = x ? [
    ['PR checks', 'pr_ci', x.opened, x.prCiDone],
    ...(qStart && x.enqueued ? [['Waiting in queue', 'mq_wait', x.enqueued, qStart], ['Queue checks', 'mq_ci', qStart, x.mqCiDone ?? x.merged]] as const : [['Merge queue', 'mq_ci', x.enqueued, x.mqCiDone ?? x.merged]] as const),
    ['Releasing', 'releasing', x.merged, x.released], [uiOnly(x.release) ? 'UI on site' : 'Installing', 'installing', x.released, doneAt(x)],
  ] : [];
  const done = x && doneAt(x), rel = item?.release || x?.release;
  // A stage without its own end ends where a later one starts; only the last stage reached runs to now.
  const known = steps.filter(([, , a]) => a);
  const ended = known.map(([l, s, a, b], i) => [l, s, a, b ?? known.slice(i + 1).map(k => k[2]).find(Boolean)] as const);
  const t0 = ms(x?.opened ?? item?.opened ?? item?.since), t1 = now;
  const url = item?.url || x?.url;
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={title}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><span class="muted mono">{owner}/{repo} #{number}</span><span class="grow" />
      {url && <Button label="Open on GitHub" icon="external-link" iconOnly onClick={() => window.open(url, '_blank', 'noopener')} />}</header>
    <h2 class="detail-title">{title}</h2>
    <div class="state-line">{item && <StateBadge family="delivery" state={item.stage} />}{x?.author && <span class="muted">by {x.author}</span>}{t0 > 0 && <AgeChip t={t0} />}
      {rel && <EntityCode code={releaseLabel(rel)} id={rel} kind="release" />}</div>
    <div class="stack-pad">
      {h.error && !h.data && <ErrorBanner text={`History did not load: ${h.error}`} />}
      {x ? <>
        <Totals items={[
          { label: done ? 'Open to installed' : 'Lead so far', m: sec((ms(done) || now) - ms(x.opened)), kind: 'duration' },
          { label: 'Queue runs', m: { value: x.queueRuns, basis: 'measured' }, kind: 'count' },
          { label: 'Removed', m: { value: x.queueRemovals, basis: 'measured' }, kind: 'count', note: x.flakes ? `${x.flakes} flaky` : undefined },
        ]} />
        <DetailSection title="Stages">{ended.map(([label, s, a, b]) => <KvRow key={label} icon={states.delivery[s].icon} label={label}
          value={<span class="state-line"><TimeStrip from={t0} to={Math.max(t1, t0 + MIN)} start={ms(a)} end={b ? ms(b) : null} now={now} state={b ? 'succeeded' : 'running'} label={label} />
            <Num m={sec((b ? ms(b) : now) - ms(a))} kind="duration" /></span>} />)}</DetailSection>
        <DetailSection title="Queue runs">{!h.data!.runs.length ? <Empty text="No merge-queue runs recorded." /> : h.data!.runs.map(r => <KvRow key={r.id + ':' + r.attempt}
          icon={r.conclusion === 'success' ? 'circle-check' : r.conclusion === 'cancelled' ? 'circle-slash' : 'octagon-x'} label={`Run ${r.attempt > 1 ? `${r.id} · attempt ${r.attempt}` : r.id}`}
          meta={r.conclusion === 'success' ? 'all checks passed' : `${r.conclusion}: ${r.jobs.filter(j => j.conclusion !== 'success').map(j => j.name).join(', ') || 'no failed job recorded'}`}
          value={<Num m={sec(r.wallMs ?? null)} kind="duration" />} />)}</DetailSection>
        <DetailSection title="Installed">{uiOnly(x.release) ? x.uiOnSite ? <KvRow icon="panels-top-left" label="Site" meta="serves this UI or a newer one" value={<AgeChip t={ms(x.uiOnSite)} />} />
            : <Empty text={`UI-only release: hosts install nothing; ${unrecorded(x, h.data?.installsFrom ? ms(h.data.installsFrom) : null) ? `its UI on site was not recorded (recorded from ${day(ms(h.data!.installsFrom!), now)})` : 'the site does not serve it yet'}.`} />
          : Object.keys(x.installed ?? {}).length === 0 ? <Empty text={x.released ? 'Not installed on any host yet.' : 'Not released yet.'} />
          : Object.entries(x.installed).map(([host, t]) => <KvRow key={host} icon="terminal" label={host} meta={x.installedVersions?.[host]} value={<AgeChip t={ms(t)} />} />)}</DetailSection>
      </> : item ? <>
        <DetailSection title="Now">
          <KvRow icon={states.delivery[item.stage].icon} label={states.delivery[item.stage].label}
            meta={item.stage === 'pr_failed' ? 'the last checks on its head failed' : item.stage === 'mq_failed' ? 'its last merge-queue run failed; it left the queue' : undefined}
            value={<AgeChip t={ms(item.since)} alertAfter={alertOf(norms, item)} alertLabel="past the stage's P90" />} />
          {item.opened && <KvRow icon="git-pull-request" label="Opened" value={<AgeChip t={ms(item.opened)} />} />}
        </DetailSection>
        <p class="muted type-sm">{fleet ? 'The stage history is recorded once the pull request merges.' : 'Stage history is recorded for the fleet repository only.'}</p>
      </> : h.loading ? <Skeleton rows={4} /> : <Empty text="This pull request is not in flight and has no recorded history." />}
    </div>
  </article>;
}
