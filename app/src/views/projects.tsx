// Projects (docs/design-system/pages/projects.md, Target): the project catalogue in the control column; the work area is
// the cross-project summary, or an opened project with its facets Sessions · Tasks · Delivery · Results · Rules & methods
// · Access.
// Tasks, Results and Delivery are the same list views as before, laid out in one column inside the facet (routes.ts maps
// `/projects/FLT/tasks/T-FLT-42` to the Tasks view filtered to the project).
import { For, Match, Show, Switch, createEffect, createMemo, createSignal } from 'solid-js';
import { useLocation, useSearch } from '../router.ts';
import {
  Avatar, Button, DetailHead, DetailSection, DiffRow, Empty, ErrorBanner, FilterBar, Icon, InheritedRows, KvRow, Preview, InfiniteList, type Pager, ListDetail, ListFrame,
  Num, OpenQuestion, RowFacts, Fact, SearchField, SessionCard, ShellActions, Skeleton, StateBadge, StateDot, TabBar, Ago, VirtualList,
  catalogueRow, matchSearch, msMeasured, life, statesOf,
  harnesses, type Harness, type IconName, type Life, type StateOf,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { useOp, useView } from '../store.ts';
import { useOpPages } from '../op-pages.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { prefs, prefsRead, scopeIds } from '../prefs.ts';
import { projectIdOf, projectSegment, type ListFacet } from '../routes.ts';
import type { Frame } from '../list-page.tsx';
import { isCode } from '../codes.ts';
import { idOf } from './delivery-model.tsx';
import { ProjectEditor, str, type ProjectDetailData } from './projects-editor.tsx';
import { Tasks } from './tasks.tsx';
import { Results } from './results.tsx';
import { Delivery } from './delivery.tsx';
import { ProjectAccess } from './access-grants.tsx';

type Facet = 'sessions' | ListFacet | 'rules' | 'access';
type CrossFacet = 'summary' | ListFacet;
type Screen = 'projects' | ListFacet;
type Session = Ops['sessions_list']['reply']['sessions'][number];
type Question = Ops['questions_list']['reply']['questions'][number];

const FACETS: { id: Facet; label: string }[] = [
  { id: 'sessions', label: 'Sessions' }, { id: 'tasks', label: 'Tasks' }, { id: 'delivery', label: 'Delivery' },
  { id: 'results', label: 'Results' }, { id: 'rules', label: 'Rules & methods' }, { id: 'access', label: 'Access' },
];
const CROSS: { id: CrossFacet; label: string }[] = [
  { id: 'summary', label: 'Summary' }, { id: 'tasks', label: 'Tasks' }, { id: 'delivery', label: 'Delivery' }, { id: 'results', label: 'Results' },
];
const ACTIVE_SESSION: string[] = statesOf('session', ['working', 'waiting']);
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

/** Counts per project of a list read grouped by project (sessions_list, task_page). */

/** The facet path of a project (the router writes the project's code in the address bar). */
const facetPath = (projectId: string, facet: Facet): string =>
  facet === 'sessions' ? '/projects/' + encodeURIComponent(projectId)
    : facet === 'rules' || facet === 'access' ? '/projects/' + encodeURIComponent(projectId) + '?facet=' + facet
      : `/${facet}?project=${encodeURIComponent(projectId)}`;

/**
 * The Projects section. `screen` is the internal screen the router opened: `projects` (the catalogue, a project on
 * Sessions or Rules & methods) or a list facet; `id` is that screen's item (a project, or a task, result or pull request).
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
    const f = params().get('facet');
    return f === 'rules' || f === 'access' ? f : 'sessions';
  });
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
            {pid => <ProjectWork id={pid} p={open()} facet={facet()} item={item()} onClose={() => go('/projects')} />}
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
const useProjectsView = () => useView('projects_view', () => (prefsRead.value ? scopeIds() : null), { quiet: 2000 });

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
                      <span class="id">{p.code || p.id}</span>
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

/** An open question as a card (OR-53, OpenQuestion), under the name of the project that asks it. */
function AskCard(props: { q: Question; label?: string }) {
  const answers = useAnswers();
  return (
    <div class="pw-ask">
      <Show when={props.label}><span class="muted type-sm">{props.label}{props.q.code ? ' · ' + props.q.code : ''}</span></Show>
      <OpenQuestion q={props.q} answers={answers} />
    </div>
  );
}

/** An opened project: its head, the facet tab bar and the facet. A tab never rebuilds the control column (OR-47). */
function ProjectWork(props: { id: string; p?: { name: string; code: string; raw: Record<string, unknown> }; facet: Facet; item?: string; onClose: () => void }) {
  const [, go] = useLocation();
  const questions = useOp('questions_list', () => ({ projectIds: [props.id], state: 'open', limit: 200 }));
  // The project's sessions, newest first, a page at a time as the facet scrolls (OR-65).
  const paged = useOpPages('sessions_list', () => ({ filter: { projectId: [props.id], archived: false }, sort: '-activity', limit: 60 }), {
    page: r => ({ rows: r.sessions, next: (r.nextAfter as unknown[] | null) ?? null }), after: after => ({ after }), key: s => s.id,
  });
  const sessions = paged.first;
  const asks = createMemo(() => questions.data?.questions ?? []);
  const working = createMemo(() => (sessions.data?.sessions ?? []).filter(s => ACTIVE_SESSION.includes(s.state)).length);
  const ready = createMemo(() => (props.p ? readiness(props.p.raw) : null));
  const pmProfile = createMemo(() => {
    const settings = isRec(props.p?.raw.settings) ? props.p!.raw.settings : {};
    const roles = isRec(settings.roles) ? settings.roles : {};
    return str(roles.pm) || 'pm';
  });
  // The project's data class (settings data: public, internal or personal), shown on its Access facet when set.
  const dataClass = createMemo(() => { const st = isRec(props.p?.raw.settings) ? props.p!.raw.settings : {}; return str(st.data) || undefined; });
  // The name as edited in Rules & methods, before Save (the editor's draft), heads the project while it is open.
  const [draftName, setDraftName] = createSignal('', { ownedWrite: true });
  const title = () => (props.facet === 'rules' && draftName() ? draftName() : props.p?.name ?? props.id);
  // Talk to PM opens the project's PM session (OR-51); with none yet, a new conversation on the project's pm role.
  const pmSession = createMemo(() => (sessions.data?.sessions ?? []).find(s => s.profileId === pmProfile()));
  const talk = () => { const pm = pmSession(); go(pm ? sessionPath(pm) : '/conversations/new?in=' + encodeURIComponent(props.id) + '&role=pm'); };
  return (
    <section class="pwork" data-ui="project-work" aria-label={props.p?.name ?? props.id}>
      <DetailHead
        crumb={`project · ${props.p?.code || props.id}`}
        title={title()}
        onClose={props.onClose}
        actions={<Button label="Talk to PM" icon="messages-square" onClick={talk} />}
        state={
          <>
            <Show when={asks().length > 0}><StateDot state="waiting" /></Show>
            <Show when={asks().length > 0}><span>{asks().length} {asks().length === 1 ? 'question' : 'questions'}</span></Show>
            <Show when={working() > 0}><span>{working()} {working() === 1 ? 'session' : 'sessions'} working</span></Show>
            <Show when={ready()?.reason}>{r => <span class="t-attention" data-tip="Fix it in Rules & methods">{r()}</span>}</Show>
          </>
        }
      />
      <TabBar cls="detail-tabs pw-tabs" label="Project facets" tabs={FACETS} value={props.facet} onChange={f => go(facetPath(props.id, f))} />
      <div class="pw-facet" role="tabpanel" aria-label={FACETS.find(f => f.id === props.facet)?.label}>
        <Switch>
          <Match when={props.facet === 'sessions'}>
            <SessionsFacet sessions={sessions.data ? paged.pages.rows : undefined} pages={paged.pages} error={sessions.error} reload={sessions.reload} questions={asks()} pm={pmProfile()} name={props.p?.name ?? props.id} onTalk={talk} />
          </Match>
          <Match when={props.facet === 'rules'}>
            <ProjectEditor id={props.id} embedded onName={setDraftName} onClose={undefined} onSaved={p => go('/projects/' + encodeURIComponent(p) + '?facet=rules')}
              renderWork={(pid, detail) => <><ProjectSettingsDiff reply={detail} /><ProjectMethods projectId={pid} /></>} />
          </Match>
          <Match when={props.facet === 'access'}>
            <ProjectAccess projectId={props.id} data={dataClass()} />
          </Match>
          <Match when={props.facet !== 'sessions' && props.facet !== 'rules' && props.facet !== 'access'}>
            <ListFacetView facet={props.facet as ListFacet} item={props.item} project={props.id} />
          </Match>
        </Switch>
      </div>
    </section>
  );
}

const lifeOfSession = (s: string): Life => life('session', s);
const sessionPath = (s: Session) => '/conversations/' + encodeURIComponent(s.code ?? s.id);

/** Sessions facet: the PM first, then the project's active sessions as cards, then the others (finished or asleep) as rows, newest first (OR-50, OR-51). */
function SessionsFacet(props: { sessions?: Session[]; pages: Pager; error: string | null; reload: () => void; questions: Question[]; pm: string; name: string; onTalk: () => void }) {
  const [, go] = useLocation();
  const pm = createMemo(() => (props.sessions ?? []).find(s => s.profileId === props.pm));
  const active = createMemo(() => (props.sessions ?? []).filter(s => s !== pm() && ACTIVE_SESSION.includes(s.state)));
  const finished = createMemo(() => (props.sessions ?? []).filter(s => s !== pm() && !ACTIVE_SESSION.includes(s.state)));
  const askOf = (s: Session) => props.questions.find(q => q.sessionId === s.id);
  const card = (s: Session, label?: string) => {
    const q = askOf(s);
    return (
      <SessionCard
        name={label ? `${label} · ${s.title ?? s.code ?? 'session'}` : s.title ?? s.code ?? 'Untitled session'}
        avatar={{ letter: (label ?? s.title ?? 'S').slice(0, 1).toUpperCase(), harness: s.harness && s.harness in harnesses ? s.harness as Harness : undefined }}
        state={q ? 'waiting' : lifeOfSession(s.state)}
        now={s.now ?? s.title ?? 'No title yet'}
        meta={[...(s.code ? [<span class="mono">{s.code}</span>] : []), ...(s.activityAt ? [<Ago t={Date.parse(s.activityAt)} />] : [])]}
        question={q ? <AskCard q={q} /> : undefined}
        onOpen={() => go(sessionPath(s))}
      />
    );
  };
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label="Sessions">
      <Switch fallback={
        <>
          <div class="pw-cards">
            <Show when={pm()} keyed fallback={
              <div class="pw-pm-empty"><Avatar letter="P" /><span>No PM session for {props.name} yet.</span><Button label="Talk to PM" icon="messages-square" onClick={props.onTalk} /></div>
            }>{s => card(s, 'PM')}</Show>
            <For each={active()}>{s => card(s)}</For>
          </div>
          <Show when={finished().length > 0}>
            <DetailSection title="Earlier">
              <InfiniteList pages={props.pages} label="Earlier sessions" endLabel="Older sessions">
                <For each={finished()}>{s => (
                  <a class="pw-row row-link" href={sessionPath(s)} onClick={e => { e.preventDefault(); go(sessionPath(s)); }}>
                    <span class="muted mono">{s.activityAt ? <Ago t={Date.parse(s.activityAt)} /> : ''}</span>
                    <StateDot family="session" state={s.state as StateOf<'session'>} />
                    <span class="pw-row-t">{s.title ?? s.code ?? 'Untitled session'}</span>
                  </a>
                )}</For>
              </InfiniteList>
            </DetailSection>
          </Show>
        </>
      }>
        <Match when={props.error && !props.sessions}><ErrorBanner text={`Sessions did not load: ${props.error}`} onRetry={props.reload} /></Match>
        <Match when={!props.sessions}><Skeleton rows={4} /></Match>
      </Switch>
    </article>
  );
}

