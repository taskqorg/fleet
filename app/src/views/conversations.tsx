// Conversations (docs/design-system/pages/conversations.md): sessions list and one conversation with its live transcript.
// Actions use the same operations agents use: session_start (conversation-new.tsx), message_send, execution_stop, session_fork.
import { For, Match, Show, Switch, createMemo, createSignal, onSettled, untrack } from 'solid-js';
import { currentUrl, useLocation, useSearch } from '../router.ts';
import {
  EntityCode, ListDetail, RowFacts, Fact, AgeChip, ProblemRow,
  StateBadge, StateCounters, Button, DetailSection,
  OpsSummary, KvRow, Num, type IconName,
  Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses, type Harness, type StateOf, states as states_,
  OpenQuestion, Latest, StateDot, type Tone,
  ShellStatus, ShellActions,
} from '../../../packages/ui/src/index.ts';
import { op, online, type Ops } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { NewConversation } from './conversation-new.tsx';
import { SessionView } from './session.tsx';
import { useOp } from '../store.ts';
import type { SessionRow as ContractSessionRow } from '../../../contracts/generated/ops.ts';
import { DEFAULT_SESSION_GROUP, DEFAULT_SESSION_SORT } from '../../../packages/view-url/view-url.ts';
import { nextUrl } from '../url.ts';
import { prefs, prefsRead, scopeIds, noProject } from '../prefs.ts';
import { useListPage, ListPagePanel, type ListPage } from '../list-page.tsx';
import { SYSTEM_VIEWS } from '../system-views.ts';

const GROUPS = [['project', 'Project'], ['state', 'State'], ['agent', 'Agent'], ['none', 'None']] as const;
const SORTS = [['activity', 'Activity'], ['created', 'Created']] as const;
/** The contract's session row. */
type SessionRow = ContractSessionRow & { state: StateOf<'session'> };
const TERMINAL = ['succeeded', 'failed', 'cancelled'];
const titleOf = (s: SessionRow) => s.title ?? 'New conversation';

