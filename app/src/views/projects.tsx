// Projects (docs/design-system/pages/projects.md, Target): the project catalogue in the control column; the work area is
// the cross-project summary, or an opened project with its facets Overview · Tasks · Pipelines · Results · Delivery ·
// Setup · History (Overview, Pipelines, a pipeline page and History: project-overview.tsx; Setup: below, board Approved P-A3).
// Tasks, Results and Delivery are the same list views as before, laid out in one column inside the facet (routes.ts maps
// `/projects/FLT/tasks/T-FLT-42` to the Tasks view filtered to the project).
import { For, Match, Show, Switch, createEffect, createMemo, createSignal } from 'solid-js';
import { useLocation, useSearch } from '../router.ts';
import {
  Button, DetailHead, DetailSection, DiffRow, Empty, ErrorBanner, FilterBar, InheritedRows, KvRow, Preview, ListDetail, ListFrame,
  RowFacts, Fact, ShellActions, Skeleton, StateBadge, StateDot, TabBar, VirtualList,
  catalogueRow, matchSearch, statesOf, type IconName,
  EntityCode,
} from '../../../packages/ui/src/index.ts';
import { useOp, useView } from '../store.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { prefs, prefsRead, scopeIds } from '../prefs.ts';
import { projectIdOf, projectSegment, toTarget, type ListFacet } from '../routes.ts';
import type { Frame } from '../list-page.tsx';
import { isCode } from '../codes.ts';
import { idOf } from './delivery-model.tsx';
import { ProjectEditor, str, type ProjectDetailData } from './projects-editor.tsx';
import { Tasks } from './tasks.tsx';
import { Results } from './results.tsx';
import { Delivery } from './delivery.tsx';
import { ProjectAccess } from './access-grants.tsx';
import { AskCard, PipelinePage, ProjectHistory, ProjectOverview, ProjectPipelines, projectFacetPath, useProjectView } from './project-overview.tsx';

type Facet = 'overview' | ListFacet | 'pipelines' | 'setup' | 'history';
type CrossFacet = 'summary' | ListFacet;
type Screen = 'projects' | ListFacet;

const FACETS: { id: Facet; label: string }[] = [
  { id: 'overview', label: 'Overview' }, { id: 'tasks', label: 'Tasks' }, { id: 'pipelines', label: 'Pipelines' },
  { id: 'results', label: 'Results' }, { id: 'delivery', label: 'Delivery' }, { id: 'setup', label: 'Setup' },
  { id: 'history', label: 'History' },
];
/** Facets served by the projects view itself (`?facet=`); an earlier `?facet=sessions` opens Overview, and the earlier
 * `rules` and `access` open Setup, which replaced them. */
const OWN_FACETS: readonly Facet[] = ['pipelines', 'setup', 'history'];
const MOVED: Record<string, Facet> = { rules: 'setup', access: 'setup' };
const CROSS: { id: CrossFacet; label: string }[] = [
  { id: 'summary', label: 'Summary' }, { id: 'tasks', label: 'Tasks' }, { id: 'delivery', label: 'Delivery' }, { id: 'results', label: 'Results' },
];
const WAITING_HREF = formatView({ view: 'conversations', state: statesOf('session', ['waiting']) });

function isRec(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v);
}

/** Readiness of a project from its list row: the repository state, then whether an environment is configured. */
function readiness(p: Record<string, unknown>): { state: 'ready' | 'missing' | 'changed' | 'draft' | 'unchecked'; reason?: string } {
  const sourceState = str(p.sourceState);
  const settings = isRec(p.settings) ? p.settings : null;
  const envs = Array.isArray(settings?.environments) ? settings.environments : [];
  if (sourceState === 'repository-deleted') return { state: 'missing', reason: 'Repository deleted' };
  if (sourceState === 'repository-changed') return { state: 'changed', reason: 'Repository changed' };
  if (sourceState === 'ahead') return { state: 'draft', reason: 'Local changes ahead' };
  return envs.length > 0 ? { state: 'ready' } : { state: 'unchecked', reason: 'No environment configured' };
}

function ReadinessBadge(props: { p: Record<string, unknown> }) {
  const r = createMemo(() => readiness(props.p));
  return (
    <Switch>
      <Match when={r().state === 'missing' || r().state === 'changed'}><StateBadge family="source" state={r().state as 'missing' | 'changed'} reason={r().reason} compact /></Match>
      <Match when={true}><StateBadge family="definition" state={r().state as 'ready' | 'draft' | 'unchecked'} reason={r().reason} compact /></Match>
    </Switch>
  );
}

