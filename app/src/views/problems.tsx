// System › Problems (docs/design-system/pages/system.md, board drafts/anomaly-routing frames B and E): every open
// problem with the bot that owns it and where it stands, grouped stuck first; the opened problem shows its owner's
// conversation, where the person reacts in words (OR-71: no Investigate or Dismiss). Reads problems_list (the change
// stream rereads it on kind problem); idle, the work area opens the first stuck, else the first working problem (UR-35).
import { createMemo, createSignal, For, Show } from 'solid-js';
import {
  Composer, DetailHead, DetailSection, Empty, ErrorBanner, GapRow, GroupHeader, KvRow, ListDetail, OfflineMarker,
  ShellStatus, Skeleton, day, dayStart, moment, useTick, type Tone,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import { SessionView } from './session.tsx';
import type { ProblemRow } from '../../../contracts/generated/ops.ts';

const path = (code: string) => `/problems/${encodeURIComponent(code)}`;
const t = (s?: string | null) => (s ? Date.parse(s) : NaN);

/** A problem's phase in words and tone (status words, never codes). */
export function phaseOf(p: ProblemRow): { text: string; tone: Tone } {
  if (!p.ownerBotId) return { text: 'No owner', tone: 'danger' };
  switch (p.phase) {
    case 'stuck': return { text: 'Stuck', tone: 'attention' };
    case 'working': return { text: 'Agent working', tone: 'running' };
    case 'review': return { text: p.work.includes('/pull/') ? `Fix in review #${p.work.split('/pull/')[1]}` : 'Fix in review', tone: 'running' };
    case 'watching': return { text: 'Watching', tone: 'neutral' };
    case 'escalated': return { text: 'Asked you', tone: 'attention' };
  }
  return { text: 'New', tone: 'neutral' };
}

type Group = { id: string; label: string; tone: Tone; rows: ProblemRow[] };

function groupsOf(rows: readonly ProblemRow[]): Group[] {
  const g = (id: string, label: string, tone: Tone, f: (p: ProblemRow) => boolean): Group => ({ id, label, tone, rows: rows.filter(f) });
  return [
    g('stuck', 'Stuck', 'attention', p => !!p.ownerBotId && p.phase === 'stuck'),
    g('unowned', 'Without owner', 'danger', p => !p.ownerBotId),
    g('working', 'Agent working', 'running', p => !!p.ownerBotId && (p.phase === 'working' || p.phase === 'review')),
    g('escalated', 'Asked you', 'attention', p => !!p.ownerBotId && p.phase === 'escalated'),
    g('watching', 'Watching', 'neutral', p => !!p.ownerBotId && p.phase === 'watching'),
    g('new', 'New', 'neutral', p => !!p.ownerBotId && p.phase === 'new'),
  ].filter(x => x.rows.length > 0);
}

/** The head: how many, and who owns them. */
function headOf(rows: readonly ProblemRow[]): string {
  const by = new Map<string, number>();
  let none = 0;
  for (const p of rows) {
    if (p.ownerName) by.set(p.ownerName, (by.get(p.ownerName) ?? 0) + 1);
    else none++;
  }
  const owners = [...by].toSorted((a, b) => b[1] - a[1]).map(([n, c]) => `${n} owns ${c}`);
  return [...owners, none ? `${none} without owner` : 'none without owner'].join(' · ');
}

export function Problems(p: { id?: string; onOpen: (path: string) => void }) {
  const read = useOp('problems_list', () => ({ limit: 200 }));
  const tick = useTick(60_000);
  const now = () => tick();
  const rows = createMemo(() => read.data?.problems ?? []);
  const groups = createMemo(() => groupsOf(rows()));
  const [folded, setFolded] = createSignal<Record<string, boolean>>({ new: true });
  const open = createMemo(() => rows().find(x => x.code === p.id || x.id === p.id));
  const idle = createMemo(() => rows().find(x => x.phase === 'stuck') ?? rows().find(x => x.phase === 'working' || x.phase === 'review') ?? rows()[0]);
  const when = (s: string) => { const at = t(s); return dayStart(at) === dayStart(now()) ? moment(at, now()) : day(at, now()); };

  const list = (
    <>
      <ShellStatus><OfflineMarker state={read.state} now={now()} /></ShellStatus>
      <Show when={read.error && !read.data}><ErrorBanner text={read.error!} onRetry={read.reload} /></Show>
      <Show when={read.data} fallback={<Show when={!read.error}><Skeleton rows={8} /></Show>}>
        <section class="panel stack" aria-label="Problems">
          <p><b>{rows().length.toLocaleString('en')} {rows().length === 1 ? 'problem' : 'problems'}</b> <span class="muted">{headOf(rows())}</span></p>
          <Show when={rows().length} fallback={<Empty text="No open problem: nothing is failing that a bot does not already handle." />}>
            <For each={groups()}>{g => (
              <div class="stack">
                <GroupHeader icon="circle-alert" label={g.label} count={g.rows.length} tone={g.tone} open={!folded()[g.id]}
                  onToggle={() => setFolded(f => ({ ...f, [g.id]: !f[g.id] }))} />
                <Show when={!folded()[g.id]}>
                  <ul class="gap-rows" aria-label={g.label}>
                    <For each={g.rows}>{x => (
                      <GapRow name={x.count > 1 ? `${firstSentence(x.text)} ×${x.count}` : firstSentence(x.text)} detail={x.note || subjectOf(x)}
                        chip={phaseOf(x)} fact={[x.ownerName, when(x.phaseAt)].filter(Boolean).join(' · ')} factTip={`changed ${moment(t(x.phaseAt), now())}`}
                        href={path(x.code)} current={x === open()} onOpen={() => p.onOpen(path(x.code))} />
                    )}</For>
                  </ul>
                </Show>
              </div>
            )}</For>
          </Show>
        </section>
      </Show>
    </>
  );

  return (
    <ListDetail
      list={list}
      detailKey={p.id ? `problem:${p.id}` : undefined}
      detail={p.id ? (
        <Show when={open()} fallback={
          <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={p.id}>
            <DetailHead crumb="system · problems" title={p.id} onClose={() => p.onOpen('/problems')} />
            <Show when={read.data} fallback={<Skeleton rows={6} />}><Empty text={`${p.id} is closed or not yours to see.`} /></Show>
          </article>
        }>{x => <ProblemDetail p={x()} onClose={() => p.onOpen('/problems')} reload={read.reload} />}</Show>
      ) : undefined}
      idle={<Show when={idle()} fallback={<Show when={read.data}><Empty text="Bots have nothing to fix right now." /></Show>}>
        {x => <ProblemDetail p={x()} reload={read.reload} />}
      </Show>}
    />
  );
}

/** The first sentence of a problem's text: its title. */
function firstSentence(text: string): string {
  const s = /^.*?[.:](\s|$)/.exec(text)?.[0] ?? text;
  return s.replace(/[.:]\s*$/, '').trim() || text;
}

/** What the problem is about, from its cause: its family and subject in plain words. */
function subjectOf(p: ProblemRow): string {
  const rest = p.text.slice(firstSentence(p.text).length).replace(/^[.:]\s*/, '');
  return rest.length > 160 ? `${rest.slice(0, 157)}…` : rest;
}

function ProblemDetail(props: { p: ProblemRow; onClose?: () => void; reload: () => void }) {
  const p = () => props.p;
  const full = useOp('problem_get', () => ({ id: p().id }));
  const conv = () => full.data?.problem.conversationId ?? p().conversationId ?? undefined;
  const [err, setErr] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const send = async (text: string) => {
    setErr(''); setBusy(true);
    try { await op('problem_talk', { id: p().id, text, requestId: crypto.randomUUID() }); full.reload(); props.reload(); }
    catch (e) { setErr(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };
  return (
    <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={p().code}>
      <DetailHead crumb={`system · problems · ${p().code}`} title={p().count > 1 ? `${firstSentence(p().text)} ×${p().count}` : firstSentence(p().text)} onClose={props.onClose} />
      <KvRow icon="bot" label={p().ownerName ?? 'No bot owns it'} meta={<span class="muted">{p().note}</span>} value={<span>{phaseOf(p()).text}</span>} />
      <Show when={err()}><ErrorBanner text={err()} /></Show>
      <Show when={conv()} fallback={
        <DetailSection title="Conversation">
          <p class="muted">{p().text}</p>
          <Show when={p().ownerBotId} fallback={<p class="muted">Give a bot the problems duty and it takes this one.</p>}>
            <Composer placeholder={`Tell ${p().ownerName ?? 'the bot'}…`} disabledReason={busy() ? 'Sending' : undefined} draftKey={`problem:${p().id}`} onSend={send} />
          </Show>
        </DetailSection>
      }>{id => <SessionView id={id()} frame="conversations" />}</Show>
    </article>
  );
}
