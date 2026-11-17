// Delivery (docs/design-system/pages/delivery.md): the pull requests of every connected project by delivery stage in the
// control column, the shared list view (list-page.tsx) over the rows delivery_status holds; the work area answers, in
// this order, what needs a look, what moves now and what the pace is, or shows the opened pull request. Data:
// delivery_status (GitHub read by the coordinator every minute with a key), delivery_history, delivery_baseline and
// delivery_regressions (the fleet repository's recorded history). Both reach the page through the change stream (kind
// delivery), so nothing here waits for a poll. Red everywhere means past the stage's P90 over the last 14 days.
import { useCallback, useEffect } from 'preact/hooks';
import {
  ListDetail, StateBadge, AgeChip, Num, Totals, TimeStrip, ProblemRow, DetailSection, KvRow, Button, Choice, Empty, ErrorBanner, Skeleton, Fact, RowFacts,
  Latest, DayChart, ShareBar, NormBar, matchSearch, states, capOf, quantile, day, dayStart, slotStart, hourLabel, dateKey, duration, actions, DELIVERY_ORDER,
  type StateOf, type DayCol, type DayMark, type SharePart, ShellStatus } from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';
import { useListPage, ListPagePanel, type LocalList } from '../list-page.tsx';
import { useUrlView } from '../url.ts';
import { DEFAULT_DELIVERY_GROUP, DEFAULT_DELIVERY_SORT, DELIVERY_STAGES, type View } from '../../../packages/view-url/view-url.ts';
import { scopeIds, prefs, prefsRead, type SavedView } from '../prefs.ts';
import type { Change } from '../../../contracts/generated/ops.ts';

type Stage = StateOf<'delivery'>;
type Status = Ops['delivery_status']['reply'];
type Item = Status['items'][number];
type History = Ops['delivery_history']['reply'];
type HistoryPr = History['prs'][number];
type Baseline = Ops['delivery_baseline']['reply'];
type Regressions = Ops['delivery_regressions']['reply'];
/** A list row: the pull request, its focus id, its code (#number) and its group and order keys. */
type Row = { id: string; code: string; sortKey: (string | number)[]; it: Item };