/** The facet path of a project (the router writes the project's code in the address bar). */
const facetPath = (projectId: string, facet: Facet): string => projectFacetPath(projectId, facet);

/**
 * The Projects section. `screen` is the internal screen the router opened: `projects` (the catalogue, a project on
 * one of its own facets) or a list facet; `id` is that screen's item (a project, or a task, result or pull request).
 */
export function Projects(props: { screen: Screen; id?: string }) {
  const [, go] = useLocation();
  const search = useSearch();
  const params = createMemo(() => new URLSearchParams(search()));
  const [creating, setCreating] = createSignal(false);
  const list = useOp('project_list', {});
  const all = createMemo(() => (list.data?.projects ?? []).map(p => ({ id: str(p.id), name: str(p.name) || str(p.id), code: str(p.code), raw: p })));
  // The open project: the item of the projects screen, or the one project a list facet is filtered to.
  const project = createMemo<string | undefined>(() => {
    if (props.screen === 'projects') return props.id ? projectIdOf(props.id) : undefined;
    const ps = (params().get('project') ?? '').split(',').filter(Boolean);
    return ps.length === 1 && ps[0] !== '-' ? ps[0] : undefined;
  });
  const facet = createMemo<Facet>(() => {
    if (props.screen !== 'projects') return props.screen;
    const raw = params().get('facet') ?? '';
    const f = (MOVED[raw] ?? raw) as Facet;
    return OWN_FACETS.includes(f) ? f : 'overview';
  });
  const pipeline = createMemo(() => (facet() === 'pipelines' ? params().get('pipeline') ?? undefined : undefined));
  const item = createMemo(() => (props.screen !== 'projects' ? props.id : undefined));
  const open = createMemo(() => all().find(p => p.id === project()));
  // An opened task or result lives in its own project's facet under its code (UR-69): an earlier link by id, or one
  // outside its project, is replaced by that address once its card is read.
  const kind = createMemo(() => (props.screen === 'tasks' ? 'task' as const : props.screen === 'results' ? 'result' as const : null));
  const card = useOp('entity_card', () => { const it = item(), k = kind(); return it && k ? (isCode(it) ? { code: it } : { kind: k, id: it }) : null; });
  createEffect(() => [card.data, item(), project()] as const, ([c, it, pid]) => {
    if (!c || !it || !(c.id === it || c.uuid === it || c.code?.toUpperCase() === it.toUpperCase())) return;
    if ((c.code && c.code !== it) || (c.projectId && c.projectId !== pid)) {
      const q = new URLSearchParams(search());
      if (c.projectId) q.set('project', c.projectId);
      go(`/${props.screen}/${encodeURIComponent(c.code ?? it)}${q.size ? '?' + q : ''}`, { replace: true });
    }
  });
  const openProject = (id: string) => { setCreating(false); go('/projects/' + encodeURIComponent(id)); };
  return (
    <ListDetail
      list={<Catalogue rows={all()} pending={!list.data && !list.error} error={list.error} reload={list.reload} selected={creating() ? undefined : project()} onOpen={openProject} onCreate={() => setCreating(true)} creating={creating()} />}
      detail={
        creating() ? (
          <ProjectEditor id={undefined} onClose={() => setCreating(false)} onSaved={p => openProject(p)} />
        ) : project() ? (
          <Show when={project()} keyed>
            {pid => <ProjectWork id={pid} p={open()} facet={facet()} item={item()} pipeline={pipeline()} tab={params().get('tab') ?? undefined} onClose={() => go('/projects')} />}
          </Show>
        ) : props.screen !== 'projects' ? (
          <CrossWork facet={props.screen} item={item()} />
        ) : undefined
      }
      idle={<CrossWork facet="summary" names={new Map(all().map(p => [p.id, p.name]))} />}
      detailKey={creating() ? 'create' : (project() ?? (props.screen !== 'projects' ? props.screen : undefined))}
    />
  );
}

/**
 * The Projects screen's one read (projects_view): the catalogue's facts per project and the summary over the focus. The
 * catalogue and the summary ask with the same request, so they share it.
 */
const useProjectsView = () => useView('projects_view', () => (prefsRead.value ? scopeIds() : null), { quiet: 5000 });

