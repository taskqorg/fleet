// Overview (docs/design-system/pages/overview.md): what needs the operator and whether the fleet is healthy. The control
// column is the Needs-you inbox, one row per question or problem, grouped by cause; the work area answers the one picked,
// and shows the fleet's state while none is (2026-11-20 decision, Overview B).
import { For, Match, Show, Switch, createMemo, createSignal } from 'solid-js';
import { useLocation, useSearch } from '../router.ts';
import { readValue, type MaybeAccessor } from '../signal.ts';
import {
  NeedsYou, OpsSummary, VirtualList, GroupHeader, catalogueRow, StatStrip, DecisionBlock, ProblemRow, StateBadge, HarnessCounts, Num, StateCounters, Empty, OfflineMarker, Icon, ErrorBanner, Skeleton, Button,
  ListDetail, DetailSection, KvRow, AgeChip, Ago, EntityCode, keyConnection, actor, ShellStatus, type IconName, type Stat,
} from '../../../packages/ui/src/index.ts';
import { online, addRequestedKey, writeOnce, useAlive } from '../api.ts';
import { useOp, type Freshness } from '../store.ts';
import type { QuestionRow, ExecutionRow, HostRow } from '../../../contracts/generated/ops.ts';
import { scopeIds, noProject, prefsRead, prefs } from '../prefs.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { idOf } from './delivery.tsx';
import { useClockOffset } from './hosts.tsx';
import { TaskQuestionContext } from './task-question-context.tsx';

/** The Overview's reads joined: open questions, runs that need a look, task counts and each project's counts. */
type OverviewData = { questions: QuestionRow[]; problems: ExecutionRow[]; counts: Record<string, number>; projects: { id: string; project: string | null; counts: Record<string, number> }[] };
// The focus and ignored projects (OR-34) narrow every part: questions, problems, counts and project rows.
function useOverview(scopeInput: MaybeAccessor<ReturnType<typeof scopeIds>>) {
  const scope = createMemo(() => readValue(scopeInput));
  const q = useOp('questions_list', () => (prefsRead.value ? { state: 'open', ...scope() } : null));
  const p = useOp('executions_list', () => (prefsRead.value ? { needsYou: true, ...scope() } : null), { quiet: 30_000 });
  // One grouped call: each project's per-state counts (groupCounts); tasks without a project are their own group.
  const page = useOp('task_page', () => {
    if (!prefsRead.value) return null;
    const { projectIds, excludeProjectIds } = scope();
    return {
      group: 'project',
      groupCounts: true,
      limit: 1,
      filter: {
        ...(projectIds && { projectId: projectIds.map(noProject) }),
        ...(excludeProjectIds && { excludeProjectId: excludeProjectIds.map(noProject) }),
      },
    };
  });
  const data = createMemo<OverviewData | null>(() =>
    q.data && p.data && page.data
      ? {
          questions: q.data.questions,
          problems: p.data.executions,
          counts: page.data.counts,
          projects: page.data.groups.map(g => ({ id: g.label ?? 'no project', project: g.label ?? null, counts: g.counts ?? {} })),
        }
      : null,
  );
  return {
    get data() { return data(); },
    get error() { return q.error ?? p.error ?? page.error; },
    get state(): Freshness { return [q.state, p.state, page.state].find(x => x !== 'current') ?? q.state; },
    get restored() { return q.restored || p.restored || page.restored; },
  };
}

/** One Needs-you item: an open question or a run that needs a look. */
type Item = { id: string; rank: number; at: number; cause: string; title: string } & ({ q: QuestionRow; e: null } | { q: null; e: ExecutionRow });

