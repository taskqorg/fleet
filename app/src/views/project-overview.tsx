// An opened project's Overview, Pipelines, pipeline page and History facets (docs/design-system/pages/projects.md,
// Target; board screens/projects.bl, Approved P-A1..P-A11). Overview and Pipelines share one read, project_view; a
// pipeline page reads project_pipeline_view; History is the shared HistoryFeed (history_view) scoped to the project.
import { For, Match, Show, Switch, createMemo, createSignal } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { useLocation } from '../router.ts';
import {
  Ago, Button, DataTable, DetailSection, Empty, ErrorBanner, Fact, Icon, LiveRow, OpenQuestion, PipelineOutline, RowFacts,
  Skeleton, StateBadge, TabBar, TableRow, day, duration, fileLabel, life, score, states, statesOf, type IconName, type Tone,
} from '../../../packages/ui/src/index.ts';
import type { Ops } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { useView } from '../store.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { CHIP_STAGE, idOf } from './delivery-model.tsx';
import { HistoryFeed } from './history.tsx';

type ProjectView = Ops['project_view']['reply'];
type Group = ProjectView['pipelines'][number];
type PipeRow = Group['pipelines'][number];
type Entry = NonNullable<PipeRow['best']>;
type Question = ProjectView['waiting'][number];
type Decision = ProjectView['decisions'][number];
type Experiment = Ops['project_pipeline_view']['reply']['experiments'][number];
type HistoryKind = NonNullable<Ops['history_view']['request']['kinds']>[number];

/** The project's one Overview read; the head (Talk to PM) and the Pipelines facet ask with the same request and share it. */
export const useProjectView = (projectId: () => string) => useView('project_view', () => ({ projectId: projectId() }), { quiet: 5000 });

const enc = encodeURIComponent;
/** A project facet's address (internal form; the router writes `/projects/<CODE>/<facet>`). */
export const projectFacetPath = (projectId: string, facet: string): string =>
  facet === 'overview' ? '/projects/' + enc(projectId)
    : facet === 'tasks' || facet === 'results' || facet === 'delivery' ? `/${facet}?project=${enc(projectId)}`
      : `/projects/${enc(projectId)}?facet=${facet}`;
/** A pipeline page of a project, on one of its tabs (Overview has no tab parameter). */
export const pipelinePath = (projectId: string, pipelineId: string, tab?: string): string =>
  `/projects/${enc(projectId)}?facet=pipelines&pipeline=${enc(pipelineId)}${tab && tab !== 'overview' ? '&tab=' + tab : ''}`;
const labNew = (useCaseId: string) => '/lab/new?useCase=' + enc(useCaseId);
const labPath = (experimentId: string) => '/lab/' + enc(experimentId);

/** A job's readable name: `document-recognition` (or a use case `dam:document-recognition`) is "Document recognition". */
export function jobName(capability: string): string {
  const s = capability.slice(capability.lastIndexOf(':') + 1).replace(/[-_]+/g, ' ').trim();
  return s ? s[0].toUpperCase() + s.slice(1) : 'Other';
}

/** Median wall time in compact form (5m, 40s), or nothing when unknown. */
const median = (ms: number | null) => (ms == null ? '' : duration(ms / 1000).text);

/** A link that opens in the app (OR-26); a modified click keeps the browser's own behaviour. */
function Go(p: { to: string; cls?: string; children: unknown }) {
  const [, go] = useLocation();
  return <a class={p.cls ?? 'row-link'} href={p.to} onClick={e => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); go(p.to); } }}>{p.children as never}</a>;
}

/** An open question as a card (OR-53, OpenQuestion), under the name of the project that asks it. */
export function AskCard(props: { q: Question; label?: string }) {
  const answers = useAnswers();
  return (
    <div class="pw-ask">
      <Show when={props.label}><span class="muted type-sm">{props.label}{props.q.code ? ' · ' + props.q.code : ''}</span></Show>
      <OpenQuestion q={props.q} answers={answers} />
    </div>
  );
}

