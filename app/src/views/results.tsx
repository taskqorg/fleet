// Results (docs/design-system/pages/results.md): stored results and other artifacts with shared list-page controls,
// cursor pagination, newest first, state icon first (OR-22); the focused one opens in the result viewer (OR-27).
import { useEffect, useMemo, useState } from 'preact/hooks';
import {
  EntityCode, ListDetail, StateBadge, Button, ResultViewer, ArtifactView, Empty, ErrorBanner, Skeleton,
  AgeChip, ShellStatus, OfflineMarker, RowFacts, Fact, DetailSection, Totals, Icon, KvRow, Num,
  bytes, fileLabel, splitTerms, viewOf, states, actions, ARTIFACT_STATES,
  type StateOf, type Chip, type IconName, type SortField, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import type { ArtifactRow } from '../../../contracts/generated/ops.ts';
import { withFocus, prefsRead, listGroups, setListGroups } from '../prefs.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { useOp } from '../store.ts';
import { useUrlView } from '../url.ts';
import { useListPage, ListPagePanel, type LocalList, type ListPage, type ListItem } from '../list-page.tsx';
import type { View } from '../../../packages/view-url/view-url.ts';

const PAGE = 100;
const BUCKET_MS = 5 * 60_000;
const PINNED = '\u0000pinned';
// Filters beyond state are typed in search (OR-36: media:image since:7d) or come in a link's extra keys.
const MEDIA: Record<string, string> = { image: 'image/', images: 'image/', video: 'video/', text: 'text/', json: 'application/json' };
const SINCE: Record<string, number> = { '1d': 864e5, '7d': 7 * 864e5, '30d': 30 * 864e5 };
const SYSTEM_PRESETS = [{ name: 'Last 24 h', view: { view: 'results' as const, extra: { since: '1d' } } }];

type ResultGroup = 'none' | 'project' | 'state' | 'media';
const GROUPS = [['none', 'None'], ['project', 'Project'], ['state', 'State'], ['media', 'Type']] as const;
const DEFAULT_GROUP: ResultGroup = 'none';
const SORTS: readonly SortField[] = [
  ['created', 'Created'],
  ['size', 'Size', ['Largest first', 'Smallest first'], 'value'],
  ['name', 'Name', ['Z–A', 'A–Z'], 'value'],
];
const DEFAULT_SORT = '-created';

type ResultRow = ArtifactRow & { code?: string | null; sortKey: (string | number)[] };
type ReplyWithCounts = { artifacts: ArtifactRow[]; nextBefore?: number | null; counts?: Record<string, number> };

function mediaKind(mediaType: string): 'image' | 'video' | 'text' | 'other' {
  const v = viewOf(mediaType);
  return v === 'download' ? 'other' : v;
}

function mediaIcon(mediaType: string): IconName {
  const k = mediaKind(mediaType);
  return k === 'image' ? 'panels-top-left' : k === 'video' ? 'play' : k === 'text' ? 'file-text' : 'archive';
}

function mediaLabel(key: string): string {
  return key === 'image' ? 'Images' : key === 'video' ? 'Video' : key === 'text' ? 'Text' : 'Other';
}

function isResultGroup(g: string | undefined): g is ResultGroup {
  return g === 'none' || g === 'project' || g === 'state' || g === 'media';
}

function groupIcon(group: string, key: string): IconName {
  if (group === 'state' && key in states.artifact) return states.artifact[key as StateOf<'artifact'>].icon;
  if (group === 'media') return key === 'image' ? 'panels-top-left' : key === 'video' ? 'play' : key === 'text' ? 'file-text' : 'archive';
  return 'folder-open';
}

export function Results({ id }: { id?: string }) {
  const [bucket, setBucket] = useState(() => Math.floor(Date.now() / BUCKET_MS) * BUCKET_MS);
  useEffect(() => {
    const t = setInterval(() => setBucket(Math.floor(Date.now() / BUCKET_MS) * BUCKET_MS), BUCKET_MS);
    return () => clearInterval(t);
  }, []);

  const [cursorState, setCursorState] = useState<{ key: string; before: number | null }>({ key: '', before: null });
  const [appended, setAppended] = useState<{ key: string; rows: ArtifactRow[]; nextBefore: number | null; seen: number[] }>({ key: '', rows: [], nextBefore: null, seen: [] });
  const [sel, setSel] = useState<string[]>([]);

  // Initial URL read for query/filter extraction before useListPage runs; useListPage owns the canonical URL hook.
  // Compute filter params from the same view shape inside local() and here via a shared helper.
  const buildFilter = (view: View) => {
    const statesFilter = (view.state ?? []) as StateOf<'artifact'>[];
    const raw = splitTerms(view.q ?? '', ['media', 'since']);
    const known = Object.fromEntries(Object.entries(raw.terms).filter(([k, v]) => k === 'media' ? Object.hasOwn(MEDIA, v.toLowerCase()) : Object.hasOwn(SINCE, v)));
    const typed = { terms: known, text: [raw.text, ...Object.entries(raw.terms).filter(([k]) => !(k in known)).map(([k, v]) => `${k}:${v}`)].filter(Boolean).join(' ') };
    const extra = { ...view.extra, ...typed.terms };
    const media = extra.media?.toLowerCase();
    const scoped = withFocus(view);
    const since = extra.since && SINCE[extra.since] ? bucket - SINCE[extra.since] : undefined;
    const filter = {
      q: typed.text || undefined,
      states: statesFilter.length ? statesFilter : undefined,
      projectIds: scoped.project?.length ? scoped.project : undefined,
      excludeProjectIds: scoped.exclude,
      taskId: extra.task,
      executionId: extra.execution,
      mediaType: media ? MEDIA[media] : undefined,
      since,
    };
    const fkey = JSON.stringify({ ...filter, sinceKey: extra.since ?? null });
    return { statesFilter, typed, extra, filter, fkey };
  };

  const { view: urlView } = useUrlView('results');
  const { typed, extra, filter, fkey } = useMemo(() => buildFilter(urlView), [urlView, bucket]);

  const ready = prefsRead.value;
  const firstPage = useOp('artifact_list', ready ? { ...filter, limit: PAGE } : null);
  const activeBefore = cursorState.key === fkey ? cursorState.before : null;
  const nextPage = useOp('artifact_list', ready && activeBefore !== null ? { ...filter, limit: PAGE, before: activeBefore } : null);

  useEffect(() => {
    if (activeBefore === null || !nextPage.data) return;
    setAppended(prev => {
      const baseRows = prev.key === fkey ? prev.rows : [];
      const seenCursors = prev.key === fkey ? prev.seen : [];
      if (seenCursors.includes(activeBefore)) return prev;
      const have = new Set(baseRows.map(r => r.id));
      const add = nextPage.data!.artifacts.filter(r => !have.has(r.id));
      return { key: fkey, rows: [...baseRows, ...add], nextBefore: nextPage.data!.nextBefore ?? null, seen: [...seenCursors, activeBefore] };
    });
  }, [fkey, activeBefore, nextPage.data]);

  const firstRows = firstPage.data?.artifacts ?? [];
  const tailRows = appended.key === fkey ? appended.rows : [];
  const firstIds = useMemo(() => new Set(firstRows.map(r => r.id)), [firstRows]);
  const allRows = useMemo(() => [...firstRows, ...tailRows.filter(r => !firstIds.has(r.id))], [firstRows, tailRows, firstIds]);

  const tailNextBefore: number | null | undefined = activeBefore !== null
    ? (appended.key === fkey && appended.seen.includes(activeBefore) ? appended.nextBefore : undefined)
    : (firstPage.data?.nextBefore ?? null);

  const firstReply = firstPage.data as ReplyWithCounts | null;
  const replyCounts = firstReply && 'counts' in firstReply && firstReply.counts ? firstReply.counts : undefined;

  const groupFromView = (v: View): ResultGroup => isResultGroup(v.extra?.group) ? v.extra!.group : DEFAULT_GROUP;
  const sortFromView = (v: View): string => v.extra?.sort && ['created', '-created', 'size', '-size', 'name', '-name'].includes(v.extra.sort) ? v.extra.sort : DEFAULT_SORT;

  const currentGroup = groupFromView(urlView);
  const projects = useOp('project_list', currentGroup === 'project' ? {} : null);
  const projectNames = useMemo(() => new Map<string, string>((projects.data?.projects ?? []).map(p => [String(p.id), String(p.name ?? p.id)])), [projects.data]);

  const local = (v: View): LocalList<ResultRow> => {
    const grp = groupFromView(v);
    const srt = sortFromView(v);
    const desc = srt.startsWith('-'), field = desc ? srt.slice(1) : srt;
    const gkey = (r: ArtifactRow) => grp === 'project' ? (r.projectId ?? '') : grp === 'state' ? r.state : grp === 'media' ? mediaKind(r.mediaType) : '';
    const glabel = (k: string) => {
      if (grp === 'project') return k ? (projectNames.get(k) ?? k) : 'No project';
      if (grp === 'state') return k in states.artifact ? states.artifact[k as StateOf<'artifact'>].label : k;
      if (grp === 'media') return mediaLabel(k);
      return '';
    };
    const cmpField = (a: ArtifactRow, b: ArtifactRow) => {
      if (field === 'size') return a.size - b.size;
      if (field === 'name') return (fileLabel(a.name) || a.name).localeCompare(fileLabel(b.name) || b.name);
      return a.createdAt - b.createdAt;
    };
    const rows: ResultRow[] = allRows.map(r => ({
      ...r,
      code: null,
      sortKey: [gkey(r), field === 'name' ? (fileLabel(r.name) || r.name) : (desc ? -(field === 'size' ? r.size : r.createdAt) : (field === 'size' ? r.size : r.createdAt))],
    })).sort((a, b) => {
      if (grp !== 'none' && a.sortKey[0] !== b.sortKey[0]) return String(a.sortKey[0]).localeCompare(String(b.sortKey[0]));
      return (desc ? -1 : 1) * cmpField(a, b) || a.id.localeCompare(b.id);
    });
    const countsPerGroup = new Map<string, number>();
    for (const r of rows) {
      const k = String(r.sortKey[0]);
      countsPerGroup.set(k, (countsPerGroup.get(k) ?? 0) + 1);
    }
    const groups = grp === 'none' ? [] : [...countsPerGroup.entries()].map(([k, n]) => ({ key: k, label: glabel(k), count: n }));
    const counts = replyCounts ?? Object.fromEntries(ARTIFACT_STATES.map(s => [s, 0]));
    const total = replyCounts ? Object.values(replyCounts).reduce((a, b) => a + b, 0) : allRows.length;
    const pending = !ready || (!firstPage.data && !firstPage.error) || (activeBefore !== null && !nextPage.data && !nextPage.error);
    const error = firstPage.error ?? nextPage.error ?? null;
    const reload = () => {
      setCursorState({ key: fkey, before: null });
      setAppended({ key: fkey, rows: [], nextBefore: null, seen: [] });
      firstPage.reload();
    };
    return { rows, all: rows, counts, groups, total, pending, error, reload };
  };

  const basePage = useListPage<ResultRow>({
    view: 'results',
    entity: 'result',
    local: local as (v: View) => LocalList<{ id: string; sortKey: (string | number)[] }>,
    defaultGroup: DEFAULT_GROUP,
    defaultSort: DEFAULT_SORT,
    system: SYSTEM_PRESETS,
    groupLabel: (_g, l) => l ?? '',
    saveExtra: v => ({ ...v.extra }),
    pickExtra: sv => ({ extra: sv.extra }),
    listId: () => fkey,
  });

  useEffect(() => { setSel([]); }, [fkey]);

  const group = groupFromView(basePage.view);
  const sort = sortFromView(basePage.view);
  const loadMore = () => {
    if (tailNextBefore == null || tailNextBefore === activeBefore) return Promise.resolve();
    if (nextPage.error) { nextPage.reload(); return Promise.resolve(); }
    setCursorState({ key: fkey, before: tailNextBefore });
    return Promise.resolve();
  };

  const set: typeof basePage.set = (patch, how) => {
    const { group: pg, sort: ps, sel: psel, preset: _pp, ...rest } = patch;
    if (psel !== undefined) setSel(psel);
    const nextExtra = { ...(rest.extra ?? basePage.view.extra) };
    let extraChanged = 'extra' in patch;
    if ('group' in patch) {
      nextExtra.group = pg && pg !== DEFAULT_GROUP ? pg : undefined;
      extraChanged = true;
    }
    if ('sort' in patch) {
      nextExtra.sort = ps && ps !== DEFAULT_SORT ? ps : undefined;
      extraChanged = true;
    }
    if (extraChanged || Object.keys(rest).length) {
      basePage.set({ ...rest, ...(extraChanged ? { extra: nextExtra } : {}) }, how);
    }
  };

  const select = (r: ResultRow) => {
    basePage.setSelectOn(true);
    basePage.setConfirm(null);
    setSel(prev => prev.includes(r.id) ? prev.filter(x => x !== r.id) : [...prev, r.id]);
  };
  const leave = () => { basePage.leave(); setSel([]); };
  const chosen = basePage.displayed.filter(r => sel.includes(r.id));
  const pinAll = (on: boolean) => { for (const r of chosen) if (basePage.pins.has(r.id) !== on) basePage.pins.toggle(r.id); };

  const groupsList = `results:${group}`, pinList = 'results:pinned';
  const closed = closedGroups([], listGroups(groupsList));
  if (listGroups(pinList).includes('pinned')) closed.add(PINNED);
  const toggleGroup = (key: string) => key === PINNED
    ? setListGroups(pinList, flipGroup(listGroups(pinList), 'pinned'))
    : setListGroups(groupsList, flipGroup(listGroups(groupsList), key));

  const pinnedIds = new Set(basePage.pins.ids);
  const pinnedRows = basePage.displayed.filter(r => pinnedIds.has(r.id));
  const restRows = basePage.displayed.filter(r => !pinnedIds.has(r.id));
  const items: ListItem<ResultRow>[] = [];
  if (pinnedRows.length) {
    items.push({ kind: 'group', key: PINNED, label: 'Pinned', count: pinnedRows.length, icon: 'pin' });
    if (!closed.has(PINNED)) for (const r of pinnedRows) items.push({ kind: 'row', r, pinned: true });
  }
  let lastGroup: string | null = null;
  for (const r of restRows) {
    const g = String(r.sortKey[0]);
    if (group !== 'none' && g !== lastGroup) {
      const meta = basePage.list.groups.find(x => x.key === g);
      items.push({ kind: 'group', key: g, label: meta?.label ?? (g || 'None'), count: meta?.count ?? 0 });
      lastGroup = g;
    }
    if (!closed.has(g)) items.push({ kind: 'row', r });
  }

  const pick = (name: string) => {
    if (name === 'Last 24 h') {
      const on = extra.since === '1d';
      set({ extra: { ...basePage.view.extra, since: on ? undefined : '1d' } });
      return;
    }
    basePage.pick(name);
  };
  const activePreset = extra.since === '1d' && !basePage.states.length && !basePage.view.q && !extra.media && !extra.task && !extra.execution
    ? 'Last 24 h'
    : basePage.activePreset;

  const page: ListPage<ResultRow> = {
    ...basePage,
    set,
    group,
    sort,
    rowsGroup: group,
    sel,
    selecting: basePage.selecting || sel.length > 0,
    select,
    leave,
    chosen,
    pinAll,
    closed,
    toggleGroup,
    items,
    endKey: tailNextBefore != null ? String(allRows.length) : '',
    list: { ...basePage.list, more: loadMore, state: firstPage.state },
    pick,
    activePreset,
  };

  const chips: Chip[] = Object.entries(basePage.view.extra ?? {})
    .filter(([k]) => !(k in typed.terms))
    .filter((e): e is [string, string] => ['media', 'since', 'task', 'execution'].includes(e[0]) && e[1] !== undefined)
    .map(([field, value]) => ({ field, value }));

  const now = Date.now();
  const list = <>
    <ShellStatus><OfflineMarker state={firstPage.state} now={now} /></ShellStatus>
    <ListPagePanel
      page={page}
      noun="Results"
      placeholder="Search results, media:image since:7d"
      family="artifact"
      groups={GROUPS}
      sorts={SORTS}
      focused={id}
      countsUnknown={!replyCounts}
      chips={chips}
      onRemoveChip={c => { const { [c.field]: _, ...rest } = basePage.view.extra ?? {}; set({ extra: rest }); }}
      listLabel="Results"
      title={r => fileLabel(r.name) || r.name}
      groupIcon={groupIcon}
      before={r => <span class="lead"><Icon name={mediaIcon(r.mediaType)} label={r.mediaType} /></span>}
      state={r => <StateBadge family="artifact" state={(r.state in states.artifact ? r.state : 'stored') as StateOf<'artifact'>} compact />}
      actions={() => []}
      menu={r => [
        ...(r.state === 'stored' ? [{ label: 'Download', icon: actions.download, onClick: () => { window.open(`/api/results/${encodeURIComponent(r.id)}`, '_blank', 'noopener'); } }] : []),
        ...(r.taskId ? [{ label: 'Results of this task', icon: actions.filter, onClick: () => set({ extra: { ...basePage.view.extra, task: r.taskId! } }) }] : []),
        ...(r.executionId ? [{ label: 'Results of this run', icon: actions.runs, onClick: () => set({ extra: { ...basePage.view.extra, execution: r.executionId! } }) }] : []),
      ]}
      empty={<Empty text={page.filtered || chips.length ? 'No results match.' : 'No results yet.'}
        action={(page.filtered || chips.length) ? <Button label="Clear filters" onClick={() => { basePage.setQuery(''); set({ state: [], q: undefined, project: [], extra: { group: group !== DEFAULT_GROUP ? group : undefined, sort: sort !== DEFAULT_SORT ? sort : undefined } }); }} /> : undefined} />}
      facts={r => <RowFacts>
        {r.taskId && <EntityCode code={r.taskCode} id={r.taskId} kind="task" />}
        {r.executionId && <EntityCode id={r.executionId} kind="execution" />}
        {r.projectId && <EntityCode code={r.projectId} id={r.projectId} kind="project" />}
        {r.scope === 'lab' && !r.taskId && <Fact text="Lab" tip="Lab experiment result" />}
        <Fact text={bytes(r.size).text} tip={`${bytes(r.size).long} · ${r.mediaType}`} />
        <Fact tip="Created"><AgeChip t={r.createdAt} /></Fact>
      </RowFacts>}
    />
  </>;

  return <ListDetail
    list={list}
    detailKey={id}
    idle={<ResultsIdle rows={allRows} counts={replyCounts} hasMore={tailNextBefore != null} busy={page.list.pending && !allRows.length} />}
    detail={id ? <Result key={id} id={id} onClose={() => set({ focus: undefined })} onFilter={(field, value) => set({ extra: { ...basePage.view.extra, [field]: value } })} /> : undefined}
  />;
}

function ResultsIdle({ rows, counts, hasMore, busy }: { rows: ArtifactRow[]; counts?: Record<string, number>; hasMore: boolean; busy: boolean }) {
  if (busy) return <article class="detail stack" data-ui="detail" aria-label="Results summary"><Skeleton rows={4} /></article>;
  if (!rows.length) return <Empty text="Pick a result to open it here." />;
  const totalBytes = rows.reduce((sum, r) => sum + (Number(r.size) || 0), 0);
  const totalCount = counts ? Object.values(counts).reduce((a, b) => a + b, 0) : rows.length;
  const storedCount = counts?.stored ?? rows.filter(r => r.state === 'stored').length;
  const failedCount = counts?.failed ?? rows.filter(r => r.state === 'failed').length;
  const pendingCount = counts?.pending ?? rows.filter(r => r.state === 'pending').length;
  const partialNote = !counts && hasMore ? 'loaded window' : undefined;
  const items: TotalItem[] = [
    { label: 'Results', m: { value: totalCount, basis: 'measured', partial: partialNote }, kind: 'count' },
    { label: 'Stored', m: { value: storedCount, basis: 'measured', partial: partialNote }, kind: 'count' },
    ...(pendingCount > 0 ? [{ label: 'Uploading', m: { value: pendingCount, basis: 'measured' as const, partial: partialNote }, kind: 'count' as const }] : []),
    ...(failedCount > 0 ? [{ label: 'Failed', m: { value: failedCount, basis: 'measured' as const, partial: partialNote }, kind: 'count' as const }] : []),
    { label: 'Size', m: { value: totalBytes, basis: 'measured', partial: hasMore ? 'loaded window' : undefined }, kind: 'bytes' },
  ];
  const byKind = new Map<'image' | 'video' | 'text' | 'other', { count: number; bytes: number }>();
  for (const r of rows) {
    const k = mediaKind(r.mediaType);
    const cur = byKind.get(k) ?? { count: 0, bytes: 0 };
    cur.count++;
    cur.bytes += Number(r.size) || 0;
    byKind.set(k, cur);
  }
  return (
    <article class="detail stack" data-ui="detail" aria-label="Results summary">
      <DetailSection title="Summary">
        <Totals items={items} />
      </DetailSection>
      <DetailSection title="By type">
        {(['image', 'video', 'text', 'other'] as const).map(k => {
          const v = byKind.get(k);
          if (!v) return null;
          return (
            <KvRow
              key={k}
              icon={k === 'image' ? 'panels-top-left' : k === 'video' ? 'play' : k === 'text' ? 'file-text' : 'archive'}
              label={mediaLabel(k)}
              meta={bytes(v.bytes).text}
              value={<Num m={{ value: v.count, basis: 'measured' }} kind="count" />}
            />
          );
        })}
      </DetailSection>
    </article>
  );
}

function Result({ id, onClose, onFilter }: { id: string; onClose: () => void; onFilter: (field: string, value: string) => void }) {
  const { data: a, error } = useOp('artifact_get', { artifactId: id });
  const [tab, setTab] = useState('');
  if (error && !a) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Result did not load: ${error}`} /></article>;
  if (!a) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const src = `/api/results/${encodeURIComponent(a.id)}`, meta = { id: a.id, name: a.name, mediaType: a.mediaType, size: Number(a.size) || 0 };
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={a.name}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      {a.projectId && <EntityCode code={a.projectId} id={a.projectId} kind="project" />}
      {a.taskId && <EntityCode id={a.taskId} kind="task" />}
      {a.executionId && <EntityCode id={a.executionId} kind="execution" />}
      <span class="grow" />
      {a.taskId && <Button label="Results of this task" icon="list-filter" iconOnly onClick={() => onFilter('task', a.taskId!)} />}
    </header>
    <ResultViewer name={a.name} type={a.mediaType} size={meta.size} source={a.executionId ? 'agent run' : 'no run'} href={a.state === 'stored' ? src : undefined}
      copies={a.copies.map(c => ({ where: c.storeId, state: c.state === 'verified' ? 'stored' : ['failed', 'deleted', 'purge_pending'].includes(c.state) ? 'unavailable' : 'pending' }))} tabs={[]} tab={tab} onTab={setTab}>
      {a.state === 'stored' ? <ArtifactView a={meta} src={src} /> : <Empty text={`This result is ${a.state}.`} />}
    </ResultViewer>
  </article>;
}