// The cause family names a group: the coordinator's problem causes (problem:delivery:…, problem:host:…) and question kinds.
const CAUSES: Record<string, string> = {
  delivery: 'Delivery got slower',
  host: 'Hosts',
  'host-poll': 'Hosts',
  store: 'Stores',
  absence: 'Coordinator',
  resource: 'Coordinator',
  work: 'Coordinator',
  telemetry: 'Coordinator',
  unknown: 'Coordinator',
  bugsink: 'Errors',
  'error-spike': 'Errors',
  'client-error': 'Errors',
  execution: 'Runs',
};
function causeOf(q: QuestionRow): string {
  const c = typeof q.cause === 'string' ? q.cause.split(':') : [];
  if (c[0] === 'problem' && c[1]) return CAUSES[c[1]] ?? c[1];
  return q.kind === 'acceptance' ? 'Acceptance' : keyConnection(q.options) ? 'Keys' : 'Decisions';
}
/** A question's headline: its first sentence, links left for the detail (raw URLs used to run past the column). */
export function headline(text: string): string {
  const t = String(text ?? '').replace(/https?:\/\/\S+/g, '').replace(/\s+/g, ' ').trim();
  const m = /^(.+?[.!?])(\s|$)/.exec(t);
  return (m ? m[1] : t).replace(/[.:]\s*$/, '') || 'Question';
}

/** Needs you order (OR-40): urgency first (a blocking question, then a problem, then an acceptance), then the most recent. */
function needsYou(d: OverviewData, now: number): Item[] {
  const rank = (q: QuestionRow) => (q.kind === 'acceptance' ? 2 : q.cause ? 1 : 0);
  return [
    ...d.questions.map(q => {
      const cause = causeOf(q), t = headline(q.text);
      // A word once per region (OR-17): a title that repeats its group's name drops it ("Delivery got slower: queue P50 …").
      const title = t.toLowerCase().startsWith(cause.toLowerCase() + ':') ? t.slice(cause.length + 1).trim() : t;
      return { id: q.id, q, e: null, rank: rank(q), at: Date.parse(q.askedAt) || now, cause, title };
    }),
    ...d.problems.map(e => ({ id: e.id, q: null, e, rank: 1, at: Date.parse(e.lastSeenAt ?? e.startedAt ?? '') || 0, cause: 'Runs', title: e.problem ?? 'Run needs a look' })),
  ].sort((a, b) => a.rank - b.rank || b.at - a.at);
}
/** Groups in the order of their most urgent item; items keep their order inside a group. */
function groupsOf(items: Item[]) {
  const g = new Map<string, Item[]>();
  for (const x of items) g.set(x.cause, [...(g.get(x.cause) ?? []), x]);
  return [...g].map(([cause, xs]) => ({ cause, items: xs }));
}