/** A Lab decision as a card: the experiment's goal, one choice per variant with its evidence; a choice opens the
 * experiment, where Lab adopts it (this page does not write). */
function DecisionCard(props: { d: Decision }) {
  return (
    <div class="qcard" data-ui="question-card" role="group" aria-label={props.d.goal}>
      <p class="qcard-q">{props.d.goal}</p>
      <div class="qcard-choices">
        <For each={props.d.choices}>{c => (
          <Go to={labPath(props.d.experimentId)} cls="pw-choice"><EntryFacts e={c} label /></Go>
        )}</For>
      </div>
    </div>
  );
}

/** A variant's cost per trial: the leaderboard sums its trials' cost. */
const perRun = (e: Entry): number | null => (e.costUsd == null || e.trials === 0 ? null : e.costUsd / e.trials);

/** A variant's evidence in one line: score, succeeded of trials, median time, cost; failures in the danger tone. */
function EntryFacts(props: { e: Entry; label?: boolean }) {
  return (
    <>
      <Show when={props.label}><span class="mono">{props.e.variantLabel}</span></Show>
      <Show when={props.e.scoreMean != null}><span class="mono" data-tip="Mean score">{score(props.e.scoreMean!).text}</span></Show>
      <span class="mono" data-tip="Succeeded of trials">{props.e.succeededTrials}/{props.e.trials}</span>
      <Show when={props.e.wallMsMedian != null}><span class="mono" data-tip="Median time">{median(props.e.wallMsMedian)}</span></Show>
      <Show when={perRun(props.e) != null}><span class="mono" data-tip="Cost per trial">${perRun(props.e)!.toFixed(2)}</span></Show>
      <Show when={props.e.failedTrials > 0}><span class="t-danger pw-tone">{props.e.failedTrials} failed</span></Show>
    </>
  );
}

/** The Overview's and the Pipelines facet's read states: an error with retry, a skeleton, then the content. */
function Loaded(props: { view: ReturnType<typeof useProjectView>; children: (v: () => ProjectView) => JSX.Element }) {
  return (
    <Show when={props.view.data} fallback={
      <Show when={props.view.error} fallback={<Skeleton rows={6} />}>{err => <ErrorBanner text={`Project did not load: ${err()}`} onRetry={props.view.reload} />}</Show>
    }>{v => props.children(v)}</Show>
  );
}

/* ---------- Overview ---------- */

