// rename Question card: Text 32 -> answer
// rename Pulse line: Text 35 -> word
// rename Pulse line: Text 36 -> word
// rename Pulse line: Text 37 -> word
// rename Pulse line: Text 38 -> word
// rename Pulse line: Text 39 -> word
// rename Pulse line: Text 40 -> word
// rename Blocker chip: Text 23 -> cause
// rename Blocker chip: Text 24 -> count
// rename Target picker: Text 41 -> search
// rename Tool line: Text 44 -> call
// rename Tool line: Text 45 -> target
// rename Tool line: Text 46 -> call time
// rename Tool line: Text 47 -> call
// rename Tool line: Text 48 -> target
// rename Tool line: Text 49 -> call time
// rename Context panel: Text 50 -> title
// rename Context panel: Text 51 -> plan title
// rename Context panel: Text 52 -> timers title
// rename Context panel: Text 53 -> automations title
// rename Context panel: Text 54 -> automation
// rename Context panel: Text 55 -> swarm title
// Board masters: the Home and session-view blocks (masters.tsx).
import {
  QuestionCard, SessionCard, BotTile, PulseLine, BlockerChip, GroupHeader, TargetPicker, Composer, QuietSummary, StatusPill,
  TextRow, ToolLine, PlanItem, TimerRow, SwarmMember, ContextPanel,
  type Life, type Target, type ToolCall, type StatusPillState,
} from '../src/index.ts';
import { slot, type MasterSpec } from './masters-spec.ts';
import { NOW, min } from './fixtures.ts';

const CHOICES = [{ label: 'Compact', primary: true }, { label: 'Wide' }];
const RETRY = [{ label: 'Retry', primary: true }, { label: 'Open' }];

const card = (s: string, q: string) => <QuestionCard question={slot('question', q)} choices={s === 'failed' ? RETRY : CHOICES} tone={s === 'failed' ? 'danger' : undefined}
  answered={s === 'answered' ? { answer: slot('question', 'Compact'), at: NOW - 2 * min } : undefined} />;

const SESSION: Record<string, { name: string; project: string; avatar: Parameters<typeof SessionCard>[0]['avatar']; now: string; meta: string; question?: string }> = {
  working: { name: 'PM · fleet', project: 'fleet', avatar: { letter: 'P' }, now: 'Swarm of 3 on T-FLT-51 «Fix receipt factory»: review running.', meta: 'step 2 of 3 · 12m ago · pds18-native · timer 22:30' },
  waiting: { name: 'PM · design', project: 'design', avatar: { letter: 'P', tone: 'attention' }, now: 'Showed the Home screen in three states; asks which card layout to keep.', meta: 'now · 4 screenshots', question: 'open' },
  queued: { name: 'PM · cyprus.bz', project: 'cyprus.bz', avatar: { letter: 'P', tone: 'neutral' }, now: 'Waits for a free slot.', meta: 'position 2 · 4m ago' },
  failed: { name: 'Release manager', project: 'fleet', avatar: { harness: 'codex' }, now: 'Rollout of PR 826 failed on windows.', meta: '9m ago · retry or ask', question: 'failed' },
  done: { name: 'PM · design', project: 'design', avatar: { letter: 'P', tone: 'success' }, now: 'Finished: Home canvas in 4 states.', meta: 'done · 2m ago' },
};
const QUESTION: Record<string, string> = { open: 'Keep the compact card or the wide card with progress?', failed: 'Retry the rollout on windows?' };