export function Overview(props: { id?: string }) {
  // ?all: the focus not applied; the header's "need you outside focus" opens it (app-shell.md).
  const search = useSearch();
  const [, go] = useLocation();
  const all = createMemo(() => new URLSearchParams(search()).has('all'));
  // ?all drops the focus, not the ignore: ignore is full silence (OR-34).
  const scope = createMemo(() => scopeIds(!all()));
  const alive = useAlive();
  // Collapsed cause groups, for this visit only: an inbox empties as it is answered.
  const [closed, setClosed] = createSignal<string[]>([]);
  const ov = useOverview(scope);
  // The Hosts page's read (one key, so one fetch serves both); liveness moves without a change row: every 5 s.
  const hostsRead = useOp('hosts_list', {}, { every: 5000 });
  const offset = useClockOffset(hostsRead);
  const now = () => Date.now();
  const hostNow = () => now() + offset();
  const keep = () => (all() ? '?all' : '');
  const open = (x: string | null) => go(x ? `/overview/${encodeURIComponent(x)}${keep()}` : `/overview${keep()}`);
  const items = createMemo(() => (ov.data ? needsYou(ov.data, now()) : []));
  const groups = createMemo(() => groupsOf(items()));
  const sel = createMemo(() => items().find(x => x.id === props.id));
  const sameGroup = createMemo(() => {
    const s = sel();
    if (!s) return [];
    return (groups().find(g => g.cause === s.cause)?.items ?? []).filter(x => x.id !== s.id);
  });
  const rows = createMemo<{ g?: string; n?: number; x?: Item }[]>(() => {
    const cl = closed();
    return groups().flatMap(g => [{ g: g.cause, n: g.items.length }, ...(cl.includes(g.cause) ? [] : g.items.map(x => ({ x })))]);
  });
  const fresh = (h: HostRow) => !!h.lastSeenAt && !!hostsRead.data && hostNow() - h.lastSeenAt < hostsRead.data.staleAfterMs;
  const hostList = () => hostsRead.data?.hosts ?? [];
  const onlineCount = createMemo(() => hostList().filter(fresh).length);
  const openProjects = createMemo(() =>
    (ov.data?.projects ?? []).filter(p => (p.counts.waiting ?? 0) + (p.counts.failed ?? 0) + (p.counts.in_progress ?? 0) + (p.counts.queued ?? 0) > 0),
  );
  const ops = createMemo<Stat[]>(() => {
    const d = ov.data;
    if (!d) return [];
    const hl = hostList(), onl = onlineCount(), its = items();
    const stats: Stat[] = [
      { label: 'need you', icon: 'bell', value: { value: its.length, basis: 'measured' }, tone: its.length ? 'attention' : undefined },
      ...(hl.length ? [{ label: 'hosts online', icon: 'panels-top-left', value: { value: onl, basis: 'measured' }, of: hl.length, tone: onl < hl.length ? 'attention' : undefined } satisfies Stat] : []),
      { label: 'failed', icon: 'octagon-x', value: { value: d.counts.failed ?? 0, basis: 'measured' }, tone: d.counts.failed ? 'danger' : undefined, href: '/tasks?state=failed' },
      { label: 'running', icon: 'activity', value: { value: d.counts.in_progress ?? 0, basis: 'measured' }, href: '/tasks?state=in_progress' },
      { label: 'queued', icon: 'list-ordered', value: { value: d.counts.queued ?? 0, basis: 'measured' }, href: '/tasks?state=queued' },
    ];
    return stats.filter(x => (x.value.value ?? 0) > 0);
  });

  const list = (
    <>
      <ShellStatus><OfflineMarker state={ov.state} now={now()} /></ShellStatus>
      <Show when={all()}>
        <header class="page-h">
          <span class="muted">Focus not applied</span>
          <Button label="Apply the focus" icon="focus" iconOnly kind="quiet" onClick={() => go('/overview')} />
        </header>
      </Show>
      <NeedsYou>
        <Switch
          fallback={
            /* One virtual list (OR-9): a group header per cause, then its items as one-line rows. */
            <VirtualList
              items={rows()}
              getKey={r => (r.g ? 'g:' + r.g : r.x!.id)}
              rowHeight={catalogueRow()}
              follow="top"
              label="Needs you"
              columns="1.25rem minmax(0,1fr) auto"
              rowClass={r => (r.g ? 'grp' : r.x!.id === props.id ? 'sel' : '')}
              render={r => {
                if (r.g) {
                  const gItems = groups().find(g => g.cause === r.g)?.items ?? [];
                  const gIcon: IconName = gItems.some(x => x.e || x.q?.cause) ? 'triangle-alert' : gItems.some(x => x.q?.kind === 'acceptance') ? 'circle-check' : 'bell';
                  return (
                    <GroupHeader
                      icon={gIcon}
                      tone="attention"
                      label={r.g}
                      count={r.n!}
                      open={!closed().includes(r.g)}
                      onToggle={() => setClosed(c => (c.includes(r.g!) ? c.filter(x => x !== r.g) : [...c, r.g!]))}
                    />
                  );
                }
                const x = r.x!;
                const isProblem = !x.q || !!x.q.cause;
                const leadIcon: IconName = isProblem ? 'triangle-alert' : x.q!.kind === 'acceptance' ? 'circle-check' : 'bell';
                const leadLabel = isProblem ? (x.q ? 'System problem' : 'Problem') : x.q!.kind === 'acceptance' ? 'Result acceptance' : 'Blocking decision';
                return (
                  <>
                    <span class="lead ny-ic"><Icon name={leadIcon} label={leadLabel} /></span>
                    <span class="tt">
                      <a class="title row-link" href={`/overview/${encodeURIComponent(x.id)}${keep()}`} onClick={ev => { ev.preventDefault(); open(x.id); }}>{x.title}</a>
                      <Show when={x.q} fallback={<span class="id">{[x.e!.hostId, x.e!.harness].filter(Boolean).join(' · ')}</span>}>
                        {q => <EntityCode code={q().code} id={q().id} kind="question" />}
                      </Show>
                    </span>
                    <Ago t={x.at} />
                  </>
                );
              }}
            />
          }
        >
          <Match when={ov.error && !ov.data}>
            <ErrorBanner text={`Overview did not load: ${ov.error}`} />
          </Match>
          <Match when={!ov.data}>
            <Skeleton rows={6} />
          </Match>
          <Match when={!items().length}>
            <Empty text={!all() && prefs.value.focus.length ? 'Nothing is waiting for you in the focused projects.' : 'Nothing is waiting for you.'} />
          </Match>
        </Switch>
      </NeedsYou>
    </>
  );

  // Nothing picked: the fleet's state, so the work area is never an empty "pick a row" (2026-11-20 decision).
  const idle = (
    <article class="detail" aria-label="Fleet now">
      <OpsSummary>
        <Switch
          fallback={
            <>
              <Show when={ops().length > 0}><StatStrip label="Fleet now" stats={ops()} /></Show>
              <Show when={hostList().length > 0 || openProjects().length > 0}>
                <div class="cols">
                  <Show when={hostList().length > 0}>
                    <DetailSection title="Hosts">
                      <For each={hostList()}>
                        {h => {
                          const live = () => fresh(h);
                          return (
                            <KvRow
                              icon="panels-top-left"
                              label={h.id}
                              meta={[h.platform, h.version].filter(Boolean).join(' · ')}
                              value={
                                <>
                                  <StateBadge family="host" state={live() ? 'online' : 'offline'} lastSeen={live() ? undefined : h.lastSeenAt ?? undefined} now={hostNow()} compact />
                                  <Show when={live() && h.running > 0}>
                                    <Num m={{ value: h.running, basis: 'measured' }} kind="count" />
                                    <HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} />
                                  </Show>
                                  <Show when={live() && h.queued > 0}>
                                    <span class="hc" data-tip="Queued commands">
                                      <Icon name="list-ordered" label="Queued commands" />
                                      <Num m={{ value: h.queued, basis: 'measured' }} kind="count" />
                                    </span>
                                  </Show>
                                </>
                              }
                            />
                          );
                        }}
                      </For>
                    </DetailSection>
                  </Show>
                  <Show when={openProjects().length > 0}>
                    <DetailSection title="Projects">
                      <For each={openProjects()}>
                        {p => {
                          const counts = { waiting: p.counts.waiting ?? 0, failed: p.counts.failed ?? 0, in_progress: p.counts.in_progress ?? 0, queued: p.counts.queued ?? 0 };
                          return (
                            <KvRow
                              icon="folder-open"
                              label={p.id}
                              value={
                                <StateCounters
                                  family="task"
                                  label={`${p.id} by state`}
                                  counts={counts}
                                  active={[]}
                                  hideZero
                                  onAll={() => go(formatView({ view: 'tasks', state: Object.keys(counts), project: [p.project] }))}
                                  onToggle={s => go(formatView({ view: 'tasks', state: [s], project: [p.project] }))}
                                />
                              }
                            />
                          );
                        }}
                      </For>
                    </DetailSection>
                  </Show>
                </div>
              </Show>
              <InFlight scope={scope()} />
            </>
          }
        >
          <Match when={ov.error && !ov.data}>
            <ErrorBanner text={`Overview did not load: ${ov.error}`} />
          </Match>
          <Match when={!ov.data}>
            <Skeleton rows={6} />
          </Match>
        </Switch>
      </OpsSummary>
    </article>
  );

  return (
    <ListDetail
      list={list}
      detail={
        props.id && (!ov.data || sel()) ? (
          <Show
            when={ov.data && sel()}
            keyed
            fallback={
              <article class="detail" data-ui="detail" tabindex={-1}>
                <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => open(null)} /></header>
                <Show when={ov.error} fallback={<Skeleton rows={6} />}>
                  {err => <ErrorBanner text={`Overview did not load: ${err()}`} />}
                </Show>
              </article>
            }
          >
            {s => (
              <article class="detail" data-ui="detail" tabindex={-1} aria-label={s.title}>
                <header class="detail-h">
                  <Button label="Close" icon="x" iconOnly onClick={() => open(null)} />
                  <Show
                    when={s.q && !s.q.cause}
                    fallback={<Icon name="triangle-alert" label={s.q ? 'System problem' : 'Problem'} />}
                  >
                    <StateBadge family="question" state="open" />
                  </Show>
                  <Show when={s.q} keyed>{q => <EntityCode code={q.code} id={q.id} kind="question" />}</Show>
                  <span class="muted">{[(s.q ?? s.e).projectId, s.cause].filter(Boolean).join(' · ')}</span>
                  <span class="grow" />
                  <AgeChip t={s.at} />
                </header>
                <h2 class="detail-title">{s.title}</h2>
                <Show
                  when={s.q}
                  keyed
                  fallback={
                    <ProblemRow
                      severity="warning"
                      title={s.e!.problem ?? 'Run needs a look'}
                      facts={[s.e!.hostId, s.e!.harness].filter(Boolean).join(' · ')}
                      actions={s.e!.taskId ? [{ label: 'Open task', onClick: () => go(`/tasks/${encodeURIComponent(s.e!.taskId!)}`) }] : []}
                    />
                  }
                >
                  {q => (
                    <DecisionBlock
                      question={q.text}
                      askedBy={actor(q.askedBy) || q.kind}
                      addressedTo="you"
                      since={s.at}
                      now={now()}
                      waits={q.taskId ? <EntityCode id={q.taskId} kind="task" /> : undefined}
                      context={<TaskQuestionContext question={q} />}
                      options={(Array.isArray(q.options) ? q.options as unknown[] : q.kind === 'acceptance' ? ['accepted', 'rejected'] : keyConnection(q.options) ? ['decline'] : []).map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
                      onAnswer={a => { if (online.value) writeOnce('questions_answer', { questionId: q.id, answer: a }, () => {}, { signal: alive }).then(() => open(null), () => { /* the question stays open */ }); }}
                      onSecret={keyConnection(q.options) ? v => addRequestedKey(q.id, keyConnection(q.options)!, v, alive) : undefined}
                    />
                  )}
                </Show>
                <Show when={sameGroup().length > 0}>
                  <DetailSection title={`Same cause, ${sameGroup().length} more`}>
                    <ul class="kv">
                      <For each={sameGroup()}>
                        {x => (
                          <li>
                            <Icon name={x.q ? 'bell' : 'triangle-alert'} />
                            <a class="row-link" href={`/overview/${encodeURIComponent(x.id)}${keep()}`} onClick={ev => { ev.preventDefault(); open(x.id); }}>{x.title}</a>
                            <Show when={x.q} keyed>{q => <EntityCode code={q.code} id={q.id} kind="question" />}</Show>
                            <span class="grow" />
                            <AgeChip t={x.at} />
                          </li>
                        )}
                      </For>
                    </ul>
                  </DetailSection>
                </Show>
              </article>
            )}
          </Show>
        ) : undefined
      }
      detailKey={props.id}
      idle={idle}
    />
  );
}

/** Pull requests in flight: the Delivery page's own read (the same doc, so one fetch serves both), under the focus (OR-34). */
function InFlight(props: { scope: ReturnType<typeof scopeIds> }) {
  const res = useOp('publishing_status', () => (prefsRead.value ? props.scope : null), { every: 300000 });
  const moving = createMemo(() => (res.data?.items ?? []).filter(it => it.stage !== 'installed' && it.stage !== 'merged'));
  return (
    <Switch
      fallback={
        <Show when={moving().length > 0}>
          <DetailSection title="Delivery in flight">
            <For each={moving()}>
              {it => (
                <KvRow
                  icon="git-pull-request"
                  label={it.title}
                  meta={<EntityCode code={`#${it.number}`} id={idOf(it)} kind="pull_request" />}
                  value={<><StateBadge family="delivery" state={it.stage} compact /><AgeChip t={Date.parse(it.since)} /></>}
                />
              )}
            </For>
          </DetailSection>
        </Show>
      }
    >
      <Match when={res.error && !res.data}>
        <DetailSection title="Delivery in flight">
          <ErrorBanner text={`Delivery did not load: ${res.error}`} />
        </DetailSection>
      </Match>
    </Switch>
  );
}