/** Overview: each block is the head of a facet (its title opens it, OR-30); an empty block is left out (OR-29). */
export function ProjectOverview(props: { projectId: string }) {
  const [, go] = useLocation();
  const view = useProjectView(() => props.projectId);
  const waitingHref = () => formatView({ view: 'conversations', state: statesOf('session', ['waiting']), project: [props.projectId] });
  const talkHref = () => formatView({ view: 'conversations', project: [props.projectId] });
  const facet = (f: string) => projectFacetPath(props.projectId, f);
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label="Overview">
      <Loaded view={view}>{v => (
        <>
          <Show when={v().waitingTotal + v().decisions.length > 0}>
            <DetailSection title="Waiting for you" count={v().waitingTotal + v().decisions.length} href={waitingHref()} onOpen={() => go(waitingHref())}>
              <div class="pw-cards">
                <For each={v().decisions}>{d => <DecisionCard d={d} />}</For>
                <For each={v().waiting}>{q => <AskCard q={q} />}</For>
              </div>
            </DetailSection>
          </Show>
          <Show when={v().inWork.length > 0}>
            <DetailSection title="In work" count={v().inWorkTotal} href={facet('tasks')} onOpen={() => go(facet('tasks'))}>
              <ul class="live-list">
                <For each={v().inWork}>{t => (
                  <LiveRow kind="task" state={life('task', t.pauseReason === 'held' ? 'held' : t.state)} title={t.title} code={t.code ?? undefined}
                    onOpen={() => go(`/tasks/${enc(t.code ?? t.id)}?project=${enc(props.projectId)}`)}
                    facts={<RowFacts>
                      <Show when={t.agent ?? t.lastAgent}>{a => <span class="lr-fact muted mono">{a()}</span>}</Show>
                      <Show when={t.stateAt ?? t.updatedAt}>{at => <Ago t={Date.parse(at())} />}</Show>
                    </RowFacts>} />
                )}</For>
              </ul>
            </DetailSection>
          </Show>
          <Show when={v().pipelines.length > 0}>
            <DetailSection title="Pipelines" count={v().pipelines.length} href={facet('pipelines')} onOpen={() => go(facet('pipelines'))}>
              <div class="pw-list">
                <For each={v().pipelines}>{g => <JobLine g={g} projectId={props.projectId} />}</For>
              </div>
            </DetailSection>
          </Show>
          <Show when={v().results.length > 0}>
            <DetailSection title="Latest results" count={v().resultsTotal} href={facet('results')} onOpen={() => go(facet('results'))}>
              <div class="pw-list">
                <For each={v().results}>{r => (
                  <Go to={`/results/${enc(r.code ?? r.id)}?project=${enc(props.projectId)}`} cls="pw-line">
                    <Icon name="file-text" />
                    <span class="pw-line-m"><span>{r.summary || fileLabel(r.name) || r.name}</span><Show when={r.code}><span class="muted mono">{r.code}</span></Show></span>
                    <RowFacts><Ago t={r.createdAt} /></RowFacts>
                  </Go>
                )}</For>
              </div>
            </DetailSection>
          </Show>
          <Show when={v().conversations.length > 0}>
            <DetailSection title="Conversations" count={v().conversationsTotal} href={talkHref()} onOpen={() => go(talkHref())}>
              <ul class="live-list">
                <For each={v().conversations}>{s => (
                  <LiveRow kind="event" state={life('session', s.state)} text={s.title ?? s.code ?? 'Untitled conversation'}
                    at={s.activityAt ? Date.parse(s.activityAt) : (s.createdAt ?? NaN)} onOpen={() => go('/conversations/' + enc(s.code ?? s.id))} />
                )}</For>
              </ul>
            </DetailSection>
          </Show>
          <DetailSection title="Delivery" count={v().delivery.length || undefined} href={facet('delivery')} onOpen={() => go(facet('delivery'))}>
            <Show when={v().delivery.length > 0} fallback={
              <p class="pw-muted-line">{['Nothing in flight', v().merged > 0 ? `${v().merged} merged` : '', v().lastMergedAt ? `last ${day(Date.parse(v().lastMergedAt!))}` : ''].filter(Boolean).join(' · ')}</p>
            }>
              <ul class="live-list">
                <For each={v().delivery}>{d => (
                  <LiveRow kind="delivery" tone={(states.delivery[d.stage]?.tone ?? 'neutral') as Tone} code={`#${d.number}`} title={d.title}
                    stage={CHIP_STAGE[d.stage]} trouble={d.stage.endsWith('failed')}
                    onOpen={() => go(`/delivery/${enc(idOf(d))}?project=${enc(props.projectId)}`)} />
                )}</For>
              </ul>
            </Show>
          </DetailSection>
        </>
      )}</Loaded>
    </article>
  );
}

/** A job on the Overview: its name, then the method in use, or "not chosen · N candidates" when Lab has candidates. */
function JobLine(props: { g: Group; projectId: string }) {
  const candidates = () => props.g.pipelines.filter(p => p.best).length;
  return (
    <Go to={projectFacetPath(props.projectId, 'pipelines')} cls="pw-line">
      <Icon name={props.g.inUse ? 'circle-check' : 'circle-dashed'} />
      <span class="pw-line-m">
        <span>{jobName(props.g.capability)}</span>
        <Show when={props.g.inUse} fallback={<Show when={candidates() > 0}><span class="t-attention pw-tone">not chosen · {candidates()} {candidates() === 1 ? 'candidate' : 'candidates'}</span></Show>}>
          <span class="muted mono">{props.g.inUse}</span>
        </Show>
      </span>
      <RowFacts>
        <Show when={props.g.inUse && props.g.inUseSource === 'shared'}><span class="muted">shared default</span></Show>
        <Show when={props.g.experiments > 0}><Fact text={`${props.g.experiments} ${props.g.experiments === 1 ? 'experiment' : 'experiments'}`} tip="Lab experiments on this job" /></Show>
      </RowFacts>
    </Go>
  );
}