export function Conversations(props: { id?: string }) {
  // Pinned conversations stay whatever the filter, search or grouping (OR-12): read by id with no filter but the archived
  // list the view shows, so an archived pin never shows among active conversations or the other way round. Active and
  // archived conversations are two lists: a switch by any path (the toggle, a saved view, Back) ends selection mode and a
  // pending confirmation; the toggle and a saved view also drop the checked rows. The rows a bulk action finished leave
  // the selection (list-page.tsx).
  const page = useListPage<SessionRow>({
    view: 'conversations', entity: 'conversation', kind: 'sessions', defaultGroup: DEFAULT_SESSION_GROUP, defaultSort: DEFAULT_SESSION_SORT, system: SYSTEM_VIEWS.conversations,
    // The list names a group by its label; the waiting group's label is "waiting" (sessions_list), no project's name.
    groupLabel: (g, label) => (g === 'project' && label === 'waiting' ? 'Waiting for you' : groupLabel(g, label)),
    pinRead: v => ({ filter: { archived: !!v.archived } }),
    saveExtra: v => ({ archived: v.archived }), pickExtra: sv => ({ archived: sv.archived }),
    listId: v => !!v.archived,
  });
  const { set } = page;
  const search = useSearch();
  const archived = () => !!page.view.archived;
  const canArchive = (s: SessionRow) => s.state === 'idle' || TERMINAL.includes(s.state);
  // Archive and Unarchive are one operation per conversation. session_archive is refused while an execution is active;
  // session_unarchive (owner or full access) returns the conversation to the active list and records a reason.
  const archive = (ss: SessionRow[]) => page.bulk(ss, s => op('session_archive', { sessionId: s.id }), 'not archived');
  const unarchive = (ss: SessionRow[]) => page.bulk(ss, s => op('session_unarchive', { sessionId: s.id, reason: 'Unarchived from the Conversations list' }), 'not unarchived');
  const toggleArchived = () => set({ archived: archived() ? undefined : true, sel: undefined, preset: undefined });
  const now = () => Date.now();
  // New conversation (fleet-conversations.md "Start a conversation") is its own URL in the work area, /conversations/new,
  // with the list view kept; ?in=<project> preselects a project. The old ?new=<project> link opens it once.
  const isNew = () => props.id === 'new';
  const initial = createMemo(() => new URLSearchParams(search()));
  // On the new page already, + goes to its composer (the draft and the choices stay).
  // The field when it takes input, else the page itself (presets loading, offline): never a dead click.
  const create = () => {
    if (!untrack(isNew)) return set({ focus: 'new', extra: { ...untrack(() => page.view.extra), in: undefined, role: undefined } });
    const detail = document.querySelector<HTMLElement>('[data-ui=detail][aria-label="New conversation"]'), field = detail?.querySelector<HTMLTextAreaElement>('[data-ui=composer] textarea');
    (field && !field.disabled ? field : detail)?.focus();
  };
  onSettled(() => {
    const p = untrack(initial).get('new');
    if (p !== null && !untrack(isNew)) set({ focus: 'new', extra: { ...untrack(() => page.view.extra), new: undefined, in: p || undefined } });
  });
  const pane = (
    <>
      <ShellStatus><OfflineMarker state={page.list.state} now={now()} /></ShellStatus>
      <ShellActions><Button label="New conversation" icon="plus" iconOnly kind="primary" pressed={isNew()} disabledReason={online.value ? undefined : 'offline'} onClick={create} /></ShellActions>
      <ListPagePanel
        page={page}
        noun="Conversations"
        placeholder="Search conversations"
        family="session"
        groups={GROUPS}
        sorts={SORTS}
        focused={isNew() ? undefined : props.id}
        tools={<Button label="Archived" icon="archive" iconOnly kind="quiet" pressed={archived()} onClick={toggleArchived} />}
        actions={() => [
          archived()
            ? { label: 'Unarchive', icon: 'archive-restore', onClick: () => unarchive(page.chosen), disabledReason: page.off }
            : { label: 'Archive', icon: 'archive', danger: true, onClick: () => page.setConfirm(page.chosen.filter(canArchive)), disabledReason: page.off ?? (page.chosen.some(canArchive) ? undefined : 'None can be archived') },
        ]}
        menu={s => [
          archived()
            ? { label: 'Unarchive', icon: 'archive-restore', disabledReason: page.off, onClick: () => unarchive([s]) }
            : { label: 'Archive', icon: 'archive', disabledReason: page.off ?? (canArchive(s) ? undefined : 'Execution active'), onClick: () => page.setConfirm([s]) },
        ]}
        confirm={page.confirm ? (
          <ProblemRow
            severity="warning"
            title={`Archive ${page.confirm.length} ${page.confirm.length === 1 ? 'conversation' : 'conversations'}?`}
            facts="Their worktrees are torn down; the owner can restore a conversation."
            actions={[
              { label: 'Yes, archive', primary: true, onClick: () => { const ss = page.confirm!; page.setConfirm(null); archive(ss); } },
              { label: 'Keep', onClick: () => page.setConfirm(null) },
            ]}
          />
        ) : undefined}
        empty={
          <Empty
            text={page.filtered ? `No ${archived() ? 'archived ' : ''}conversations match these filters.` : prefs.value.focus.length && !page.view.project?.length ? `No ${archived() ? 'archived ' : ''}conversations in the focused projects.` : archived() ? 'No archived conversations.' : 'No conversations yet.'}
            action={page.filtered ? <Button label="Clear filters" onClick={page.clearFilters} /> : archived() ? <Button label="Show active" onClick={toggleArchived} /> : <Button label="New conversation" onClick={create} />}
          />
        }
        listLabel={archived() ? 'Archived conversations' : 'Conversations'}
        title={titleOf}
        groupIcon={(g, key) => groupIcon(g, key)}
        groupTone={(g, key): Tone => (isWaitingGroup(g, key) ? 'attention' : 'neutral')}
        state={s => <StateBadge family="session" state={s.state} compact />}
        facts={s => <SessionFacts s={s} />}
      />
    </>
  );
  return (
    <ListDetail
      list={pane}
      detail={
        isNew() ? (
          <NewConversation
            initialProject={initial().get('in')}
            initialRole={initial().get('role')}
            onClose={() => set({ focus: undefined, extra: { ...page.view.extra, in: undefined, role: undefined } })}
            onStarted={sessionId => set({ focus: sessionId, extra: { ...page.view.extra, in: undefined, role: undefined } })}
          />
        ) : props.id ? (
          <Show when={props.id} keyed>
            {id => <SessionView id={id} frame="conversations" onClose={() => set({ focus: undefined })} />}
          </Show>
        ) : undefined
      }
      idle={<ConversationsIdle page={page} filtered={page.filtered} archived={archived()} />}
      detailKey={props.id}
    />
  );
}

