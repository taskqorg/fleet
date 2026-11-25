// History (docs/designs/fleet-history.md, docs/design-system/pages/history.md): what happened in Fleet, newest first. One
// list for every scope: the History page reads everything, a section its own (Lab: section lab), an object its own
// (taskId, sessionId, hostId, botId, experimentId). Data: history_view; its first page rereads on activity changes and
// older pages load on scroll (OR-65). Repeats come folded from the server with ×N (OR-58); the time is rightmost (OR-64).
import { createMemo, For, Show } from 'solid-js';
import { useLocation, useSearch } from '../router.ts';
import {
  Button, Empty, ErrorBanner, Icon, InfiniteList, LiveRow, Skeleton, WorkFacts, day, dayStart, harnesses, peerFence, type Life,
} from '../../../packages/ui/src/index.ts';
import type { Ops } from '../api.ts';
import { useOpPages } from '../op-pages.ts';
import { useOp } from '../store.ts';
import { scopeIds } from '../prefs.ts';

type Req = Ops['history_view']['request'];
type Entry = Ops['history_view']['reply']['entries'][number];

/** An entry's outcome in the one state vocabulary (OR-55); a plain record (a change, a created task) is neutral. */
const LIFE: Record<Entry['outcome'], Life> = { ok: 'done', failed: 'failed', stopped: 'stopped', waiting: 'waiting', running: 'working', info: 'asleep' };
const TASK_WORDS: Record<string, string> = { in_progress: 'in progress', completed: 'done', cancelled: 'cancelled', waiting: 'waiting', paused: 'paused', failed: 'failed', queued: 'queued' };

/** An actor as a row shows it: a person by the name part of their email, an agent execution as "agent". */
const shortActor = (a: string) => a.startsWith('execution:') ? 'agent' : a.includes('@') ? a.slice(0, a.indexOf('@')) : a;

/** What a run is called when it has no title (execution kinds, as the entity card names them). */
const RUN_WORDS: Record<string, string> = { agent_run: 'agent run', pipeline_run: 'pipeline run', pipeline_step: 'pipeline step', child_agent: 'child agent', tool_run: 'tool run', host_job: '' };
const KIND_WORDS: Record<Entry['kind'], string> = { run: 'run', task: 'task', question: 'question', lab: 'experiment', settings: 'settings', access: 'access grant' };

/** The subject's words when it has no title: its kind, never its id (OR-38). */
function untitled(e: Entry): string {
  const k = e.detail?.executionKind;
  return e.kind === 'run' && typeof k === 'string' && k in RUN_WORDS ? RUN_WORDS[k] : KIND_WORDS[e.kind] ?? '';
}

/** The row's sentence: the subject's title and what happened to it. */
export function historyText(e: Entry): string {
  const what = e.title || untitled(e);
  if (e.kind === 'task' && e.verb !== 'created') return `${what} ${TASK_WORDS[e.verb] ?? e.verb}`;
  if (e.kind === 'question') return `Question ${e.verb}: ${what}`;
  if (e.kind === 'lab') return `${what} ${e.verb === 'decided' ? 'decided' : e.verb}`;
  if (e.kind === 'run' && e.verb === 'trials') {
    // An experiment's trials in one entry (history_view folds them): how many, and how they stand.
    const d = e.detail ?? {}, c = (k: string) => (typeof d[k] === 'number' ? d[k] as number : 0);
    const parts = [`${e.n} ${e.n === 1 ? 'trial' : 'trials'}`, ...(['running', 'finished', 'failed', 'stopped'] as const).filter(k => c(k) > 0).map(k => `${c(k)} ${k}`)];
    return `${e.title.replace(/ trial$/, '')}: ${parts.join(', ')}`;
  }
  if (e.kind === 'run') {
    // A host job names its host; an untitled run (written before fleet_activity named it) its harness.
    if (e.detail?.executionKind === 'host_job') {
      const on = e.hostId ? ` on ${e.hostId}` : '';
      return e.title && e.title !== 'Host job' ? `${e.title} · host job ${e.verb}${on}` : `Host job ${e.verb}${on}`;
    }
    const harness = typeof e.detail?.harness === 'string' ? e.detail.harness : null;
    return `${e.title || (harness ? `${harness} run` : 'Run')} ${e.verb}`;
  }
  return `${what} ${e.verb}`;
}