const MIN = 6e4, DAY = 864e5;
const FLEET = 'taskqorg/fleet';
const ORDER = DELIVERY_ORDER;
const DONE: Stage[] = ['installed', 'merged'];
const doneOnly = (v: View) => !!v.state?.length && v.state.every(s => DONE.includes(s as Stage));
const FAILED: Stage[] = ['pr_failed', 'mq_failed'];
// Each stage's norm is its P50 and P90 in delivery_baseline (the last 14 days): the key of the history duration it is.
// Open, Checks failed and Queue failed have none: people work there, and a failure needs a look at once.
// Waiting in queue is both "set to enter" and "in the queue, waiting for a runner": the queue's norm covers both.
// A UI-only release installs on no host: its Installing is the time until the site serves its UI (`ui_install`).
const NORM_KEY: Partial<Record<Stage, string>> = { pr_ci: 'pr_ci', mq_wait: 'queue', mq_ci: 'queue', releasing: 'release', installing: 'install' };
const normKey = (it: Pick<Item, 'stage' | 'release'>) => it.stage === 'installing' && uiOnly(it.release) ? 'ui_install' : NORM_KEY[it.stage];
/** The norms are the fleet repository's history: other repositories' pull requests have none. */
type Norms = { stage: (it: Pick<Item, 'stage' | 'repo' | 'release'>) => [number, number] | null; total: [number, number] | null };
/** A UI-only release, ui-<backend>-v<UI version> (release.yml): the web app alone, delivered when the site serves it. */
// The shape go/coordinator/delivery_history.go uiReleaseTag matches; the server sends the tag only.
const UI_TAG = /^ui-\d+(?:\.\d+)*-v(\d+(?:\.\d+)*)$/;
export const uiOnly = (tag?: string | null) => !!tag && UI_TAG.test(tag);
/** A release as people name it: v1.2.3 is 1.2.3; ui-1.2.2-v1.2.3 is "UI 1.2.3" (its UI version; the backend stays 1.2.2). */
export const releaseLabel = (tag: string) => { const m = UI_TAG.exec(tag); return m ? `UI ${m[1]}` : tag.replace(/^v/, ''); };
const GROUPS = [['stage', 'Stage'], ['project', 'Project'], ['none', 'None']] as const;
const SORTS = [['since', 'In stage since'], ['opened', 'Opened']] as const;
// System presets (OR-33) set the stage filter only; grouping, sort and search stay.
const SYSTEM_VIEWS: SavedView[] = [
  { name: 'Needs me', view: { view: 'delivery', state: [...FAILED] } },
  { name: 'In flight', view: { view: 'delivery', state: DELIVERY_STAGES.filter(s => !DONE.includes(s)) } },
  { name: 'Installed', view: { view: 'delivery', state: [...DONE] } },
];
const RANGES = [{ id: '24h', label: '24 h' }, { id: '7d', label: '7 days' }, { id: '30d', label: '30 days' }];
const ms = (s?: string | null) => s ? Date.parse(s) : NaN;
const fmt = (v: number) => duration(v / 1000).text;
/** The focus segment of a pull request: owner~repo~number (stable, readable, no slash). */
const idOf = (it: { repo: string; number: number }) => `${it.repo.replace('/', '~')}~${it.number}`;
// Current rows change with delivery-state:<project>; the recorded history (records and the regression check) with the rest.
const touchesStatus = (c: Change) => c.kind === 'delivery' && c.id.startsWith('delivery-state:');
const touchesHistory = (c: Change) => c.kind === 'delivery' && !c.id.startsWith('delivery-state:');
const NONE = 'none' as const; // no history kept (404): known, unlike still loading
const no404 = <T,>(p: Promise<T>) => p.catch(e => { if (/404/.test(String(e?.message ?? e))) return NONE; throw e; });
const q = quantile;
const sec = (v: number | null | undefined) => ({ value: v == null ? null : v / 1000, basis: v == null ? 'unavailable' as const : 'measured' as const, reason: v == null ? 'no pull requests' : undefined });

type DeliveryExtra = { range?: string; at?: string };
/** The pace keys of a link: valid values other than the defaults. A link may carry anything (an old one has the text "undefined"). */
export function deliveryExtra(x: Record<string, string | undefined> | undefined): DeliveryExtra {
  const out: DeliveryExtra = {};
  if (x?.range && x.range !== '7d' && RANGES.some(r => r.id === x.range)) out.range = x.range;
  if (x?.at && /^\d{4}-\d{2}-\d{2}(T\d{2})?$/.test(x.at)) out.at = x.at;
  return out;
}

/* ---------- Buckets: calendar days (or two-hour slots) in the viewer's zone ---------- */

type Bucket = DayCol & { start: number; end: number; today: boolean };
/** 24 h: twelve two-hour slots of local time ending with the current one; 7 and 30 days: calendar days to today, from the first recorded day. */
export function bucketsOf(range: string, now: number, first?: number): Bucket[] {
  if (range === '24h') {
    return Array.from({ length: 12 }, (_, i) => { const s = slotStart(now, 2, i - 11), e = slotStart(now, 2, i - 10);
      return { key: dateKey(s, true), start: s, end: e, label: hourLabel(s), tip: `${day(s, now)} ${hourLabel(s)}–${hourLabel(e)}${e > now ? ', so far' : ''}`, today: e > now }; });
  }
  const out: Bucket[] = [];
  let s = dayStart(now, range === '30d' ? -29 : -6);
  if (first && first > s) s = dayStart(first);
  for (; s <= now; s = dayStart(s, 1)) { const t = s === dayStart(now); out.push({ key: dateKey(s), start: s, end: dayStart(s, 1), label: day(s, now), tip: t ? `${day(s, now)}, today so far` : day(s, now), today: t }); }
  return out;
}

