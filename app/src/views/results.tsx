// Results (docs/designs/fleet-conversations.md, Stage 4 item 26): stored results and other artifacts with Tasks-like filters,
// newest first, state icon first (OR-22); the focused one opens in the result viewer beside the list (OR-14).
import { useState } from 'preact/hooks';
import {
  EntityCode, ListDetail, ListFrame, FilterBar, StateCounters, VirtualList, StateBadge, Button, ResultViewer, ArtifactView, Empty, ErrorBanner, Skeleton,
  AgeChip, bytes, fileLabel, splitTerms, catalogueRow, type StateOf, type Chip,
} from '../../../packages/ui/src/index.ts';
import { withFocus, prefsRead } from '../prefs.ts';
import { useOp } from '../store.ts';
import { useUrlView, useSearchBox } from '../url.ts';

const PAGE = 100;
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
  const scoped = withFocus(view);
  const filter = { q: typed.text || undefined, states: states.length ? states : undefined, projectIds: scoped.project?.length ? scoped.project : undefined, excludeProjectIds: scoped.exclude, taskId: extra.task, executionId: extra.execution,
    mediaType: media ? MEDIA[media] : undefined, since: extra.since && SINCE[extra.since] ? Date.now() - SINCE[extra.since] : undefined };
  // The shown window grows by a page at the end (OR-11); the whole window is one read, so a change rereads what is shown and
  // paging never throws rows away. A filter change starts at one page again.
  const fkey = JSON.stringify({ ...filter, since: extra.since });
  const [pages, setPages] = useState({ key: fkey, n: 1 });
  const n = pages.key === fkey ? pages.n : 1;
  const ready = prefsRead.value; // the filter carries the focus and ignore
  const read = useOp('artifact_list', ready ? { ...filter, limit: PAGE * n } : null);
  const rows = read.data?.artifacts ?? null, next = read.data?.nextBefore ?? null, error = read.error;
  const busy = !!ready && !read.data && !read.error;
  const load = () => read.reload();
  const chips: Chip[] = Object.entries(view.extra ?? {}).filter(([k]) => !(k in typed.terms)).filter((e): e is [string, string] => ['media', 'since', 'task', 'execution'].includes(e[0]) && e[1] !== undefined).map(([field, value]) => ({ field, value }));
  const list = <>
    {error && <ErrorBanner text={`Results did not load: ${error}`} onRetry={() => load()} />}
    <section class="panel">
      <ListFrame pending={busy} bar={<FilterBar query={query} placeholder="Search results, media:image since:7d" onQuery={setQuery} chips={chips}
        onRemove={c => { const { [c.field]: _, ...rest } = view.extra ?? {}; set({ extra: rest }); }}
        onClear={() => { setQuery(''); set({ q: undefined, state: [], extra: {} }); }} shown={rows?.length ?? 0} total={rows?.length ?? 0}
        counters={<StateCounters family="artifact" label="Results by state" counts={{ stored: null, pending: null, failed: null, deleted: null }} active={states}
          onAll={() => set({ state: [] })} onToggle={s => set({ state: states.includes(s) ? states.filter(x => x !== s) : [...states, s] })} />} />}>
        {!rows ? <Skeleton rows={8} /> : !rows.length ? <Empty text="No results match." />
          : <VirtualList items={rows} getKey={r => r.id} rowHeight={catalogueRow()} follow="top" label="Results" onEnd={() => next !== null && setPages({ key: fkey, n: n + 1 })}
            columns="1.25rem minmax(0,1fr) 4rem 3rem" rowClass={r => r.id === id ? 'sel' : ''}
            header={<><span /><span>Result</span><span class="r">Size</span><span class="r">Age</span></>}
            render={r => <>
              <span class="lead"><StateBadge family="artifact" state={r.state as StateOf<'artifact'>} compact /></span>
              <span class="tt"><a class="title row-link" href={href({ focus: r.id })} onClick={e => { e.preventDefault(); set({ focus: r.id }); }}>{fileLabel(r.name) || r.name}</a>{r.taskId && r.taskCode && <EntityCode code={r.taskCode} id={r.taskId} kind="task" />}{(r.projectId || r.scope === 'lab') && <span class="id">{[r.scope === 'lab' && !r.taskCode ? 'Lab' : null, r.projectId].filter(Boolean).join(' · ')}</span>}</span>
              <span class="r svc">{bytes(r.size).text}</span><span class="r"><AgeChip t={r.createdAt} /></span>
            </>} />}
      </ListFrame>
    </section>
  </>;
  return <ListDetail list={list} detail={id ? <Result key={id} id={id} onClose={() => set({ focus: undefined })} onFilter={(field, value) => set({ extra: { ...extra, [field]: value } })} /> : undefined} detailKey={id} />;
}

function Result({ id, onClose, onFilter }: { id: string; onClose: () => void; onFilter: (field: string, value: string) => void }) {
  const { data: a, error } = useOp('artifact_get', { artifactId: id });
  const [tab, setTab] = useState('View');
  if (error && !a) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Result did not load: ${error}`} /></article>;
  if (!a) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const src = `/api/results/${encodeURIComponent(a.id)}`, meta = { id: a.id, name: a.name, mediaType: a.mediaType, size: Number(a.size) || 0 };
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={a.name}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><span class="grow" />
      {a.taskId && <Button label="Results of this task" icon="list-filter" iconOnly onClick={() => onFilter('task', a.taskId!)} />}</header>
    <ResultViewer name={a.name} type={a.mediaType} size={meta.size} source={a.executionId ? 'agent run' : 'no run'} href={a.state === 'stored' ? src : undefined}
      copies={a.copies.map(c => ({ where: c.storeId, state: c.state === 'verified' ? 'stored' : ['failed', 'deleted', 'purge_pending'].includes(c.state) ? 'unavailable' : 'pending' }))} tabs={['View']} tab={tab} onTab={setTab}>
      {a.state === 'stored' ? <ArtifactView a={meta} src={src} /> : <Empty text={`This result is ${a.state}.`} />}
    </ResultViewer>
  </article>;
}
