import { projectDefinitionTouched } from '../invalidation.ts';
// Projects configuration & resource analytics (docs/design-system/pages/projects.md).
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  AreaChart, StatusLine, Button, Choice, CodeEditor, CostFigures, DetailSection, Empty, ErrorBanner, Icon,
  KvRow, ListDetail, Num, SearchField, TextField, Totals, VirtualList, matchSearch,
  msMeasured, numMeasured, type ChartBucket, type ChartSeries, type Measured, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { op, useAlive, writeOnce, preparedResourcesAvailable, createDraftPersistence, authorizedDraftScope, AccessChanged, OpError, type DraftPersistence, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';
import { subscribe } from '../feed.ts';
import { accessGeneration, isAccessCurrent } from '../access.ts';
import { resourceTotals, useProjectResources, PreparedProjectResources } from './resources.tsx';

type Bundle = { settings: Record<string, any>; contextFiles: { path: string; text: string }[] };
type AnalyticsReply = Ops['analytics_query']['reply'];
type AnalyticsDim = NonNullable<Ops['analytics_query']['request']['dimensions']>[number];
type AnalyticsFilter = NonNullable<Ops['analytics_query']['request']['filters']>;
type WindowKey = '24h' | '7d' | '30d';
type GrainKey = 'hour' | 'day' | 'week';
type ChartMetric = 'tokens' | 'executions' | 'cost';

const ANALYTICS_MEASURES: NonNullable<Ops['analytics_query']['request']['measures']> = [
  'executions', 'failed', 'tokens_input', 'tokens_output', 'tokens_cache_read', 'tokens_cache_write',
  'tokens_reasoning', 'wall_ms', 'cpu_ms', 'peak_memory_bytes', 'calls', 'credits', 'cost',
];
const WINDOW_MS: Record<WindowKey, number> = { '24h': 86400e3, '7d': 7 * 86400e3, '30d': 30 * 86400e3 };
const DIMS: { id: AnalyticsDim; label: string }[] = [
  { id: 'model', label: 'Model' },
  { id: 'harness', label: 'Harness' },
  { id: 'pipeline_revision', label: 'Pipeline rev' },
  { id: 'project', label: 'Project' },
  { id: 'profile_id', label: 'Agent' },
  { id: 'host', label: 'Host' },
  { id: 'outcome', label: 'Outcome' },
  { id: 'state', label: 'State' },
];

const fresh = (): Bundle => ({
  settings: { id: '', name: '', contextFiles: ['rules.md'], roles: { pm: 'pm' }, repos: [], environments: [] },
  contextFiles: [{ path: 'rules.md', text: '' }],
});

function parseBucketMs(key: string, grain: GrainKey): [number, number] | null {
  const start = Date.parse(key.includes('T') ? key : `${key}T00:00:00Z`);
  if (!Number.isFinite(start)) return null;
  const span = grain === 'hour' ? 3600e3 : grain === 'week' ? 7 * 86400e3 : 86400e3;
  return [start, start + span];
}

function shortBucketLabel(key: string, grain: GrainKey): string {
  if (grain === 'hour') return key.slice(11, 16) || key;
  return key.length >= 10 ? key.slice(5, 10) : key;
}

function sumTokens(m: AnalyticsReply['rows'][number]['measures']): Measured {
  const req = [m.tokens_input, m.tokens_output, m.tokens_cache_read, m.tokens_cache_write].filter((p): p is NonNullable<typeof p> => !!p);
  const all = m.tokens_reasoning ? [...req, m.tokens_reasoning] : req;
  if (!all.length) return { value: null, basis: 'unavailable' };
  let sum = 0, known = 0, sub = false, est = false, partial: string | undefined;
  for (const p of all) {
    if (p.value != null) {
      sum += p.value;
      known++;
      if (p.basis === 'subscription') sub = true;
      else if (p.basis === 'estimated') est = true;
      if (p.partial && !partial) partial = p.partial;
    } else if (p !== m.tokens_reasoning && p.basis === 'unavailable' && !partial) {
      partial = 'incomplete telemetry';
    }
  }
  if (known === 0) return { value: null, basis: 'unavailable' };
  const basis: Measured['basis'] = est ? 'estimated' : sub ? 'subscription' : 'measured';
  return { value: sum, basis, ...(partial ? { partial } : {}) };
}

export function Projects({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [creating, setCreating] = useState(false);
  const [mobileAnalytics, setMobileAnalytics] = useState(false);
  const [query, setQuery] = useState('');
  const list = useDoc<any>('projects', () => op('project_list', {}), c => c.kind === 'definition');
  const rows = (list.data?.projects ?? []).filter((p: any) => matchSearch(query, `${p.name} ${p.id}`, { project: p.id }));
  const pane = (
    <>
      <header class="page-h">
        <span class="grow" />
        <Button label="Analytics" icon="chart-no-axes-column" kind="quiet" onClick={() => { setCreating(false); setMobileAnalytics(true); go('/projects'); }} />
        <Button label="Create project" icon="plus" onClick={() => { setMobileAnalytics(false); setCreating(true); }} />
      </header>
      <SearchField label="Find project" value={query} onInput={setQuery} onSearch={() => {}} />
      {list.error ? <div role="alert">{list.error}</div> : !list.data ? <Empty text="Loading…" /> : (
        <VirtualList
          items={rows}
          getKey={(p: any) => p.id}
          rowHeight={52}
          follow="top"
          label="Projects"
          render={(p: any) => (
            <>
              <Icon name="folder-open" />
              <a
                class="title row-link"
                href={'/projects/' + encodeURIComponent(p.id)}
                onClick={e => { e.preventDefault(); setCreating(false); setMobileAnalytics(false); go('/projects/' + encodeURIComponent(p.id)); }}
              >
                {p.name}<span class="id">{p.id}</span>
              </a>
            </>
          )}
        />
      )}
      <span>{rows.length} of {list.data?.projects.length ?? '—'}</span>
    </>
  );
  const overview = (
    <article class="detail" data-ui="detail" tabIndex={-1} aria-label="Projects analytics">
      <header class="detail-h">
        {mobileAnalytics && <Button label="Close" icon="x" iconOnly onClick={() => setMobileAnalytics(false)} />}
        <h2 class="detail-title">Resource analytics</h2>
      </header>
      <ProjectAnalytics onOpenProject={pid => { setMobileAnalytics(false); go('/projects/' + encodeURIComponent(pid)); }} />
    </article>
  );
  return (
    <ListDetail
      list={pane}
      detail={creating || id ? (
        <ProjectEditor key={creating ? 'create' : id} id={creating ? undefined : id} onSaved={p => { setCreating(false); go('/projects/' + encodeURIComponent(p)); }} />
      ) : mobileAnalytics ? overview : undefined}
      idle={overview}
      detailKey={creating ? 'create' : id || (mobileAnalytics ? 'analytics' : undefined)}
    />
  );
}

function ProjectEditor({ id, onSaved }: { id?: string; onSaved: (id: string) => void }) {
  const [, go] = useLocation();
  const editorGeneration = useRef(accessGeneration()).current;
  const [bundle, applyBundle] = useState<Bundle>(fresh);
  const baseline = useRef(JSON.stringify(bundle)), latest = useRef(bundle); latest.current = bundle;
  type EditBase = { content: Bundle; revision: number; contentSha: string; repoSha: string | null };
  type PendingSave = { name: 'project_update'; body: Ops['project_update']['request'] } | { name: 'project_create'; body: Ops['project_create']['request'] };
  const draft = useRef<DraftPersistence | null>(null), editBase = useRef<EditBase | null>(null), pendingSave = useRef<PendingSave | null>(null);
  const [priorSave, setPriorSave] = useState(false);
  const remember = (value: Bundle) => {
    if (!draft.current || !editBase.current) return;
    try { draft.current.write(JSON.stringify(value) === baseline.current && !pendingSave.current ? '' : JSON.stringify({ bundle: value, base: editBase.current, requestId: requestId.current, pendingSave: pendingSave.current })); }
    catch (e) { if (!(e instanceof AccessChanged)) setError('Draft storage unavailable; keep this page open.'); }
  };
  const setBundle = (next: Bundle | ((old: Bundle) => Bundle)) => { const value = typeof next === 'function' ? next(latest.current) : next; latest.current = value; applyBundle(value); remember(value); };
  const validBundle = (value: any): value is Bundle => value && value.settings && typeof value.settings === 'object' && Array.isArray(value.contextFiles) && value.contextFiles.every((file: any) => typeof file.path === 'string' && typeof file.text === 'string');
  const restoreEdit = () => {
    try {
      const text = draft.current?.read(); if (!text) return null;
      const saved = JSON.parse(text);
      return validBundle(saved.bundle) && validBundle(saved.base?.content) && Number.isSafeInteger(saved.base.revision) && typeof saved.base.contentSha === 'string' && typeof saved.requestId === 'string' && (!saved.pendingSave || (['project_create', 'project_update'].includes(saved.pendingSave.name) && typeof saved.pendingSave.body?.requestId === 'string' && validBundle(saved.pendingSave.body.content))) ? saved as { bundle: Bundle; base: EditBase; requestId: string; pendingSave?: PendingSave | null } : null;
    } catch { return null; }
  };
  const [conflict, setConflict] = useState(''), [prepared, setPrepared] = useState<boolean | null>(null);
  const [breakdown, setBreakdown] = useState<'tools' | 'costs' | null>(null);
  const summary = useProjectResources(id, prepared === true && !breakdown);
  useEffect(() => { let active = true; preparedResourcesAvailable().then(v => { if (active) setPrepared(v); }).catch(e => { if (active && !(e instanceof AccessChanged)) setPrepared(false); }); return () => { active = false; }; }, []);
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const saving = useRef(false);
  const [ready, setReady] = useState<any>(null);
  const [access, setAccess] = useState<any>(null);
  const [resources, setResources] = useState<Ops['project_context']['reply']['resources']>(undefined);
  const [repoSha, setRepoSha] = useState<string | null>(null);
  const [sourceState, setSourceState] = useState('');
  const requestId = useRef<string>(crypto.randomUUID()), alive = useAlive();
  const [loaded, setLoaded] = useState(false);
  const loadSequence = useRef(0);
  const reloadAfterSave = useRef(false);
  useEffect(() => {
    if (id || !isAccessCurrent(editorGeneration)) return;
    draft.current = createDraftPersistence(authorizedDraftScope({ kind: 'project-create', id: '' }));
    const saved = restoreEdit(); editBase.current = saved?.base ?? { content: fresh(), revision: 0, contentSha: '', repoSha: null };
    baseline.current = JSON.stringify(editBase.current.content); requestId.current = saved?.requestId ?? crypto.randomUUID(); pendingSave.current = saved?.pendingSave ?? null; setPriorSave(!!pendingSave.current);
    if (saved) setBundle(saved.bundle);
    setLoaded(true);
  }, [id]);
  const readDetail = async (projectId: string) => {
    if (prepared) {
      try { return await op('project_detail', { projectId }, { signal: alive }); }
      catch (e) { if (!(e instanceof OpError) || e.status !== 503) throw e; setPrepared(false); }
    }
    return op('project_context', { projectId }, { signal: alive });
  };
  const load = async () => {
    if (!id || prepared === null) return;
    if (saving.current) { reloadAfterSave.current = true; return; }
    const generation = accessGeneration(), sequence = ++loadSequence.current;
    try {
      const p = await readDetail(id);
      if (!isAccessCurrent(generation) || alive.aborted || sequence !== loadSequence.current || saving.current) return;
      if (JSON.stringify(latest.current) !== baseline.current) {
        if (p.revision !== editBase.current?.revision) setConflict('Project definition changed while you were editing. Review your edits before saving; reload cannot discard them.');
        return;
      }
      // The target is freshly authorized before authored input for this actor is inspected.
      draft.current = createDraftPersistence(authorizedDraftScope({ kind: 'project-editor', id }));
      const saved = restoreEdit();
      if (editBase.current?.contentSha !== p.contentSha || editBase.current?.repoSha !== (p.repoSha ?? null)) setReady(null);
      editBase.current = saved?.base ?? { content: p.content, revision: p.revision, contentSha: p.contentSha, repoSha: p.repoSha ?? null };
      baseline.current = JSON.stringify(editBase.current.content);
      requestId.current = saved?.requestId ?? crypto.randomUUID(); pendingSave.current = saved?.pendingSave ?? null; setPriorSave(!!pendingSave.current);
      setError(''); setBundle(saved?.bundle ?? p.content); setRevision(editBase.current.revision); setAccess(p.initialAccess);
      setResources('resources' in p ? p.resources : undefined);
      setRepoSha(editBase.current.repoSha); setSourceState(p.sourceState ?? '');
      setConflict(saved && saved.base.contentSha !== p.contentSha ? 'Project definition changed while you were editing. Review your edits before saving; reload cannot discard them.' : ''); setLoaded(true);
    } catch (e: any) { if (!(e instanceof AccessChanged) && !alive.aborted && isAccessCurrent(generation) && sequence === loadSequence.current) setError(e.message); }
  };
  const reload = useRef(load); reload.current = load;
  useEffect(() => {
    void reload.current();
    return subscribe({ resync: () => { void reload.current(); }, changes: changes => { if (projectDefinitionTouched(changes, id)) void reload.current(); } });
  }, [id, prepared]);
  const setting = (key: string, value: any) => setBundle(b => ({ ...b, settings: { ...b.settings, [key]: value } }));
  const field = (label: string, value: string, save: (s: string) => void) => (
    <TextField help="Save project applies the complete version." label={label} value={value} onChange={save} />
  );
  const save = async () => {
    if (saving.current) return;
    saving.current = true;
    // Ordinary reads cannot replace the optimistic base or frozen request during a save.
    ++loadSequence.current;
    const generation = accessGeneration();
    const mutation: PendingSave = pendingSave.current ?? (id
      ? { name: 'project_update', body: { projectId: id, requestId: requestId.current, baseRevision: revision, content: bundle, repoSha } }
      : { name: 'project_create', body: { id: bundle.settings.id, requestId: requestId.current, content: bundle } });
    const submitted = mutation.body.content;
    pendingSave.current = mutation; setPriorSave(true); remember(latest.current); setBusy(true);
    try {
      const unknown = () => setError('unknown, retrying');
      const p = mutation.name === 'project_update'
        ? await writeOnce(mutation.name, mutation.body, unknown, { signal: alive })
        : await writeOnce(mutation.name, mutation.body, unknown, { signal: alive });
      if (!isAccessCurrent(generation)) return;
      if (typeof p.projectId !== 'string' || typeof p.revision !== 'number' || !Number.isSafeInteger(p.revision) || typeof p.contentSha !== 'string') throw Error('Saved reply has no verifiable project version.');
      const check = await readDetail(p.projectId);
      if (!isAccessCurrent(generation) || alive.aborted) return;
      if (check.revision !== p.revision || check.contentSha !== p.contentSha) throw Error('Saved version changed; reload before editing.');
      setError(''); pendingSave.current = null; setPriorSave(false); requestId.current = crypto.randomUUID();
      baseline.current = JSON.stringify(check.content);
      if (editBase.current?.contentSha !== check.contentSha || editBase.current?.repoSha !== (check.repoSha ?? null)) setReady(null);
      editBase.current = { content: check.content, revision: check.revision, contentSha: check.contentSha, repoSha: check.repoSha ?? null };
      if (JSON.stringify(latest.current) === JSON.stringify(submitted)) setBundle(check.content); else remember(latest.current);
      setRevision(check.revision); setAccess(check.initialAccess);
      setResources('resources' in check ? check.resources : undefined);
      setRepoSha(check.repoSha ?? null); setSourceState(check.sourceState ?? '');
      setConflict(''); onSaved(p.projectId);
    } catch (e: any) { if (isAccessCurrent(generation) && !alive.aborted) {
      if (e instanceof OpError && [400, 404, 409].includes(e.status)) { pendingSave.current = null; setPriorSave(false); requestId.current = crypto.randomUUID(); remember(latest.current); }
      setError(e.message);
    } } finally {
      saving.current = false;
      if (isAccessCurrent(generation) && !alive.aborted) {
        setBusy(false);
        if (reloadAfterSave.current) { reloadAfterSave.current = false; void reload.current(); }
      }
    }
  };
  const bindings = (kind: 'repos' | 'environments', title: string, keys: string[]) => {
    const items: any[] = bundle.settings[kind] ?? [];
    const change = (i: number, k: string, v: string) => setting(kind, items.map((item, n) => n === i ? { ...item, [k]: v } : item));
    return (
      <DetailSection title={title}>
        {items.map((item, i) => (
          <div key={i}>
            {keys.map(k => field(`${title} ${i + 1} ${{ id: 'ID', remote: 'URL', defaultBranch: 'Default branch', connection: 'Git connection', hostId: 'Host', checkoutPath: 'Checkout path', repo: 'Repository' }[k] ?? k}`, item[k] ?? '', v => change(i, k, v)))}
            <Button label={`Remove ${title} ${i + 1}`} icon="x" onClick={() => setting(kind, items.filter((_, n) => n !== i))} />
          </div>
        ))}
        <Button label={`Add ${title}`} icon="plus" onClick={() => setting(kind, [...items, kind === 'repos' ? { id: 'repo-' + (items.length + 1), remote: '', forge: 'github', defaultBranch: 'main' } : { hostId: '', checkoutPath: '' }])} />
      </DetailSection>
    );
  };
  if (!loaded) return error ? <ErrorBanner text={error} onRetry={() => void load()} /> : <Empty text="Loading project…" />;
  if (id && prepared && breakdown) return <PreparedProjectResources projectId={id} summary={summary} category={breakdown} onClose={() => setBreakdown(null)} />;
  const ctxTotals = resources ? resourceTotals(resources, { hideUnavailable: false }) : [];
  return (
    <article class="detail" data-ui="detail">
      <header class="detail-h">
        <h2>{id ? bundle.settings.name : 'Create project'}</h2><span class="grow" />
        <Button label={priorSave && !busy ? "Retry original save" : "Save project"} icon="save" busy={busy} onClick={save} />
        {id && <Button label={conflict ? "Discard edits and reload" : "Reload project"} icon="rotate-cw" onClick={() => { if (conflict) { pendingSave.current = null; setPriorSave(false); try { draft.current?.write(''); } catch (e) { if (e instanceof AccessChanged) return; /* cleared in authorized memory before optional persistence */ } baseline.current = JSON.stringify(latest.current); } void load(); }} />}
      </header>
      <StatusLine>{conflict || error || (priorSave && !busy ? 'Previous save has no verified readback. Retry original save keeps its request identity and original body; newer edits remain local.' : '')}</StatusLine>
      {sourceState === 'repository-changed' && <p>Source configuration changed. Saving keeps this edited project version.</p>}
      {id && prepared && <DetailSection title="Project resources"><PreparedProjectResources projectId={id} summary={summary} onOpen={setBreakdown} /></DetailSection>}
      <DetailSection title="Context">
        {!id && field('Project ID', bundle.settings.id, v => setting('id', v))}
        {field('Name', bundle.settings.name, v => setting('name', v))}
        {bundle.contextFiles.map((f, i) => (
          <div key={f.path}>
            <CodeEditor label={`Project context ${f.path}`} value={f.text} onChange={text => setBundle(b => ({ ...b, contextFiles: b.contextFiles.map((old, n) => i === n ? { ...old, text } : old) }))} />
          </div>
        ))}
        <KvRow icon="folder-open" label="Saved version" value={revision ? String(revision) : 'Not saved'} />
      </DetailSection>
      {bindings('repos', 'Repository', ['id', 'remote', 'defaultBranch', 'connection'])}
      {bindings('environments', 'Environment', ['hostId', 'checkoutPath', 'repo'])}
      <DetailSection title="Agent roles">
        {Object.entries(bundle.settings.roles ?? {}).map(([role, profile]) => field(role, String(profile), v => setting('roles', { ...bundle.settings.roles, [role]: v })))}
      </DetailSection>
      {id && (
        <DetailSection title="Readiness">
          <Button label="Check readiness" icon="circle-check" onClick={() => op('project_readiness', { projectId: id }).then(setReady, e => setError(e.message))} />
          {ready && (
            <>
              {(ready.hosts ?? []).length === 0 ? <Empty text="No environment is configured." /> : (ready.hosts ?? []).map((h: any) =>
                h.executionId ? <ReadinessResult key={h.executionId} hostId={h.hostId} executionId={h.executionId} /> : (
                  <KvRow icon="panels-top-left" label={h.hostId ?? 'Environment'} value="Unavailable" meta={h.problem ?? 'Readiness check unavailable'} />
                )
              )}
            </>
          )}
          <Button label="Open PM conversation" icon="messages-square" onClick={() => location.assign('/conversations?new=' + encodeURIComponent(id))} />
        </DetailSection>
      )}
      {access && (
        <DetailSection title="Initial access">
          <KvRow icon="folder-open" label="Initial member" value={access.member ?? 'None'} />
          <KvRow icon="folder-open" label="State" value={!access.member ? 'None' : access.revoked ? 'Revoked' : 'Active'} />
          {access.member && !access.revoked && id && (
            <Button label="Revoke initial access" icon="shield" onClick={() => op('project_initial_access_revoke', { projectId: id, baseAccessVersion: access.version }).then(() => load(), e => setError(e.message))} />
          )}
          <p>Separate explicit grants and full administrator access remain effective.</p>
        </DetailSection>
      )}
      {id && !prepared && ctxTotals.length > 0 && (
        <DetailSection title="Project resources">
          <Totals items={ctxTotals} />
        </DetailSection>
      )}
      {id && (
        <DetailSection title="Resource analytics">
          <ProjectAnalytics projectId={id} onOpenProject={pid => go('/projects/' + encodeURIComponent(pid))} />
        </DetailSection>
      )}
    </article>
  );
}

function ReadinessResult({ hostId, executionId }: { hostId: string; executionId: string }) {
  const result = useDoc<any>('project-readiness:' + executionId, () => op('execution_status', { executionId }), c => c.kind === 'event' || (c.kind === 'execution' && c.id === executionId));
  const latest = result.data?.readiness?.at(-1);
  return (
    <>
      <KvRow icon="panels-top-left" label={hostId} value={result.error ? 'Observation unavailable' : latest ? (latest.ok ? 'Ready' : 'Not ready') : result.data?.state ?? 'Check requested'} meta={result.error ?? (latest ? undefined : 'Awaiting readiness result')} />
      {(latest?.checks ?? []).map((check: any) => <KvRow icon="circle-check" label={check.check} value={check.status} meta={[check.target, check.reason].filter(Boolean).join(' · ')} />)}
    </>
  );
}

function ProjectAnalytics({ projectId, onOpenProject }: { projectId?: string; onOpenProject: (id: string) => void }) {
  const [, go] = useLocation();
  const initialSearch = typeof location !== 'undefined' ? new URLSearchParams(location.search) : null;
  const [win, setWin] = useState<WindowKey>(() => {
    const w = initialSearch?.get('window');
    return w === '24h' || w === '7d' || w === '30d' ? w : '7d';
  });
  const [grain, setGrain] = useState<GrainKey>(() => {
    const g = initialSearch?.get('grain');
    return g === 'hour' || g === 'day' || g === 'week' ? g : 'day';
  });
  const [metric, setMetric] = useState<ChartMetric>('tokens');
  const [dim, setDim] = useState<AnalyticsDim>(() => {
    const d = initialSearch?.get('dim') as AnalyticsDim | null;
    if (d && DIMS.some(x => x.id === d)) return d;
    return projectId ? 'model' : 'project';
  });
  const [bucket, setBucket] = useState<string | null>(() => initialSearch?.get('bucket') || null);
  const [filters, setFilters] = useState<AnalyticsFilter>(() => {
    const out: AnalyticsFilter = {};
    const m = initialSearch?.get('model'); if (m) out.model = [m];
    const h = initialSearch?.get('harness'); if (h) out.harness = [h];
    const p = initialSearch?.get('pipeline'); if (p) out.pipeline = [p];
    const pr = initialSearch?.get('profile_id'); if (pr) out.profile_id = [pr];
    const pv = initialSearch?.get('pipeline_revision'); if (pv) out.pipeline_revision = [pv];
    return out;
  });
  const [trend, setTrend] = useState<AnalyticsReply | null>(null);
  const [breakdown, setBreakdown] = useState<AnalyticsReply | null>(null);
  const [trendErr, setTrendErr] = useState<string | null>(null);
  const [breakErr, setBreakErr] = useState<string | null>(null);
  const [costCur, setCostCur] = useState<string>('USD');
  const [rev, setRev] = useState(0);

  const now = useMemo(() => Date.now(), [projectId, win, grain, rev]);
  const from = now - WINDOW_MS[win];
  const filterKey = JSON.stringify(filters);

  const dimsAvail = projectId ? DIMS.filter(d => d.id !== 'project') : DIMS;
  const activeDim = dimsAvail.some(d => d.id === dim) ? dim : dimsAvail[0].id;

  useEffect(() => {
    let live = true;
    setTrendErr(null);
    op('analytics_query', {
      window: { from, to: now },
      dimensions: [grain],
      measures: ANALYTICS_MEASURES,
      filters,
      ...(projectId ? { projectId } : {}),
    }).then(
      r => { if (live) setTrend(r); },
      e => { if (live) { setTrend(null); setTrendErr(String(e?.message ?? e)); } },
    );
    return () => { live = false; };
  }, [projectId, win, grain, filterKey, now, rev]);

  useEffect(() => {
    let live = true;
    setBreakErr(null);
    const range = bucket ? parseBucketMs(bucket, grain) : null;
    const bw = range ? { from: range[0], to: Math.max(range[0] + 1, Math.min(range[1], now)) } : { from, to: now };
    op('analytics_query', {
      window: bw,
      dimensions: [activeDim],
      measures: ANALYTICS_MEASURES,
      filters,
      ...(projectId ? { projectId } : {}),
    }).then(
      r => { if (live) setBreakdown(r); },
      e => { if (live) { setBreakdown(null); setBreakErr(String(e?.message ?? e)); } },
    );
    return () => { live = false; };
  }, [projectId, win, grain, activeDim, bucket, filterKey, now, rev]);

  const changeWin = (w: WindowKey) => {
    setWin(w);
    setBucket(null);
    if (w === '24h' && grain === 'week') setGrain('hour');
    if (w !== '24h' && grain === 'hour') setGrain('day');
  };

  const allTrendRows = trend?.rows ?? [];
  const unkBucketRuns = allTrendRows.find(r => r.dimensions[grain] === 'unknown')?.measures.executions?.value ?? 0;
  const trendRows = allTrendRows
    .filter(r => parseBucketMs(r.dimensions[grain] ?? '', grain) != null)
    .sort((a, b) => (a.dimensions[grain] ?? '').localeCompare(b.dimensions[grain] ?? ''));
  const totalM = trend?.totals.measures;
  const execM = numMeasured(totalM?.executions);
  const wallM = numMeasured(totalM?.wall_ms, 0.001);
  const cpuM = numMeasured(totalM?.cpu_ms, 0.001);
  const peakM = numMeasured(totalM?.peak_memory_bytes);
  const totalTokens = totalM ? sumTokens(totalM) : null;
  const callsM = numMeasured(totalM?.calls);
  const hasTotalCost = !!trend?.totals.cost.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription');
  const totalItems: TotalItem[] = trend ? [
    ...(execM.basis !== 'unavailable' ? [{ label: 'Executions', m: execM, kind: 'count' as const, note: totalM?.failed?.value ? `${totalM.failed.value} failed` : undefined }] : []),
    ...(wallM.basis !== 'unavailable' ? [{ label: 'Wall', m: wallM, kind: 'duration' as const }] : []),
    ...(cpuM.basis !== 'unavailable' ? [{ label: 'CPU', m: cpuM, kind: 'duration' as const }] : []),
    ...(peakM.basis !== 'unavailable' ? [{ label: 'Peak memory', m: peakM, kind: 'bytes' as const }] : []),
    ...(totalTokens && totalTokens.basis !== 'unavailable' ? [{ label: 'Tokens', m: totalTokens, kind: 'count' as const }] : []),
    ...(totalM?.calls?.value && callsM.basis !== 'unavailable' ? [{ label: 'Tools', m: callsM, kind: 'count' as const }] : []),
    ...(hasTotalCost ? [{ label: 'Cost', cost: trend.totals.cost, showUnavailable: false }] : []),
  ] : [];

  const buckets: ChartBucket[] = trendRows.map(r => {
    const k = r.dimensions[grain] ?? '';
    const tokM = sumTokens(r.measures);
    const tokStr = tokM.value != null && tokM.basis !== 'unavailable' ? ` · ${tokM.partial ? '≥ ' : ''}${tokM.value.toLocaleString('en')} tokens` : '';
    const vRuns = r.measures.executions?.value ?? 0;
    const vCost = r.cost.filter(c => c.value != null).map(c => `${c.basis === 'subscription' ? '≈' : ''}${c.value!.toFixed(2)} ${c.currency ?? 'USD'}${c.basis === 'subscription' ? ' sub' : ''}`).join(', ');
    return {
      key: k,
      label: shortBucketLabel(k, grain),
      tip: `${k}: ${vRuns} runs${tokStr}${vCost ? ` · ${vCost}` : ''}`,
    };
  });

  const costCurrencies = [...new Set(trendRows.flatMap(r => r.cost.filter(c => c.value != null).map(c => c.currency ?? 'USD')))]
    .sort((a, b) => (a === 'USD' ? -1 : b === 'USD' ? 1 : a.localeCompare(b)));
  const activeCur = costCurrencies.includes(costCur) ? costCur : (costCurrencies[0] ?? 'USD');
  const sumCostKind = (r: AnalyticsReply['rows'][number], sub: boolean) =>
    r.cost.reduce((n, c) => ((c.currency ?? 'USD') === activeCur && (c.basis === 'subscription') === sub && c.value != null ? n + c.value : n), 0);
  const series: ChartSeries[] = (() => {
    if (metric === 'cost') {
      const meteredPts = trendRows.flatMap(r => {
        const range = parseBucketMs(r.dimensions[grain] ?? '', grain);
        return range ? [{ at: range[0], end: Math.min(range[1], now), v: sumCostKind(r, false) }] : [];
      });
      const subPts = trendRows.flatMap(r => {
        const range = parseBucketMs(r.dimensions[grain] ?? '', grain);
        return range ? [{ at: range[0], end: Math.min(range[1], now), v: sumCostKind(r, true) }] : [];
      });
      const hasMetered = meteredPts.some(q => q.v > 0);
      const hasSub = subPts.some(q => q.v > 0);
      const out: ChartSeries[] = [];
      if (hasMetered || !hasSub) out.push({ label: `${activeCur} metered`, points: meteredPts, tone: 'd1', step: true });
      if (hasSub) out.push({ label: `${activeCur} subscription equiv.`, points: subPts, tone: hasMetered ? 'd2' : 'd1', pattern: true, step: true });
      return out;
    }
    const pts = trendRows.flatMap(r => {
      const range = parseBucketMs(r.dimensions[grain] ?? '', grain);
      const v = metric === 'executions' ? (r.measures.executions?.value ?? 0) : (sumTokens(r.measures).value ?? 0);
      return range ? [{ at: range[0], end: Math.min(range[1], now), v }] : [];
    });
    return [{ label: metric === 'executions' ? 'executions' : 'tokens', points: pts, tone: 'd1', step: true }];
  })();
  const chartMax = Math.max(1, ...series.flatMap(s => s.points.map(q => q.v)));
  const chartFormat = (v: number) => metric === 'cost'
    ? (activeCur === 'USD' ? `$${v.toFixed(2)}` : `${v.toFixed(2)} ${activeCur}`)
    : metric === 'executions' ? String(Math.round(v)) : (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : String(Math.round(v)));

  const filterChips = Object.entries(filters).flatMap(([k, vals]) => Array.isArray(vals) ? vals.map(v => ({ k: k as keyof AnalyticsFilter, v: String(v) })) : []);
  const toggleFilter = (d: AnalyticsDim, val: string) => {
    if (!val || val === '(none)') return;
    if (d === 'project') { onOpenProject(val); return; }
    const key = d as keyof AnalyticsFilter;
    const cur = ((filters[key] as unknown[] | undefined) ?? []).map(String);
    setFilters({ ...filters, [key]: cur.includes(val) ? cur.filter(x => x !== val) : [...cur, val] });
  };

  const qTerms = [
    filters.harness?.[0] ? `agent:${filters.harness[0]}` : '',
    filters.host?.[0] ? `host:${filters.host[0]}` : '',
  ].filter(Boolean).join(' ') || undefined;
  const tasksHref = formatView({ view: 'tasks', project: projectId ? [projectId] : undefined, q: qTerms });
  const convHref = formatView({ view: 'conversations', project: projectId ? [projectId] : undefined });

  return (
    <div class="stack">
      <div class="res-bar">
        <Choice label="Window" value={win} onChange={w => changeWin(w as WindowKey)} options={[{ id: '24h', label: '24h' }, { id: '7d', label: '7d' }, { id: '30d', label: '30d' }]} />
        <Choice label="Grain" value={grain} onChange={g => { setGrain(g as GrainKey); setBucket(null); }} options={[{ id: 'hour', label: 'Hour' }, { id: 'day', label: 'Day' }, { id: 'week', label: 'Week' }]} />
        <Choice label="Series" value={metric} onChange={m => setMetric(m as ChartMetric)} options={[{ id: 'tokens', label: 'Tokens' }, { id: 'executions', label: 'Executions' }, { id: 'cost', label: 'Cost' }]} />
        {metric === 'cost' && costCurrencies.length > 1 && (
          <Choice label="Currency" value={activeCur} onChange={setCostCur} options={costCurrencies.map(c => ({ id: c, label: c }))} />
        )}
        <span class="grow" />
        {unkBucketRuns > 0 && (
          <span class="chip" data-tip="Executions missing start time are excluded from time buckets">
            <Icon name="circle-alert" label="Unbucketed" /><Num m={{ value: unkBucketRuns, basis: 'measured' }} kind="count" /> unbucketed
          </span>
        )}
        {(trend?.unknown ?? 0) > 0 && (
          <span class="chip" data-tip="Executions missing usage or price telemetry in this window">
            <Icon name="circle-alert" label="Unknown telemetry" /><Num m={{ value: trend!.unknown, basis: 'measured' }} kind="count" /> unknown
          </span>
        )}
        {(trend?.hidden ?? 0) > 0 && (
          <span class="chip" data-tip="Executions excluded by project visibility permissions">
            <Icon name="circle-alert" label="Hidden executions" /><Num m={{ value: trend!.hidden, basis: 'measured' }} kind="count" /> hidden
          </span>
        )}
      </div>

      {filterChips.length > 0 && (
        <div class="res-bar">
          {filterChips.map(f => (
            <span key={`${f.k}:${f.v}`} class="chip">
              {f.k}: {f.v}
              <button type="button" aria-label={`Remove ${f.k} filter ${f.v}`} onClick={() => setFilters({ ...filters, [f.k]: ((filters[f.k] as unknown[]) ?? []).filter(x => String(x) !== f.v) })}><Icon name="x" /></button>
            </span>
          ))}
        </div>
      )}

      {trendErr && <ErrorBanner text={`Analytics failed: ${trendErr}`} onRetry={() => setRev(r => r + 1)} />}
      {!trend && !trendErr && <Empty text="Loading analytics…" />}
      {totalItems.length > 0 && <Totals items={totalItems} />}

      {trend && (
        <AreaChart
          label={`${projectId ?? 'All projects'} ${metric} by ${grain}`}
          series={series}
          from={from}
          to={now}
          now={now}
          max={chartMax}
          value={chartFormat}
          buckets={buckets}
          selectedBucket={bucket}
          onSelectBucket={k => setBucket(cur => cur === k ? null : k)}
        />
      )}

      <div class="res-bar">
        <Choice label="Breakdown" value={activeDim} onChange={d => setDim(d as AnalyticsDim)} options={dimsAvail} />
        {bucket && (
          <Button label={`Clear bucket (${bucket})`} icon="x" kind="quiet" onClick={() => setBucket(null)} />
        )}
        <span class="grow" />
        <a class="row-link" href={tasksHref} onClick={e => { e.preventDefault(); go(tasksHref); }}>Tasks</a>
        <a class="row-link" href={convHref} onClick={e => { e.preventDefault(); go(convHref); }}>Conversations</a>
        <a class="row-link" href="/agents" onClick={e => { e.preventDefault(); go('/agents'); }}>Agents</a>
        <a class="row-link" href="/pipelines" onClick={e => { e.preventDefault(); go('/pipelines'); }}>Pipelines</a>
        <a class="row-link" href="/runners" onClick={e => { e.preventDefault(); go('/runners'); }}>Runners</a>
        <a class="row-link" href="/hosts" onClick={e => { e.preventDefault(); go('/hosts'); }}>Hosts</a>
      </div>

      {breakErr && <ErrorBanner text={`Breakdown failed: ${breakErr}`} onRetry={() => setRev(r => r + 1)} />}
      {breakdown && (
        breakdown.rows.length === 0 ? <Empty text="No recorded analytics in this window." /> : (
          <div class="table-frame">
            <table class="limit-table">
              <thead>
                <tr>
                  <th scope="col">{dimsAvail.find(d => d.id === activeDim)?.label ?? activeDim}</th>
                  <th scope="col" class="num">Runs</th>
                  <th scope="col" class="num">Wall</th>
                  <th scope="col" class="num">CPU</th>
                  <th scope="col" class="num">Peak mem</th>
                  <th scope="col" class="num">Tokens</th>
                  <th scope="col" class="num">Tools</th>
                  <th scope="col" class="num">Cost</th>
                </tr>
              </thead>
              <tbody>
                {breakdown.rows.map(r => {
                  const rawVal = r.dimensions[activeDim];
                  const keyVal = rawVal || '(none)';
                  const tok = sumTokens(r.measures);
                  const curFilter = (filters as any)[activeDim] as unknown[] | undefined;
                  const active = activeDim !== 'project' && Array.isArray(curFilter) && curFilter.some(x => String(x) === rawVal);
                  return (
                    <tr key={keyVal} class={active ? 'on' : undefined}>
                      <th scope="row">
                        {rawVal ? (
                          <button type="button" class="seg" onClick={() => toggleFilter(activeDim, rawVal)} data-tip={activeDim === 'project' ? `Open project ${rawVal}` : `Filter by ${rawVal}`}>
                            <span class="mono">{rawVal}</span>
                          </button>
                        ) : <span class="muted">(none)</span>}
                      </th>
                      <td class="num"><Num m={numMeasured(r.measures.executions)} kind="count" />{r.measures.failed?.value ? <span class="chip t-danger">{r.measures.failed.value} fail</span> : null}</td>
                      <td class="num"><Num m={numMeasured(r.measures.wall_ms, 0.001)} kind="duration" /></td>
                      <td class="num">{r.measures.cpu_ms?.value != null ? <Num m={numMeasured(r.measures.cpu_ms, 0.001)} kind="duration" /> : <span class="muted">—</span>}</td>
                      <td class="num">{r.measures.peak_memory_bytes?.value != null ? <Num m={numMeasured(r.measures.peak_memory_bytes)} kind="bytes" /> : <span class="muted">—</span>}</td>
                      <td class="num"><Num m={tok} kind="count" /></td>
                      <td class="num">{r.measures.calls?.value ? <Num m={numMeasured(r.measures.calls)} kind="count" /> : <span class="muted">—</span>}</td>
                      <td class="num"><CostFigures cost={r.cost} showUnavailable empty="—" /></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )
      )}
    </div>
  );
}