export function Delivery({ id }: { id?: string }) {
  const scope = scopeIds(), focus = scope.projectIds ?? [];
  const now = Date.now();
  const status = useDoc<Status>(prefsRead.value ? 'delivery:' + JSON.stringify(scope) : null, () => op('delivery_status', scope), touchesStatus, 300000);
  // The norms: every stage's P50 and P90 over the last 14 days (the fleet repository's history); 404 is no history kept.
  const baseDoc = useDoc<Baseline | typeof NONE>(prefsRead.value ? 'delivery-baseline:14' : null, () => no404(op('delivery_baseline', { days: 14 })), touchesHistory, 600000);
  const base = baseDoc.data === NONE ? null : baseDoc.data;
  const stat = (k: string) => base?.rolling.stats.find(s => s.key === k);
  const norms: Norms = {
    stage: it => { const k = normKey(it), x = it.repo === FLEET && k && stat(k); return x ? [x.p50Ms, x.p90Ms] : null; },
    total: stat('total') ? [stat('total')!.p50Ms, stat('total')!.p90Ms] : null,
  };
  // The recorded history covers the fleet repository only; a 404 (no history kept), a focus away from fleet or an ignored fleet hides Pace.
  const fleetShown = (!focus.length || focus.includes('fleet')) && !prefs.value.ignored.includes('fleet');
  // A stage this app does not know yet (a newer coordinator) is left out rather than breaking the page.
  const data = status.data, items = (data?.items ?? []).filter(it => it.stage in states.delivery), incomplete = !!data?.error;
  // The pace range and the picked bucket ride in the link's extra keys; the history is read for the range shown.
  const urlRange = deliveryExtra(useUrlView('delivery').view.extra).range ?? '7d';
  // The doc name carries the first bucket, so a page open across midnight (or a two-hour slot) reads the new window.
  const histDoc = useDoc<{ h: History } | typeof NONE>(prefsRead.value && fleetShown ? 'delivery-pace:' + urlRange + ':' + bucketsOf(urlRange, now)[0].key : null,
    () => no404(op('delivery_history', { since: new Date(bucketsOf(urlRange, Date.now())[0].start).toISOString() }).then(h => ({ h }))), touchesHistory, 600000);
  const hist = histDoc.data === NONE ? null : histDoc.data;
  const regDoc = useDoc<Regressions | typeof NONE>(prefsRead.value && fleetShown ? 'delivery-regressions' : null, () => no404(op('delivery_regressions', {})), touchesHistory, 600000);
  const regressions = regDoc.data === NONE ? [] : regDoc.data?.regressions ?? [];
  const histLoading = !prefsRead.value || fleetShown && histDoc.data === null && !histDoc.error;
  const paceOf = (v: View) => {
    const ex = deliveryExtra(v.extra), range = ex.range ?? '7d', buckets = bucketsOf(range, now, hist?.h.backfilledFrom ? ms(hist.h.backfilledFrom) : undefined);
    return { ex, range, buckets, picked: ex.at ? buckets.find(b => b.key === ex.at) ?? null : null };
  };
  const local = (v: View): LocalList<Row> => {
    const { picked } = paceOf(v);
    // A picked pace bucket shows the pull requests merged in it (from the history), in place of the current rows; none
    // while the history loads.
    const source = !picked ? items : hist ? hist.h.prs.filter(x => ms(x.merged) >= picked.start && ms(x.merged) < picked.end).map(x => fromHistory(x, hist.h.installsFrom)) : [];
    const row = (it: Item): Row => ({ id: idOf(it), code: '#' + it.number, sortKey: [], it });
    const rowsOf = source.map(row);
    const searched = rowsOf.filter(r => matchSearch(v.q ?? '', `${r.it.title} #${r.it.number} ${r.it.repo}`, { state: r.it.stage, project: r.it.projectId }));
    const counts: Record<string, number> = Object.fromEntries(ORDER.map(s => [s, 0]));
    for (const r of searched) counts[r.it.stage]++;
    const st = v.state ?? [], group = v.group ?? DEFAULT_DELIVERY_GROUP, sort = v.sort ?? DEFAULT_DELIVERY_SORT;
    const desc = sort.startsWith('-'), field = desc ? sort.slice(1) : sort;
    const key = (r: Row) => ms(field === 'opened' ? r.it.opened : r.it.since) || 0;
    const gkey = (r: Row) => group === 'stage' ? r.it.stage : group === 'project' ? r.it.projectId || '' : '';
    const order = group === 'stage' ? ORDER as string[] : [...new Set(searched.map(gkey))].sort(), at = new Map(order.map((k, i) => [k, i]));
    // The order key carries the direction, as a server list's does, so pinned rows (compareKeys) follow it too.
    const keyed = (r: Row): Row => ({ ...r, sortKey: [gkey(r), desc ? -key(r) : key(r)] });
    const rows = searched.filter(r => !st.length || st.includes(r.it.stage)).map(keyed)
      .sort((a, b) => at.get(String(a.sortKey[0]))! - at.get(String(b.sortKey[0]))! || (a.sortKey[1] as number) - (b.sortKey[1] as number));
    const per = new Map<string, number>();
    for (const r of rows) per.set(String(r.sortKey[0]), (per.get(String(r.sortKey[0])) ?? 0) + 1);
    const groups = order.filter(k => per.has(k)).map(k => ({ key: k, label: group === 'stage' ? states.delivery[k as Stage].label : k || 'No project', count: per.get(k)! }));
    // Pinned rows stay whatever the filter or the picked day (OR-12): the current rows and the day's, in the page's order.
    const current = items.map(row), ids = new Set(current.map(r => r.id));
    const all = [...current, ...rowsOf.filter(r => !ids.has(r.id))].map(keyed);
    // The rows come from the history while a day is picked: its failure is the list's, never "none merged then".
    const error = picked ? (hist ? null : histDoc.error && `history: ${histDoc.error}`) : status.error && !data ? status.error : null;
    return { rows, all, counts, groups, total: rowsOf.length, pending: !picked && !data && !status.error || !!picked && histLoading, error, reload: picked ? histDoc.reload : status.reload };
  };
  const page = useListPage<Row>({
    view: 'delivery', local: local as (v: View) => LocalList<{ id: string; sortKey: (string | number)[] }>, closedGroups: { stage: ['installed'] },
    // Installed starts collapsed only while it is one group among the rows in flight: a picked day (merged pull requests,
    // most installed) or a filter to installed rows is its own context, where it starts open.
    // The day in the URL, not the bucket found in the history: the context does not change when the history arrives.
    groupsContext: v => deliveryExtra(v.extra).at || doneOnly(v) ? 'merged' : undefined,
    defaultGroup: DEFAULT_DELIVERY_GROUP, defaultSort: DEFAULT_DELIVERY_SORT, system: SYSTEM_VIEWS, groupLabel: (_g, l) => l ?? '',
  });
  const { view, set } = page;
  const { ex, range, buckets, picked } = paceOf(view);
  // A link saved while the "undefined" bug was live, or one with an old range, carries keys the page ignores: drop them.
  // So does a picked bucket outside the range, or one with no history to show (none kept, or focused away from fleet).
  const keep: DeliveryExtra = { ...ex, at: ex.at && (picked && (hist || histLoading || histDoc.error)) ? ex.at : undefined };
  useEffect(() => { const x = view.extra ?? {}; if (Object.keys(x).some(k => (x as Record<string, string | undefined>)[k] !== (keep as Record<string, string | undefined>)[k])) set({ extra: deliveryExtra(keep) }, 'type'); }, [JSON.stringify(view.extra), !!hist, histLoading]);
  const setPace = (patch: DeliveryExtra) => set({ extra: deliveryExtra({ ...ex, ...patch }) });
  const go = useCallback((it?: Item) => set({ focus: it ? idOf(it) : undefined }), [set]);
  const open = !id ? undefined : items.find(it => idOf(it) === id) ?? (id.startsWith('taskqorg~fleet~') ? hist?.h.prs.filter(x => x.number === Number(id.split('~')[2])).map(x => fromHistory(x, hist.h.installsFrom))[0] : undefined);
  const p = keep.at ? picked : null;

  const list = <>
    <ShellStatus>
      {data?.source === 'public' && <span class="chip-age t-attention" data-tip="No github key in API keys: unauthenticated reads of the fleet repository every 6 minutes">public GitHub</span>}
      {data?.refreshedAt && <AgeChip t={ms(data.refreshedAt)} now={now} alertAfter={900} alertLabel="GitHub read is stale" />}</ShellStatus>
    {data?.error && <ErrorBanner text={`GitHub: ${data.error}`} />}
    {incomplete && <p class="muted type-sm stack-pad">Some GitHub data is unavailable: current totals are unknown; the rows are the last ones read.</p>}
    <ListPagePanel page={page} noun="Pull requests" placeholder="Search pull requests, state:mq_wait project:fleet" family="delivery" groups={GROUPS} sorts={SORTS} focused={id} countersLabel="Pull requests by stage"
      countsUnknown={incomplete && !p} chips={p ? [{ field: 'Merged', value: p.tip ?? p.label }] : []} onRemoveChip={() => setPace({ at: undefined })}
      listLabel="Pull requests" title={r => r.it.title || `#${r.it.number}`}
      groupIcon={(g, k) => g === 'stage' && k in states.delivery ? states.delivery[k as Stage].icon : 'folder-open'} groupTone={(g, k) => g === 'stage' && k in states.delivery ? states.delivery[k as Stage].tone : 'neutral'}
      state={r => <StateBadge family="delivery" state={r.it.stage} compact />} actions={() => []}
      menu={r => [{ label: 'Open on GitHub', icon: actions.open, onClick: () => window.open(r.it.url, '_blank', 'noopener') }]}
      empty={<Empty text={p ? 'No pull requests merged then.' : page.filtered ? 'No pull requests match.' : incomplete ? 'Unavailable. GitHub data is incomplete.' : 'No pull requests in flight.'} />}
      facts={r => <RowFacts>
        {r.it.repo !== FLEET && <Fact text={r.it.repo} tip="Repository" />}
        {r.it.release && <Fact text={releaseLabel(r.it.release)} tip={uiOnly(r.it.release) ? `UI-only release ${r.it.release}: hosts install nothing` : 'Release'} />}
        <Fact tip={`${states.delivery[r.it.stage].label} for`}><AgeChip t={ms(r.it.since)} now={now} alertAfter={alertOf(norms, r.it)} alertLabel="past the stage's P90" /></Fact>
      </RowFacts>} />
  </>;
  return <ListDetail list={list} detailKey={id}
    idle={<Overview now={now} items={items} incomplete={incomplete} norms={norms} hist={hist} regressions={regressions} histError={histDoc.error} fleetShown={fleetShown && histDoc.data !== NONE}
      range={range} buckets={buckets} picked={p} href={it => page.href({ focus: idOf(it) })} onOpen={go}
      onRange={r => setPace({ range: r, at: undefined })} onPick={k => setPace({ at: ex.at === k ? undefined : k })} />}
    detail={id ? <PrDetail key={id} id={id} item={open} now={now} norms={norms} onClose={() => go()} /> : undefined} />;
}