/** The control column: one row per project with readiness, name and code, and its live facts (OR-22, OR-29). */
function Catalogue(props: {
  rows: { id: string; name: string; code: string; raw: Record<string, unknown> }[]; pending: boolean; error: string | null; reload: () => void;
  selected?: string; creating: boolean; onOpen: (id: string) => void; onCreate: () => void;
}) {
  const [query, setQuery] = createSignal('');
  // Ignored projects are hidden everywhere for this person (OR-34); a project the URL names still opens.
  const shown = createMemo(() => props.rows.filter(p => !prefs.value.ignored.includes(p.id) && matchSearch(query(), `${p.name} ${p.id} ${p.code}`, { project: p.id })));
  const view = useProjectsView();
  const counts = createMemo(() => new Map((view.data?.catalogue ?? []).filter(c => c.projectId).map(c => [c.projectId!, c])));
  const working = createMemo(() => new Map([...counts()].map(([p, c]) => [p, c.working])));
  const inFlight = createMemo(() => new Map([...counts()].map(([p, c]) => [p, c.inFlight])));
  const asks = createMemo(() => new Map([...counts()].map(([p, c]) => [p, c.questions])));
  return (
    <>
      <ShellActions>
        <Button label="New project" icon="plus" iconOnly kind="primary" pressed={props.creating} onClick={props.onCreate} />
      </ShellActions>
      <Show when={props.error}>{err => <ErrorBanner text={`Projects did not load: ${err()}`} onRetry={props.reload} />}</Show>
      <section class="panel" data-ui="project-catalogue">
        <ListFrame pending={props.pending} bar={<FilterBar query={query()} onQuery={setQuery} placeholder="Find project" shown={shown().length} total={props.rows.length} />}>
          <Switch
            fallback={
              <VirtualList
                fill
                items={shown()}
                getKey={p => p.id}
                rowHeight={catalogueRow()}
                follow="top"
                label="Projects"
                columns="1.25rem minmax(0,1fr) auto"
                rowClass={p => (p.id === props.selected ? 'sel' : '')}
                render={p => (
                  <>
                    <span class="lead"><ReadinessBadge p={p.raw} /></span>
                    <span class="tt">
                      <a class="title row-link" href={'/projects/' + encodeURIComponent(projectSegment(p.id))} onClick={e => { e.preventDefault(); props.onOpen(p.id); }}>{p.name}</a>
                      <EntityCode code={p.code || p.id} id={p.id} kind="project" form="code" />
                    </span>
                    <span class="rz">
                      <RowFacts>
                        <Show when={asks().get(p.id)}>{n => <Fact tip="Open questions"><span class="t-attention">{n()} {n() === 1 ? 'question' : 'questions'}</span></Fact>}</Show>
                        <Show when={working().get(p.id)}>{n => <Fact tip="Sessions working"><span class="t-running">{n()} working</span></Fact>}</Show>
                        <Show when={inFlight().get(p.id)}>{n => <Fact text={`${n()} in flight`} tip="Tasks not finished" />}</Show>
                        <Show when={view.data && !asks().get(p.id) && !working().get(p.id) && !inFlight().get(p.id)}><span class="muted">nothing in flight</span></Show>
                      </RowFacts>
                    </span>
                  </>
                )}
              />
            }
          >
            <Match when={props.pending}><Skeleton rows={6} /></Match>
            <Match when={shown().length === 0}><Empty text={query() ? 'No projects match.' : 'No projects yet.'} /></Match>
          </Switch>
        </ListFrame>
      </section>
    </>
  );
}

/** A list facet in one column: the opened item replaces the list; with nothing open the view's summary (Delivery's
 * stages and pace, the Tasks and Results totals) sits under the list. */
const column: Frame = p => <>{p.detail ?? <div class="pw-split">{p.list}<Show when={p.idle}>{idle => <div class="pw-idle">{idle()}</div>}</Show></div>}</>;

/** One list facet's view, scoped to a project or across the projects in focus. */
function ListFacetView(props: { facet: ListFacet; item?: string; project?: string }) {
  return (
    <Switch>
      <Match when={props.facet === 'tasks'}><Tasks id={props.item} project={props.project} frame={column} /></Match>
      <Match when={props.facet === 'results'}><Results id={props.item} project={props.project} frame={column} /></Match>
      <Match when={props.facet === 'delivery'}><Delivery id={props.item} project={props.project} frame={column} /></Match>
    </Switch>
  );
}

/** The work area with no project open: Summary, Tasks, Delivery and Results over every project in the focus. */
function CrossWork(props: { facet: CrossFacet; item?: string; names?: Map<string, string> }) {
  const [, go] = useLocation();
  return (
    <section class="pwork" data-ui="project-work" aria-label="All projects">
      <TabBar cls="detail-tabs pw-tabs" label="Across projects" tabs={CROSS} value={props.facet} onChange={f => go(f === 'summary' ? '/projects' : '/' + f)} />
      <div class="pw-facet" role="tabpanel" aria-label={CROSS.find(c => c.id === props.facet)?.label}>
        <Show when={props.facet !== 'summary'} fallback={<Summary names={props.names ?? new Map()} />}>
          <ListFacetView facet={props.facet as ListFacet} item={props.item} />
        </Show>
      </div>
    </section>
  );
}