/* ---------- Pipelines facet ---------- */

/** Pipelines: one group per job, the method in use first, then the candidates Lab compared (board Approved P-A11). */
export function ProjectPipelines(props: { projectId: string }) {
  const view = useProjectView(() => props.projectId);
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label="Pipelines">
      <Loaded view={view}>{v => (
        <Show when={v().pipelines.length > 0} fallback={<Empty text="No pipelines for this project yet. A method in the catalogue or a Lab experiment adds one." />}>
          <For each={v().pipelines}>{g => <JobGroup g={g} decisions={v().decisions} projectId={props.projectId} />}</For>
        </Show>
      )}</Loaded>
    </article>
  );
}

function JobGroup(props: { g: Group; decisions: Decision[]; projectId: string }) {
  const [, go] = useLocation();
  // The decision about this job: one whose choices ran a pipeline of the group.
  const decision = createMemo(() => {
    const ids = new Set(props.g.pipelines.map(p => p.pipelineId));
    return props.decisions.find(d => d.choices.some(c => ids.has(c.pipelineId)));
  });
  const useCase = () => props.g.useCaseId;
  return (
    <section class="pw-group" aria-label={jobName(props.g.capability)}>
      <div class="pw-group-h">
        <b>{jobName(props.g.capability)}</b>
        <Show when={useCase()}>{u => <span class="muted mono">{u()}</span>}</Show>
        <span class="grow" />
        <Show when={useCase()}>{u => <>
          <Go to={'/lab/' + enc('use-case:' + u())}>Compare</Go>
          <Button label="Run in Lab" icon="play" kind="quiet" onClick={() => go(labNew(u()))} />
        </>}</Show>
      </div>
      <Show when={!props.g.inUse && decision()}>{d => (
        <p class="pw-muted-line t-attention pw-tone">Not chosen yet: {d().goal} waits for your decision</p>
      )}</Show>
      <div class="pw-list">
        <For each={props.g.pipelines}>{p => <PipelineLine p={p} inUse={!!props.g.inUse && p.methodId === props.g.inUse} projectId={props.projectId} />}</For>
      </div>
    </section>
  );
}

function PipelineLine(props: { p: PipeRow; inUse: boolean; projectId: string }) {
  const icon = (): IconName => (props.inUse ? 'circle-check' : 'circle-dashed');
  return (
    <Go to={pipelinePath(props.projectId, props.p.pipelineId)} cls="pw-line">
      <span class={props.inUse ? 't-success pw-tone' : 'muted'}><Icon name={icon()} /></span>
      <span class="pw-line-m">
        <span class="mono">{props.p.pipelineId}</span>
        <span class={['badge', props.inUse ? 't-success' : 't-neutral']}>{props.inUse ? 'in use' : props.p.status ?? (props.p.trials ? 'candidate' : 'no trials yet')}</span>
        <Show when={props.p.name ?? props.p.description}>{t => <span class="pw-line-sub">{t()}</span>}</Show>
      </span>
      <RowFacts>
        <Show when={props.p.best}>{b => <EntryFacts e={b()} />}</Show>
        <Show when={props.p.lastAt}>{at => <Ago t={Date.parse(at())} />}</Show>
      </RowFacts>
    </Go>
  );
}

/* ---------- A pipeline page ---------- */

const PIPE_TABS = [
  { id: 'overview', label: 'Overview' }, { id: 'experiments', label: 'Experiments' }, { id: 'definition', label: 'Definition' }, { id: 'history', label: 'History' },
] as const;
type PipeTab = (typeof PIPE_TABS)[number]['id'];