/** Where a row opens: the object it belongs to, most specific first. Inside an object's own history (a task's tab) a
 *  row opens what it adds there, the conversation, rather than the object already open. */
export function historyPath(e: Entry, inside?: 'task'): string | null {
  if (e.taskId && inside !== 'task') return `/tasks/${encodeURIComponent(e.taskId)}`;
  if (e.experimentId) return `/lab/${encodeURIComponent(e.experimentId)}`;
  if (e.sessionId) return `/conversations/${encodeURIComponent(e.sessionId)}`;
  if (e.hostId) return `/hosts/${encodeURIComponent(e.hostId)}`;
  return null;
}

/** A day heading's words (board screens/history.bl): Today, Yesterday, else the date. */
const dayLabel = (t: number, now: number) => t >= dayStart(now) ? 'Today' : t >= dayStart(now, -1) ? 'Yesterday' : day(t, now);

/** The rows of a history list: state icon, sentence, actor and ×N, age rightmost; with days, a heading where the day
 *  changes; still (Live's wall) opens nothing. */
export function HistoryRows(p: { entries: readonly Entry[]; inside?: 'task'; days?: boolean; still?: boolean }) {
  const [, go] = useLocation();
  const fence = createMemo(() => peerFence(p.entries.map(e => e.verb === 'trials' ? 1 : e.n)));
  const now = Date.now();
  return <For each={p.entries}>{(e, i) => {
    const to = historyPath(e, p.inside);
    const prev = i() > 0 ? p.entries[i() - 1] : null;
    const head = p.days && (!prev || dayStart(prev.at) !== dayStart(e.at));
    return <>
      <Show when={head}><li class="history-day" data-ui="history-day"><h3>{dayLabel(e.at, now)}</h3></li></Show>
      <LiveRow kind="event" at={e.at} state={LIFE[e.outcome]} text={historyText(e)} onOpen={to && !p.still ? () => go(to) : undefined}
        facts={<>
          {/* A harness shows as its mark in the work facts; a person, bot or system actor as a quiet name. */}
          <Show when={e.actor && !(e.actor in harnesses)}><span class="lr-fact muted">{shortActor(e.actor!)}</span></Show>
          <WorkFacts attempts={e.verb === 'trials' ? 1 : e.n} attemptsFence={fence()} agent={e.actor} />
        </>} />
    </>;
  }}</For>;
}

/** A scoped history that loads on scroll: the History page and an object's History tab. */
export function HistoryFeed(p: { request: () => Req; label: string; inside?: 'task' }) {
  const paged = useOpPages('history_view', () => p.request(), {
    page: r => ({ rows: r.entries, next: r.nextCursor ?? null }), after: before => ({ before }), key: e => String(e.seq),
  });
  const rows = createMemo(() => paged.pages.rows);
  return <Show when={paged.first.data || paged.first.error} fallback={<Skeleton rows={8} />}>
    <Show when={!paged.first.error || paged.first.data} fallback={<ErrorBanner text={`History did not load: ${paged.first.error}`} onRetry={() => paged.first.reload()} />}>
      <Show when={rows().length > 0} fallback={<Empty text="Nothing has happened here yet." />}>
        <InfiniteList pages={paged.pages} label={p.label} endLabel="Older entries">
          <ul class="live-list" data-ui="history-list"><HistoryRows entries={rows()} inside={p.inside} days /></ul>
        </InfiniteList>
      </Show>
    </Show>
  </Show>;
}

/** A section's newest entries for its empty detail pane, read by the section's own view (lab_view history); the heading
 *  opens the full scoped History. */
export function HistoryPreview(p: { entries: readonly Entry[] | undefined; to: string }) {
  const [, go] = useLocation();
  return <Show when={(p.entries?.length ?? 0) > 0}>
    <section class="panel" aria-label="History" data-ui="history-preview">
      <header class="panel-h"><Icon name="history" /><h2>
        <a class="dsec-open" href={p.to} onClick={e => { if (!e.metaKey && !e.ctrlKey) { e.preventDefault(); go(p.to); } }}>History<Icon name="chevron-right" /></a>
      </h2></header>
      <ul class="live-list"><HistoryRows entries={p.entries!} /></ul>
    </section>
  </Show>;
}

