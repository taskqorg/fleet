import { createSignal } from 'solid-js';
// Book demos of the session-view blocks (board: docs/design-system/board/blocks.bl, Blocks · Session view).
import type { JSX } from '@solidjs/web';
import { TextRow, ToolLine, ThreadList, type ThreadItem, PlanItem, TimerRow, TimerChip, SwarmMember, ContextPanel, Composer, CommandPreview, type ToolCall } from '../src/index.ts';
import { NOW, min } from './fixtures.ts';

type BookSection = [string, string, string | undefined, () => JSX.Element, (() => JSX.Element)?];

function MessageDemo() {
  return <div class="pane">
    <TextRow quiet who="agent" markdown="Собрал три состояния Home. Главный вопрос — какую карточку оставить: компактную или широкую с прогрессом." at={NOW - 3 * min} now={NOW} />
    <TextRow quiet who="operator" by="owner@fleet" markdown="Давай доведём Home: карточки сессий, состояния, телефон." at={NOW - 2 * min} now={NOW} />
  </div>;
}

const CALLS: ToolCall[] = [
  { name: 'commandExecution', kind: 'shell', target: 'python3 make_square.py', secs: 0.4, input: { command: 'python3 make_square.py' }, output: { exitCode: 0 } },
  { name: 'artifact_save', kind: 'results', target: 'blue-square.png', secs: 0.002, failed: true, error: 'path must be inside your working area or $TMPDIR', input: { path: '/srv/x/blue-square.png' }, output: { error: 'path must be inside your working area or $TMPDIR' } },
  { name: 'artifact_save', kind: 'results', target: 'blue-square.png', secs: 0.24, input: { path: 'output/blue-square.png' }, output: { code: 'R-FLT-5530', state: 'pending' } },
  { name: 'artifact_save', kind: 'results', target: 'summary.md', secs: 0.61, input: { path: 'output/summary.md' }, output: { code: 'R-FLT-5531', state: 'pending' } },
  { name: 'questions_list', kind: 'fleet', target: 'fleet', secs: 0.3, n: 2 },
];
function ToolLineDemo() {
  return <div class="col">
    <ToolLine words="Ran a command, saved 3 results, used Fleet" secs={20} calls={CALLS} />
    <ToolLine words="Ran a command, saved 3 results, used Fleet" secs={20} calls={CALLS} open />
    <ToolLine words="Exporting the Live canvas" state="running" startedAt={NOW - 4000} calls={[{ name: 'export', kind: 'other', target: 'live.bl', secs: 0, running: true }]} />
    <ToolLine words="Push to origin failed: permission denied" state="failed" secs={2} calls={[{ name: 'bash', kind: 'shell', target: 'git push origin', error: 'permission denied', secs: 2, failed: true }]} open />
  </div>;
}

const THREADS: ThreadItem[] = [
  { key: 'topic:a1', title: 'Аудит UI', code: null, itemState: null, lastActivityAt: NOW - 2 * min, turns: 3, open: true, unread: 0 },
  { key: 'question:q432', title: 'P90 выпуска вырос до 2:56', code: 'Q-FLT-432', itemState: 'open', lastActivityAt: NOW - 5 * min, turns: 2, open: true, unread: 1 },
  { key: 'task:t63', title: 'Форматирование Go-файлов', code: 'T-FLT-63', itemState: 'queued', lastActivityAt: NOW - 120 * min, turns: 1, open: true, unread: 0 },
  { key: 'task:t64', title: 'Ответ Telegram на /help', code: 'T-FLT-64', itemState: 'held', lastActivityAt: NOW - 60 * min, turns: 4, open: false, unread: 0 },
  { key: '', title: 'Main thread', code: null, itemState: null, lastActivityAt: NOW - 180 * min, turns: 9, open: false, unread: 0 },
];
function ThreadListDemo() {
  const [sel, setSel] = createSignal('topic:a1');
  return <div style={{ width: 'calc(var(--size-rail) + var(--space-7) + var(--space-6))', border: '1px solid var(--line)' }}>
    <ThreadList threads={THREADS} selected={sel()} onSelect={setSel} quiet={{ schedule: 'Every 5m · quiet checks', text: '14 quiet checks since 13:05 · nothing changed' }} />
  </div>;
}

function PartsDemo() {
  return <div class="col parts">
    <ul class="plan"><PlanItem text="Home: phone" /><PlanItem text="Audit current screens" done /></ul>
    <TimerRow schedule="Every 5m" text="Review your focus with task_page and questions_list" nextAt={NOW + 3 * min} now={NOW} quiet="12 quiet checks since 14:05" />
    <TimerRow schedule="Daily 07:00" text="Nightly design report" paused />
    <TimerRow schedule="Every 1h" text="Check the deploy" nextAt={NOW + 40 * min} now={NOW} readOnly />
    <SwarmMember name="Auditor · screens" avatar={{ letter: 'A', tone: 'success' }} state="done" href="#swarm" />
  </div>;
}