/** One pipeline of the project: what it does, how it works, how it ranks on the project's sample, its experiments. */
export function PipelinePage(props: { projectId: string; projectName: string; pipelineId: string; tab?: string }) {
  const [, go] = useLocation();
  const view = useView('project_pipeline_view', () => ({ projectId: props.projectId, pipelineId: props.pipelineId }), { quiet: 5000 });
  const tab = createMemo<PipeTab>(() => PIPE_TABS.find(t => t.id === props.tab)?.id ?? 'overview');
  const back = () => projectFacetPath(props.projectId, 'pipelines');
  return (
    <article class="detail pw-pipe" data-ui="detail" tabindex={-1} aria-label={props.pipelineId}>
      <header class="pw-pipe-h">
        <Button label="Pipelines" icon="arrow-left" iconOnly kind="quiet" onClick={() => go(back())} />
        <Show when={view.data?.capability}>{c => <span class="muted">Pipelines · {jobName(c())}</span>}</Show>
        <span class="grow" />
        <Show when={view.data?.useCaseId}>{u => <Button label="Run in Lab" icon="play" onClick={() => go(labNew(u()))} />}</Show>
      </header>
      <div class="pw-pipe-h">
        <h3>{props.pipelineId}</h3>
        <Show when={view.data}>{v => <span class={['badge', v().inUse ? 't-success' : 't-neutral']}>{v().inUse ? `in use for ${props.projectName}` : 'candidate'}</span>}</Show>
      </div>
      <Show when={view.data} fallback={
        <Switch fallback={<Skeleton rows={6} />}>
          <Match when={view.status === 404}><Empty text="This pipeline is not part of the project." /></Match>
          <Match when={view.error}><ErrorBanner text={`Pipeline did not load: ${view.error}`} onRetry={view.reload} /></Match>
        </Switch>
      }>{v => (
          <>
            <Show when={v().description}><p>{v().description}</p></Show>
            <InOut input={v().reads} output={v().returns} />
            <TabBar cls="detail-tabs" label="Pipeline" tabs={PIPE_TABS} value={tab()} onChange={t => go(pipelinePath(props.projectId, props.pipelineId, t))} />
            <div role="tabpanel" aria-label={PIPE_TABS.find(t => t.id === tab())?.label}>
              <Switch>
                <Match when={tab() === 'overview'}>
                  <Show when={v().outline.length > 0}>
                    <DetailSection title="How it works"><PipelineOutline lines={v().outline} /></DetailSection>
                  </Show>
                  <Show when={v().ranking.length > 0}>
                    <DetailSection title={`On ${props.projectName} sample`}><Ranking entries={v().ranking} pipelineId={props.pipelineId} /></DetailSection>
                  </Show>
                  <Show when={v().experiments.length > 0}>
                    <DetailSection title="Experiments" count={v().experiments.length} href={pipelinePath(props.projectId, props.pipelineId, 'experiments')}
                      onOpen={() => go(pipelinePath(props.projectId, props.pipelineId, 'experiments'))}>
                      <Experiments rows={v().experiments.slice(0, 3)} />
                    </DetailSection>
                  </Show>
                </Match>
                <Match when={tab() === 'experiments'}>
                  <Show when={v().experiments.length > 0} fallback={<Empty text="No experiments ran this pipeline yet." />}>
                    <Experiments rows={v().experiments} />
                  </Show>
                </Match>
                <Match when={tab() === 'definition'}>
                  <Show when={v().outline.length > 0} fallback={<Empty text="This pipeline has no readable steps." />}>
                    <DetailSection title="Steps"><PipelineOutline lines={v().outline} label="Steps" /></DetailSection>
                  </Show>
                  <p class="pw-muted-line"><Go to={'/library/pipelines/' + enc(props.pipelineId)}>Definition JSON</Go> · revision {v().revision}</p>
                </Match>
                <Match when={tab() === 'history'}>
                  <section class="panel"><HistoryFeed request={() => ({ projectIds: [props.projectId], kinds: ['lab'] })} label="Lab history" /></section>
                </Match>
              </Switch>
            </div>
          </>
      )}</Show>
    </article>
  );
}