/** The project grouping's first group: the sessions waiting for an answer (sessions_list, key empty, label "waiting"). */
const isWaitingGroup = (group: string, key: string) => group === 'project' && key === '';
function groupIcon(group: string, key: string): IconName {
  if (isWaitingGroup(group, key)) return states_.session.waiting.icon;
  return group === 'state' ? 'activity' : group === 'agent' ? 'bot' : 'folder-open';
}
const FINISHED = ['succeeded', 'failed', 'cancelled'];
type QuestionRow = Ops['questions_list']['reply']['questions'][number];

/**
 * The work area while no session is open (pages/conversations.md, Target): what waits for the person first, as question
 * cards answered in place (OR-53), then the summary by project and state as counters (OR-29), then the latest finished
 * sessions with their outcome line. Never a second copy of the list.
 */
function ConversationsIdle(props: { page: ListPage<SessionRow>; filtered: boolean; archived: boolean }) {
  const [, go] = useLocation();
  // The focus and ignored projects (OR-34) narrow the questions as they narrow the list.
  const questions = useOp('questions_list', () => (prefsRead.value && !props.archived ? { state: 'open', ...scopeIds() } : null));
  const waiting = createMemo(() => (questions.data?.questions ?? []).filter((q): q is QuestionRow & { sessionId: string } => !!q.sessionId));
  const finished = useOp('sessions_list', () => {
    if (!prefsRead.value || props.archived) return null;
    const { projectIds, excludeProjectIds } = scopeIds();
    return {
      group: 'none', sort: '-activity', limit: 5,
      filter: { state: FINISHED, ...(projectIds && { projectId: projectIds.map(noProject) }), ...(excludeProjectIds && { excludeProjectId: excludeProjectIds.map(noProject) }) },
    };
  }, { every: 30_000 }); // sessions_list names no change kinds, so the feed does not reread it
  const names = createMemo(() => new Map(props.page.displayed.map(r => [r.id, titleOf(r)])));
  const answers = useAnswers();
  const counts = () => props.page.list.counts as Partial<Record<StateOf<'session'>, number>>;
  const hasCounts = createMemo(() => Object.values(counts()).some(n => (n ?? 0) > 0));
  // Project groups of the list; the waiting group is the cards above.
  const groups = createMemo(() => props.page.list.groups.filter(g => g.count > 0 && !isWaitingGroup(props.page.rowsGroup, g.key)));
  const projects = useOp('project_list', () => (props.page.rowsGroup === 'project' ? {} : null));
  const projectNames = createMemo(() => new Map<string, string>((projects.data?.projects ?? []).map(p => [String(p.id), String(p.name ?? p.id)])));
  const groupTitle = createMemo(() => GROUPS.find(g => g[0] === props.page.rowsGroup)?.[1] ?? props.page.rowsGroup);
  const open = (id: string) => go(nextUrl(currentUrl(), 'conversations', { focus: id }));
  return (
    <article class="detail" aria-label="Conversations summary">
      <OpsSummary>
        <Show when={questions.error} keyed>{e => <ErrorBanner text={`Questions did not load: ${e}`} onRetry={questions.reload} />}</Show>
        <Show when={waiting().length > 0}>
          <DetailSection title={`Waiting for you · ${waiting().length}`}>
            <div class="stack">
              <Latest
                n={3}
                items={waiting().map(q => {
                  const name = () => names().get(q.sessionId);
                  return <OpenQuestion q={q} answers={answers} title={name() ? `${name()}: ${q.text}` : q.text} />;
                })}
              />
            </div>
          </DetailSection>
        </Show>
        <Show when={!(props.page.list.pending && !props.page.list.total)} fallback={<Skeleton rows={3} />}>
          <Switch fallback={<Show when={!waiting().length}><Empty text={props.filtered ? 'No matching conversations to summarize.' : props.archived ? 'Archived summary is empty.' : 'No conversations to summarize.'} /></Show>}>
            <Match when={groups().length > 0 && props.page.rowsGroup !== 'none'}>
              <DetailSection title={`By ${groupTitle().toLowerCase()}`}>
                <For each={groups()}>
                  {g => (
                    <KvRow
                      icon={groupIcon(props.page.rowsGroup, g.key)}
                      label={groupLabel(props.page.rowsGroup, props.page.rowsGroup === 'project' ? projectNames().get(g.key) ?? g.label : g.label) || g.key}
                      value={<Num m={{ value: g.count, basis: 'measured' }} kind="count" />}
                    />
                  )}
                </For>
              </DetailSection>
            </Match>
          </Switch>
          <Show when={hasCounts()}>
            <DetailSection title="By state">
              <StateCounters
                family="session"
                label="Conversations by state"
                counts={counts()}
                active={(props.page.view.state ?? []) as StateOf<'session'>[]}
                hideZero
                onAll={() => props.page.set({ state: [], preset: undefined })}
                onToggle={st => {
                  const cur = (props.page.view.state ?? []) as StateOf<'session'>[];
                  props.page.set({ state: cur.includes(st) ? cur.filter(x => x !== st) : [...cur, st], preset: undefined });
                }}
              />
            </DetailSection>
          </Show>
        </Show>
        <Show when={(finished.data?.sessions.length ?? 0) > 0}>
          <DetailSection title="Latest finished">
            <Latest
              n={3}
              items={(finished.data?.sessions ?? []).map(raw => {
                const r = raw as SessionRow;
                const at = r.activityAt ? Date.parse(r.activityAt) : NaN;
                // The title, then the outcome line: what the session said last.
                const line = r.now ? `${titleOf(r)}: ${r.now}` : titleOf(r);
                return (
                  <a class="kv-row row-link" href={props.page.href({ focus: r.id })} onClick={e => { e.preventDefault(); open(r.id); }}>
                    <StateDot family="session" state={r.state} />
                    <span class="kv-line" data-tip={line}>{line}</span>
                    <EntityCode code={r.code} id={r.id} kind="conversation" />
                    <Show when={!isNaN(at)}><AgeChip t={at} /></Show>
                  </a>
                );
              })}
            />
          </DetailSection>
        </Show>
      </OpsSummary>
    </article>
  );
}

