// Delivery (docs/design-system/pages/delivery.md): the pull requests of every connected project by delivery stage in the
// control column, the shared list view (list-page.tsx) over the rows delivery_status holds; the work area answers, in
// this order, what needs a look, what moves now and what the pace is, or shows the opened pull request. Data:
// delivery_status (GitHub read by the coordinator every minute with a key), delivery_history, delivery_baseline and
// delivery_regressions (the fleet repository's recorded history). Both reach the page through the change stream (kind
// delivery), so nothing here waits for a poll. Red everywhere means past the stage's P90 over the last 14 days.
import { createEffect, createMemo, createSignal, onSettled, Show } from 'solid-js';
import {
  EntityCode, ListDetail, StateBadge, AgeChip, Empty, ErrorBanner, Fact, RowFacts,
  matchSearch, states, actions, ShellStatus,
} from '../../../packages/ui/src/index.ts';
import { useOp } from '../store.ts';
import { useListPage, ListPagePanel, type LocalList, type Frame } from '../list-page.tsx';
import { SYSTEM_VIEWS } from '../system-views.ts';
import { useUrlView } from '../url.ts';
import { DEFAULT_DELIVERY_GROUP, DEFAULT_DELIVERY_SORT, type View } from '../../../packages/view-url/view-url.ts';
import { scopeIds, prefs, prefsRead } from '../prefs.ts';
import {
  FLEET, ORDER, releaseLabel, idOf, providerLabel, deliveryExtra, bucketsOf, doneOnly, normKey, ms,
  kept, missing, failed, alertOf, fromHistory,
  type Stage, type Item, type PublishingCapability, type History, type Baseline, type Regressions, type Row, type Norms, type DeliveryExtra,
} from './delivery-model.tsx';
import { Overview, PrDetail } from './delivery-panels.tsx';

export { uiOnly, releaseLabel, idOf, deliveryExtra, bucketsOf, totalMark } from './delivery-model.tsx';

const GROUPS = [['stage', 'Stage'], ['project', 'Project'], ['none', 'None']] as const;
const SORTS = [['since', 'In stage since'], ['opened', 'Opened']] as const;

