// Project configuration editor and readiness checks (docs/design-system/pages/projects.md).
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, onSettled, untrack } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { useLocation } from '../router.ts';
import {
  Button, CodeEditor, DetailSection, Disclosure, Empty, ErrorBanner, IdentityPicker, KvRow, Skeleton, StatusLine, TextField,
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
export const str = (v: unknown) => (typeof v === 'string' ? v : '');
const isRec = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const recs = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.filter(isRec) : []);

const fresh = (): Bundle => ({
  settings: { id: '', name: '', contextFiles: ['rules.md'], roles: { pm: 'pm' }, repos: [], environments: [] },
  contextFiles: [{ path: 'rules.md', text: '' }],
});

/** Configuration and creation editor for a project, including draft persistence, readiness checks, and resource breakdown. */
export function ProjectEditor(props: {
  id?: string;
  /** The project's code, for its tile in the colour picker (OR-72). */
  code?: string;
  /** The stored automatic colour the Auto swatch shows. */
  autoColor?: string;
  onClose?: () => void;
  onSaved: (id: string) => void;
  renderWork?: (projectId: string, detail: Detail | null) => JSX.Element;
  /** Inside a project's Setup facet: the project head above names it, so the editor shows no title of its own (OR-17). */
  embedded?: boolean;
  /** The name as edited, before Save: the project head shows it while the editor is open. */
  onName?: (name: string) => void;
}) {
  const [, go] = useLocation();
  const editorGeneration = accessGeneration();
  const [bundle, applyBundle] = createSignal<Bundle>(fresh(), { ownedWrite: true });
  createEffect(() => str(bundle().settings.name), name => { props.onName?.(name); });
  let baseline = JSON.stringify(untrack(bundle));
  let latest = untrack(bundle);
  type EditBase = { content: Bundle; revision: number; contentSha: string; repoSha: string | null };
  type PendingSave = { name: 'project_update'; body: Ops['project_update']['request'] } | { name: 'project_create'; body: Ops['project_create']['request'] };
  let draft: DraftPersistence | null = null;
  let editBase: EditBase | null = null;
  let pendingSave: PendingSave | null = null;
  const [priorSave, setPriorSave] = createSignal(false, { ownedWrite: true });
  let requestId: string = crypto.randomUUID();
  const alive = useAlive();
  const [error, setError] = createSignal('', { ownedWrite: true });
  const [conflict, setConflict] = createSignal('', { ownedWrite: true });
  const [prepared, setPrepared] = createSignal<boolean | null>(null, { ownedWrite: true });
  const [breakdown, setBreakdown] = createSignal<'tools' | 'costs' | null>(null);
  const summary = useProjectResources(() => props.id, () => prepared() === true && !breakdown());
  onSettled(() => {
    let active = true;
    preparedResourcesAvailable()
      .then(v => { if (active) setPrepared(v); })
      .catch(e => { if (active && !(e instanceof AccessChanged)) setPrepared(false); });
    return () => { active = false; };
  });
  const [revision, setRevision] = createSignal(0, { ownedWrite: true });
  const [busy, setBusy] = createSignal(false, { ownedWrite: true });
  let saving = false;
  const [ready, setReady] = createSignal<Readiness | null>(null, { ownedWrite: true });
  const [access, setAccess] = createSignal<ProjectInitialAccess | undefined>(undefined, { ownedWrite: true });
  const [repoSha, setRepoSha] = createSignal<string | null>(null, { ownedWrite: true });
  const [sourceState, setSourceState] = createSignal('', { ownedWrite: true });
  const [loaded, setLoaded] = createSignal(false, { ownedWrite: true });
  let reloadAfterSave = false;

  const remember = (value: Bundle) => {
    if (!draft || !editBase) return;
    try {
      draft.write(
        JSON.stringify(value) === baseline && !pendingSave
          ? ''
          : JSON.stringify({ bundle: value, base: editBase, requestId, pendingSave }),
      );
    } catch (e) {
      if (!(e instanceof AccessChanged)) setError('Draft storage unavailable; keep this page open.');
    }
  };
  const setBundle = (next: Bundle | ((old: Bundle) => Bundle)) => {
    const value = typeof next === 'function' ? next(latest) : next;
    latest = value;
    applyBundle(value);
    remember(value);
  };
  const validBundle = (value: unknown): value is Bundle =>
    isRec(value) && isRec(value.settings) && Array.isArray(value.contextFiles)
    && value.contextFiles.every(file => isRec(file) && typeof file.path === 'string' && typeof file.text === 'string');
  const restoreEdit = () => {
    try {
      const text = draft?.read();
      if (!text) return null;
      const saved = JSON.parse(text);
      return validBundle(saved.bundle) && validBundle(saved.base?.content) && Number.isSafeInteger(saved.base.revision) && typeof saved.base.contentSha === 'string' && typeof saved.requestId === 'string' && (!saved.pendingSave || (['project_create', 'project_update'].includes(saved.pendingSave.name) && typeof saved.pendingSave.body?.requestId === 'string' && validBundle(saved.pendingSave.body.content)))
        ? (saved as { bundle: Bundle; base: EditBase; requestId: string; pendingSave?: PendingSave | null })
        : null;
    } catch { return null; }
  };

  createEffect(
    () => props.id,
    id => {
      if (id || !isAccessCurrent(editorGeneration)) return;
      draft = createDraftPersistence(authorizedDraftScope({ kind: 'project-create', id: '' }));
      const saved = restoreEdit();
      editBase = saved?.base ?? { content: fresh(), revision: 0, contentSha: '', repoSha: null };
      baseline = JSON.stringify(editBase.content);
      requestId = saved?.requestId ?? crypto.randomUUID();
      pendingSave = saved?.pendingSave ?? null;
      setPriorSave(!!pendingSave);
      if (saved) setBundle(saved.bundle);
      setLoaded(true);
    },
  );

  // The project's definition: project_detail where prepared reads are advertised (503: not on this server), else
  // project_context; both reread when the server says a change touches them. A save fences them: what arrives while it
  // runs is applied after it, and the save's readback is the first answer at or past the saved revision.
  const detailDoc = useOp('project_detail', () => (props.id && prepared() === true ? { projectId: props.id } : null));
  const contextDoc = useOp('project_context', () => (props.id && prepared() === false ? { projectId: props.id } : null));
  const doc = () => (prepared() ? detailDoc : contextDoc);
  createEffect(() => detailDoc.status, status => { if (status === 503) setPrepared(false); });
  let currentRef: Detail | null = null;
  let readback: { revision: number; resolve: (d: Detail) => void; reject: (e: Error) => void } | null = null;

  const apply = (p: Detail) => {
    const id = untrack(() => props.id);
    if (!id || !isAccessCurrent(editorGeneration) || alive.aborted) return;
    try {
      if (JSON.stringify(latest) !== baseline) {
        if (p.revision !== editBase?.revision) setConflict('Project definition changed while you were editing. Review your edits before saving; reload cannot discard them.');
        return;
      }
      // The target is freshly authorized before authored input for this actor is inspected.
      draft = createDraftPersistence(authorizedDraftScope({ kind: 'project-editor', id }));
      const saved = restoreEdit();
      if (editBase?.contentSha !== p.contentSha || editBase?.repoSha !== (p.repoSha ?? null)) setReady(null);
      editBase = saved?.base ?? { content: p.content, revision: p.revision, contentSha: p.contentSha, repoSha: p.repoSha ?? null };
      baseline = JSON.stringify(editBase.content);
      requestId = saved?.requestId ?? crypto.randomUUID();
      pendingSave = saved?.pendingSave ?? null;
      setPriorSave(!!pendingSave);
      setError('');
      setBundle(saved?.bundle ?? p.content);
      setRevision(editBase.revision);
      setAccess(p.initialAccess);
      setRepoSha(editBase.repoSha);
      setSourceState(p.sourceState ?? '');
      setConflict(saved && saved.base.contentSha !== p.contentSha ? 'Project definition changed while you were editing. Review your edits before saving; reload cannot discard them.' : '');
      setLoaded(true);
    } catch (e) {
      if (!(e instanceof AccessChanged)) setError(e instanceof Error ? e.message : String(e));
    }
  };

  createEffect(
    () => doc().data,
    cur => {
      currentRef = cur;
      const w = readback;
      if (w) {
        if (cur && cur.revision >= w.revision) { readback = null; w.resolve(cur); }
        return;
      }
      if (!cur) return;
      if (saving) { reloadAfterSave = true; return; }
      apply(cur);
    },
  );
  createEffect(
    () => [doc().error, doc().status] as const,
    ([err, status]) => {
      if (!err || status === 503) return;
      const w = readback;
      if (w) { readback = null; w.reject(Error(err)); }
      else if (!saving) setError(err);
    },
  );

  /** The first answer at or past a saved revision (the saved version, or a newer one that the check then refuses). */
  const readDetail = (rev: number) => new Promise<Detail>((resolve, reject) => {
    const now = currentRef;
    if (now && now.revision >= rev) { resolve(now); return; }
    readback = { revision: rev, resolve, reject };
    untrack(doc).reload();
  });
  // The latest answer, never the one of the render a save started in (its finally runs this after the save).
  const load = () => {
    const now = currentRef;
    if (now && !saving) apply(now);
    untrack(doc).reload();
  };
  const setting = (key: string, value: unknown) => setBundle(b => ({ ...b, settings: { ...b.settings, [key]: value } }));
  // A cleared identity setting (Auto, Solid) leaves the key out: the project takes the automatic colour and bar.
  const unsetting = (key: string, value: string | null) => setBundle(b => {
    const { [key]: _, ...rest } = b.settings;
    return { ...b, settings: value == null ? rest : { ...rest, [key]: value } };
  });
  const field = (label: string, value: string, onChange: (s: string) => void) => (
    <TextField label={label} value={value} onChange={onChange} />
  );
  const save = async () => {
    if (saving) return;
    saving = true;
    const id = untrack(() => props.id);
    const curBundle = untrack(bundle), curRev = untrack(revision), curRepoSha = untrack(repoSha);
    // Ordinary reads cannot replace the optimistic base or frozen request during a save (the reads' effect checks saving).
    const generation = accessGeneration();
    const mutation: PendingSave = pendingSave ?? (id
      ? { name: 'project_update', body: { projectId: id, requestId, baseRevision: curRev, content: curBundle, repoSha: curRepoSha } }
      : { name: 'project_create', body: { id: str(curBundle.settings.id), requestId, content: curBundle } });
    const submitted = mutation.body.content;
    pendingSave = mutation; setPriorSave(true); remember(latest); setBusy(true);
    try {
      const unknown = () => setError('unknown, retrying');
      const p = mutation.name === 'project_update'
        ? await writeOnce(mutation.name, mutation.body, unknown, { signal: alive })
        : await writeOnce(mutation.name, mutation.body, unknown, { signal: alive });
      if (!isAccessCurrent(generation)) return;
      if (typeof p.projectId !== 'string' || typeof p.revision !== 'number' || !Number.isSafeInteger(p.revision) || typeof p.contentSha !== 'string') throw Error('Saved reply has no verifiable project version.');
      if (!id) { // created: the editor reopens on the new project and reads and checks it there
        pendingSave = null; setPriorSave(false); requestId = crypto.randomUUID();
        baseline = JSON.stringify(submitted); remember(latest); setError(''); props.onSaved(p.projectId); return;
      }
      const check = await readDetail(p.revision);
      if (!isAccessCurrent(generation) || alive.aborted) return;
      if (check.revision !== p.revision || check.contentSha !== p.contentSha) throw Error('Saved version changed; reload before editing.');
      setError(''); pendingSave = null; setPriorSave(false); requestId = crypto.randomUUID();
      baseline = JSON.stringify(check.content);
      if (editBase?.contentSha !== check.contentSha || editBase?.repoSha !== (check.repoSha ?? null)) setReady(null);
      editBase = { content: check.content, revision: check.revision, contentSha: check.contentSha, repoSha: check.repoSha ?? null };
      if (JSON.stringify(latest) === JSON.stringify(submitted)) setBundle(check.content); else remember(latest);
      setRevision(check.revision); setAccess(check.initialAccess);
      setRepoSha(check.repoSha ?? null); setSourceState(check.sourceState ?? '');
      setConflict(''); props.onSaved(p.projectId);
    } catch (e) {
      if (isAccessCurrent(generation) && !alive.aborted) {
        if (e instanceof OpError && [400, 404, 409].includes(e.status)) {
          pendingSave = null; setPriorSave(false); requestId = crypto.randomUUID(); remember(latest);
        }
        setError(e instanceof Error ? e.message : String(e));
      }
    } finally {
      saving = false;
      if (isAccessCurrent(generation) && !alive.aborted) {
        setBusy(false);
        if (reloadAfterSave) { reloadAfterSave = false; load(); }
      }
    }
  };

  const renderBindings = (kind: 'repos' | 'environments', title: string, keys: string[]) => {
    const items = () => recs(bundle().settings[kind]);
    const change = (i: number, k: string, v: string) => setting(kind, items().map((item, n) => (n === i ? { ...item, [k]: v } : item)));
    return (
      <DetailSection title={title}>
        <For each={items()}>
          {(item, i) => (
            <div>
              <For each={keys}>
                {k => field(`${title} ${i() + 1} ${{ id: 'ID', remote: 'URL', defaultBranch: 'Default branch', connection: 'Git connection', hostId: 'Host', checkoutPath: 'Checkout path', repo: 'Repository' }[k] ?? k}`, str(item[k]), v => change(i(), k, v))}
              </For>
              <Button label={`Remove ${title} ${i() + 1}`} icon="x" onClick={() => setting(kind, items().filter((_, n) => n !== i()))} />
            </div>
          )}
        </For>
        <Button
          label={`Add ${title}`}
          icon="plus"
          onClick={() => setting(kind, [...items(), kind === 'repos' ? { id: 'repo-' + (items().length + 1), remote: '', forge: 'github', defaultBranch: 'main' } : { hostId: '', checkoutPath: '' }])}
        />
      </DetailSection>
    );
  };

  const roleEntries = createMemo(() =>
    Object.entries(isRec(bundle().settings.roles) ? bundle().settings.roles as Record<string, unknown> : {}),
  );

  return (
    <Switch
      fallback={
        <article class="detail" data-ui="detail" tabindex={-1}>
          <header class="detail-h">
            <Show when={props.onClose}><Button label="Close" icon="x" iconOnly onClick={props.onClose!} /></Show>
            <Show when={!props.embedded}><h2 class="detail-title">{props.id ? str(bundle().settings.name) : 'Create project'}</h2></Show>
            <span class="grow" />
            <Button label={priorSave() && !busy() ? 'Retry original save' : 'Save project'} icon="save" busy={busy()} onClick={save} />
            <Show when={props.id}>
              <Button
                label={conflict() ? 'Discard edits and reload' : 'Reload project'}
                icon="rotate-cw"
                onClick={() => {
                  if (conflict()) {
                    pendingSave = null; setPriorSave(false);
                    try { draft?.write(''); } catch (e) { if (e instanceof AccessChanged) return; /* cleared in authorized memory before optional persistence */ }
                    baseline = JSON.stringify(latest);
                  }
                  load();
                }}
              />
            </Show>
          </header>
          <StatusLine>{conflict() || error() || (priorSave() && !busy() ? 'Previous save has no verified readback. Retry original save keeps its request identity and original body; newer edits remain local.' : '')}</StatusLine>
          <Show when={sourceState() === 'repository-changed'}>
            <p>Source configuration changed. Saving keeps this edited project version.</p>
          </Show>
          <Show when={props.id} keyed>
            {pid => (
              <DetailSection title="Readiness">
                <Button label="Check readiness" icon="circle-check" onClick={() => op('project_readiness', { projectId: pid }).then(setReady, e => setError(e.message))} />
                <Show when={ready()} keyed>
                  {r => (
                    <Show when={r.hosts.length > 0} fallback={<Empty text="No environment is configured." />}>
                      <For each={r.hosts}>
                        {h => (
                          <Show
                            when={h.executionId}
                            keyed
                            fallback={<KvRow icon="panels-top-left" label={h.hostId ?? 'Environment'} value="Unavailable" meta={h.problem ?? 'Readiness check unavailable'} />}
                          >
                            {exId => <ReadinessResult hostId={h.hostId} executionId={exId} />}
                          </Show>
                        )}
                      </For>
                    </Show>
                  )}
                </Show>
                <Button label="Open PM conversation" icon="messages-square" onClick={() => go('/conversations/new?in=' + encodeURIComponent(pid) + '&role=pm')} />
              </DetailSection>
            )}
          </Show>
          <Show when={props.id && props.renderWork}>{props.renderWork!(props.id!, doc().data)}</Show>
          <Show when={props.id && prepared()}>
            <DetailSection title="Project resources">
              <PreparedProjectResources projectId={props.id!} summary={summary} onOpen={setBreakdown} />
            </DetailSection>
          </Show>
          <DetailSection title="Context">
            <Show when={!props.id}>{field('Project ID', str(bundle().settings.id), v => setting('id', v))}</Show>
            {field('Name', str(bundle().settings.name), v => setting('name', v))}
            {field('Description', str(bundle().settings.description), v => setting('description', v))}
            {field('Domain', str(bundle().settings.domain), v => setting('domain', v))}
            <KvRow label="Colour" value={<IdentityPicker code={props.code ?? ''} auto={props.autoColor}
              color={str(bundle().settings.color)} pattern={str(bundle().settings.pattern)}
              onColor={c => unsetting('color', c)} onPattern={x => unsetting('pattern', x)} />} />
            <For each={bundle().contextFiles}>
              {(f, i) => (
                <Disclosure summary={f.path} summaryClass="muted">
                  <CodeEditor
                    label={`Project context ${f.path}`}
                    value={f.text}
                    onChange={text => setBundle(b => ({ ...b, contextFiles: b.contextFiles.map((old, n) => (i() === n ? { ...old, text } : old)) }))}
                  />
                </Disclosure>
              )}
            </For>
            <KvRow icon="folder-open" label="Saved version" value={revision() ? String(revision()) : 'Not saved'} />
          </DetailSection>
          {renderBindings('repos', 'Repository', ['id', 'remote', 'defaultBranch', 'connection'])}
          {renderBindings('environments', 'Environment', ['hostId', 'checkoutPath', 'repo'])}
          <DetailSection title="Agent roles">
            <For each={roleEntries()}>
              {([role, profile]) => field(role, String(profile), v => {
                const roles = bundle().settings.roles;
                setting('roles', { ...(isRec(roles) ? roles : {}), [role]: v });
              })}
            </For>
          </DetailSection>
          <Show when={access()} keyed>
            {acc => (
              <DetailSection title="Initial access">
                <KvRow icon="folder-open" label="Initial member" value={acc.member ?? 'None'} />
                <KvRow icon="folder-open" label="State" value={!acc.member ? 'None' : acc.revoked ? 'Revoked' : 'Active'} />
                <Show when={acc.member && !acc.revoked && props.id}>
                  <Button
                    label="Revoke initial access"
                    icon="shield"
                    onClick={() => op('project_initial_access_revoke', { projectId: props.id!, baseAccessVersion: acc.version }).then(load, e => setError(e.message))}
                  />
                </Show>
                <p>Separate explicit grants and full administrator access remain effective.</p>
              </DetailSection>
            )}
          </Show>
        </article>
      }
    >
      <Match when={!loaded()}>
        <article class="detail" data-ui="detail" tabindex={-1}>
          <header class="detail-h"><Show when={props.onClose}><Button label="Close" icon="x" iconOnly onClick={props.onClose!} /></Show></header>
          <Show when={error()} fallback={<Skeleton rows={6} />}>
            {err => <ErrorBanner text={err()} onRetry={load} />}
          </Show>
        </article>
      </Match>
      <Match when={props.id && prepared() && breakdown()}>
        <PreparedProjectResources projectId={props.id!} summary={summary} category={breakdown()!} onClose={() => setBreakdown(null)} />
      </Match>
    </Switch>
  );
}

function ReadinessResult(props: { hostId: string; executionId: string }) {
  const result = useOp('execution_status', () => ({ executionId: props.executionId }));
  const latest = createMemo(() => {
    const last = result.data?.readiness?.at(-1);
    return isRec(last) ? last : null;
  });
  const checks = createMemo(() => recs(latest()?.checks));
  return (
    <>
      <KvRow
        icon="panels-top-left"
        label={props.hostId}
        value={result.error ? 'Observation unavailable' : latest() ? (latest()!.ok === true ? 'Ready' : 'Not ready') : result.data?.state ?? 'Check requested'}
        meta={result.error ?? (latest() ? undefined : 'Awaiting readiness result')}
      />
      <For each={checks()}>
        {check => (
          <KvRow
            icon="circle-check"
            label={str(check.check)}
            value={str(check.status)}
            meta={[str(check.target), str(check.reason)].filter(Boolean).join(' · ')}
          />
        )}
      </For>
    </>
  );
}