/** The cross-project glance (UR-52): what waits for the person, then each project's work in one line, then delivery in flight. */
function Summary(props: { names: Map<string, string> }) {
  const [, go] = useLocation();
  const view = useProjectsView();
  const name = (id: string | null) => (id ? props.names.get(id) ?? id : 'No project');
  // Every project with sessions working or tasks in flight, in name order.
  const lines = createMemo(() => (view.data?.lines ?? []).filter(l => l.projectId).map(l => ({ id: l.projectId!, tasks: l.inFlight, working: l.working }))
    .sort((a, b) => name(a.id).localeCompare(name(b.id))));
  const flight = createMemo(() => view.data?.delivery ?? []);
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label="Projects summary">
      <DetailSection title="Waiting for you" count={view.data?.waitingTotal || undefined}
        href={WAITING_HREF} onOpen={() => go(WAITING_HREF)}>
        <Switch fallback={<Preview n={3} items={(view.data?.waiting ?? []).map(q => <AskCard q={q} label={name(q.projectId)} />)} />}>
          <Match when={view.error && !view.data}><ErrorBanner text={`Projects did not load: ${view.error}`} onRetry={view.reload} /></Match>
          <Match when={!view.data}><Skeleton rows={2} /></Match>
          <Match when={view.data!.waitingTotal === 0}><p class="muted">Nothing waits for you.</p></Match>
        </Switch>
      </DetailSection>
      <DetailSection title="In flight">
        <Switch fallback={
          <For each={lines()}>
            {l => (
              <KvRow
                icon="folder-open"
                label={name(l.id)}
                meta={<WorkLine working={l.working} tasks={l.tasks} />}
                value={<Button label="Open" icon="arrow-right" iconOnly kind="quiet" onClick={() => go('/projects/' + encodeURIComponent(l.id))} />}
              />
            )}
          </For>
        }>
          <Match when={!view.data}><Skeleton rows={3} /></Match>
          <Match when={lines().length === 0}><p class="muted">No work in the focused projects.</p></Match>
        </Switch>
      </DetailSection>
      <DetailSection title={`Delivery in flight${flight().length ? ' · ' + flight().length : ''}`}>
        <Switch fallback={
          <For each={flight()}>
            {it => (
              <KvRow
                icon="git-pull-request"
                label={it.title || `#${it.number}`}
                meta={<span class="deps"><span class="mono">#{it.number}</span><span>{name(it.projectId ?? null)}</span></span>}
                value={<span class="deps"><StateBadge family="delivery" state={it.stage} compact /><Button label="Open" icon="arrow-right" iconOnly kind="quiet" onClick={() => go('/delivery/' + encodeURIComponent(idOf(it)) + (it.projectId ? '?project=' + encodeURIComponent(it.projectId) : ''))} /></span>}
              />
            )}
          </For>
        }>
          <Match when={!view.data}><Skeleton rows={2} /></Match>
          <Match when={flight().length === 0}><p class="muted">No pull requests in flight.</p></Match>
        </Switch>
      </DetailSection>
    </article>
  );
}

/** A project's work in one line: sessions working and tasks in flight; zeros are left out (OR-29). */
function WorkLine(props: { working: number; tasks: number }) {
  return (
    <span class="deps">
      <Show when={props.working > 0}><span class="t-running">{props.working} {props.working === 1 ? 'session' : 'sessions'} working</span></Show>
      <Show when={props.tasks > 0}><span>{props.tasks} {props.tasks === 1 ? 'task' : 'tasks'} in flight</span></Show>
    </span>
  );
}