const TARGETS: Target[] = [
  { id: 'pm-fleet', kind: 'pm', name: slot('name', 'PM · fleet'), about: slot('about', 'fleet'), project: 'fleet', avatar: { letter: 'P' }, state: 'working' },
  { id: 'pm-cy', kind: 'pm', name: slot('name', 'PM · cyprus.bz'), about: slot('about', 'cyprus.bz'), project: 'cyprus.bz', avatar: { letter: 'P', tone: 'neutral' }, state: 'queued' },
  { id: 'hal', kind: 'bot', name: slot('name', 'HAL0'), about: slot('about', 'watches Fleet tasks'), avatar: { letter: 'H', tone: 'success' }, state: 'waiting' },
  { id: 'setup', kind: 'bot', name: slot('name', 'Fleet setup'), about: slot('about', 'settings, hosts, accounts'), avatar: { harness: 'claude' }, state: 'asleep' },
];
const picker = (targets = TARGETS) => <TargetPicker targets={targets} value="pm-fleet" onPick={() => {}} onNew={() => {}} />;
/** The picker with its list open (a click on the chip once drawn), in a box tall enough for the floating list. */
const opened = (targets = TARGETS) => <div style={{ 'min-height': '440px' }} ref={(el: HTMLDivElement) => {
  // Several pickers stay open side by side: focus leaving one (into the next one's search) must not close it.
  for (const k of ['focusout', 'focusin']) el.addEventListener(k, e => e.stopPropagation(), true);
  setTimeout(() => el.querySelector<HTMLElement>('.target-chip')?.click(), 50);
}}>{picker(targets)}</div>;
/** Picker row: the first row that is not the highlighted one (the current target) is the variant's row. */
const ROWS: Record<string, Target[]> = { pm: TARGETS.slice(0, 2), bot: [TARGETS[0], ...TARGETS.slice(2)], new: TARGETS.slice(0, 1) };
const typed = { key: 'masters-typed', read: () => 'давай обсудим дизайн Home', write: () => {}, clearIfUnchanged: () => {} };

const PILL: Record<StatusPillState, { text?: string; at?: number }> = {
  working: {}, problem: { text: slot('text', 'pds18-native offline 4m') }, connecting: {}, stale: { at: NOW - 3 * min },
};

const READS: ToolCall[] = [
  { name: slot('call', 'read'), kind: 'files', target: slot('target', 'screens/home.bl'), secs: 4 },
  { name: slot('call', 'read'), kind: 'files', target: slot('target', 'screens/live.bl'), secs: 9 },
];
const TOOL: Record<string, Parameters<typeof ToolLine>[0]> = {
  folded: { words: 'Looked at 2 screens', calls: READS, secs: 13 },
  open: { words: 'Looked at 2 screens', calls: READS, secs: 13, open: true },
  running: { words: 'Exporting the Live canvas', state: 'running', startedAt: NOW - 4000, calls: [{ name: slot('call', 'export'), kind: 'other', target: slot('target', 'live.bl'), secs: 4, running: true }] },
  failed: { words: 'Push to origin failed: permission denied', state: 'failed', secs: 2, open: true,
    calls: [{ name: slot('call', 'bash'), kind: 'shell', target: slot('target', 'git push origin'), error: 'permission denied', secs: 2, failed: true }] },
};

const timer = { schedule: slot('schedule', 'Every 5m'), text: slot('text', 'Review your focus with task_page and questions_list'), nextAt: NOW + 3 * min, now: NOW,
  quiet: slot('quiet', '12 quiet checks since 14:05') };

