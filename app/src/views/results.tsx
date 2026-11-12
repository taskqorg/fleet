// Results (docs/designs/fleet-conversations.md, Stage 4 item 26): stored results and other artifacts with Tasks-like filters,
// newest first, state icon first (OR-22); the focused one opens in the result viewer beside the list (OR-14).
import { useEffect, useRef, useState } from 'preact/hooks';
import {
  EntityCode, ListDetail, ListFrame, FilterBar, StateCounters, VirtualList, StateBadge, Button, ResultViewer, ArtifactView, Empty, ErrorBanner, Skeleton,
  AgeChip, bytes, splitTerms, type StateOf, type Chip,
} from '../../../packages/ui/src/index.ts';
import tokens from '../../../packages/ui/tokens.json';
import { op } from '../api.ts';
import { withFocus, prefsRead } from '../prefs.ts';
import { subscribe } from '../feed.ts';
import { useDoc } from '../store.ts';
import { useUrlView, useSearchBox } from '../url.ts';

const ROW = parseInt(tokens.size['row-2']); // two-line catalogue row
const PAGE = 100;
type Row = { id: string; name: string; mediaType: string; size: number; state: StateOf<'artifact'>; taskId: string | null; taskCode?: string | null; executionId: string | null; projectId: string | null; createdAt: number; seq: number };
// Filters beyond state are typed in search (OR-36: media:image since:7d) or come in a link's extra keys.
const MEDIA: Record<string, string> = { image: 'image/', images: 'image/', video: 'video/', text: 'text/', json: 'application/json' };
const SINCE: Record<string, number> = { '1d': 864e5, '7d': 7 * 864e5, '30d': 30 * 864e5 };