/** An opened project: its head, the facet tab bar and the facet. A tab never rebuilds the control column (OR-47). */
function ProjectWork(props: {
  id: string; p?: { name: string; code: string; raw: Record<string, unknown> }; facet: Facet; item?: string; pipeline?: string; tab?: string; onClose: () => void;
}) {
  const [, go] = useLocation();
  // The head's counts come from the catalogue's read; Talk to PM finds the PM conversation in the Overview's read.
  const catalogue = useProjectsView();
  const counts = createMemo(() => catalogue.data?.catalogue.find(c => c.projectId === props.id));
  const overview = useProjectView(() => props.id);
  const asks = () => counts()?.questions ?? 0;
  const working = () => counts()?.working ?? 0;
  const ready = createMemo(() => (props.p ? readiness(props.p.raw) : null));
  const pmProfile = createMemo(() => {
    const settings = isRec(props.p?.raw.settings) ? props.p!.raw.settings : {};
    const roles = isRec(settings.roles) ? settings.roles : {};
    return str(roles.pm) || 'pm';
  });
  // The name as edited in Setup, before Save (the editor's draft), heads the project while the editor is open.
  const [draftName, setDraftName] = createSignal('', { ownedWrite: true });
  const title = () => (props.facet === 'setup' && draftName() ? draftName() : props.p?.name ?? props.id);
  // After the name, muted: `description · domain` (board P-A1); nothing when both are empty.
  const aside = () => [str(props.p?.raw.description).trim(), str(props.p?.raw.domain).trim()].filter(Boolean).join(' · ');
  // Talk to PM opens the project's PM conversation (OR-51); with none yet, a new conversation on the project's pm role.
  const pmSession = createMemo(() => overview.data?.pm ?? (overview.data?.conversations ?? []).find(s => s.profileId === pmProfile()));
  const talk = () => {
    const pm = pmSession();
    go(pm ? '/conversations/' + encodeURIComponent(pm.code ?? pm.id) : '/conversations/new?in=' + encodeURIComponent(props.id) + '&role=pm');
  };
  return (
    <section class="pwork" data-ui="project-work" aria-label={props.p?.name ?? props.id}>
      <DetailHead
        title={title()}
        aside={aside() || undefined}
        onClose={props.onClose}
        actions={<Button label="Talk to PM" icon="messages-square" onClick={talk} />}
        state={
          <>
            <Show when={asks() > 0}><StateDot state="waiting" /></Show>
            <Show when={asks() > 0}><span>{asks()} {asks() === 1 ? 'question' : 'questions'}</span></Show>
            <Show when={working() > 0}><span>{working()} {working() === 1 ? 'session' : 'sessions'} working</span></Show>
            <Show when={ready()?.reason}>{r => <span class="t-attention" data-tip="Fix it in Setup">{r()}</span>}</Show>
          </>
        }
      />
      <TabBar cls="detail-tabs pw-tabs" label="Project facets" tabs={FACETS} value={props.facet} onChange={f => go(facetPath(props.id, f))} />
      <div class="pw-facet" role="tabpanel" aria-label={FACETS.find(f => f.id === props.facet)?.label}>
        <Switch>
          <Match when={props.facet === 'overview'}>
            <ProjectOverview projectId={props.id} />
          </Match>
          <Match when={props.facet === 'pipelines' && props.pipeline}>
            {pid => <PipelinePage projectId={props.id} projectName={props.p?.name ?? props.id} pipelineId={pid()} tab={props.tab} />}
          </Match>
          <Match when={props.facet === 'pipelines'}>
            <ProjectPipelines projectId={props.id} />
          </Match>
          <Match when={props.facet === 'history'}>
            <ProjectHistory projectId={props.id} />
          </Match>
          <Match when={props.facet === 'setup'}>
            <ProjectSetup projectId={props.id} name={props.p?.name ?? props.id} raw={props.p?.raw} onName={setDraftName} />
          </Match>
          <Match when={props.facet === 'tasks' || props.facet === 'results' || props.facet === 'delivery'}>
            <ListFacetView facet={props.facet as ListFacet} item={props.item} project={props.id} />
          </Match>
        </Switch>
      </div>
    </section>
  );
}

/* ---------- Setup (board Approved P-A3): the project's settings read as plain values; a pencil opens the editor ---------- */

const recs = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.filter(isRec) : []);

/** A repository's host and `owner/repo` from its remote: `git@host:owner/repo.git`, `ssh://git@host:22/owner/repo.git` or an https URL. */
function remoteParts(remote: string): { host: string; path: string } {
  const r = remote.trim();
  const scp = /^[^@/\s]+@([^:/\s]+):(.+)$/.exec(r);
  let host = '', path = r;
  if (scp) { host = scp[1]; path = scp[2]; } else {
    try { const u = new URL(r); host = u.hostname; path = u.pathname; } catch { /* a bare path stays as it is */ }
  }
  return { host, path: path.replace(/^\/+/, '').replace(/\/+$/, '').replace(/\.git$/, '') };
}

const FORGE: Record<string, string> = { github: 'GitHub', gitlab: 'GitLab' };
const MERGE: Record<string, string> = { squash: 'squash', merge: 'merge commit', rebase: 'rebase' };

/** How a change lands, in words: "merge request, squash, merged automatically", or "pushed to the default branch". */
function changesLine(integration: Record<string, unknown>, forges: string[]): string {
  if (str(integration.mode) === 'direct') return 'pushed to the default branch';
  const request = forges.length > 0 && forges.every(f => f === 'gitlab') ? 'merge request' : 'pull request';
  const method = MERGE[str(integration.mergeMethod) || 'squash'] ?? str(integration.mergeMethod);
  return [request, method, integration.autoMerge === false ? 'merged by hand' : 'merged automatically'].join(', ');
}

