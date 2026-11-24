// Book demos of the session-view blocks (board: docs/design-system/board/blocks.bl, Blocks · Session view).
import type { JSX } from '@solidjs/web';
import { TextRow, ToolLine, PlanItem, TimerRow, SwarmMember, ContextPanel, type ToolCall } from '../src/index.ts';
import { NOW, min } from './fixtures.ts';

type BookSection = [string, string, string | undefined, () => JSX.Element, (() => JSX.Element)?];

function MessageDemo() {
  return <div class="pane">
    <TextRow quiet who="agent" markdown="Собрал три состояния Home. Главный вопрос — какую карточку оставить: компактную или широкую с прогрессом." at={NOW - 3 * min} now={NOW} />
    <TextRow quiet who="operator" by="owner@fleet" markdown="Давай доведём Home: карточки сессий, состояния, телефон." at={NOW - 2 * min} now={NOW} />
  </div>;
}

const CALLS: ToolCall[] = [{ name: 'read', target: 'screens/home.bl', secs: 4 }, { name: 'export', target: '3 frames', secs: 9, n: 3 }];
function ToolLineDemo() {
  return <div class="col">
    <ToolLine words="Looked at the Home canvas and 3 session states" secs={13} calls={CALLS} />
    <ToolLine words="Looked at the Home canvas and 3 session states" secs={13} calls={CALLS} open />
    <ToolLine words="Exporting the Live canvas" state="running" startedAt={NOW - 4000} calls={[]} />
    <ToolLine words="Push to origin failed: permission denied" state="failed" secs={2} calls={[{ name: 'bash', target: 'git push origin', secs: 2, failed: true }]} />
  </div>;
}

function PartsDemo() {
  return <div class="col parts">
    <ul class="plan"><PlanItem text="Home: phone" /><PlanItem text="Audit current screens" done /></ul>
    <TimerRow text="Check the build at 23:30" when="in 42m · once" />
    <TimerRow text="Nightly design report" when="paused · daily 07:00" paused />
    <SwarmMember name="Auditor · screens" avatar={{ letter: 'A', tone: 'success' }} state="done" href="#swarm" />
  </div>;
}

function ContextPanelDemo() {
  return <div class="ctx-demo">
    <ContextPanel
      onHide={() => {}}
      plan={[{ text: 'Three principles agreed', done: true }, { text: 'Home: card states', done: true }, { text: 'Audit current screens', done: true }, { text: 'Rules v2 into owner-rules.md' }, { text: 'Home: phone' }]}
      timers={[{ id: 't1', text: 'Check the build at 23:30', when: 'in 42m · once' }]}
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

export const SESSION_BLOCKS: BookSection[] = [
  ['message', 'Message in the session view', 'transcript-viewer', () => <MessageDemo />, () => <>OR-54: <span class="mono">TextRow quiet</span>, no service row; the agent's words as text, the person's in a bubble on the right.</>],
  ['tool-line', 'Tool line', 'tool-line', () => <ToolLineDemo />, () => <>OR-52: folded, open (click the line; arrows move between calls), running (the time ticks) and failed.</>],
  ['context-parts', 'Plan item, timer row, swarm member', 'context-panel', () => <PartsDemo />, () => <>Todo and done steps; a timer with Pause, Edit and Cancel (Cancel confirms in place), and a paused one; a swarm member opens its own session.</>],
  ['context-panel', 'Context panel', 'context-panel', () => <ContextPanelDemo />, () => <>OR-51: plan, timers, automations, swarm, then the pointer to Details. Empty sections hide; loading shows skeleton rows.</>],
];