/* ---------- Rules & methods: settings as differences from the shared defaults (OR-45), then methods ---------- */

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

function ProjectSettingsDiff(props: { reply: ProjectDetailData | null }) {
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
      <DetailSection title={`Differs from defaults${computed().diffs.length ? ' · ' + computed().diffs.length : ''}`}>
        <Show when={computed().diffs.length > 0} fallback={<p class="muted">Every setting is the shared default.</p>}>
          <For each={computed().diffs}>{f => <DiffRow field={f.label} value={f.currentText} base={f.baseText} />}</For>
        </Show>
        <Show when={computed().unknownKeys.length > 0}>
          <KvRow icon="triangle-alert" label="Unknown keys" meta={computed().unknownKeys.join(', ')} />
        </Show>
        <Show when={computed().inherited.length > 0}>
          <InheritedRows count={computed().inherited.length}>
            <For each={computed().inherited}>{f => <DiffRow field={f.label} value={f.currentText} base={f.baseText} />}</For>
          </InheritedRows>
        </Show>
      </DetailSection>
    </Show>
  );
}

/* ---------- Recommended methods with their Lab evidence ---------- */

function methodStatusTone(s: string | undefined): 'success' | 'attention' | 'neutral' {
  return s === 'recommended' ? 'success' : s === 'experimental' ? 'attention' : 'neutral';
}