/**
 * Row facts of a conversation (OR-37, pages/conversations.md): the now line, the open questions in the attention tone,
 * time since the last activity. The harness mark, the executions and the spend are dropped (OR-54): they stay in the
 * session's details.
 */
function SessionFacts(props: { s: SessionRow }) {
  const at = () => (props.s.activityAt ? Date.parse(props.s.activityAt) : NaN);
  const q = () => props.s.openQuestions ?? 0;
  return (
    <RowFacts>
      <Show when={props.s.now} keyed>
        {now => <span class="row-now" data-tip={now}>{now}</span>}
      </Show>
      <Show when={q() > 0}>
        <span class="chip-age t-attention alert">{q() === 1 ? '1 question' : `${q()} questions`}</span>
      </Show>
      <Show when={!isNaN(at())}>
        <Fact tip="Last activity"><AgeChip t={at()} /></Fact>
      </Show>
    </RowFacts>
  );
}

function groupLabel(group: string, label: string | null, key?: string) {
  if (group === 'agent') return label && label in harnesses ? harnesses[label as Harness].label : label ?? 'No agent';
  if (group === 'state') return label && label in states_.session ? states_.session[label as StateOf<'session'>].label : label ?? '';
  if (key !== undefined && isWaitingGroup(group, key)) return 'Waiting for you';
  return label ?? 'No project';
}
