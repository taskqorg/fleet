// Project configuration editor and readiness checks (docs/design-system/pages/projects.md).
import type { ComponentChildren } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, CodeEditor, DetailSection, Disclosure, Empty, ErrorBanner, KvRow, Skeleton, StatusLine, TextField,
} from '../../../packages/ui/src/index.ts';
import {
  op, useAlive, writeOnce, preparedResourcesAvailable, createDraftPersistence, authorizedDraftScope,
  AccessChanged, OpError, type DraftPersistence, type Ops,
} from '../api.ts';
import { useOp } from '../store.ts';
import { accessGeneration, isAccessCurrent } from '../access.ts';
import { useProjectResources, PreparedProjectResources } from './resources.tsx';
import type { ProjectBundle, ProjectInitialAccess } from '../../../contracts/generated/ops.ts';

type Bundle = ProjectBundle;
export type ProjectDetailData = Ops['project_detail']['reply'] | Ops['project_context']['reply'];
type Detail = ProjectDetailData;
type Readiness = Ops['project_readiness']['reply'];
// Project settings, list rows and readiness records are open JSON in the contract: read their fields by type here.
export const str = (v: unknown) => typeof v === 'string' ? v : '';
const isRec = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const recs = (v: unknown): Record<string, unknown>[] => Array.isArray(v) ? v.filter(isRec) : [];

const fresh = (): Bundle => ({
  settings: { id: '', name: '', contextFiles: ['rules.md'], roles: { pm: 'pm' }, repos: [], environments: [] },
  contextFiles: [{ path: 'rules.md', text: '' }],
});