/** A role's name in words: `pm` is PM. */
const roleName = (r: string) => (r.length <= 3 ? r.toUpperCase() : r[0].toUpperCase() + r.slice(1));

/** Settings that Setup reads as its own sections (Code, Where agents work, Who works) or that the Pipelines facet shows. */
const SHOWN = new Set(['repos', 'environments', 'roles', 'integration.mode', 'integration.mergeMethod', 'integration.autoMerge', 'capability', 'recommendedMethod']);

/**
 * Setup: About (the context text), Code, Where agents work, Who works, Access, Open questions, then what differs from the
 * shared defaults with "Inherited defaults (N)" folded (OR-45). Each section's pencil opens the project editor in place of
 * the reading; its Save is the one write, and Close (or a verified save) returns to the reading.
 */
function ProjectSetup(props: { projectId: string; name: string; raw?: Record<string, unknown>; onName: (name: string) => void }) {
  const [editing, setEditing] = createSignal(false);
  const doc = useOp('project_context', () => ({ projectId: props.projectId }));
  const view = useProjectView(() => props.projectId);
  const settings = createMemo(() => (isRec(doc.data?.settings) ? doc.data.settings : {}));
  const ready = createMemo(() => (props.raw ? readiness(props.raw) : null));
  const about = createMemo(() => {
    const files = Array.isArray(doc.data?.context) ? doc.data.context : [];
    const file = files.find(f => isRec(f) && f.path === 'rules.md') ?? files[0];
    return str(isRec(file) ? file.text : '').split(/\n\s*\n/).map(p => p.trim()).filter(Boolean);
  });
  const repos = createMemo(() => recs(settings().repos));
  const envs = createMemo(() => recs(settings().environments));
  const roles = createMemo(() => Object.entries(isRec(settings().roles) ? settings().roles as Record<string, unknown> : {}).filter(([, v]) => str(v)));
  const integration = createMemo(() => (isRec(settings().integration) ? settings().integration as Record<string, unknown> : {}));
  const dataClass = () => str(settings().data) || undefined;
  const close = () => { props.onName(''); setEditing(false); };
  const edit = () => <Button label="Edit" icon="pencil" iconOnly kind="quiet" onClick={() => setEditing(true)} />;
  return (
    <Show when={!editing()} fallback={
      <ProjectEditor id={props.projectId} embedded onName={props.onName} onClose={close} onSaved={() => { doc.reload(); close(); }} />
    }>
      <article class="detail" data-ui="project-setup" tabindex={-1} aria-label="Setup">
        <Show when={ready()?.reason}>{r => <p class="t-attention pw-tone pw-setup-alert">{r()}</p>}</Show>
        <Switch>
          <Match when={doc.error && !doc.data}><ErrorBanner text={`Setup did not load: ${doc.error}`} onRetry={doc.reload} /></Match>
          <Match when={!doc.data}><Skeleton rows={6} /></Match>
          <Match when={doc.data}>
            <DetailSection title="About" action={edit()}>
              <Show when={str(doc.data?.description).trim() || str(doc.data?.domain).trim()}>
                <div class="pw-list">
                  <Show when={str(doc.data?.description).trim()}>{d => <div class="pw-line"><span class="pw-k">Description</span><span class="pw-line-m">{d()}</span></div>}</Show>
                  <Show when={str(doc.data?.domain).trim()}>{d => <div class="pw-line"><span class="pw-k">Domain</span><span class="pw-line-m">{d()}</span></div>}</Show>
                </div>
              </Show>
              <div class="pw-about">
                <For each={about()} fallback={<p class="muted">Nothing written about {props.name} yet.</p>}>{p => <p>{p}</p>}</For>
              </div>
            </DetailSection>
            <DetailSection title="Code" action={edit()}>
              <div class="pw-list">
                <For each={repos()} fallback={<div class="pw-line"><span class="muted">No repository.</span></div>}>
                  {r => {
                    const parts = remoteParts(str(r.remote));
                    const where = [FORGE[str(r.forge)] ?? str(r.forge), str(r.host) || parts.host].filter(Boolean).join(' · ');
                    return (
                      <div class="pw-line">
                        <span class="pw-k">Repository</span>
                        <span class="pw-line-m"><span class="mono">{parts.path || str(r.id)}</span><Show when={where}><span class="muted">{where}</span></Show></span>
                        <Show when={str(r.defaultBranch)}>{b => <span class="mono muted" data-tip="Default branch">{b()}</span>}</Show>
                      </div>
                    );
                  }}
                </For>
                <div class="pw-line">
                  <span class="pw-k">Changes</span>
                  <span class="pw-line-m">{changesLine(integration(), repos().map(r => str(r.forge) || 'github'))}</span>
                </div>
              </div>
            </DetailSection>
            <DetailSection title="Where agents work" action={edit()}>
              <Show when={envs().length > 0} fallback={<p class="muted">No environment yet.</p>}>
                <div class="pw-list">
                  <For each={envs()}>
                    {e => (
                      <div class="pw-line">
                        <span class="pw-k">{str(e.hostId) || 'Any host'}</span>
                        <span class="pw-line-m"><span class="mono">{str(e.checkoutPath)}</span><Show when={repos().length > 1 && str(e.repo)}>{r => <span class="muted">{r()}</span>}</Show></span>
                      </div>
                    )}
                  </For>
                </div>
              </Show>
            </DetailSection>
            <DetailSection title="Who works" action={edit()}>
              <Show when={roles().length > 0} fallback={<p class="muted">Shared roles: PM and workers. Nothing set for {props.name}.</p>}>
                <div class="pw-list">
                  <For each={roles()}>
                    {([role, profile]) => (
                      <div class="pw-line"><span class="pw-k">{roleName(role)}</span><span class="pw-line-m"><span class="mono">{str(profile)}</span></span></div>
                    )}
                  </For>
                </div>
              </Show>
            </DetailSection>
            <DetailSection title="Access">
              <ProjectAccess projectId={props.projectId} data={dataClass()} />
            </DetailSection>
            <Show when={(view.data?.waiting.length ?? 0) > 0}>
              <DetailSection title="Open questions">
                <For each={view.data!.waiting}>{q => <AskCard q={q} />}</For>
              </DetailSection>
            </Show>
            <ProjectSettingsDiff reply={doc.data ?? null} name={props.name} />
          </Match>
        </Switch>
      </article>
    </Show>
  );
}