/** Seconds in a stage after which its time is red: the stage's P90 over the last 14 days; none for stages without a norm. */
const alertOf = (n: Norms, it: Item) => { const x = n.stage(it); return x ? x[1] / 1000 : undefined; };

/** Released before install times were recorded: its install is unknown, never measured. */
const unrecorded = (x: HistoryPr, installsFrom: number | null) => !!installsFrom && !!x.released && ms(x.released) < installsFrom;
/** Due a measure: it has one, or it reached the hosts (installed), or recording had not begun. One not installed yet is
 *  on its way (or stuck before the hosts), not unmeasured; the rule reads the data only, never the clock. */
const due = (x: HistoryPr, installsFrom: number | null) => typeof x.durations.total === 'number' || !!doneAt(x) || unrecorded(x, installsFrom);
/** When it was delivered: on the PDS18 coordinator, or for a UI-only release when the site served its UI. */
const doneAt = (x: HistoryPr) => uiOnly(x.release) ? x.uiOnSite : x.installed?.coordinator;
/** A stage duration, a UI-only release's UI on site standing for the install it does not have. */
const stageMs = (x: HistoryPr, k: string) => k === 'install' && uiOnly(x.release) ? x.durations.ui : x.durations[k];

/** A recorded (merged) pull request as a list row: its stage from how far it got. */
function fromHistory(x: HistoryPr, installsFrom?: string): Item {
  // Released before installs were recorded: its install is unknown, so it is done (merged), not installing for weeks.
  const unrec = unrecorded(x, installsFrom ? ms(installsFrom) : null);
  const done = doneAt(x), stage: Stage = done ? 'installed' : unrec ? 'merged' : x.released ? 'installing' : 'releasing';
  return { projectId: 'fleet', repo: FLEET, number: x.number, title: x.title, url: x.url, stage, since: (done ?? x.released ?? x.merged) as string, opened: x.opened, merged: x.merged, release: x.release };
}