export function Results({ id }: { id?: string }) {
  const { view, set, href } = useUrlView('results');
  const [query, setQuery] = useSearchBox(view.q, q => set({ q: q || undefined }, 'type'));
  const states = (view.state ?? []) as StateOf<'artifact'>[];
  // A media: or since: value the list does not know stays search text, so the list never empties silently.
  const raw = splitTerms(view.q ?? '', ['media', 'since']), known = Object.fromEntries(Object.entries(raw.terms).filter(([k, v]) => k === 'media' ? Object.hasOwn(MEDIA, v.toLowerCase()) : Object.hasOwn(SINCE, v)));
  const typed = { terms: known, text: [raw.text, ...Object.entries(raw.terms).filter(([k]) => !(k in known)).map(([k, v]) => `${k}:${v}`)].filter(Boolean).join(' ') };
  const extra = { ...view.extra, ...typed.terms };
  const media = extra.media?.toLowerCase();
  const filter = { q: typed.text || undefined, states: states.length ? states : undefined, projectIds: withFocus(view).project?.length ? withFocus(view).project : undefined, taskId: extra.task, executionId: extra.execution,
    mediaType: media ? MEDIA[media] : undefined, since: extra.since && SINCE[extra.since] ? Date.now() - SINCE[extra.since] : undefined };
  const fkey = JSON.stringify({ ...filter, since: extra.since });
  const [rows, setRows] = useState<Row[] | null>(null), [next, setNext] = useState<number | null>(null), [error, setError] = useState<string | null>(null), [busy, setBusy] = useState(false);
  // gen: a newer load (filter change, refresh) wins; an older answer is dropped.
  const gen = useRef(0), pages = useRef(0); // live refresh only while one page is shown, so paging is never thrown away
  const load = async (before?: number) => {
    const g = before ? gen.current : ++gen.current; setBusy(true);
    try { const r = await op('artifact_list', { ...filter, limit: PAGE, ...(before ? { before } : {}) });
      if (g !== gen.current) return;
      pages.current = before ? pages.current + 1 : 1;
      setRows(x => before && x ? [...x, ...(r.artifacts as Row[]).filter(a => !x.some(y => y.id === a.id))] : r.artifacts as Row[]); setNext(r.nextBefore ?? null); setError(null); }
    catch (e: any) { setError(e.message); } finally { setBusy(false); }
  };
  const ready = prefsRead.value; // the filter carries the focus
  useEffect(() => { if (!ready) { gen.current++; setRows(null); return; } load(); return subscribe({ resync: () => load(), changes: cs => { if (cs.some(c => c.kind === 'artifact' || c.kind === 'task') && pages.current <= 1) load(); } }); }, [fkey, ready]);
  const chips: Chip[] = Object.entries(view.extra ?? {}).filter(([k]) => !(k in typed.terms)).filter(([k]) => ['media', 'since', 'task', 'execution'].includes(k)).map(([field, value]) => ({ field, value }));
  const now = Date.now();
  const list = <>
    <header class="page-h"><h1>Results</h1></header>
    {error && <ErrorBanner text={`Results did not load: ${error}`} onRetry={() => load()} />}
    <section class="panel">
      <ListFrame pending={busy} bar={<FilterBar query={query} placeholder="Search results, media:image since:7d" onQuery={setQuery} chips={chips}
        onRemove={c => { const { [c.field]: _, ...rest } = view.extra ?? {}; set({ extra: rest }); }}
        onClear={() => { setQuery(''); set({ q: undefined, state: [], extra: {} }); }} shown={rows?.length ?? 0} total={rows?.length ?? 0}
        extra={<StateCounters family="artifact" label="Results by state" counts={{ stored: null, pending: null, failed: null, deleted: null }} active={states}
          onAll={() => set({ state: [] })} onToggle={s => set({ state: states.includes(s) ? states.filter(x => x !== s) : [...states, s] })} />} />}>
        {!rows ? <Skeleton rows={8} /> : !rows.length ? <Empty text="No results match." />
          : <VirtualList items={rows} getKey={r => r.id} rowHeight={ROW} follow="top" label="Results" onEnd={() => next !== null && !busy && load(next)}
            columns="1.25rem minmax(0,1fr) 4rem 3rem" rowClass={r => r.id === id ? 'sel' : ''}
            header={<><span /><span>Result</span><span class="r">Size</span><span class="r">Age</span></>}
            render={r => <>
              <span class="lead"><StateBadge family="artifact" state={r.state} compact /></span>
              <span class="tt"><a class="title row-link" href={href({ focus: r.id })} onClick={e => { e.preventDefault(); set({ focus: r.id }); }}>{r.name}</a>{r.taskId && <EntityCode code={r.taskCode} id={r.taskId} />}{r.projectId && <span class="id">{r.projectId}</span>}</span>
              <span class="r svc">{bytes(r.size).text}</span><span class="r"><AgeChip t={r.createdAt} now={now} /></span>
            </>} />}
      </ListFrame>
    </section>
  </>;
  return <ListDetail list={list} detail={id ? <Result key={id} id={id} onClose={() => set({ focus: undefined })} onFilter={(field, value) => set({ extra: { ...extra, [field]: value } })} /> : undefined} detailKey={id} />;
}

function Result({ id, onClose, onFilter }: { id: string; onClose: () => void; onFilter: (field: string, value: string) => void }) {
  const { data: a, error } = useDoc<any>(`artifact:${id}`, () => op('artifact_get', { artifactId: id }), c => c.kind === 'artifact' && c.id === id);
  const [tab, setTab] = useState('View');
  if (error && !a) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Result did not load: ${error}`} /></article>;
  if (!a) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const src = `/api/results/${encodeURIComponent(a.id)}`, meta = { id: a.id, name: a.name, mediaType: a.mediaType, size: Number(a.size) || 0 };
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={a.name}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><span class="grow" />
      {a.taskId && <Button label="Results of this task" icon="list-filter" iconOnly onClick={() => onFilter('task', a.taskId)} />}</header>
    <ResultViewer name={a.name} type={a.mediaType} size={meta.size} source={a.executionId ? 'agent run' : 'no run'} href={a.state === 'stored' ? src : undefined}
      copies={(a.copies ?? []).map((c: any) => ({ where: c.storeId, state: c.state === 'verified' ? 'stored' : ['failed', 'deleted', 'purge_pending'].includes(c.state) ? 'unavailable' : 'pending' }))} tabs={['View']} tab={tab} onTab={setTab}>
      {a.state === 'stored' ? <ArtifactView a={meta} src={src} /> : <Empty text={`This result is ${a.state}.`} />}
    </ResultViewer>
  </article>;
}