/* ---------- Setup's last block: settings as differences from the shared defaults (OR-45) ---------- */

type DiffField = {
  key: string;
  icon: IconName;
  label: string;
  baseText: string;
  currentText: string;
  isCode?: boolean;
  diff: boolean;
};

function stableVal(v: unknown): string {
  if (v === undefined) return 'null';
  if (Array.isArray(v)) return JSON.stringify(v.map(x => (isRec(x) ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => a.localeCompare(b))) : x)));
  if (isRec(v)) return JSON.stringify(Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))));
  return JSON.stringify(v);
}

function fmtVal(v: unknown, emptyLabel = 'none'): string {
  if (v === null || v === undefined || v === '') return emptyLabel;
  if (typeof v === 'boolean' || typeof v === 'number') return String(v);
  if (typeof v === 'string') return v;
  if (Array.isArray(v)) {
    if (v.length === 0) return emptyLabel;
    if (v.every(x => typeof x === 'string')) return (v as string[]).join(', ');
    return `${v.length} configured`;
  }
  if (isRec(v)) {
    const entries = Object.entries(v);
    if (entries.length === 0) return emptyLabel;
    return entries.map(([k, val]) => `${k}=${typeof val === 'string' ? val : JSON.stringify(val)}`).join(', ');
  }
  return String(v);
}