/** The work area while no pull request is open: what needs a look, what moves now, the pace. */
function Overview({ now, items, incomplete, norms, hist, regressions, histError, fleetShown, range, buckets, picked, onRange, onPick, onOpen, href }: {
  now: number; items: Item[]; incomplete: boolean; norms: Norms; hist: { h: History } | null; regressions: Regressions['regressions']; histError: string | null; fleetShown: boolean;
  range: string; buckets: Bucket[]; picked: Bucket | null; onRange: (r: string) => void; onPick: (k: string) => void; onOpen: (it: Item) => void; href: (it: Item) => string;
}) {
  const moving = ORDER.filter(s => !DONE.includes(s)).flatMap(s => items.filter(it => it.stage === s));
  const over = (it: Item) => { const n = norms.stage(it); return !!n && now - ms(it.since) > n[1]; };
  const failed = items.filter(it => FAILED.includes(it.stage)), stuck = moving.filter(over);
  const look = [
    ...regressions.map(g => <ProblemRow key={'r' + g.key + g.metric} severity="warning" title={`${keyLabel(g.key)} ${g.metric.toUpperCase()} is ${fmt(g.currentMs)}, baseline ${fmt(g.baselineMs)}`}
      facts={`${g.firstPr ? `Since #${g.firstPr} · ` : ''}${g.n} pull requests in the last 24 h`} actions={[...(g.compareUrl ? [{ label: 'Compare', onClick: () => window.open(g.compareUrl!, '_blank', 'noopener') }] : [])]} />),
    ...failed.map(it => <ProblemRow key={'f' + idOf(it)} severity="failure" title={`#${it.number} ${it.stage === 'mq_failed' ? 'removed from the merge queue' : 'checks failed'}`} facts={`${it.title} · ${fmt(Math.max(0, now - ms(it.since)))} ago`}
      actions={[{ label: `Open #${it.number}`, primary: true, onClick: () => onOpen(it) }]} />),
    ...stuck.map(it => <ProblemRow key={'s' + idOf(it)} severity="warning" title={`#${it.number} ${states.delivery[it.stage].label.toLowerCase()} for ${fmt(now - ms(it.since))}, P90 ${fmt(norms.stage(it)![1])}`}
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
            <span class="r"><AgeChip t={ms(it.since)} now={now} alertAfter={alertOf(norms, it)} alertLabel="past the stage's P90" /></span>
            <span class={`r mono type-sm${red ? ' t-danger' : ' muted'}`} style={red ? { color: 'var(--tone-fg)' } : undefined}
              data-tip={n ? `${states.delivery[it.stage].label}: P50 ${fmt(n[0])}, P90 ${fmt(n[1])} over 14 days` : undefined}>{n ? `P90 ${fmt(n[1])}` : 'no norm'}</span>
          </div>;
        })}</div>}
      {incomplete && moving.length > 0 && <p class="muted type-sm">The last rows read; GitHub coverage is incomplete.</p>}
    </DetailSection>
    {fleetShown && <Pace hist={hist?.h ?? null} error={histError} now={now} norms={norms} range={range} buckets={buckets} picked={picked} onRange={onRange} onPick={onPick} />}
  </div>;
}
const KEYS: Record<string, string> = { pr_ci: 'PR checks', enqueue: 'Enqueue', queue: 'Merge queue', merge: 'Merge', release: 'Release', install: 'Install', ui: 'UI on site', lead: 'Lead', total: 'Open to installed', availability: 'Availability', time_to_available: 'Time to available', run: 'Queue run',
  availability_wait: 'Waiting for availability', ui_wait: 'Waiting for the UI on site', ui_install: 'UI-only install' };
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

