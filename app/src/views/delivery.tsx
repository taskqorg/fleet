// Delivery (docs/design-system/pages/delivery.md): the pull requests of every connected project by delivery stage in the
// control column, like Tasks; the work area answers, in this order, what needs a look, what moves now and what the pace
// is, or shows the opened pull request. Data: delivery_status (GitHub read by the coordinator every minute with a key),
// delivery_history, delivery_baseline and delivery_regressions (the fleet repository's recorded history). Both reach the
// page through the change stream (kind delivery), so nothing here waits for a poll.
import { useCallback } from 'preact/hooks';
import {
  ListDetail, ListFrame, FilterBar, StateCounters, GroupHeader, GroupControl, SortControl, StateBadge, AgeChip, Num, Totals, AreaChart, TimeStrip,
  ProblemRow, DetailSection, KvRow, Button, Choice, Empty, ErrorBanner, Skeleton, Fact, RowFacts, Latest, matchSearch, states,
  type ChartSeries, type ChartBucket, type StateOf, type StripPart,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';
import { useUrlView, useSearchBox } from '../url.ts';
import { parseView } from '../../../packages/view-url/view-url.ts';
import { focusIds, prefsRead } from '../prefs.ts';
import type { Change } from '../../../contracts/generated/ops.ts';

type Stage = StateOf<'delivery'>;
type Status = Ops['delivery_status']['reply'];
type Item = Status['items'][number];
type History = Ops['delivery_history']['reply'];
type HistoryPr = History['prs'][number];
type Row = { kind: 'group'; key: string; label: string; stage?: Stage; count: number } | { kind: 'item'; it: Item };

const MIN = 6e4, HOUR = 36e5, DAY = 864e5;
const FLEET = 'taskqorg/fleet';
// Seconds in a stage after which a pull request needs a look; none for the stages people work in.
const LIMIT: Partial<Record<Stage, number>> = { pr_ci: 1800, mq_wait: 900, mq_ci: 1800, releasing: 1200, installing: 1200 };
// Attention order: failed checks first, then the merge queue and checks, then the release tail.
const ORDER: Stage[] = ['pr_failed', 'mq_ci', 'mq_wait', 'pr_ci', 'pr_open', 'releasing', 'installing', 'installed', 'merged'];
const DONE: Stage[] = ['installed', 'merged'];
const GROUPS = [['stage', 'Stage'], ['project', 'Project'], ['none', 'None']] as const;
const SORTS = [['since', 'In stage since', ['Newest first', 'Oldest first']], ['opened', 'Opened', ['Newest first', 'Oldest first']]] as const;
const RANGES = [{ id: '24h', label: '24 h', ms: DAY, bucket: 2 * HOUR }, { id: '7d', label: '7 days', ms: 7 * DAY, bucket: DAY }, { id: '30d', label: '30 days', ms: 30 * DAY, bucket: DAY }];
const ms = (s?: string | null) => s ? Date.parse(s) : NaN;
const late = (it: Item, now: number) => LIMIT[it.stage] !== undefined && now - ms(it.since) >= LIMIT[it.stage]! * 1000;
/** The focus segment of a pull request: owner~repo~number (stable, readable, no slash). */
const idOf = (it: { repo: string; number: number }) => `${it.repo.replace('/', '~')}~${it.number}`;
// Current rows change with delivery-state:<project>; the recorded history (records and the regression check) with the rest.
const touchesStatus = (c: Change) => c.kind === 'delivery' && c.id.startsWith('delivery-state:');
const touchesHistory = (c: Change) => c.kind === 'delivery' && !c.id.startsWith('delivery-state:');
const NONE = 'none' as const; // no history kept (404): known, unlike still loading
const p = (xs: number[], q: number) => { if (!xs.length) return null; const s = [...xs].sort((a, b) => a - b); return s[Math.min(s.length - 1, Math.ceil(q * s.length) - 1)]; };
const sec = (v: number | null | undefined) => ({ value: v == null ? null : v / 1000, basis: v == null ? 'unavailable' as const : 'measured' as const, reason: v == null ? 'no pull requests' : undefined });

type DeliveryExtra = { group?: string; sort?: string; range?: string; at?: string };
/** The Delivery keys of a link: valid values other than the defaults. A link may carry anything (an old one has the text "undefined"). */
export function deliveryExtra(x: Record<string, string | undefined> | undefined): DeliveryExtra {
  const out: DeliveryExtra = {};
  if (x?.group && x.group !== 'stage' && GROUPS.some(g => g[0] === x.group)) out.group = x.group;
  if (x?.sort && x.sort !== 'since' && /^-?[a-z]+$/.test(x.sort) && SORTS.some(f => f[0] === x.sort!.replace(/^-/, ''))) out.sort = x.sort;
  if (x?.range && x.range !== '7d' && RANGES.some(r => r.id === x.range)) out.range = x.range;
  if (x?.at && /^[1-9]\d*$/.test(x.at)) out.at = x.at;
  return out;
}

export function Delivery({ id }: { id?: string }) {
  const { view, set, href } = useUrlView('delivery');
  const ex = deliveryExtra(view.extra);
  const [query, setQuery] = useSearchBox(view.q, q => set({ q: q || undefined }, 'type'));
  const focus = focusIds() ?? [];
  const status = useDoc<Status>(prefsRead.value ? 'delivery:' + focus.join(',') : null, () => op('delivery_status', { projectIds: focusIds() }), touchesStatus, 300000);
  const range = RANGES.find(r => r.id === ex.range) ?? RANGES[1];
  const now = Date.now();
  // The recorded history covers the fleet repository only; a 404 (no history kept) or a focus away from fleet hides Pace.
  const fleetShown = !focus.length || focus.includes('fleet');
  const paceDoc = useDoc<{ h: History; b: Ops['delivery_baseline']['reply']; r: Ops['delivery_regressions']['reply'] } | typeof NONE>(
    prefsRead.value && fleetShown ? 'delivery-pace:' + range.id : null,
    () => Promise.all([op('delivery_history', { since: new Date(Date.now() - range.ms).toISOString() }), op('delivery_baseline', { days: 14 }), op('delivery_regressions', {})])
      .then(([h, b, r]) => ({ h, b, r }), e => { if (/404/.test(String(e?.message ?? e))) return NONE; throw e; }), touchesHistory, 600000);
  const pace = { data: paceDoc.data === NONE ? null : paceDoc.data, error: paceDoc.error, none: paceDoc.data === NONE };

  const data = status.data;
  const items = data?.items ?? [];
  const incomplete = !!data?.error;
  const stages = (view.state ?? []) as Stage[];
  const group = ex.group ?? 'stage', sort = ex.sort ?? 'since';
  // Built from the URL at the moment of the change (url.ts), keeping only valid values that are not the defaults.
  const setExtra = (patch: Partial<Record<keyof DeliveryExtra, string | undefined>>) => set({ extra: deliveryExtra({ ...parseView(location.pathname + location.search)?.extra, ...patch }) });
  // A picked pace bucket shows the pull requests merged in it (from the history), in place of the current rows.
  const at = ex.at ? Number(ex.at) : null;
  const picked: Item[] | null = at !== null && pace.data ? pace.data.h.prs.filter(x => ms(x.merged) >= at && ms(x.merged) < at + range.bucket).map(fromHistory) : null;
  const source = picked ?? items;
  const shown = source.filter(it => (!stages.length || stages.includes(it.stage)) && matchSearch(query, `${it.title} #${it.number} ${it.repo}`, { state: it.stage, project: it.projectId }));
  const desc = sort.startsWith('-'), field = desc ? sort.slice(1) : sort;
  const key = (it: Item) => ms(field === 'opened' ? it.opened : it.since) || 0;
  shown.sort((a, b) => desc ? key(b) - key(a) : key(a) - key(b));
  const counts: Partial<Record<Stage, number | null>> = {};
  for (const s of ORDER) counts[s] = incomplete && !picked ? null : source.filter(it => it.stage === s).length;
  const rows: Row[] = [];
  if (group === 'none') for (const it of shown) rows.push({ kind: 'item', it });
  else {
    const keys = group === 'stage' ? ORDER : [...new Set(shown.map(it => it.projectId))].sort();
    for (const k of keys) {
      const its = shown.filter(it => (group === 'stage' ? it.stage : it.projectId) === k);
      if (!its.length) continue;
      rows.push(group === 'stage' ? { kind: 'group', key: k, label: states.delivery[k as Stage].label, stage: k as Stage, count: its.length } : { kind: 'group', key: k, label: k || 'No project', count: its.length });
      for (const it of its) rows.push({ kind: 'item', it });
    }
  }
  const open = items.find(it => idOf(it) === id) ?? (pace.data?.h.prs.map(fromHistory).find(it => idOf(it) === id));
  const go = useCallback((it?: Item) => set({ focus: it ? idOf(it) : undefined }), [set]);

  const list = <>
    <header class="page-h"><h1>Delivery</h1><span class="grow" />
      {data?.source === 'public' && <span class="chip-age t-attention" data-tip="No github key in API keys: unauthenticated reads of the fleet repository every 6 minutes">public GitHub</span>}
      {data?.refreshedAt && <AgeChip t={ms(data.refreshedAt)} now={now} alertAfter={900} alertLabel="GitHub read is stale" />}</header>
    <section class="panel">
      <ListFrame bar={<FilterBar query={query} onQuery={setQuery} placeholder="Search pull requests, state:mq_wait project:fleet" shown={shown.length} total={source.length}
        chips={picked && at !== null ? [{ field: 'Merged', value: bucketLabel(at, range.bucket) }] : []} onRemove={() => setExtra({ at: undefined })}
        counters={<StateCounters family="delivery" label="Pull requests by stage" counts={counts} active={stages} onAll={() => set({ state: [] })}
          onToggle={s => set({ state: stages.includes(s) ? stages.filter(x => x !== s) : [...stages, s] })} />}
        tools={<>
          <GroupControl value={group as typeof GROUPS[number][0]} options={GROUPS} onChange={g => setExtra({ group: g === 'stage' ? undefined : g })} />
          <SortControl value={sort} fields={SORTS} onChange={v => setExtra({ sort: v === 'since' ? undefined : v })} />
        </>} />}>
        {status.error && !data ? <ErrorBanner text={`Delivery did not load: ${status.error}`} />
          : !data ? <Skeleton rows={6} />
          : <>
            {data.error && <ErrorBanner text={`GitHub: ${data.error}`} />}
            {incomplete && <p class="muted type-sm stack-pad">Some GitHub data is unavailable: current totals are unknown; the rows are the last ones read.</p>}
            {!rows.length ? <Empty text={picked ? 'No pull requests merged then.' : stages.length || query ? 'No pull requests match.' : incomplete ? 'Unavailable. GitHub data is incomplete.' : 'No pull requests in flight.'} />
              : <div class="vl-wrap" role="grid" aria-label="Pull requests" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>{rows.map(r => r.kind === 'group'
                ? <div key={'g' + r.key} class="vl-row static grp" role="row"><GroupHeader icon={r.stage ? states.delivery[r.stage].icon : 'folder-open'} tone={r.stage ? states.delivery[r.stage].tone : 'neutral'} label={r.label} count={r.count} open onToggle={() => {}} /></div>
                : <PrRow key={idOf(r.it)} it={r.it} now={now} sel={idOf(r.it) === id} href={href({ focus: idOf(r.it) })} onOpen={() => go(r.it)} />)}</div>}
          </>}
      </ListFrame>
    </section>
  </>;
  return <ListDetail list={list} detailKey={id}
    idle={<Overview now={now} items={items} incomplete={incomplete} pace={pace.data ?? null} paceError={pace.error} fleetShown={fleetShown && !pace.none} range={range} at={at} href={it => href({ focus: idOf(it) })}
      onRange={r => setExtra({ range: r === '7d' ? undefined : r, at: undefined })} onAt={a => setExtra({ at: a === null ? undefined : String(a) })} onOpen={go} />}
    detail={id ? <PrDetail key={id} id={id} item={open} now={now} onClose={() => go()} /> : undefined} />;
}

/** A recorded (merged) pull request as a list row: its stage from how far it got. */
function fromHistory(x: HistoryPr): Item {
  const installed = !!x.installed?.coordinator, stage: Stage = installed ? 'installed' : x.released ? 'installing' : 'releasing';
  return { projectId: 'fleet', repo: FLEET, number: x.number, title: x.title, url: x.url, stage, since: (installed ? x.installed.coordinator : x.released ?? x.merged) as string, opened: x.opened, merged: x.merged, release: x.release };
}
const bucketLabel = (at: number, len: number) => len >= DAY ? new Date(at).toUTCString().slice(5, 11) : `${new Date(at).toISOString().slice(11, 16)} UTC`;

function PrRow({ it, now, sel, href, onOpen }: { it: Item; now: number; sel: boolean; href: string; onOpen: () => void }) {
  return <div role="row" class={`vl-row static${sel ? ' sel' : ''}`}>
    <span class="lead"><StateBadge family="delivery" state={it.stage} compact /></span>
    <span class="tt" style={{ flexWrap: 'wrap', rowGap: 0 }}>
      <a class="title row-link" style={{ flex: '0 0 100%' }} href={href} onClick={e => { e.preventDefault(); onOpen(); }}>{it.title || `#${it.number}`}</a>
      <span class="id">#{it.number}</span>{it.repo !== FLEET && <span class="id">{it.repo}</span>}{it.release && <span class="id">{it.release.replace(/^v/, '')}</span>}</span>
    <span class="rz"><RowFacts><Fact tip={`${states.delivery[it.stage].label} for`}><AgeChip t={ms(it.since)} now={now} alertAfter={LIMIT[it.stage]} alertLabel="past the stage limit" /></Fact></RowFacts></span>
  </div>;
}

/** The work area while no pull request is open: what needs a look, what moves now, the pace. */
function Overview({ now, items, incomplete, pace, paceError, fleetShown, range, at, onRange, onAt, onOpen, href }: {
  now: number; items: Item[]; incomplete: boolean; pace: { h: History; b: Ops['delivery_baseline']['reply']; r: Ops['delivery_regressions']['reply'] } | null; paceError: string | null;
  fleetShown: boolean; range: typeof RANGES[number]; at: number | null; onRange: (r: string) => void; onAt: (a: number | null) => void; onOpen: (it: Item) => void; href: (it: Item) => string;
}) {
  const moving = ORDER.filter(s => !DONE.includes(s)).flatMap(s => items.filter(it => it.stage === s));
  const failed = items.filter(it => it.stage === 'pr_failed'), stuck = moving.filter(it => late(it, now));
  const look = [
    ...(pace?.r.regressions ?? []).map(g => <ProblemRow key={'r' + g.key + g.metric} severity="warning" title={`${keyLabel(g.key)} ${g.metric.toUpperCase()} is ${mins(g.currentMs)}, baseline ${mins(g.baselineMs)}`}
      facts={`${g.firstPr ? `Since #${g.firstPr} · ` : ''}${g.n} pull requests in the last 24 h`} actions={[...(g.compareUrl ? [{ label: 'Compare', onClick: () => open(g.compareUrl!) }] : [])]} />),
    ...failed.map(it => <ProblemRow key={'f' + idOf(it)} severity="failure" title={`#${it.number} checks failed`} facts={`${it.title} · ${fmtAge(ms(it.since), now)} ago`} actions={[{ label: `Open #${it.number}`, primary: true, onClick: () => onOpen(it) }]} />),
    ...stuck.map(it => <ProblemRow key={'s' + idOf(it)} severity="warning" title={`#${it.number} ${states.delivery[it.stage].label.toLowerCase()} for ${fmtAge(ms(it.since), now)}, limit ${LIMIT[it.stage]! / 60}m`}
      facts={it.title} actions={[{ label: `Open #${it.number}`, primary: true, onClick: () => onOpen(it) }]} />),
  ];
  return <div class="stack-pad" data-ui="delivery-overview">
    {look.length > 0 && <DetailSection title="Needs a look"><Latest items={look} n={3} /></DetailSection>}
    <DetailSection title="Moving now, 6 hours">
      {!moving.length ? <Empty text={incomplete ? 'Unavailable. GitHub data is incomplete.' : 'Nothing in flight.'} />
        : <div class="vl-wrap" role="grid" aria-label="Pull requests in flight" style={{ '--cols': '3.5rem minmax(0,1fr) 3rem' }}>{moving.map(it =>
          <div key={idOf(it)} class="vl-row static" role="row">
            <a class="mono row-link" href={href(it)} onClick={e => { e.preventDefault(); onOpen(it); }}>#{it.number}</a>
            <TimeStrip from={now - 6 * HOUR} to={now} now={now} label={`#${it.number} ${it.title}`} parts={lane(it, now)} />
            <span class="r"><AgeChip t={ms(it.since)} now={now} alertAfter={LIMIT[it.stage]} alertLabel="past the stage limit" /></span>
          </div>)}</div>}
      {incomplete && moving.length > 0 && <p class="muted type-sm">The last rows read; GitHub coverage is incomplete.</p>}
    </DetailSection>
    {fleetShown && <Pace pace={pace} error={paceError} now={now} range={range} at={at} onRange={onRange} onAt={onAt} />}
  </div>;
}
const open = (url: string) => window.open(url, '_blank', 'noopener');
const mins = (ms: number) => ms >= HOUR ? `${Math.floor(ms / HOUR)}h ${String(Math.round(ms % HOUR / MIN)).padStart(2, '0')}m` : `${Math.round(ms / MIN)}m`;
const fmtAge = (t: number, now: number) => mins(Math.max(0, now - t));
const KEYS: Record<string, string> = { pr_ci: 'PR checks', enqueue: 'Enqueue', queue: 'Merge queue', merge: 'Merge', release: 'Release', install: 'Install', ui: 'UI on site', lead: 'Lead', total: 'Open to installed', availability: 'Availability', time_to_available: 'Time to available', run: 'Queue run' };
const keyLabel = (k: string) => KEYS[k] ?? k;

/** A pull request's lane: what came before its current stage (known only as one span), then the current stage. */
function lane(it: Item, now: number): StripPart[] {
  const since = ms(it.since), opened = ms(it.opened), over = late(it, now);
  const parts: StripPart[] = [];
  if (opened && opened < since) parts.push({ start: opened, end: since, tone: 'neutral', label: 'Earlier stages' });
  parts.push({ start: since, end: null, tone: over ? 'danger' : states.delivery[it.stage].tone, label: `${states.delivery[it.stage].label}${over ? ', past the limit' : ''}` });
  return parts;
}

// Where the time goes: a pull request's stage durations folded into four layers, bottom up in pipeline order.
const LAYERS: { label: string; tone: ChartSeries['tone']; keys: string[] }[] = [
  { label: 'PR checks', tone: 'd1', keys: ['pr_ci'] }, { label: 'Queue', tone: 'd2', keys: ['enqueue', 'queue', 'merge'] },
  { label: 'Release', tone: 'd3', keys: ['release'] }, { label: 'Install', tone: 'd4', keys: ['install'] },
];

function Pace({ pace, error, now, range, at, onRange, onAt }: { pace: { h: History; b: Ops['delivery_baseline']['reply'] } | null; error: string | null; now: number; range: typeof RANGES[number]; at: number | null; onRange: (r: string) => void; onAt: (a: number | null) => void }) {
  const choice = <div class="state-line"><Choice label="Range" options={RANGES} value={range.id} onChange={onRange} /></div>;
  if (error && !pace) return <DetailSection title="Pace">{choice}<ErrorBanner text={`Delivery history did not load: ${error}`} /></DetailSection>;
  if (!pace) return <DetailSection title="Pace">{choice}<Skeleton rows={3} /></DetailSection>;
  const from = now - range.ms, prs = pace.h.prs.filter(x => ms(x.merged) >= from);
  const total = prs.map(x => x.durations.total).filter((v): v is number => typeof v === 'number');
  const base = (k: string) => pace.b.rolling.stats.find(s => s.key === k);
  const removals = prs.reduce((n, x) => n + x.queueRemovals, 0), flakes = prs.reduce((n, x) => n + x.flakes, 0);
  const n = Math.ceil(range.ms / range.bucket), start = (i: number) => now - (n - i) * range.bucket;
  const inBucket = (i: number) => prs.filter(x => ms(x.merged) >= start(i) && ms(x.merged) < start(i) + range.bucket);
  const med = (xs: HistoryPr[], keys: string[]) => p(xs.map(x => keys.reduce((s, k) => s + (x.durations[k] ?? 0), 0)).filter(v => v > 0), 0.5) ?? 0;
  // Step series: one point per bucket start, and the last value again at now, so the last bucket is drawn to the edge.
  const steps = (v: (i: number) => number) => [...Array.from({ length: n }, (_, i) => ({ at: start(i), v: v(i) })), { at: now, v: v(n - 1) }];
  const byStage: ChartSeries[] = LAYERS.map(l => ({ label: l.label, tone: l.tone, step: true, points: steps(i => med(inBucket(i), l.keys) / MIN) }));
  const merged: ChartSeries[] = [{ label: 'Merged', tone: 'd1', step: true, points: steps(i => inBucket(i).length) }];
  const stackMax = Math.max(10, ...Array.from({ length: n }, (_, i) => byStage.reduce((s, l) => s + l.points[i].v, 0)));
  const buckets: ChartBucket[] = n <= 31 ? Array.from({ length: n }, (_, i) => ({ key: String(start(i)), label: bucketLabel(start(i), range.bucket), valueLabel: `${inBucket(i).length} merged` })) : [];
  const perDay = prs.length / (range.ms / DAY);
  return <DetailSection title="Pace">
    {choice}
    <Totals items={[
      { label: 'Open to installed P50', m: sec(p(total, 0.5)), kind: 'duration', note: base('total') ? `baseline ${mins(base('total')!.p50Ms)}` : undefined },
      { label: 'Open to installed P90', m: sec(p(total, 0.9)), kind: 'duration', note: base('total') ? `baseline ${mins(base('total')!.p90Ms)}` : undefined },
      { label: 'Merged', m: { value: prs.length, basis: 'measured' }, kind: 'count', note: range.ms > DAY ? `${perDay.toFixed(perDay < 10 ? 1 : 0)} a day` : undefined },
      { label: 'Queue removals', m: { value: removals, basis: 'measured' }, kind: 'count', note: flakes ? `${flakes} flaky` : undefined },
    ]} />
    <div class="cols">
      <div><b class="type-sm">Where the time goes, P50 per stage</b>
        <AreaChart label="Time per stage, P50 per bucket" series={byStage} stacked from={from} to={now} now={now} max={stackMax} value={v => mins(v * MIN)}
          buckets={buckets} selectedBucket={at === null ? null : String(at)} onSelectBucket={k => onAt(at === Number(k) ? null : Number(k))} /></div>
      <div><b class="type-sm">Merged per {range.bucket >= DAY ? 'day' : '2 hours'}</b>
        <AreaChart label="Pull requests merged per bucket" series={merged} from={from} to={now} now={now} max={Math.max(4, ...merged[0].points.map(q => q.v))} value={v => String(Math.round(v))} /></div>
    </div>
    <p class="muted type-sm">{at === null ? 'A bucket shows the pull requests merged in it in the list.' : `The list shows the pull requests merged ${bucketLabel(at, range.bucket)}.`}</p>
  </DetailSection>;
}

/** The opened pull request: its stages on one time axis, queue runs, release and installs per host (the fleet repository
 *  has a recorded history; other repositories show what the current read knows). */
function PrDetail({ id, item, now, onClose }: { id: string; item?: Item; now: number; onClose: () => void }) {
  const [owner, repo, num] = id.split('~'), number = Number(num), fleet = `${owner}/${repo}` === FLEET;
  const hd = useDoc<History | typeof NONE>(fleet ? 'delivery-pr:' + number : null, () => op('delivery_history', { number }).catch(e => { if (/404/.test(String(e?.message ?? e))) return NONE; throw e; }), touchesHistory, 600000);
  const h = { data: hd.data === NONE ? null : hd.data, error: hd.error, loading: fleet && hd.data === null && !hd.error };
  const x = h.data?.prs.find(q => q.number === number);
  // The merge queue splits at its last successful run's start: waiting for a runner before, its checks after.
  const qStart = h.data?.runs.filter(r => r.conclusion === 'success' && r.started).map(r => r.started!).sort().at(-1);
  const title = item?.title || x?.title || `#${number}`;
  const steps: (readonly [string, Stage, string | undefined, string | undefined])[] = x ? [
    ['PR checks', 'pr_ci', x.opened, x.prCiDone],
    ...(qStart && x.enqueued ? [['Waiting in queue', 'mq_wait', x.enqueued, qStart], ['Queue checks', 'mq_ci', qStart, x.mqCiDone ?? x.merged]] as const : [['Merge queue', 'mq_ci', x.enqueued, x.mqCiDone ?? x.merged]] as const),
    ['Releasing', 'releasing', x.merged, x.released], ['Installing', 'installing', x.released, x.installed?.coordinator],
  ] : [];
  // A stage without its own end ends where a later one starts; only the last stage reached runs to now.
  const known = steps.filter(([, , a]) => a);
  const ended = known.map(([l, s, a, b], i) => [l, s, a, b ?? known.slice(i + 1).map(k => k[2]).find(Boolean)] as const);
  const t0 = ms(x?.opened ?? item?.opened ?? item?.since), t1 = now;
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={title}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><span class="muted mono">{owner}/{repo} #{number}</span><span class="grow" />
      {(item?.url || x?.url) && <Button label="Open on GitHub" icon="external-link" iconOnly onClick={() => open((item?.url || x?.url)!)} />}</header>
    <h2 class="detail-title">{title}</h2>
    <div class="state-line">{item && <StateBadge family="delivery" state={item.stage} />}{x?.author && <span class="muted">by {x.author}</span>}{t0 > 0 && <AgeChip t={t0} now={now} />}
      {(item?.release || x?.release) && <span class="id">{(item?.release || x?.release)!.replace(/^v/, '')}</span>}</div>
    <div class="stack-pad">
      {h.error && !h.data && <ErrorBanner text={`History did not load: ${h.error}`} />}
      {x ? <>
        <Totals items={[
          { label: x.installed?.coordinator ? 'Open to installed' : 'Lead so far', m: sec((ms(x.installed?.coordinator) || now) - ms(x.opened)), kind: 'duration' },
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
        <DetailSection title="Installed">{Object.keys(x.installed ?? {}).length === 0 ? <Empty text={x.released ? 'Not installed on any host yet.' : 'Not released yet.'} />
          : Object.entries(x.installed).map(([host, t]) => <KvRow key={host} icon="terminal" label={host} meta={x.installedVersions?.[host]} value={<AgeChip t={ms(t)} now={now} />} />)}</DetailSection>
      </> : item ? <>
        <DetailSection title="Now">
          <KvRow icon={states.delivery[item.stage].icon} label={states.delivery[item.stage].label} meta={item.stage === 'pr_failed' ? 'the last checks on its head failed' : undefined}
            value={<AgeChip t={ms(item.since)} now={now} alertAfter={LIMIT[item.stage]} alertLabel="past the stage limit" />} />
          {item.opened && <KvRow icon="git-pull-request" label="Opened" value={<AgeChip t={ms(item.opened)} now={now} />} />}
        </DetailSection>
        <p class="muted type-sm">{fleet ? 'The stage history is recorded once the pull request merges.' : 'Stage history is recorded for the fleet repository only.'}</p>
      </> : h.loading ? <Skeleton rows={4} /> : <Empty text="This pull request is not in flight and has no recorded history." />}
    </div>
  </article>;
}