/** The settings Setup does not read as a section: those that differ as diff rows, the rest folded; cost lives in System › Spend. */
function ProjectSettingsDiff(props: { reply: ProjectDetailData | null; name: string }) {
  const [, go] = useLocation();
  const computed = createMemo(() => {
    const reply = props.reply;
    if (!reply) return { diffs: [] as DiffField[], inherited: [] as DiffField[], unknownKeys: [] as string[] };
    const resolved = isRec(reply.settings) ? reply.settings : {};
    const authored = isRec(reply.content?.settings) ? reply.content.settings : {};
    const get = (k: string, sub?: string): unknown => {
      const top = k in resolved ? resolved[k] : authored[k];
      if (!sub) return top;
      return isRec(top) ? top[sub] : undefined;
    };
    const rows: DiffField[] = [];
    const check = (key: string, icon: IconName, label: string, def: unknown, curRaw: unknown, opts: { empty?: string; isCode?: boolean } = {}) => {
      if (SHOWN.has(key)) return;
      const cur = curRaw === undefined ? def : curRaw;
      const isCode = opts.isCode ?? (typeof def === 'string' || typeof def === 'boolean' || typeof def === 'number');
      rows.push({
        key,
        icon,
        label,
        baseText: fmtVal(def, opts.empty ?? 'none'),
        currentText: fmtVal(cur, opts.empty ?? 'none'),
        isCode,
        diff: stableVal(cur) !== stableVal(def),
      });
    };

    // Defaults match go/coordinator/projects.go settingsDefaults.
    check('worktree.envMode', 'git-pull-request', 'Worktree mode', 'worktree', get('worktree', 'envMode'));
    check('worktree.branchPrefix', 'git-pull-request', 'Branch prefix', 'fleet', get('worktree', 'branchPrefix'));
    check('worktree.baseRef', 'git-pull-request', 'Base ref', null, get('worktree', 'baseRef'), { isCode: true });
    check('worktree.startFromOrigin', 'git-pull-request', 'Start from origin', true, get('worktree', 'startFromOrigin'));
    check('worktree.linkPaths', 'link', 'Link paths', [], get('worktree', 'linkPaths'));
    check('worktree.sharedDirectories', 'folder-open', 'Shared directories', [], get('worktree', 'sharedDirectories'));
    check('scripts.setup', 'terminal', 'Setup script', null, get('scripts', 'setup'), { isCode: true });
    check('scripts.teardown', 'terminal', 'Teardown script', null, get('scripts', 'teardown'), { isCode: true });
    check('setupStart', 'play', 'Setup start', 'wait', get('setupStart'));
    check('integration.mode', 'git-merge', 'Integration mode', 'pr', get('integration', 'mode'));
    check('integration.mergeMethod', 'git-merge', 'Merge method', 'squash', get('integration', 'mergeMethod'));
    check('integration.autoMerge', 'git-merge', 'Auto-merge', true, get('integration', 'autoMerge'));
    check('integration.syncCheckoutAfterMerge', 'rotate-cw', 'Sync checkout after merge', true, get('integration', 'syncCheckoutAfterMerge'));
    check('cleanup.keepWhen', 'archive', 'Keep worktree when', ['uncommitted', 'unpushed', 'teardown-failed'], get('cleanup', 'keepWhen'));
    check('identity', 'bot', 'Git identity', null, get('identity'));
    check('subscriptions', 'bell', 'Subscriptions', [], get('subscriptions'));
    check('execution.tools', 'settings', 'Execution tools', [], get('execution', 'tools'));
    check('execution.forge', 'git-pull-request', 'Execution forge', false, get('execution', 'forge'));
    check('execution.profiles', 'bot', 'Execution profiles', [], get('execution', 'profiles'));
    check('errors.bugsink', 'circle-alert', 'Bugsink projects', [], get('errors', 'bugsink'));
    check('presets', 'messages-square', 'Conversation presets', [], get('presets'), { empty: 'system defaults' });
    check('contextSources', 'file-text', 'Context sources', [], get('contextSources'));
    check('artifact_locations', 'save', 'Artifact locations', null, get('artifact_locations'));
    check('artifact_rules', 'archive', 'Artifact rules', [], get('artifact_rules'));
    check('roles', 'bot', 'Agent roles', {}, get('roles'));
    check('repos', 'git-pull-request', 'Repositories', [], get('repos'));
    check('environments', 'panels-top-left', 'Environments', [], get('environments'));
    check('capability', 'rows-3', 'Capability', null, authored.capability ?? null);
    check('recommendedMethod', 'circle-check', 'Recommended method', null, authored.recommendedMethod ?? null, { empty: 'shared default' });

    return {
      diffs: rows.filter(r => r.diff),
      inherited: rows.filter(r => !r.diff),
      unknownKeys: Array.isArray(reply.unknownKeys) ? reply.unknownKeys : [],
    };
  });

  return (
    <Show when={props.reply}>
      <Show when={computed().diffs.length > 0 || computed().unknownKeys.length > 0}>
        <DetailSection title="Differs from defaults" count={computed().diffs.length || undefined}>
          <For each={computed().diffs}>{f => <DiffRow field={f.label} value={f.currentText} base={f.baseText} />}</For>
          <Show when={computed().unknownKeys.length > 0}>
            <KvRow icon="triangle-alert" label="Unknown keys" meta={computed().unknownKeys.join(', ')} />
          </Show>
        </DetailSection>
      </Show>
      <div class="pw-defaults">
        <Show when={computed().inherited.length > 0}>
          <InheritedRows count={computed().inherited.length}>
            <For each={computed().inherited}>{f => <DiffRow field={f.label} value={f.currentText} base={f.baseText} />}</For>
          </InheritedRows>
        </Show>
        <a href={toTarget('/spend')} onClick={e => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); go('/spend'); } }}>Cost of {props.name} is in System › Spend</a>
      </div>
    </Show>
  );
}