/**
 * Open to installed in a bucket: P50 and P90 of its pull requests measured through install; none measured is "not
 * measured", never 0. Only pull requests due a measure (`due`) count against "partly measured"; one not installed yet
 * is on its way.
 */
export function totalMark(prs: HistoryPr[], b: Bucket, installsFrom: number | null): DayMark {
  const xs = prs.map(x => x.durations.total).filter((v): v is number => typeof v === 'number');
  if (!prs.length) return { v: null, missing: 'nothing merged' };
  // Why none is measured, the most specific first: installed but its install time not matched, recording not begun, on its way.
  // An installed one without a measure: its hosts took a later release, or its install was not matched to this one.
  if (!xs.length) return { v: null, missing: prs.some(x => !!doneAt(x)) ? 'installed with a later release, or not matched to its own'
    : installsFrom && (b.end <= installsFrom || prs.some(x => unrecorded(x, installsFrom))) ? `install times recorded from ${day(installsFrom)} ${hourLabel(installsFrom)}`
    : 'not installed yet' };
  const n = prs.filter(x => due(x, installsFrom)).length;
  return { v: q(xs, 0.5), hi: q(xs, 0.9), some: xs.length < n ? `${xs.length} of ${n} measured` : undefined, partial: b.today ? 'so far' : undefined };
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
function PrDetail({ id, item, now, norms, onClose }: { id: string; item?: Item; now: number; norms: Norms; onClose: () => void }) {
  const [owner, repo, num] = id.split('~'), number = Number(num), fleet = `${owner}/${repo}` === FLEET;
  const hd = useDoc<History | typeof NONE>(fleet ? 'delivery-pr:' + number : null, () => no404(op('delivery_history', { number })), touchesHistory, 600000);
  const h = { data: hd.data === NONE ? null : hd.data, error: hd.error, loading: fleet && hd.data === null && !hd.error };
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
    <div class="state-line">{item && <StateBadge family="delivery" state={item.stage} />}{x?.author && <span class="muted">by {x.author}</span>}{t0 > 0 && <AgeChip t={t0} now={now} />}
      {rel && <span class="id" data-tip={uiOnly(rel) ? `UI-only release ${rel}: hosts install nothing` : undefined}>{releaseLabel(rel)}</span>}</div>
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
        <DetailSection title="Installed">{uiOnly(x.release) ? x.uiOnSite ? <KvRow icon="panels-top-left" label="Site" meta={releaseLabel(x.release!)} value={<AgeChip t={ms(x.uiOnSite)} now={now} />} />
            : <Empty text="UI-only release: hosts install nothing; the site does not serve it yet." />
          : Object.keys(x.installed ?? {}).length === 0 ? <Empty text={x.released ? 'Not installed on any host yet.' : 'Not released yet.'} />
          : Object.entries(x.installed).map(([host, t]) => <KvRow key={host} icon="terminal" label={host} meta={x.installedVersions?.[host]} value={<AgeChip t={ms(t)} now={now} />} />)}</DetailSection>
      </> : item ? <>
        <DetailSection title="Now">
          <KvRow icon={states.delivery[item.stage].icon} label={states.delivery[item.stage].label}
            meta={item.stage === 'pr_failed' ? 'the last checks on its head failed' : item.stage === 'mq_failed' ? 'its last merge-queue run failed; it left the queue' : undefined}
            value={<AgeChip t={ms(item.since)} now={now} alertAfter={alertOf(norms, item)} alertLabel="past the stage's P90" />} />
          {item.opened && <KvRow icon="git-pull-request" label="Opened" value={<AgeChip t={ms(item.opened)} now={now} />} />}
        </DetailSection>
        <p class="muted type-sm">{fleet ? 'The stage history is recorded once the pull request merges.' : 'Stage history is recorded for the fleet repository only.'}</p>
      </> : h.loading ? <Skeleton rows={4} /> : <Empty text="This pull request is not in flight and has no recorded history." />}
    </div>
  </article>;
}