/** Configuration and creation editor for a project, including draft persistence, readiness checks, and resource breakdown. */
export function ProjectEditor({ id, onClose, onSaved, renderWork, renderAnalytics }: {
  id?: string;
  onClose?: () => void;
  onSaved: (id: string) => void;
  renderWork?: (projectId: string, detail: Detail | null) => ComponentChildren;
  renderAnalytics: (projectId: string) => ComponentChildren;
}) {
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
  const validBundle = (value: unknown): value is Bundle => isRec(value) && isRec(value.settings) && Array.isArray(value.contextFiles) && value.contextFiles.every(file => isRec(file) && typeof file.path === 'string' && typeof file.text === 'string');
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
  const [ready, setReady] = useState<Readiness | null>(null);
  const [access, setAccess] = useState<ProjectInitialAccess | undefined>(undefined);
  const [repoSha, setRepoSha] = useState<string | null>(null);
  const [sourceState, setSourceState] = useState('');
  const requestId = useRef<string>(crypto.randomUUID()), alive = useAlive();
  const [loaded, setLoaded] = useState(false);
  const reloadAfterSave = useRef(false);
  useEffect(() => {
    if (id || !isAccessCurrent(editorGeneration)) return;
    draft.current = createDraftPersistence(authorizedDraftScope({ kind: 'project-create', id: '' }));
    const saved = restoreEdit(); editBase.current = saved?.base ?? { content: fresh(), revision: 0, contentSha: '', repoSha: null };
    baseline.current = JSON.stringify(editBase.current.content); requestId.current = saved?.requestId ?? crypto.randomUUID(); pendingSave.current = saved?.pendingSave ?? null; setPriorSave(!!pendingSave.current);
    if (saved) setBundle(saved.bundle);
    setLoaded(true);
  }, [id]);
  // The project's definition: project_detail where prepared reads are advertised (503: not on this server), else
  // project_context; both reread when the server says a change touches them. A save fences them: what arrives while it
  // runs is applied after it, and the save's readback is the first answer at or past the saved revision.
  const detailDoc = useOp('project_detail', id && prepared === true ? { projectId: id } : null);
  const contextDoc = useOp('project_context', id && prepared === false ? { projectId: id } : null);
  const doc = prepared ? detailDoc : contextDoc;
  useEffect(() => { if (detailDoc.status === 503) setPrepared(false); }, [detailDoc.status]);
  const current: Detail | null = doc.data, currentRef = useRef(current); currentRef.current = current;
  const readback = useRef<{ revision: number; resolve: (d: Detail) => void; reject: (e: Error) => void } | null>(null);
  const apply = (p: Detail) => {
    if (!id || !isAccessCurrent(editorGeneration) || alive.aborted) return;
    try {
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
      setRepoSha(editBase.current.repoSha); setSourceState(p.sourceState ?? '');
      setConflict(saved && saved.base.contentSha !== p.contentSha ? 'Project definition changed while you were editing. Review your edits before saving; reload cannot discard them.' : ''); setLoaded(true);
    } catch (e) { if (!(e instanceof AccessChanged)) setError(e instanceof Error ? e.message : String(e)); }
  };
  useEffect(() => {
    const w = readback.current;
    if (w) { if (current && current.revision >= w.revision) { readback.current = null; w.resolve(current); } return; }
    if (!current) return;
    if (saving.current) { reloadAfterSave.current = true; return; }
    apply(current);
  }, [current]);
  useEffect(() => {
    if (!doc.error || doc.status === 503) return;
    const w = readback.current;
    if (w) { readback.current = null; w.reject(Error(doc.error)); } else if (!saving.current) setError(doc.error);
  }, [doc.error]);
  /** The first answer at or past a saved revision (the saved version, or a newer one that the check then refuses). */
  const readDetail = (revision: number) => new Promise<Detail>((resolve, reject) => {
    const now = currentRef.current;
    if (now && now.revision >= revision) { resolve(now); return; }
    readback.current = { revision, resolve, reject }; doc.reload();
  });
  // The latest answer, never the one of the render a save started in (its finally runs this after the save).
  const load = () => { const now = currentRef.current; if (now && !saving.current) apply(now); doc.reload(); };
  const setting = (key: string, value: unknown) => setBundle(b => ({ ...b, settings: { ...b.settings, [key]: value } }));
  const field = (label: string, value: string, save: (s: string) => void) => (
    <TextField label={label} value={value} onChange={save} />
  );
  const save = async () => {
    if (saving.current) return;
    saving.current = true;
    // Ordinary reads cannot replace the optimistic base or frozen request during a save (the reads' effect checks saving).
    const generation = accessGeneration();
    const mutation: PendingSave = pendingSave.current ?? (id
      ? { name: 'project_update', body: { projectId: id, requestId: requestId.current, baseRevision: revision, content: bundle, repoSha } }
      : { name: 'project_create', body: { id: str(bundle.settings.id), requestId: requestId.current, content: bundle } });
    const submitted = mutation.body.content;
    pendingSave.current = mutation; setPriorSave(true); remember(latest.current); setBusy(true);
    try {
      const unknown = () => setError('unknown, retrying');
      const p = mutation.name === 'project_update'
        ? await writeOnce(mutation.name, mutation.body, unknown, { signal: alive })
        : await writeOnce(mutation.name, mutation.body, unknown, { signal: alive });
      if (!isAccessCurrent(generation)) return;
      if (typeof p.projectId !== 'string' || typeof p.revision !== 'number' || !Number.isSafeInteger(p.revision) || typeof p.contentSha !== 'string') throw Error('Saved reply has no verifiable project version.');
      if (!id) { // created: the editor reopens on the new project and reads and checks it there
        pendingSave.current = null; setPriorSave(false); requestId.current = crypto.randomUUID();
        baseline.current = JSON.stringify(submitted); remember(latest.current); setError(''); onSaved(p.projectId); return;
      }
      const check = await readDetail(p.revision);
      if (!isAccessCurrent(generation) || alive.aborted) return;
      if (check.revision !== p.revision || check.contentSha !== p.contentSha) throw Error('Saved version changed; reload before editing.');
      setError(''); pendingSave.current = null; setPriorSave(false); requestId.current = crypto.randomUUID();
      baseline.current = JSON.stringify(check.content);
      if (editBase.current?.contentSha !== check.contentSha || editBase.current?.repoSha !== (check.repoSha ?? null)) setReady(null);
      editBase.current = { content: check.content, revision: check.revision, contentSha: check.contentSha, repoSha: check.repoSha ?? null };
      if (JSON.stringify(latest.current) === JSON.stringify(submitted)) setBundle(check.content); else remember(latest.current);
      setRevision(check.revision); setAccess(check.initialAccess);
      setRepoSha(check.repoSha ?? null); setSourceState(check.sourceState ?? '');
      setConflict(''); onSaved(p.projectId);
    } catch (e) { if (isAccessCurrent(generation) && !alive.aborted) {
      if (e instanceof OpError && [400, 404, 409].includes(e.status)) { pendingSave.current = null; setPriorSave(false); requestId.current = crypto.randomUUID(); remember(latest.current); }
      setError(e instanceof Error ? e.message : String(e));
    } } finally {
      saving.current = false;
      if (isAccessCurrent(generation) && !alive.aborted) {
        setBusy(false);
        if (reloadAfterSave.current) { reloadAfterSave.current = false; load(); }
      }
    }
  };
  const bindings = (kind: 'repos' | 'environments', title: string, keys: string[]) => {
    const items = recs(bundle.settings[kind]);
    const change = (i: number, k: string, v: string) => setting(kind, items.map((item, n) => n === i ? { ...item, [k]: v } : item));
    return (
      <DetailSection title={title}>
        {items.map((item, i) => (
          <div key={i}>
            {keys.map(k => field(`${title} ${i + 1} ${{ id: 'ID', remote: 'URL', defaultBranch: 'Default branch', connection: 'Git connection', hostId: 'Host', checkoutPath: 'Checkout path', repo: 'Repository' }[k] ?? k}`, str(item[k]), v => change(i, k, v)))}
            <Button label={`Remove ${title} ${i + 1}`} icon="x" onClick={() => setting(kind, items.filter((_, n) => n !== i))} />
          </div>
        ))}
        <Button label={`Add ${title}`} icon="plus" onClick={() => setting(kind, [...items, kind === 'repos' ? { id: 'repo-' + (items.length + 1), remote: '', forge: 'github', defaultBranch: 'main' } : { hostId: '', checkoutPath: '' }])} />
      </DetailSection>
    );
  };
  if (!loaded) return (
    <article class="detail" data-ui="detail" tabIndex={-1}>
      <header class="detail-h">{onClose && <Button label="Close" icon="x" iconOnly onClick={onClose} />}</header>
      {error ? <ErrorBanner text={error} onRetry={load} /> : <Skeleton rows={6} />}
    </article>
  );
  if (id && prepared && breakdown) return <PreparedProjectResources projectId={id} summary={summary} category={breakdown} onClose={() => setBreakdown(null)} />;
  return (
    <article class="detail" data-ui="detail" tabIndex={-1}>
      <header class="detail-h">
        {onClose && <Button label="Close" icon="x" iconOnly onClick={onClose} />}
        <h2 class="detail-title">{id ? str(bundle.settings.name) : 'Create project'}</h2><span class="grow" />
        <Button label={priorSave && !busy ? "Retry original save" : "Save project"} icon="save" busy={busy} onClick={save} />
        {id && <Button label={conflict ? "Discard edits and reload" : "Reload project"} icon="rotate-cw" onClick={() => { if (conflict) { pendingSave.current = null; setPriorSave(false); try { draft.current?.write(''); } catch (e) { if (e instanceof AccessChanged) return; /* cleared in authorized memory before optional persistence */ } baseline.current = JSON.stringify(latest.current); } load(); }} />}
      </header>
      <StatusLine>{conflict || error || (priorSave && !busy ? 'Previous save has no verified readback. Retry original save keeps its request identity and original body; newer edits remain local.' : '')}</StatusLine>
      {sourceState === 'repository-changed' && <p>Source configuration changed. Saving keeps this edited project version.</p>}
      {id && (
        <DetailSection title="Readiness">
          <Button label="Check readiness" icon="circle-check" onClick={() => op('project_readiness', { projectId: id }).then(setReady, e => setError(e.message))} />
          {ready && (
            <>
              {ready.hosts.length === 0 ? <Empty text="No environment is configured." /> : ready.hosts.map((h, i) =>
                h.executionId ? <ReadinessResult key={h.executionId} hostId={h.hostId} executionId={h.executionId} /> : (
                  <KvRow key={h.hostId ?? `host-${i}`} icon="panels-top-left" label={h.hostId ?? 'Environment'} value="Unavailable" meta={h.problem ?? 'Readiness check unavailable'} />
                )
              )}
            </>
          )}
          <Button label="Open PM conversation" icon="messages-square" onClick={() => go('/conversations/new?in=' + encodeURIComponent(id) + '&role=pm')} />
        </DetailSection>
      )}
      {id && renderWork?.(id, current)}
      {id && prepared && <DetailSection title="Project resources"><PreparedProjectResources projectId={id} summary={summary} onOpen={setBreakdown} /></DetailSection>}
      {id && (
        <DetailSection title="Resource analytics">
          {renderAnalytics(id)}
        </DetailSection>
      )}
      <DetailSection title="Context">
        {!id && field('Project ID', str(bundle.settings.id), v => setting('id', v))}
        {field('Name', str(bundle.settings.name), v => setting('name', v))}
        {bundle.contextFiles.map((f, i) => (
          <Disclosure key={f.path} summary={f.path} summaryClass="muted">
            <CodeEditor label={`Project context ${f.path}`} value={f.text} onChange={text => setBundle(b => ({ ...b, contextFiles: b.contextFiles.map((old, n) => i === n ? { ...old, text } : old) }))} />
          </Disclosure>
        ))}
        <KvRow icon="folder-open" label="Saved version" value={revision ? String(revision) : 'Not saved'} />
      </DetailSection>
      {bindings('repos', 'Repository', ['id', 'remote', 'defaultBranch', 'connection'])}
      {bindings('environments', 'Environment', ['hostId', 'checkoutPath', 'repo'])}
      <DetailSection title="Agent roles">
        {Object.entries(isRec(bundle.settings.roles) ? bundle.settings.roles : {}).map(([role, profile]) => field(role, String(profile), v => setting('roles', { ...(isRec(bundle.settings.roles) ? bundle.settings.roles : {}), [role]: v })))}
      </DetailSection>
      {access && (
        <DetailSection title="Initial access">
          <KvRow icon="folder-open" label="Initial member" value={access.member ?? 'None'} />
          <KvRow icon="folder-open" label="State" value={!access.member ? 'None' : access.revoked ? 'Revoked' : 'Active'} />
          {access.member && !access.revoked && id && (
            <Button label="Revoke initial access" icon="shield" onClick={() => op('project_initial_access_revoke', { projectId: id, baseAccessVersion: access.version }).then(load, e => setError(e.message))} />
          )}
          <p>Separate explicit grants and full administrator access remain effective.</p>
        </DetailSection>
      )}
    </article>
  );
}

function ReadinessResult({ hostId, executionId }: { hostId: string; executionId: string }) {
  const result = useOp('execution_status', { executionId });
  const last = result.data?.readiness?.at(-1), latest = isRec(last) ? last : null;
  return (
    <>
      <KvRow icon="panels-top-left" label={hostId} value={result.error ? 'Observation unavailable' : latest ? (latest.ok === true ? 'Ready' : 'Not ready') : result.data?.state ?? 'Check requested'} meta={result.error ?? (latest ? undefined : 'Awaiting readiness result')} />
      {recs(latest?.checks).map((check, i) => <KvRow key={`${str(check.check)}:${str(check.target)}:${i}`} icon="circle-check" label={str(check.check)} value={str(check.status)} meta={[str(check.target), str(check.reason)].filter(Boolean).join(' · ')} />)}
    </>
  );
}