export const MASTERS_HOME: MasterSpec[] = [
  { name: 'Question card', axes: { state: ['open', 'answered', 'failed'] }, w: 300, texts: ['answer', 'sep', 'when'], render: v => card(v.state, QUESTION[v.state === 'failed' ? 'failed' : 'open']) },
  { name: 'Session card', axes: { state: ['working', 'waiting', 'queued', 'failed', 'done'] }, w: 340, render: v => {
    const s = SESSION[v.state];
    return <SessionCard name={slot('name', s.name)} project={s.project} avatar={s.avatar} state={v.state as Life} now={slot('now', s.now)} meta={[slot('meta', s.meta)]}
      progress={v.state === 'working' ? { step: 2, of: 3 } : undefined} question={s.question ? card(s.question, QUESTION[s.question]) : undefined} />;
  } },
  { name: 'Bot tile', axes: { state: ['asleep', 'waking', 'failed'] }, w: 300, texts: ['about'], render: v =>
    <BotTile name={slot('name', 'Release manager')} about={slot('about', 'delivery, rollouts')} avatar={{ harness: 'codex' }} state={v.state as 'asleep'} reason="no free slot" /> },
  { name: 'Pulse line', axes: { state: ['normal', 'blocked'] }, texts: ['count', 'word', 'count', 'word', 'count', 'word', 'live'], render: v =>
    <PulseLine working={4} queued={7} done={41} liveHref="#live" countHref={s => `#${s}`}
      blockers={v.state === 'blocked' ? [{ cause: 'Codex cannot start on pds18-native', n: 22, href: '#pds18' }] : undefined} /> },
  { name: 'Blocker chip', texts: ['times', 'count'], render: () => <BlockerChip cause={slot('cause', 'Codex cannot start on pds18-native')} n={22} href="#pds18" /> },
  { name: 'Group head', pick: '.group-h', texts: ['count'], render: () =>
    <section class="home-group"><GroupHeader icon="bell" tone="attention" label={slot('title', 'Waiting for you')} count={2} open onToggle={() => {}} /></section> },
  { name: 'Target chip', pick: '.target-chip', render: () => picker() },
  { name: 'Composer', axes: { state: ['idle', 'typing'] }, w: 640, texts: ['text'], render: v =>
    <Composer placeholder="Talk to anyone…" onSend={() => {}} target={picker()} draftPersistence={v.state === 'typing' ? typed : undefined} /> },
  { name: 'Picker row', axes: { kind: ['pm', 'bot', 'new'] }, w: 300, texts: ['name'], render: v => opened(ROWS[v.kind]),
    pick: '.pick-row[data-kind]:not(.on), .pick-row.new' },
  { name: 'Target picker', pick: '.target-panel', texts: ['search', 'group', 'group', 'name'], render: () => opened() },
  { name: 'Quiet summary', w: 720, texts: ['title', 'end', 'watcher', 'watch'], render: () =>
    <QuietSummary facts={slot('text', 'Overnight: 12 tasks done, 2 pull requests merged, 0 failures')} span="" watcher="HAL0" onAsk={() => {}} onLive={() => {}} /> },
  { name: 'Status pill', axes: { state: ['working', 'problem', 'connecting', 'stale'] }, texts: ['text', 'when', 'ago'], render: v =>
    <StatusPill state={v.state as StatusPillState} {...PILL[v.state as StatusPillState]} /> },
  { name: 'Message', axes: { from: ['agent', 'operator'] }, w: 480, render: v => v.from === 'agent'
    ? <TextRow quiet who="agent" markdown={slot('text', 'Собрал три состояния Home. Главный вопрос — какую карточку оставить: компактную или широкую с прогрессом.')} at={NOW - 3 * min} now={NOW} />
    : <TextRow quiet who="operator" by="owner@fleet" markdown={slot('text', 'Давай доведём Home: карточки сессий, состояния, телефон.')} at={NOW - 2 * min} now={NOW} /> },
  { name: 'Tool line', axes: { state: ['folded', 'open', 'running', 'failed'] }, w: 480,
    texts: ['text', 'count', 'chip time', 'time', 'kind', 'calls', 'calls word', 'sep', 'total', 'call time', 'call time'], render: v => <ToolLine {...TOOL[v.state]} /> },
  { name: 'Plan item', axes: { state: ['todo', 'done'] }, w: 196, pick: '.plan-item', render: v =>
    <ul class="plan"><PlanItem text={slot('text', v.state === 'done' ? 'Audit current screens' : 'Home: phone')} done={v.state === 'done'} /></ul> },
  { name: 'Timer row', w: 196, texts: ['when'], render: () => <TimerRow {...timer} /> },
  { name: 'Swarm member', w: 196, render: () => <SwarmMember name={slot('name', 'Auditor · screens')} avatar={{ letter: 'A', tone: 'success' }} state="done" href="#swarm" /> },
  { name: 'Context panel', w: 220, texts: ['title', 'plan title', 'timers title', 'automations title', 'sep', 'automation when', 'swarm title', 'details'], render: () =>
    <ContextPanel
      onHide={() => {}}
      plan={[{ text: 'Three principles agreed', done: true }, { text: 'Home: card states', done: true }, { text: 'Audit current screens', done: true }, { text: 'Rules v2 into owner-rules.md' }, { text: 'Home: phone' }]
        .map(s => ({ ...s, text: slot('text', s.text) }))}
      timers={[{ id: 't1', ...timer }]}
      automations={[{ rule: slot('automation', 'Nightly design report'), when: '07:00' }]}
      swarm={[
        { id: 'a', name: slot('name', 'Auditor · screens'), avatar: { letter: 'A', tone: 'success' }, state: 'done', href: '#swarm' },
        { id: 'b', name: slot('name', 'Mock builder · Home'), avatar: { letter: 'M', tone: 'success' }, state: 'done', href: '#swarm' },
        { id: 'c', name: slot('name', 'Reviewer · rules'), avatar: { letter: 'R' }, state: 'working', href: '#swarm' },
      ]}
    /> },
];