/** What goes in and what comes out, in one quiet line. */
function InOut(props: { input: string | null; output: string | null }) {
  return (
    <Show when={props.input || props.output}>
      <p class="pw-muted-line">
        <Show when={props.input}><span>In </span><span class="mono">{props.input}</span></Show>
        <Show when={props.input && props.output}><span> · </span></Show>
        <Show when={props.output}><span>Out </span><span class="mono">{props.output}</span></Show>
      </p>
    </Show>
  );
}

/** The variants ranked on the project's sample (DataTable stacks itself at narrow widths, OR-66); this pipeline's rows
 * are marked. A row opens its latest experiment. */
function Ranking(props: { entries: Entry[]; pipelineId: string }) {
  const [, go] = useLocation();
  return (
    <DataTable label="Ranking" head={<tr><th scope="col" class="num">#</th><th scope="col">Variant</th><th scope="col" class="num">Score</th><th scope="col" class="num">OK</th><th scope="col" class="num">Median</th><th scope="col" class="num">Cost</th></tr>}>
      <For each={props.entries}>{(e, i) => (
        <TableRow selected={e.pipelineId === props.pipelineId} onOpen={() => go(labPath(e.latestExperimentId))}>
          <td class="num mono">{i() + 1}</td>
          <th scope="row" class="t-name"><span class="mono">{e.variantLabel}</span> <span class="muted mono">· {e.pipelineId}</span></th>
          <td class="num mono">{e.scoreMean == null ? '—' : score(e.scoreMean).text}</td>
          <td class={['num', 'mono', { 't-danger pw-tone': e.failedTrials > 0 }]}>
            {e.succeededTrials}/{e.trials}<Show when={e.failedTrials > 0}> · {e.failedTrials} failed</Show>
          </td>
          <td class="num mono">{median(e.wallMsMedian) || '—'}</td>
          <td class="num mono">{perRun(e) == null ? '—' : '$' + perRun(e)!.toFixed(2)}</td>
        </TableRow>
      )}</For>
    </DataTable>
  );
}

/** Experiments as rows: state, goal, variants, age; a row opens the experiment in Lab. */
function Experiments(props: { rows: Experiment[] }) {
  return (
    <div class="pw-list">
      <For each={props.rows}>{x => (
        <Go to={labPath(x.id)} cls="pw-line">
          <Show when={x.state in states.experiment} fallback={<Icon name="circle-dashed" />}>
            <StateBadge family="experiment" state={x.state as keyof typeof states.experiment} compact />
          </Show>
          <span class="pw-line-m"><span>{x.goal || 'Untitled experiment'}</span></span>
          <RowFacts>
            <Fact text={`${x.variants.length} ${x.variants.length === 1 ? 'variant' : 'variants'}`} tip="Variants compared" />
            <Ago t={Date.parse(x.createdAt)} />
          </RowFacts>
        </Go>
      )}</For>
    </div>
  );
}

/* ---------- History facet ---------- */

const KINDS: { id: 'all' | HistoryKind; label: string }[] = [
  { id: 'all', label: 'All' }, { id: 'run', label: 'Runs' }, { id: 'task', label: 'Tasks' }, { id: 'question', label: 'Questions' },
  { id: 'lab', label: 'Lab' }, { id: 'settings', label: 'Settings' }, { id: 'access', label: 'Access' },
];

/** History: what happened in the project, newest first, filtered by kind; older entries load on scroll (OR-65). */
export function ProjectHistory(props: { projectId: string }) {
  const [kind, setKind] = createSignal<'all' | HistoryKind>('all');
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label="History">
      <div class="presets" role="group" aria-label="Kinds">
        <For each={KINDS}>{k => <Button label={k.label} kind="quiet" pressed={kind() === k.id} onClick={() => setKind(k.id)} />}</For>
      </div>
      <section class="panel">
        <HistoryFeed request={() => ({ projectIds: [props.projectId], limit: 60, ...(kind() === 'all' ? {} : { kinds: [kind() as HistoryKind] }) })} label="Project history" />
      </section>
    </article>
  );
}