function methodStatusIcon(s: string | undefined): IconName {
  return s === 'recommended' ? 'circle-check' : s === 'experimental' ? 'activity' : 'circle-slash';
}

function ProjectMethods(props: { projectId: string }) {
  const [, go] = useLocation();
  const [search, setSearch] = createSignal('');
  const [busy, setBusy] = createSignal<string | null>(null);
  const [actionError, setActionError] = createSignal<string | null>(null);
  const cat = useOp('catalogue', () => ({ projectId: props.projectId }));
  const c = () => cat.data;

  const recommend = async (methodId: string) => {
    setBusy(methodId);
    setActionError(null);
    try {
      await op('recommendation_set', { projectId: props.projectId, methodId });
      cat.reload();
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  const recommendedMethod = () => {
    const data = c();
    return data && typeof data.recommendedMethod === 'string' ? data.recommendedMethod : null;
  };
  const sharedDefault = () => {
    const data = c();
    return data && typeof data.sharedDefault === 'string' ? data.sharedDefault : null;
  };
  const source = () => {
    const data = c();
    return data && typeof data.recommendationSource === 'string' ? data.recommendationSource : 'shared';
  };
  const capability = () => {
    const data = c();
    return data && typeof data.capability === 'string' ? data.capability : null;
  };
  const filtered = createMemo(() => {
    const data = c();
    if (!data) return [];
    const q = search();
    return data.methods.filter(m =>
      matchSearch(q, `${m.name} ${m.id} ${m.capability ?? ''} ${m.description ?? ''} ${m.status ?? ''} ${m.pipeline}`),
    );
  });

  return (
    <Switch
      fallback={
        <>
          <div class="res-bar">
            <SearchField label="Find method" value={search()} onInput={setSearch} onSearch={() => {}} />
            <span class="grow" />
          </div>
          <Show when={actionError()}>{err => <ErrorBanner text={err()} />}</Show>
          <DetailSection title="Current recommendation">
            <KvRow
              icon="circle-check"
              label={recommendedMethod() ?? 'None'}
              meta={
                <span data-tip={sharedDefault() ? `Shared default: ${sharedDefault()}` : undefined}>
                  source: <code>{source()}</code>
                  <Show when={capability()}>{cap => <> · capability: <code>{cap()}</code></>}</Show>
                </span>
              }
            />
          </DetailSection>
          <DetailSection title={`Methods (${filtered().length})`}>
            <Show when={filtered().length > 0} fallback={<Empty text="No methods match." />}>
              <For each={filtered()}>
                {m => {
                  const isCurrent = () => m.id === recommendedMethod();
                  const a = m.assessment;
                  return (
                    <div class="stack">
                      <KvRow
                        icon={isCurrent() ? 'circle-check' : 'network'}
                        label={m.name}
                        meta={
                          <span class="deps">
                            <code>{m.id}</code>
                            <span>{m.scope}</span>
                            <a class="row-link" href={'/pipelines/' + encodeURIComponent(m.pipeline)} onClick={e => { e.preventDefault(); go('/pipelines/' + encodeURIComponent(m.pipeline)); }}>{m.pipeline}</a>
                            <Show when={m.status}>
                              <span class={`badge t-${methodStatusTone(m.status)}`}>
                                <Icon name={methodStatusIcon(m.status)} /><span>{m.status}</span>
                              </span>
                            </Show>
                          </span>
                        }
                        value={
                          <span class="deps">
                            <Show when={isCurrent()}><span class="badge t-success">Recommended</span></Show>
                            <Button
                              label={isCurrent() ? 'Selected' : 'Recommend'}
                              icon="check"
                              kind={isCurrent() ? 'quiet' : 'default'}
                              busy={busy() === m.id}
                              disabledReason={isCurrent() ? 'Already recommended for this project' : undefined}
                              onClick={() => void recommend(m.id)}
                            />
                          </span>
                        }
                      />
                      <Show when={m.description}><p class="muted">{m.description}</p></Show>
                      <KvRow
                        icon="chart-no-axes-column"
                        label="Assessment"
                        meta={
                          <span class="deps">
                            <span>{a.basis}</span>
                            <Show when={a.score != null}><Num m={{ value: a.score!, basis: a.basis === 'none' ? 'unavailable' : 'measured' }} kind="score" /></Show>
                            <Show when={a.wallMsMedian != null}><Num m={msMeasured(a.wallMsMedian!, 'measured')} kind="duration" /></Show>
                            <Show when={a.costUsd != null}><Num m={{ value: a.costUsd!, basis: a.costBasis === 'subscription' ? 'subscription' : 'measured', currency: 'USD' }} kind="money" /></Show>
                            <Show when={a.examples != null}><span>{a.examples} {a.examples === 1 ? 'example' : 'examples'}</span></Show>
                          </span>
                        }
                        value={a.experimentId ? (
                          <a class="row-link" href={'/lab/' + encodeURIComponent(a.experimentId)} onClick={e => { e.preventDefault(); go('/lab/' + encodeURIComponent(a.experimentId!)); }}>Experiment</a>
                        ) : undefined}
                      />
                    </div>
                  );
                }}
              </For>
            </Show>
          </DetailSection>
        </>
      }
    >
      <Match when={cat.status === 404}><Empty text="No method catalogue is configured." /></Match>
      <Match when={cat.error && !c()}><ErrorBanner text={`Methods did not load: ${cat.error}`} onRetry={cat.reload} /></Match>
      <Match when={!c()}><Skeleton rows={5} /></Match>
    </Switch>
  );
}