function ContextPanelDemo() {
  return <div class="ctx-demo">
    <ContextPanel
      onHide={() => {}}
      plan={[{ text: 'Three principles agreed', done: true }, { text: 'Home: card states', done: true }, { text: 'Audit current screens', done: true }, { text: 'Rules v2 into owner-rules.md' }, { text: 'Home: phone' }]}
      timers={[{ id: 't1', schedule: 'Every 5m', text: 'Review your focus with task_page and questions_list', nextAt: NOW + 3 * min, quiet: '12 quiet checks since 14:05' }]}
      automations={[{ rule: 'Nightly design report', when: '07:00' }]}
      swarm={[
        { id: 'a', name: 'Auditor · screens', avatar: { letter: 'A', tone: 'success' }, state: 'done', href: '#swarm' },
        { id: 'b', name: 'Mock builder · Home', avatar: { letter: 'M', tone: 'success' }, state: 'done', href: '#swarm' },
        { id: 'c', name: 'Reviewer · rules', avatar: { letter: 'R' }, state: 'working', href: '#swarm' },
      ]}
    />
    <ContextPanel onHide={() => {}} plan={[{ text: 'Answer the owner', done: false }]} />
    <ContextPanel loading />
  </div>;
}

function TimerChipDemo() {
  return <div class="row">
    <TimerChip every="5m" nextAt={NOW + 3 * min} now={NOW} />
    <TimerChip every="5m" paused />
    <TimerChip every="15s" nextAt={NOW + 10_000} now={NOW} more={1} />
  </div>;
}

const CMD = '/trigger conversation-check every 10m if changed: Check open questions first, then task_page';
function CommandDemo() {
  return <div class="col">
    <Composer onSend={() => {}} fill={{ text: CMD, seq: 0 }} command={{ label: 'Apply' }} above={
      <CommandPreview kind="change" icon="clock" title="Change timer conversation-check" note="Next check in 10m after apply."
        changes={[{ label: 'Every', before: '5m', after: '10m' }, { label: 'Text', before: 'Review your focus with task_page and questions_list', after: 'Check open questions first, then task_page' }]} />
    } />
    <Composer onSend={() => {}} fill={{ text: CMD, seq: 0 }} command={{ label: 'Apply' }} above={
      <CommandPreview kind="conflict" message="Now paused · every 15m, changed by the agent 1m ago. Nothing was changed." onLoad={() => {}} />
    } />
  </div>;
}

export const SESSION_BLOCKS: BookSection[] = [
  ['message', 'Message in the session view', 'transcript-viewer', () => <MessageDemo />, () => <>OR-54: <span class="mono">TextRow quiet</span>, no service row; the agent's words as text, the person's in a bubble on the right.</>],
  ['tool-line', 'Tool line', 'tool-line', () => <ToolLineDemo />, () => <>OR-52, Conversations v2 (c2): chips per kind of call with count, failures and time; a chip opens its calls (a call opens its input and output); running (the time ticks) and failed. A failure the agent recovered from only counts in its chip.</>],
  ['thread-list', 'Thread list', 'context-panel', () => <ThreadListDemo />, () => <>Conversations v2 (b2): work-item threads, Open then Parked, newest first; unread as “1 new”; the timers' quiet checks fold into the foot row.</>],
  ['context-parts', 'Plan item, timer row, swarm member', 'context-panel', () => <PartsDemo />, () => <>Todo and done steps; a timer with its schedule, live time to the next check, its message, quiet checks, Pause, Edit and Cancel (Cancel confirms in place); a paused one; one the person may not change; a swarm member opens its own session.</>],
  ['timer-chip', 'Timer chip', 'context-panel', () => <TimerChipDemo />, () => <>Owner pick A: in the session head while a timer exists; the interval and the live time to the next check, paused, and +1 for more timers. A click opens Context.</>],
  ['composer-command', 'Composer command with preview', 'composer', () => <CommandDemo />, () => <>Edit of a timer fills the composer with its command; the preview shows what changes before Apply, and a conflict (409) says what the timer is now with Load current.</>],
  ['context-panel', 'Context panel', 'context-panel', () => <ContextPanelDemo />, () => <>OR-51: plan, timers, automations, swarm, then the pointer to Details. Empty sections hide; loading shows skeleton rows.</>],
];