// The History page's filters, kept in the address so a scope is a link (Lab's History heading opens ?section=lab).
const KINDS = [
  { id: 'all', label: 'All' }, { id: 'run', label: 'Runs' }, { id: 'task', label: 'Tasks' }, { id: 'question', label: 'Questions' },
  { id: 'lab', label: 'Lab' }, { id: 'settings', label: 'Settings' }, { id: 'access', label: 'Access' },
] as const;
type KindTab = (typeof KINDS)[number]['id'];
const SCOPES = [
  { key: 'section', label: (v: string) => ({ work: 'Work', lab: 'Lab', system: 'System' } as Record<string, string>)[v] ?? v },
  { key: 'taskId', label: (v: string) => `Task ${v}` }, { key: 'sessionId', label: (v: string) => `Conversation ${v}` },
  { key: 'experimentId', label: (v: string) => `Experiment ${v}` }, { key: 'hostId', label: (v: string) => `Host ${v}` },
  { key: 'botId', label: (v: string) => `Bot ${v}` },
] as const;

export function History() {
  const [, go] = useLocation();
  const search = useSearch();
  const q = createMemo(() => new URLSearchParams(search()));
  const set = (key: string, value: string | null) => {
    const n = new URLSearchParams(q());
    if (value === null) n.delete(key); else n.set(key, value);
    const s = n.toString();
    go('/history' + (s ? '?' + s : ''), { replace: true });
  };
  const kind = createMemo<KindTab>(() => (KINDS.find(k => k.id === q().get('kind'))?.id ?? 'all'));
  const failed = createMemo(() => q().get('outcome') === 'failed');
  const request = createMemo<Req>(() => {
    // The header focus, as Live and every cross-project screen apply it (prefs scopeIds); one object's history is that
    // object's, whatever the focus.
    const object = SCOPES.some(s => s.key !== 'section' && q().get(s.key));
    const r: Req = object ? {} : { ...scopeIds() };
    for (const s of SCOPES) { const v = q().get(s.key); if (v) (r as Record<string, string>)[s.key] = v; }
    if (kind() !== 'all') r.kinds = [kind() as Exclude<KindTab, 'all'>];
    if (failed()) r.outcome = 'failed';
    return r;
  });
  // The object's readable name comes with the first page (scopeTitle); this read shares the feed's (one cache key).
  const first = useOp('history_view', () => request());
  const scope = createMemo(() => SCOPES.flatMap(s => {
    const v = q().get(s.key);
    if (!v) return [];
    return [{ key: s.key, label: s.key !== 'section' && first.data?.scopeTitle ? s.label(first.data.scopeTitle) : s.label(v) }];
  }));
  return (
    <div class="live" data-ui="history">
      <div class="fbar">
        <div class="fbar-line">
          <nav class="crumbs" aria-label="Scope">
            <a href="/history" onClick={e => { e.preventDefault(); go('/history'); }}>All</a>
            <For each={scope()}>{(s, i) => <>
              <Icon name="chevron-right" />
              <Show when={i() === scope().length - 1} fallback={<span>{s.label}</span>}>
                <span class="chip">{s.label}<Button label={`Widen: remove ${s.label}`} icon="x" iconOnly onClick={() => set(s.key, null)} /></span>
              </Show>
            </>}</For>
          </nav>
        </div>
        <div class="fbar-line">
          <div class="presets" role="group" aria-label="Kinds">
            <For each={KINDS}>{k => <Button label={k.label} kind="quiet" pressed={kind() === k.id} onClick={() => set('kind', k.id === 'all' ? null : k.id)} />}</For>
          </div>
          <Button label="Failed" icon="octagon-x" pressed={failed()} onClick={() => set('outcome', failed() ? null : 'failed')} />
        </div>
      </div>
      <section class="panel">
        <HistoryFeed request={request} label="History" />
      </section>
    </div>
  );
}