/** Delivery screen showing live pull request stages, release history, and pace analytics. */
export function Delivery(props: { id?: string; project?: string; frame?: Frame }) {
  // A project's Delivery facet (docs/design-system/pages/projects.md) reads that project alone, whatever the focus.
  const scope = createMemo(() => (props.project ? { projectIds: [props.project] } : scopeIds()));
  const focus = createMemo(() => scope().projectIds ?? []);
  const [tick, setTick] = createSignal(Date.now());
  onSettled(() => {
    const t = setInterval(() => setTick(Date.now()), 60_000);
    return () => clearInterval(t);
  });
  const now = () => tick();
  const status = useOp('publishing_status', () => (prefsRead.value ? scope() : null), { every: 300000 });
  // The norms: every stage's P50 and P90 over the last 14 days (the fleet repository's history); 404 is no history kept.
  const baseDoc = useOp('delivery_baseline', () => (prefsRead.value ? { days: 14 } : null), { every: 600000 });
  const base = createMemo<Baseline | null>(() => kept(baseDoc));
  const stat = (k: string) => base()?.rolling.stats.find(s => s.key === k);
  const norms = createMemo<Norms>(() => ({
    stage: it => { const k = normKey(it), x = it.repo === FLEET && k && stat(k); return x ? [x.p50Ms, x.p90Ms] : null; },
    total: stat('total') ? [stat('total')!.p50Ms, stat('total')!.p90Ms] : null,
  }));
  // The recorded history covers the fleet repository only; a 404 (no history kept), a focus away from fleet or an ignored fleet hides Pace.
  const fleetShown = createMemo(() => (!focus().length || focus().includes('fleet')) && !prefs.value.ignored.includes('fleet'));
  // A stage this app does not know yet (a newer coordinator) is left out rather than breaking the page.
  const data = () => status.data;
  const items = createMemo(() => (data()?.items ?? []).filter(it => it.stage in states.delivery));
  const incomplete = createMemo(() => !!data()?.error);
  // The pace range and the picked bucket ride in the link's extra keys; the history is read for the range shown.
  const urlView = useUrlView('delivery');
  const url = createMemo(() => deliveryExtra(urlView.view.extra));
  const urlRange = createMemo(() => url().range ?? '7d');
  const releaseDoc = useOp('delivery_history', () => (prefsRead.value && fleetShown() && url().release ? { release: url().release! } : null), { every: 600000 });
  const releaseHistory = createMemo<History | null>(() => kept(releaseDoc));
  const releaseRows = createMemo(() => fleetShown() && releaseHistory() ? releaseHistory()!.prs.map(pr => fromHistory(pr, releaseHistory()!.installsFrom)) : []);
  const releaseItems = createMemo(() => url().release ? items().filter(it => it.release === url().release) : []);
  const releaseRowIds = createMemo(() => new Set(releaseItems().map(idOf)));
  // The request carries the first bucket, so a page open across midnight (or a two-hour slot) reads the new window.
  const histDoc = useOp('delivery_history', () => (prefsRead.value && fleetShown() ? { since: new Date(bucketsOf(urlRange(), now())[0].start).toISOString() } : null), { every: 600000 });
  const hist = createMemo<History | null>(() => kept(histDoc));
  const regDoc = useOp('delivery_regressions', () => (prefsRead.value && fleetShown() ? {} : null), { every: 600000 });
  const regressions = createMemo<Regressions['regressions']>(() => kept(regDoc)?.regressions ?? []);
  const histLoading = createMemo(() => !prefsRead.value || fleetShown() && histDoc.data === null && !failed(histDoc));
  const paceOf = (v: View) => {
    const ex = deliveryExtra(v.extra), range = ex.range ?? '7d', h = hist();
    const buckets = bucketsOf(range, now(), h?.backfilledFrom ? ms(h.backfilledFrom) : undefined);
    return { ex, range, buckets, picked: ex.at ? buckets.find(b => b.key === ex.at) ?? null : null };
  };
  const local = (v: View): LocalList<Row> => {
    const { picked } = paceOf(v);
    const curUrl = url(), curItems = items(), curData = data(), h = hist(), fs = fleetShown();
    // A picked pace bucket shows the pull requests merged in it (from the history), in place of the current rows; none
    // while the history loads.
    const source = curUrl.release ? [...releaseItems(), ...releaseRows().filter(row => !releaseRowIds().has(idOf(row)))] : !picked ? curItems : h ? h.prs.filter(x => ms(x.merged) >= picked.start && ms(x.merged) < picked.end).map(x => fromHistory(x, h.installsFrom)) : [];
    const row = (it: Item): Row => ({ id: idOf(it), code: '#' + it.number, sortKey: [], it });
    const rowsOf = source.map(row);
    const searched = rowsOf.filter(r => matchSearch(v.q ?? '', `${r.it.title} #${r.it.number} ${r.it.repo} ${r.it.release ?? ''} release=${r.it.release ?? ''}.`, { state: r.it.stage, project: r.it.projectId }));
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
    const current = curItems.map(row), ids = new Set(current.map(r => r.id));
    const all = [...current, ...rowsOf.filter(r => !ids.has(r.id))].map(keyed);
    // The rows come from the history while a day is picked: its failure is the list's, never "none merged then".
    const error = curUrl.release ? failed(releaseDoc) ?? (!curData ? status.error : null) : picked ? (h ? null : failed(histDoc) && `history: ${failed(histDoc)}`) : status.error && !curData ? status.error : null;
    return { rows, all, counts, groups, total: rowsOf.length, pending: curUrl.release ? !prefsRead.value || (fs && releaseDoc.data === null && !failed(releaseDoc)) || (!curData && !status.error) : !picked && !curData && !status.error || !!picked && histLoading(), error, reload: curUrl.release ? () => { if (fs) releaseDoc.reload(); status.reload(); } : picked ? histDoc.reload : status.reload };
  };
  const page = useListPage<Row>({
    view: 'delivery', entity: 'pull_request', local: local as (v: View) => LocalList<{ id: string; sortKey: (string | number)[] }>, closedGroups: { stage: ['installed'] },
    // Installed starts collapsed only while it is one group among the rows in flight: a picked day (merged pull requests,
    // most installed) or a filter to installed rows is its own context, where it starts open.
    // The day in the URL, not the bucket found in the history: the context does not change when the history arrives.
    groupsContext: v => deliveryExtra(v.extra).at || deliveryExtra(v.extra).release || doneOnly(v) ? 'merged' : undefined,
    defaultGroup: DEFAULT_DELIVERY_GROUP, defaultSort: DEFAULT_DELIVERY_SORT, system: SYSTEM_VIEWS.delivery, groupLabel: (_g, l) => l ?? '',
  });
  const pace = createMemo(() => paceOf(page.view));
  // A link saved while the "undefined" bug was live, or one with an old range, carries keys the page ignores: drop them.
  // So does a picked bucket outside the range, or one with no history to show (none kept, or focused away from fleet).
  const keep = createMemo<DeliveryExtra>(() => {
    const { ex, picked } = pace();
    return { ...ex, at: ex.at && (picked && (hist() || histLoading() || failed(histDoc))) ? ex.at : undefined };
  });
  createEffect(
    () => [JSON.stringify(page.view.extra), page.view.extra, keep()] as const,
    ([, rawExtra, nextKeep]) => {
      const x = rawExtra ?? {};
      if (Object.keys(x).some(k => (x as Record<string, string | undefined>)[k] !== (nextKeep as Record<string, string | undefined>)[k])) {
        page.set({ extra: deliveryExtra(nextKeep) }, 'type');
      }
    },
  );
  const setPace = (patch: DeliveryExtra) => page.set({ extra: deliveryExtra({ ...pace().ex, ...patch }) });
  const go = (it?: Item) => page.set({ focus: it ? idOf(it) : undefined });
  const open = createMemo(() => {
    const id = props.id;
    if (!id) return undefined;
    const h = hist();
    return items().find(it => idOf(it) === id) ?? releaseRows().find(it => idOf(it) === id) ?? (id.startsWith('taskqorg~fleet~') ? h?.prs.filter(x => x.number === Number(id.split('~')[2])).map(x => fromHistory(x, h.installsFrom))[0] : undefined);
  });
  const openCap = createMemo<PublishingCapability | undefined>(() => {
    const it = open();
    if (!it) return undefined;
    return data()?.capabilities.find(c => c.projectId === it.projectId && c.repo.toLowerCase() === it.repo.toLowerCase());
  });
  const p = createMemo(() => (keep().at ? pace().picked : null));

  const list = <>
    <ShellStatus>
      <Show when={data()?.source === 'public'}><span class="chip-age t-attention" data-tip="No github key in API keys: unauthenticated reads of the fleet repository every 6 minutes">public GitHub</span></Show>
      <Show when={data()?.refreshedAt}>{refreshed => <AgeChip t={ms(refreshed())} alertAfter={900} alertLabel="GitHub read is stale" />}</Show>
    </ShellStatus>
    <Show when={data()?.error}>{err => <ErrorBanner text={`GitHub: ${err()}`} />}</Show>
    <Show when={incomplete()}><p class="muted type-sm stack-pad">Some GitHub data is unavailable: current totals are unknown; the rows are the last ones read.</p></Show>
    <ListPagePanel page={page} noun="Pull requests" placeholder="Search pull requests, state:mq_wait project:fleet" family="delivery" groups={GROUPS} sorts={SORTS} focused={props.id} countersLabel="Pull requests by stage"
      countsUnknown={incomplete() && !p()}
      chips={url().release ? [{ field: 'Release', value: releaseLabel(url().release!) }] : p() ? [{ field: 'Merged', value: p()!.tip ?? p()!.label }] : []}
      onRemoveChip={() => setPace(url().release ? { release: undefined } : { at: undefined })}
      listLabel="Pull requests" title={r => r.it.title || `#${r.it.number}`}
      groupIcon={(g, k) => g === 'stage' && k in states.delivery ? states.delivery[k as Stage].icon : 'folder-open'} groupTone={(g, k) => g === 'stage' && k in states.delivery ? states.delivery[k as Stage].tone : 'neutral'}
      state={r => <StateBadge family="delivery" state={r.it.stage} compact />} actions={() => []}
      menu={r => [{ label: r.it.provider === 'gitlab' || r.it.kind === 'merge_request' ? 'Open on GitLab' : 'Open on GitHub', icon: actions.open, onClick: () => window.open(r.it.url, '_blank', 'noopener') }]}
      empty={<Empty text={p() ? 'No pull requests merged then.' : page.filtered ? 'No pull requests match.' : incomplete() ? 'Unavailable. GitHub data is incomplete.' : 'No pull requests in flight.'} />}
      facts={r => <RowFacts>
        {r.it.provider === 'gitlab' && <Fact text={`${providerLabel(r.it.provider, r.it.kind)}${r.it.host && r.it.host !== 'gitlab.com' ? ` · ${r.it.host}` : ''}`} tip="Provider" />}
        {r.it.repo !== FLEET && <Fact text={r.it.repo} tip="Repository" />}
        {r.it.release && <EntityCode code={releaseLabel(r.it.release)} id={r.it.release} kind="release" />}
        <Fact tip={`${states.delivery[r.it.stage].label} for`}><AgeChip t={ms(r.it.since)} alertAfter={alertOf(norms(), r.it)} alertLabel="past the stage's P90" /></Fact>
      </RowFacts>} />
  </>;
  const Layout = props.frame ?? ListDetail;
  return <Layout list={list} detailKey={props.id}
    idle={<Overview now={now()} items={items()} incomplete={incomplete()} norms={norms()} hist={hist()} regressions={regressions()} histError={failed(histDoc)} fleetShown={fleetShown() && !missing(histDoc)}
      range={pace().range} buckets={pace().buckets} picked={p()} href={it => page.href({ focus: idOf(it) })} onOpen={go}
      onRange={r => setPace({ range: r, at: undefined })} onPick={k => setPace({ at: pace().ex.at === k ? undefined : k })} />}
    detail={props.id ? <Show when={props.id} keyed>{id => <PrDetail id={id} item={open()} capability={openCap()} now={now()} norms={norms()} onReload={() => status.reload()} onClose={() => go()} />}</Show> : undefined} />;
}
